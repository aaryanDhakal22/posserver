"""The menu bank + its views (main 3.0, PORTS-PLAN.md step 2).

  * sizes: the store-wide size list (add, duplicate, rename, reorder, delete,
    another store's size)
  * bank fields: an item's kitchen name / SKU / sizes, a group's halves flag
    and its options' defaults + per-size prices; bad sizes / prices refused
  * views: one default per kind on first use, rules (markup + round, hidden
    categories), entries (item override, merge, augment), anchors must be the
    store's own items, a vendor view only maps
  * /channel/v1/menu: the channel's view, OpenAPI-checked, ETag → 304, the
    version moves on an edit, a channel pointed at its own view, menu
    permission
  * the vendor view (Brygid): a name nothing routes is on its needs-attention
    list; mapping it routes the next order's line to that item's printer
"""

from __future__ import annotations

import pytest
import requests

from conftest import ADMIN_V1, MAIN_BASE_URL, STORE_V1, wait_order
from openapi import check

MENU = f"{STORE_V1}/me/menu"
CH_V1 = f"{MAIN_BASE_URL}/channel/v1"


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


def _ok(r: requests.Response, status: int = 200) -> dict:
    assert r.status_code == status, f"{r.status_code}: {r.text}"
    return r.json() if r.content else {}


def _bank(mgr) -> dict:
    """A small bank: Pizza (Cheese Pizza, Wings 8pc), Desserts (two
    cheesecakes), Drinks (Soda); sizes 12 / 16; a Toppings group on the pizza."""
    m = _ok(mgr.post(f"{MENU}/menus", json={"name": "Main"}, timeout=5), 201)
    cats, items = {}, {}
    for cat, names in {"Pizza": ["Cheese Pizza", "Wings 8pc"], "Desserts": ["Cheesecake Strawberry", "Cheesecake Oreo"],
                       "Drinks": ["Soda"]}.items():
        cats[cat] = _ok(mgr.post(f"{MENU}/menus/{m['id']}/categories", json={"name": cat}, timeout=5), 201)
        for n in names:
            items[n] = _ok(mgr.post(f"{MENU}/categories/{cats[cat]['id']}/items",
                                    json={"name": n, "price_cents": 1099}, timeout=5), 201)
    s12 = _ok(mgr.post(f"{MENU}/sizes", json={"name": "12 Medium", "short": "12\""}, timeout=5), 201)
    s16 = _ok(mgr.post(f"{MENU}/sizes", json={"name": "16 X-Large", "short": "16\""}, timeout=5), 201)
    g = _ok(mgr.post(f"{MENU}/modifier-groups", json={"name": "Toppings", "max_select": 5, "allow_multiple": True}, timeout=5), 201)
    pep = _ok(mgr.post(f"{MENU}/modifier-groups/{g['id']}/options", json={"name": "Pepperoni", "price_delta_cents": 150}, timeout=5), 201)
    _ok(mgr.post(f"{MENU}/items/{items['Cheese Pizza']['id']}/modifier-groups", json={"group_id": g["id"]}, timeout=5), 204)
    return {"menu": m, "cats": cats, "items": items, "s12": s12, "s16": s16, "group": g, "pep": pep}


def _sized_pizza(mgr, b) -> None:
    _ok(mgr.put(f"{MENU}/items/{b['items']['Cheese Pizza']['id']}/bank", json={
        "kitchen_name": "CHZ", "sku": "PZ-CHZ",
        "sizes": [{"size_id": b["s12"]["id"], "price_cents": 1199}, {"size_id": b["s16"]["id"], "price_cents": 1599}]}, timeout=5), 204)
    _ok(mgr.put(f"{MENU}/modifier-groups/{b['group']['id']}/bank", json={
        "portions": True, "options": [{"option_id": b["pep"]["id"], "is_default": False, "kitchen_name": "PEP",
                                       "prices": [{"size_id": b["s12"]["id"], "price_cents": 150},
                                                  {"size_id": b["s16"]["id"], "price_cents": 225}]}]}, timeout=5), 204)


def _views(mgr) -> dict[str, dict]:
    return {v["kind"]: v for v in _ok(mgr.get(f"{MENU}/views", timeout=5))}


