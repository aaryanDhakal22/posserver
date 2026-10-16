"""Round-trip for the menu module + operators.

Covers the store-manager menu editor (menus → categories → items → reusable
modifier groups) over the session cookie, and the operator lifecycle (admin
create → login → read allowed menu → token revoke).

Self-provisioning: each test runs against a throwaway tenant (conftest `store`)
with its own manager login — no dependence on the seeded dev restaurants.
Deleting the tenant at teardown cascades its menus; groups + operators are
deleted explicitly.
"""

import secrets

import pytest
import requests

from conftest import ADMIN_V1 as ADMIN_BASE, STORE_V1 as STORE_BASE

TAG = secrets.token_hex(4)  # unique-per-run suffix so names never collide


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    s.headers.update({"Content-Type": "application/json"})
    yield s
    s.close()


@pytest.fixture
def location_id(store) -> str:
    return store.location_id


def test_menu_round_trip(mgr) -> None:
    menu_id = cat_id = item_id = group_id = None
    try:
        # menu → category → item
        r = mgr.post(f"{STORE_BASE}/me/menu/menus", json={"name": f"Dinner {TAG}"}, timeout=5)
        assert r.status_code == 201, r.text
        menu_id = r.json()["id"]

        r = mgr.post(
            f"{STORE_BASE}/me/menu/menus/{menu_id}/categories",
            json={"name": "Mains"},
            timeout=5,
        )
        assert r.status_code == 201, r.text
        cat_id = r.json()["id"]

        r = mgr.post(
            f"{STORE_BASE}/me/menu/categories/{cat_id}/items",
            json={"name": "Burger", "price_cents": 500},
            timeout=5,
        )
        assert r.status_code == 201, r.text
        item_id = r.json()["id"]
        assert r.json()["price_cents"] == 500

        # edit item: price in cents + tax in bps + tags + stock
        r = mgr.patch(
            f"{STORE_BASE}/me/menu/items/{item_id}",
            json={
                "name": "Deluxe Burger",
                "description": "with cheese",
                "price_cents": 1299,
                "tax_rate_bps": 600,
                "in_stock": True,
                "out_until": None,
                "tags": ["Popular"],
                "attributes": {},
            },
            timeout=5,
        )
        assert r.status_code == 200, r.text
        body = r.json()
        assert body["price_cents"] == 1299 and body["tax_rate_bps"] == 600
        assert body["tags"] == ["Popular"]

        # reusable modifier group + option, then attach to the item
        r = mgr.post(
            f"{STORE_BASE}/me/menu/modifier-groups",
            json={"name": f"Add-ons {TAG}", "min_select": 0, "max_select": 2},
            timeout=5,
        )
        assert r.status_code == 201, r.text
        group_id = r.json()["id"]

        r = mgr.post(
            f"{STORE_BASE}/me/menu/modifier-groups/{group_id}/options",
            json={"name": "Bacon", "price_delta_cents": 150},
            timeout=5,
        )
        assert r.status_code == 201, r.text
        assert r.json()["price_delta_cents"] == 150

        r = mgr.post(
            f"{STORE_BASE}/me/menu/items/{item_id}/modifier-groups",
            json={"group_id": group_id},
            timeout=5,
        )
        assert r.status_code == 204, r.text

        r = mgr.get(f"{STORE_BASE}/me/menu/items/{item_id}/modifier-groups", timeout=5)
        assert r.status_code == 200, r.text
        assert any(link["group"]["id"] == group_id for link in r.json())
    finally:
        if group_id:
            mgr.delete(f"{STORE_BASE}/me/menu/modifier-groups/{group_id}", timeout=5)
        if menu_id:
            # cascades categories + items
            mgr.delete(f"{STORE_BASE}/me/menu/menus/{menu_id}", timeout=5)


def test_menu_rejects_cross_location(mgr) -> None:
    """A random uuid is out of the session's location scope → 404, not 500."""
    bogus = "00000000-0000-0000-0000-0000000000ff"
    r = mgr.patch(
        f"{STORE_BASE}/me/menu/items/{bogus}",
        json={"name": "x", "price_cents": 0, "tax_rate_bps": 0, "in_stock": True,
              "tags": [], "attributes": {}},
        timeout=5,
    )
    assert r.status_code == 404, r.text


def test_operator_lifecycle(admin, mgr, location_id) -> None:
    username = f"op_{TAG}"
    op_id = None
    try:
        # admin creates the operator
        r = admin.post(
            f"{ADMIN_BASE}/operators",
            json={"username": username, "password": "s3cret-pw", "display_name": "Smoke Op"},
            timeout=5,
        )
        assert r.status_code == 201, r.text
        op_id = r.json()["id"]

        # grant the session's location
        r = admin.put(
            f"{ADMIN_BASE}/operators/{op_id}/locations",
            json={"location_ids": [location_id]},
            timeout=5,
        )
        assert r.status_code == 204, r.text

        # operator logs in → read-only bearer token
        r = requests.post(
            f"{STORE_BASE}/operator/login",
            json={"username": username, "password": "s3cret-pw"},
            timeout=5,
        )
        assert r.status_code == 200, r.text
        token = r.json()["token"]
        assert token.startswith("qopr_")
        opsess = requests.Session()
        opsess.headers.update({"Authorization": f"Bearer {token}"})

        # reads the allowed location's menu
        r = opsess.get(f"{STORE_BASE}/operator/menu", params={"location_id": location_id}, timeout=5)
        assert r.status_code == 200, r.text

        # a not-allowed location is forbidden
        r = opsess.get(
            f"{STORE_BASE}/operator/menu",
            params={"location_id": "00000000-0000-0000-0000-0000000000ff"},
            timeout=5,
        )
        assert r.status_code == 403, r.text

        # revoke → token dies immediately
        r = admin.post(f"{ADMIN_BASE}/operators/{op_id}/tokens/revoke", timeout=5)
        assert r.status_code == 204, r.text
        r = opsess.get(f"{STORE_BASE}/operator/me", timeout=5)
        assert r.status_code == 401, r.text
    finally:
        if op_id:
            admin.delete(f"{ADMIN_BASE}/operators/{op_id}", timeout=5)
