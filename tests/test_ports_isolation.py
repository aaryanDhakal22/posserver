"""main 3.0 isolation sweep: every route the ports added refuses the wrong
credential, and no store can reach another store's rows by id.

  * the route table is read from /__routes, so a new store-session / channel
    / admin route is covered the day it is added: no credential → 401, the
    other kinds of credential → 401 (a channel key is not a session, a
    session is not a channel key, neither is an admin key)
  * store B's manager on every by-id route with store A's ids → 404 (or 400
    for an id inside a body), and A's rows are unchanged afterwards
  * an admin can't point A's channel at B's view
"""

from __future__ import annotations

import re
import uuid

import pytest
import requests

from conftest import ADMIN_V1, MAIN_BASE_URL, STORE_V1

PORTS = re.compile(r"^/channel/v1/|^/admin/v1/channels|^/store/v1/me/(status|ordering|pause|hours-exceptions|menu/(bank|sizes|views|entries|vendor)|menu/items/:id/bank|menu/modifier-groups/:id/bank)")
MENU = f"{STORE_V1}/me/menu"


def _routes() -> list[dict]:
    body = requests.get(f"{MAIN_BASE_URL}/__routes", timeout=5).json()
    routes = body["routes"] if isinstance(body, dict) else body
    return [r for r in routes if PORTS.search(r["path"])]


def _path(p: str) -> str:
    return MAIN_BASE_URL + re.sub(r":[A-Za-z_]+", str(uuid.uuid4()), p)


def _call(method: str, url: str, **kw) -> requests.Response:
    return requests.request(method, url, json={} if method in ("POST", "PUT", "PATCH") else None, timeout=5, **kw)


def test_every_new_route_wants_its_own_credential(admin, store) -> None:
    routes = _routes()
    assert len(routes) >= 40, f"the port route table shrank: {len(routes)}"
    mgr = store.manager_session()
    key = admin.post(f"{ADMIN_V1}/channels", json={"location_id": store.location_id, "kind": "web", "name": "Web"}, timeout=5).json()["key"]
    admin_hdr = {"Authorization": admin.headers["Authorization"]}
    # An operator's channel sign-in (step 7) opens no store or admin route.
    user, pw = "iso" + uuid.uuid4().hex[:8], "pw-" + uuid.uuid4().hex[:6]
    op = admin.post(f"{ADMIN_V1}/operators", json={"username": user, "password": pw, "display_name": "Iso"}, timeout=5).json()
    admin.put(f"{ADMIN_V1}/operators/{op['id']}/locations", json={"location_ids": [store.location_id]}, timeout=5)
    op_token = requests.post(f"{MAIN_BASE_URL}/channel/v1/operator/login", json={"username": user, "password": pw}, timeout=5).json()["token"]
    op_hdr = {"Authorization": f"Bearer {op_token}", "X-Location-ID": store.location_id}
    bad = []
    try:
        for r in routes:
            m, url = r["method"], _path(r["path"])
            if r["auth"] == "none":
                continue
            tries = {"nothing": {}}
            if r["auth"] == "store-session":
                tries["channel key"] = {"headers": {"Authorization": f"Bearer {key}"}}
                tries["admin key"] = {"headers": admin_hdr}
                tries["operator sign-in"] = {"headers": op_hdr}
            elif r["auth"] == "channel":
                tries["manager cookie"] = {"cookies": mgr.cookies}
                tries["admin key"] = {"headers": admin_hdr}
                tries["wrong key"] = {"headers": {"Authorization": "Bearer qch_" + "0" * 64}}
            elif r["auth"] == "admin":
                tries["channel key"] = {"headers": {"Authorization": f"Bearer {key}"}}
                tries["manager cookie"] = {"cookies": mgr.cookies}
                tries["operator sign-in"] = {"headers": op_hdr}
            for what, kw in tries.items():
                code = _call(m, url, **kw).status_code
                if code != 401:
                    bad.append(f"{m} {r['path']} with {what}: {code}")
    finally:
        mgr.close()
        admin.delete(f"{ADMIN_V1}/operators/{op['id']}", timeout=5)
    assert not bad, "\n".join(bad)


@pytest.fixture
def two(make_store):
    a, b = make_store(), make_store()
    ma, mb = a.manager_session(), b.manager_session()
    yield a, ma, b, mb
    ma.close()
    mb.close()