def _items(build: dict) -> dict[str, dict]:
    return {it["name"]: it for c in build["categories"] for it in c["items"]}


def _channel(admin, store, kind: str = "web", **extra) -> dict:
    r = admin.post(f"{ADMIN_V1}/channels", json={"location_id": store.location_id, "kind": kind, "name": kind.title(), **extra}, timeout=5)
    return _ok(r, 201)


def _bearer(key: str) -> dict[str, str]:
    return {"Authorization": f"Bearer {key}"}


# --- sizes + bank fields ------------------------------------------------------------


def test_sizes(mgr, make_store) -> None:
    a = _ok(mgr.post(f"{MENU}/sizes", json={"name": "12 Medium", "short": "M"}, timeout=5), 201)
    b = _ok(mgr.post(f"{MENU}/sizes", json={"name": "16 X-Large"}, timeout=5), 201)
    assert (a["position"], b["position"]) == (0, 1)
    assert mgr.post(f"{MENU}/sizes", json={"name": "12 medium"}, timeout=5).status_code == 409
    assert mgr.post(f"{MENU}/sizes", json={"name": "  "}, timeout=5).status_code == 400
    _ok(mgr.patch(f"{MENU}/sizes/{a['id']}", json={"name": "12 Med", "short": "M"}, timeout=5))
    _ok(mgr.post(f"{MENU}/sizes/reorder", json={"ids": [b["id"], a["id"]]}, timeout=5), 204)
    assert [s["name"] for s in _ok(mgr.get(f"{MENU}/sizes", timeout=5))] == ["16 X-Large", "12 Med"]

    other = make_store().manager_session()
    try:
        assert other.patch(f"{MENU}/sizes/{a['id']}", json={"name": "x"}, timeout=5).status_code == 404
        assert other.delete(f"{MENU}/sizes/{a['id']}", timeout=5).status_code == 404
    finally:
        other.close()
    _ok(mgr.delete(f"{MENU}/sizes/{a['id']}", timeout=5), 204)
    assert [s["name"] for s in _ok(mgr.get(f"{MENU}/sizes", timeout=5))] == ["16 X-Large"]


def test_bank_fields(mgr, make_store) -> None:
    b = _bank(mgr)
    _sized_pizza(mgr, b)
    bank = _ok(mgr.get(f"{MENU}/bank", timeout=5))
    assert [s["name"] for s in bank["sizes"]] == ["12 Medium", "16 X-Large"]
    pizza = next(it for m in bank["menus"] for c in m["categories"] for it in c["items"] if it["name"] == "Cheese Pizza")
    assert pizza["kitchen_name"] == "CHZ" and pizza["sku"] == "PZ-CHZ" and pizza["group_ids"] == [b["group"]["id"]]
    assert [(s["size_id"], s["price_cents"]) for s in pizza["sizes"]] == [(b["s12"]["id"], 1199), (b["s16"]["id"], 1599)]
    g = bank["groups"][b["group"]["id"]]
    assert g["portions"] is True and g["options"][0]["kitchen_name"] == "PEP" and len(g["options"][0]["prices"]) == 2

    url = f"{MENU}/items/{b['items']['Cheese Pizza']['id']}/bank"
    foreign = make_store().manager_session()
    try:
        fs = _ok(foreign.post(f"{MENU}/sizes", json={"name": "Huge"}, timeout=5), 201)
        assert foreign.put(url, json={"sizes": []}, timeout=5).status_code == 404, "another store's item"
    finally:
        foreign.close()
    assert mgr.put(url, json={"sizes": [{"size_id": fs["id"], "price_cents": 1}]}, timeout=5).status_code == 400
    assert mgr.put(url, json={"sizes": [{"size_id": b["s12"]["id"], "price_cents": -1}]}, timeout=5).status_code == 400
    dup = [{"size_id": b["s12"]["id"], "price_cents": 1}] * 2
    assert mgr.put(url, json={"sizes": dup}, timeout=5).status_code == 400

    # Deleting a size drops its prices with it.
    _ok(mgr.delete(f"{MENU}/sizes/{b['s16']['id']}", timeout=5), 204)
    bank = _ok(mgr.get(f"{MENU}/bank", timeout=5))
    pizza = next(it for m in bank["menus"] for c in m["categories"] for it in c["items"] if it["name"] == "Cheese Pizza")
    assert [s["price_cents"] for s in pizza["sizes"]] == [1199]


