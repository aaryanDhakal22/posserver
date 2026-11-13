"""Store › Menu bulk edit: POST /store/v1/me/menu/items/bulk (contract/main.md §2.5b "Bulk edit").

Several items changed in one transaction: category, printer ("" = the category's), no kitchen ticket, in
stock, price, size prices, tax. All or nothing: one item / category / printer of another store, one size
the item doesn't come in, or one bad price, and nothing changes. Undo = the old values sent back.
"""

from __future__ import annotations

import pytest
import requests

from conftest import STORE_V1

ME = f"{STORE_V1}/me"
MENU = f"{ME}/menu"
BULK = f"{MENU}/items/bulk"


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


def _ok(r: requests.Response, status: int = 200) -> dict:
    assert r.status_code == status, f"{r.status_code}: {r.text}"
    return r.json() if r.content else {}


def _setup(mgr: requests.Session) -> dict:
    _ok(mgr.post(f"{MENU}/bank/apply", json={
        "format": 1, "views": [], "deals": [], "groups": [],
        "sizes": [{"name": "Small", "short": "S"}, {"name": "Large", "short": "L"}],
        "menus": [{"name": "Main", "categories": [
            {"name": "Pizza", "items": [
                {"name": "Cheese Pizza", "sizes": [{"size": "Small", "price_cents": 999}, {"size": "Large", "price_cents": 1499}]},
                {"name": "Garlic Knots", "price_cents": 499}]},
            {"name": "Sides", "items": [{"name": "Fries", "price_cents": 399}]}]}],
    }))
    bank = _ok(mgr.get(f"{MENU}/bank"))
    items = {it["name"]: it for m in bank["menus"] for c in m["categories"] for it in c["items"]}
    cats = {c["name"]: c for m in bank["menus"] for c in m["categories"]}
    sizes = {s["name"]: s["id"] for s in bank["sizes"]}
    printer = _ok(mgr.post(f"{ME}/printers", json={"name": "Kitchen", "ip": "10.0.0.7"}), 201)
    return {"items": items, "cats": cats, "sizes": sizes, "printer": printer}


def _bank_items(mgr: requests.Session) -> dict:
    bank = _ok(mgr.get(f"{MENU}/bank"))
    return {it["name"]: (c["name"], it) for m in bank["menus"] for c in m["categories"] for it in c["items"]}


def test_bulk_edit_applies_everything_and_undoes(store, mgr):
    s = _setup(mgr)
    pizza, knots, fries = (s["items"][n] for n in ("Cheese Pizza", "Garlic Knots", "Fries"))
    small, large = s["sizes"]["Small"], s["sizes"]["Large"]
    before_version = _ok(mgr.get(f"{MENU}/bank"))["version"]
    r = _ok(mgr.post(BULK, json={"changes": [
        {"item_id": pizza["id"], "printer_id": s["printer"]["id"], "size_prices": {small: 1099, large: 1599}, "tax_rate_bps": 700},
        {"item_id": knots["id"], "category_id": s["cats"]["Sides"]["id"], "in_stock": False, "price_cents": 549},
        {"item_id": fries["id"], "no_kitchen_ticket": True},
    ]}))
    assert r == {"updated": 3}
    got = _bank_items(mgr)
    cat, p = got["Cheese Pizza"]
    assert p["printer_id"] == s["printer"]["id"] and p["tax_rate_bps"] == 700
    assert sorted(x["price_cents"] for x in p["sizes"]) == [1099, 1599]
    cat, k = got["Garlic Knots"]
    assert cat == "Sides" and k["in_stock"] is False and k["price_cents"] == 549
    sides = _ok(mgr.get(f"{MENU}/categories/{s['cats']['Sides']['id']}/items"))
    assert [i["name"] for i in sides] == ["Fries", "Garlic Knots"]  # a moved item goes last
    assert got["Fries"][1]["no_kitchen_ticket"] is True
    assert _ok(mgr.get(f"{MENU}/bank"))["version"] > before_version

    # Undo = the old values back.
    _ok(mgr.post(BULK, json={"changes": [
        {"item_id": pizza["id"], "printer_id": "", "size_prices": {small: 999, large: 1499}, "tax_rate_bps": 0},
        {"item_id": knots["id"], "category_id": s["cats"]["Pizza"]["id"], "in_stock": True, "price_cents": 499},
        {"item_id": fries["id"], "no_kitchen_ticket": False},
    ]}))
    got = _bank_items(mgr)
    assert got["Cheese Pizza"][1]["printer_id"] is None and sorted(x["price_cents"] for x in got["Cheese Pizza"][1]["sizes"]) == [999, 1499]
    assert got["Garlic Knots"][0] == "Pizza" and got["Garlic Knots"][1]["in_stock"] is True
    assert not got["Fries"][1].get("no_kitchen_ticket")


@pytest.mark.parametrize("change, status, code", [
    ({"price_cents": -1}, 400, "bad_price"),
    ({"size_prices": {"00000000-0000-0000-0000-000000000001": 100}}, 400, "unknown_size"),
    ({"category_id": "00000000-0000-0000-0000-000000000001"}, 404, "not_found"),
    ({"printer_id": "00000000-0000-0000-0000-000000000001"}, 404, "not_found"),
    ({}, 400, "bad_body"),
])
def test_bulk_edit_is_all_or_nothing(store, mgr, change, status, code):
    s = _setup(mgr)
    good = {"item_id": s["items"]["Fries"]["id"], "price_cents": 1}
    bad = {"item_id": s["items"]["Garlic Knots"]["id"], **change}
    r = mgr.post(BULK, json={"changes": [good, bad]})
    assert r.status_code == status, r.text
    assert r.json()["code"] == code
    assert _bank_items(mgr)["Fries"][1]["price_cents"] == 399  # the good change didn't land either


def test_bulk_edit_refuses_another_stores_item_and_keeps_quick_add(store, mgr, make_store):
    s = _setup(mgr)
    other = make_store()
    o = other.manager_session()
    try:
        r = o.post(BULK, json={"changes": [{"item_id": s["items"]["Fries"]["id"], "in_stock": False}]})
        assert r.status_code == 404 and r.json()["code"] == "not_found"
    finally:
        o.close()
    qa = _ok(mgr.get(f"{MENU}/quick-add"))
    r = mgr.post(BULK, json={"changes": [{"item_id": qa["item_id"], "category_id": s["cats"]["Pizza"]["id"]}]})
    assert r.status_code == 409 and r.json()["code"] == "quick_add_kept"
    # Its printer may change like any item's.
    _ok(mgr.post(BULK, json={"changes": [{"item_id": qa["item_id"], "printer_id": s["printer"]["id"]}]}))