def test_no_store_reaches_another_by_id(admin, two) -> None:
    a, ma, b, mb = two
    # Store A: a menu with an item, a group + option, a size, a view + entry, an exception.
    m = ma.post(f"{MENU}/menus", json={"name": "Main"}, timeout=5).json()
    cat = ma.post(f"{MENU}/menus/{m['id']}/categories", json={"name": "Pizza"}, timeout=5).json()
    item = ma.post(f"{MENU}/categories/{cat['id']}/items", json={"name": "Cheese Pizza", "price_cents": 999}, timeout=5).json()
    grp = ma.post(f"{MENU}/modifier-groups", json={"name": "Toppings"}, timeout=5).json()
    opt = ma.post(f"{MENU}/modifier-groups/{grp['id']}/options", json={"name": "Pepperoni", "price_delta_cents": 150}, timeout=5).json()
    size = ma.post(f"{MENU}/sizes", json={"name": "12 Medium"}, timeout=5).json()
    views = {v["kind"]: v for v in ma.get(f"{MENU}/views", timeout=5).json()}
    web = views["web"]
    entry = ma.post(f"{MENU}/views/{web['id']}/entries", json={"kind": "item", "anchor": {"item_ids": [item["id"]]}, "display_name": "Cheese"}, timeout=5).json()
    exc = ma.post(f"{STORE_V1}/me/hours-exceptions", json={"date": "2030-01-01", "closed": True}, timeout=5).json()
    before = ma.get(f"{MENU}/bank", timeout=5).json()

    # Store B tries every by-id route with A's ids.
    mb_size = mb.post(f"{MENU}/sizes", json={"name": "Big"}, timeout=5).json()
    attempts = [
        ("PATCH", f"{MENU}/sizes/{size['id']}", {"name": "x"}),
        ("DELETE", f"{MENU}/sizes/{size['id']}", None),
        ("POST", f"{MENU}/sizes/reorder", {"ids": [size["id"], mb_size["id"]]}),
        ("PUT", f"{MENU}/items/{item['id']}/bank", {"kitchen_name": "x", "sizes": []}),
        ("PUT", f"{MENU}/modifier-groups/{grp['id']}/bank", {"portions": True, "options": []}),
        ("GET", f"{MENU}/views/{web['id']}", None),
        ("PATCH", f"{MENU}/views/{web['id']}", {"name": "x"}),
        ("GET", f"{MENU}/views/{web['id']}/build", None),
        ("GET", f"{MENU}/views/{web['id']}/entries", None),
        ("POST", f"{MENU}/views/{web['id']}/entries", {"kind": "item", "anchor": {"item_ids": [item["id"]]}}),
        ("PUT", f"{MENU}/entries/{entry['id']}", {"anchor": {"item_ids": [item["id"]]}, "hidden": True}),
        ("DELETE", f"{MENU}/entries/{entry['id']}", None),
        ("DELETE", f"{MENU}/views/{web['id']}", None),
        ("DELETE", f"{STORE_V1}/me/hours-exceptions/{exc['id']}", None),
    ]
    bad = []
    for method, url, body in attempts:
        r = mb.request(method, url, json=body, timeout=5)
        if r.status_code not in (400, 404):
            bad.append(f"{method} {url.replace(MAIN_BASE_URL, '')}: {r.status_code} {r.text[:120]}")
    # Ids of A inside B's own bodies are refused too.
    b_views = {v["kind"]: v for v in mb.get(f"{MENU}/views", timeout=5).json()}
    for method, url, body in [
        ("POST", f"{MENU}/views/{b_views['web']['id']}/entries", {"kind": "item", "anchor": {"item_ids": [item["id"]]}}),
        ("POST", f"{MENU}/vendor/maps", {"vendor_name": "Pie", "item_id": item["id"]}),
    ]:
        r = mb.request(method, url, json=body, timeout=5)
        if r.status_code != 400:
            bad.append(f"{method} {url.replace(MAIN_BASE_URL, '')} with A's item: {r.status_code}")
    b_grp = mb.post(f"{MENU}/modifier-groups", json={"name": "Sauce"}, timeout=5).json()
    r = mb.put(f"{MENU}/modifier-groups/{b_grp['id']}/bank", json={"portions": False, "options": [
        {"option_id": opt["id"], "is_default": True, "kitchen_name": "x", "prices": []}]}, timeout=5)
    if r.status_code != 404:
        bad.append(f"B's group bank with A's option: {r.status_code}")
    assert not bad, "\n".join(bad)

    # A is untouched.
    assert ma.get(f"{MENU}/bank", timeout=5).json() == before
    assert ma.get(f"{MENU}/views/{web['id']}/entries", timeout=5).json()[0]["display_name"] == "Cheese"
    assert len(ma.get(f"{STORE_V1}/me/ordering", timeout=5).json()["exceptions"]) == 1

    # An admin can't point A's channel at B's view, nor at A's view of another kind.
    ch = admin.post(f"{ADMIN_V1}/channels", json={"location_id": a.location_id, "kind": "web", "name": "Web"}, timeout=5).json()
    assert admin.patch(f"{ADMIN_V1}/channels/{ch['id']}", json={"view_id": b_views["web"]["id"]}, timeout=5).status_code == 400
    assert admin.patch(f"{ADMIN_V1}/channels/{ch['id']}", json={"view_id": views["operator"]["id"]}, timeout=5).status_code == 400
    # A's channel only ever sees A.
    hdr = {"Authorization": f"Bearer {ch['key']}"}
    assert requests.get(f"{MAIN_BASE_URL}/channel/v1/store", headers=hdr, timeout=5).json()["store"]["name"] == a.name
    names = [it["name"] for c in requests.get(f"{MAIN_BASE_URL}/channel/v1/menu", headers=hdr, timeout=5).json()["categories"] for it in c["items"]]
    assert names == ["Cheese"]