# --- views ------------------------------------------------------------------------------


def test_default_views_and_rules(mgr) -> None:
    b = _bank(mgr)
    _sized_pizza(mgr, b)
    views = _views(mgr)
    assert {k: v["name"] for k, v in views.items()} == {"web": "Web", "operator": "Call center", "counter": "Counter", "vendor": "Brygid"}
    assert len(_ok(mgr.get(f"{MENU}/views", timeout=5))) == 4, "defaults are created once"

    web = views["web"]
    as_is = _ok(mgr.get(f"{MENU}/views/{web['id']}/build", timeout=5))
    assert _items(as_is)["Cheese Pizza"]["sizes"][1]["price_cents"] == 1599 and as_is["needs_attention"] == []

    bad = {"markup_bps": 20000, "round": "none", "hidden_categories": []}
    assert mgr.patch(f"{MENU}/views/{web['id']}", json={"rules": bad}, timeout=5).status_code == 400
    assert mgr.patch(f"{MENU}/views/{web['id']}", json={"rules": {"round": "floor"}}, timeout=5).status_code == 400
    rules = {"markup_bps": 2000, "round": "ceil_99", "hidden_categories": [b["cats"]["Drinks"]["id"]], "require_photo": False}
    _ok(mgr.patch(f"{MENU}/views/{web['id']}", json={"rules": rules}, timeout=5))
    built = _ok(mgr.get(f"{MENU}/views/{web['id']}/build", timeout=5))
    its = _items(built)
    assert "Soda" not in its and [s["price_cents"] for s in its["Cheese Pizza"]["sizes"]] == [1499, 1999]
    assert built["modifier_groups"][0]["portions"] is True and its["Cheese Pizza"]["modifier_group_ids"] == [b["group"]["id"]]
    # The call center still reads the bank as is.
    assert "Soda" in _items(_ok(mgr.get(f"{MENU}/views/{views['operator']['id']}/build", timeout=5)))

    extra = _ok(mgr.post(f"{MENU}/views", json={"kind": "web", "name": "Catering"}, timeout=5), 201)
    assert mgr.post(f"{MENU}/views", json={"kind": "fax", "name": "x"}, timeout=5).status_code == 400
    _ok(mgr.delete(f"{MENU}/views/{extra['id']}", timeout=5), 204)
    assert mgr.get(f"{MENU}/views/{extra['id']}", timeout=5).status_code == 404


def test_entries(mgr, make_store) -> None:
    b = _bank(mgr)
    it = b["items"]
    web = _views(mgr)["web"]
    url = f"{MENU}/views/{web['id']}/entries"
    over = _ok(mgr.post(url, json={"kind": "item", "anchor": {"item_ids": [it["Wings 8pc"]["id"]]},
                                   "display_name": "Party Wings (8)", "category": "Wings", "price_override_cents": 1299}, timeout=5), 201)
    _ok(mgr.post(url, json={"kind": "merge", "anchor": {"item_ids": [it["Cheesecake Strawberry"]["id"], it["Cheesecake Oreo"]["id"]]},
                            "display_name": "Cheesecake", "extra": {"labels": {it["Cheesecake Strawberry"]["id"]: "Strawberry"}}}, timeout=5), 201)
    _ok(mgr.post(url, json={"kind": "augment", "anchor": {"item_ids": [it["Cheese Pizza"]["id"], it["Wings 8pc"]["id"]]},
                            "display_name": "Pizza + Wings", "category": "Deals", "price_override_cents": 2299}, timeout=5), 201)
    its = _items(_ok(mgr.get(f"{MENU}/views/{web['id']}/build", timeout=5)))
    assert its["Party Wings (8)"]["price_cents"] == 1299 and "Wings 8pc" not in its
    assert [c["name"] for c in its["Cheesecake"]["choices"]] == ["Strawberry", "Cheesecake Oreo"] and "Cheesecake Oreo" not in its
    assert its["Pizza + Wings"]["price_cents"] == 2299 and len(its["Pizza + Wings"]["anchor"]["item_ids"]) == 2

    # Edit + delete; the build follows.
    _ok(mgr.put(f"{MENU}/entries/{over['id']}", json={"anchor": {"item_ids": [it["Wings 8pc"]["id"]]}, "hidden": True}, timeout=5))
    assert "Wings 8pc" not in _items(_ok(mgr.get(f"{MENU}/views/{web['id']}/build", timeout=5)))
    _ok(mgr.delete(f"{MENU}/entries/{over['id']}", timeout=5), 204)
    assert "Wings 8pc" in _items(_ok(mgr.get(f"{MENU}/views/{web['id']}/build", timeout=5)))
    assert len(_ok(mgr.get(url, timeout=5))) == 2

    # Refused: another store's item, a one-item merge, an augment without a
    # price, a map on a web view, an item entry on the vendor view.
    other = make_store().manager_session()
    try:
        ob = _bank(other)
        assert other.get(f"{MENU}/views/{web['id']}/entries", timeout=5).status_code == 404
    finally:
        other.close()
    one = lambda i: {"item_ids": [i]}  # noqa: E731
    for body in ({"kind": "item", "anchor": one(ob["items"]["Soda"]["id"])},
                 {"kind": "merge", "anchor": one(it["Soda"]["id"]), "display_name": "x"},
                 {"kind": "augment", "anchor": one(it["Soda"]["id"]), "display_name": "x"},
                 {"kind": "map", "anchor": one(it["Soda"]["id"]), "extra": {"vendor_name": "Pop"}},
                 {"kind": "item", "anchor": one(it["Soda"]["id"]), "price_override_cents": -5},
                 {"kind": "item", "anchor": one(it["Soda"]["id"]), "extra": {"size_prices": {ob["s12"]["id"]: 100}}}):
        assert mgr.post(url, json=body, timeout=5).status_code == 400, body
    vendor = _views(mgr)["vendor"]
    assert mgr.post(f"{MENU}/views/{vendor['id']}/entries", json={"kind": "item", "anchor": one(it["Soda"]["id"])},
                    timeout=5).status_code == 400


# --- /channel/v1/menu ----------------------------------------------------------------


def test_channel_menu(admin, store, mgr) -> None:
    b = _bank(mgr)
    _sized_pizza(mgr, b)
    ch = _channel(admin, store, "web")
    hdr = _bearer(ch["key"])

    r = requests.get(f"{CH_V1}/menu", headers=hdr, timeout=5)
    menu = check(r, "getMenu")
    assert menu["view"]["kind"] == "web" and menu["view"]["name"] == "Web" and "needs_attention" not in menu
    pizza = _items(menu)["Cheese Pizza"]
    assert pizza["anchor"]["item_ids"] == [b["items"]["Cheese Pizza"]["id"]]
    assert [s["price_cents"] for s in pizza["sizes"]] == [1199, 1599]
    groups = {g["id"]: g for g in menu["modifier_groups"]}
    assert pizza["modifier_group_ids"] == [b["group"]["id"]] and len(groups) == 1
    assert groups[b["group"]["id"]]["options"][0]["prices"][1]["price_cents"] == 225

    etag = r.headers["ETag"]
    assert etag == f'"v{menu["version"]}"'
    assert requests.get(f"{CH_V1}/menu", headers={**hdr, "If-None-Match": etag}, timeout=5).status_code == 304
    v = check(requests.get(f"{CH_V1}/menu/version", params={"since": menu["version"]}, headers=hdr, timeout=5), "getMenuVersion")
    assert v["version"] == menu["version"] and v["changed"] is False

    # Any bank edit moves the version.
    _ok(mgr.patch(f"{MENU}/items/{b['items']['Soda']['id']}", json={"name": "Soda", "price_cents": 249, "in_stock": True}, timeout=5))
    v = check(requests.get(f"{CH_V1}/menu/version", params={"since": menu["version"]}, headers=hdr, timeout=5), "getMenuVersion")
    assert v["changed"] is True and v["version"] > menu["version"]
    r = requests.get(f"{CH_V1}/menu", headers={**hdr, "If-None-Match": etag}, timeout=5)
    assert _items(check(r, "getMenu"))["Soda"]["price_cents"] == 249
    assert check(requests.get(f"{CH_V1}/menu/version", params={"since": "x"}, headers=hdr, timeout=5), "getMenuVersion") is not None

    # Point the channel at its own view; a view of another kind is refused.
    cat = _ok(mgr.post(f"{MENU}/views", json={"kind": "web", "name": "Catering", "rules": {"markup_bps": 1000}}, timeout=5), 201)
    op = _views(mgr)["operator"]
    assert admin.patch(f"{ADMIN_V1}/channels/{ch['id']}", json={"view_id": op["id"]}, timeout=5).status_code == 400
    assert _ok(admin.patch(f"{ADMIN_V1}/channels/{ch['id']}", json={"view_id": cat["id"]}, timeout=5))["view_id"] == cat["id"]
    menu = check(requests.get(f"{CH_V1}/menu", headers=hdr, timeout=5), "getMenu")
    assert menu["view"]["name"] == "Catering" and _items(menu)["Wings 8pc"]["price_cents"] == 1209
    assert _ok(admin.patch(f"{ADMIN_V1}/channels/{ch['id']}", json={"view_id": None}, timeout=5))["view_id"] is None
    assert check(requests.get(f"{CH_V1}/menu", headers=hdr, timeout=5), "getMenu")["view"]["name"] == "Web"

    # A vendor feed may not read the menu; no key → 401.
    vendor = _channel(admin, store, "vendor")
    check(requests.get(f"{CH_V1}/menu", headers=_bearer(vendor["key"]), timeout=5), "getMenu")
    assert requests.get(f"{CH_V1}/menu", headers=_bearer(vendor["key"]), timeout=5).status_code == 403
    assert check(requests.get(f"{CH_V1}/menu", timeout=5), "getMenu") is not None