def test_a_store_with_everything_deletes(admin, make_store) -> None:
    """A store with a menu, sizes, bank data, views + entries, a channel on a
    view, ordering settings, a pause and a special day deletes cleanly."""
    st = make_store()
    m = st.manager_session()
    try:
        menu = m.post(f"{MENU}/menus", json={"name": "Main"}, timeout=5).json()
        cat = m.post(f"{MENU}/menus/{menu['id']}/categories", json={"name": "Pizza"}, timeout=5).json()
        it = m.post(f"{MENU}/categories/{cat['id']}/items", json={"name": "Cheese", "price_cents": 999}, timeout=5).json()
        g = m.post(f"{MENU}/modifier-groups", json={"name": "Toppings"}, timeout=5).json()
        o = m.post(f"{MENU}/modifier-groups/{g['id']}/options", json={"name": "Ham"}, timeout=5).json()
        m.post(f"{MENU}/items/{it['id']}/modifier-groups", json={"group_id": g["id"]}, timeout=5)
        z = m.post(f"{MENU}/sizes", json={"name": "12"}, timeout=5).json()
        m.put(f"{MENU}/items/{it['id']}/bank", json={"sizes": [{"size_id": z["id"], "price_cents": 1099}]}, timeout=5)
        m.put(f"{MENU}/modifier-groups/{g['id']}/bank", json={"portions": True, "options": [
            {"option_id": o["id"], "is_default": False, "kitchen_name": "", "prices": [{"size_id": z["id"], "price_cents": 100}]}]}, timeout=5)
        web = next(v for v in m.get(f"{MENU}/views", timeout=5).json() if v["kind"] == "web")
        m.post(f"{MENU}/views/{web['id']}/entries", json={"kind": "item", "anchor": {"item_ids": [it["id"]]}, "display_name": "X"}, timeout=5)
        m.post(f"{MENU}/vendor/maps", json={"vendor_name": "Cheese Pie", "item_id": it["id"]}, timeout=5)
        ch = admin.post(f"{ADMIN_V1}/channels", json={"location_id": st.location_id, "kind": "web", "name": "Web"}, timeout=5).json()
        admin.patch(f"{ADMIN_V1}/channels/{ch['id']}", json={"view_id": web["id"]}, timeout=5)
        requests.get(f"{MAIN_BASE_URL}/channel/v1/menu", headers={"Authorization": f"Bearer {ch['key']}"}, timeout=5)
        m.post(f"{STORE_V1}/me/pause", json={"preset": "15m"}, timeout=5)
        m.post(f"{STORE_V1}/me/hours-exceptions", json={"date": "2030-01-01", "closed": True}, timeout=5)
    finally:
        m.close()
    r = admin.delete(f"{ADMIN_V1}/tenants/{st.tenant_id}", timeout=10)
    assert r.status_code in (200, 204), r.text
    assert admin.get(f"{ADMIN_V1}/channels/{ch['id']}", timeout=5).status_code == 404


def test_vendor_channel_cant_check_addresses(admin, store) -> None:
    key = admin.post(f"{ADMIN_V1}/channels", json={"location_id": store.location_id, "kind": "vendor", "name": "Feed"}, timeout=5).json()["key"]
    r = requests.post(f"{MAIN_BASE_URL}/channel/v1/store/delivery-check", json={"line1": "1 Main St", "zip": "07002"},
                      headers={"Authorization": f"Bearer {key}"}, timeout=5)
    assert r.status_code == 403 and r.json()["code"] == "forbidden"