# --- the vendor view (Brygid) ----------------------------------------------------------


def _line(name: str) -> dict:
    return {"name": name, "size_name": "", "size_id": 1, "quantity": 1, "price": 5.0, "plu": "",
            "who": "", "group_id": "", "notes": "", "modifiers": []}


def test_vendor_mapping_routes_orders(store, mgr, publish) -> None:
    b = _bank(mgr)
    wings_printer = _ok(mgr.post(f"{STORE_V1}/me/printers", json={"name": "Wings", "ip": "10.0.0.3"}, timeout=5), 201)
    _ok(mgr.patch(f"{MENU}/items/{b['items']['Wings 8pc']['id']}/printer", json={"printer_id": wings_printer["id"]}, timeout=5), 204)

    o = store.order(items=[_line("Jumbo Party Wings")])
    publish(o)
    first = wait_order(store.key, o["order_id"])["items"][0]
    assert first["routing"] == "unmatched", first
    vendor = _ok(mgr.get(f"{MENU}/vendor", timeout=5))
    assert vendor["view"]["kind"] == "vendor" and vendor["categories"] == []
    att = [n for n in vendor["needs_attention"] if n["code"] == "unmatched"]
    assert [(n["name"], n.get("orders")) for n in att] == [("Jumbo Party Wings", 1)], vendor["needs_attention"]

    e = _ok(mgr.post(f"{MENU}/vendor/maps", json={"vendor_name": "jumbo party  wings", "item_id": b["items"]["Wings 8pc"]["id"]}, timeout=5), 201)
    assert e["kind"] == "map" and e["extra"]["vendor_name"] == "jumbo party  wings"
    vendor = _ok(mgr.get(f"{MENU}/vendor", timeout=5))
    assert vendor["needs_attention"] == [] and vendor["maps"][0]["item_id"] == b["items"]["Wings 8pc"]["id"]
    assert mgr.post(f"{MENU}/vendor/maps", json={"vendor_name": "", "item_id": b["items"]["Soda"]["id"]}, timeout=5).status_code == 400

    o2 = store.order(items=[_line("JUMBO party wings"), _line("Cheese Pizza")])
    publish(o2)
    by = {i["name"].lower(): i for i in wait_order(store.key, o2["order_id"])["items"]}
    w = by["jumbo party wings"]
    assert w["routing"] == "matched" and w["menu_item_id"] == b["items"]["Wings 8pc"]["id"] and w["printer_id"] == wings_printer["id"], w
    assert by["cheese pizza"]["routing"] == "unassigned" and by["cheese pizza"]["menu_item_id"] == b["items"]["Cheese Pizza"]["id"]
