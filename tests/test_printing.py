"""Kitchen printing, main's half: the receipt-printer flag and item→printer
routing stamped at ingest.

main matches each vendor item to a menu item by exact name (case/whitespace
insensitive, size ignored) and stamps `printer_id` + `routing` on the line
item, so the agent can split the order into station tickets.
"""

import json

import pytest

from conftest import STORE_V1, wait_order
from sse import SSEStream


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


def _printer(mgr, name: str, ip: str) -> dict:
    r = mgr.post(f"{STORE_V1}/me/printers", json={"name": name, "ip": ip}, timeout=5)
    assert r.status_code == 201, r.text
    return r.json()


def _printers(mgr) -> dict[str, dict]:
    r = mgr.get(f"{STORE_V1}/me/printers", timeout=5)
    assert r.status_code == 200, r.text
    return {p["name"]: p for p in r.json()}


def test_first_printer_is_receipt_and_flag_is_unique(mgr) -> None:
    front = _printer(mgr, "Front", "10.0.0.1")
    pizza = _printer(mgr, "Pizza", "10.0.0.2")
    ps = _printers(mgr)
    assert ps["Front"]["is_receipt"] is True, "first printer should become the receipt printer"
    assert ps["Pizza"]["is_receipt"] is False

    r = mgr.patch(f"{STORE_V1}/me/printers/{pizza['id']}", json={"name": "Pizza", "ip": "10.0.0.2", "is_receipt": True}, timeout=5)
    assert r.status_code == 200, r.text
    ps = _printers(mgr)
    assert ps["Pizza"]["is_receipt"] is True and ps["Front"]["is_receipt"] is False, ps

    # Deleting the receipt printer hands the flag to a remaining printer.
    assert mgr.delete(f"{STORE_V1}/me/printers/{pizza['id']}", timeout=5).status_code == 204
    ps = _printers(mgr)
    assert ps["Front"]["is_receipt"] is True, ps
    assert front["id"] == ps["Front"]["id"]


def test_receipt_flag_rides_store_config(store, mgr) -> None:
    _printer(mgr, "Front", "10.0.0.1")
    with SSEStream(store.key) as s:
        cfg = json.loads(s.next()["data"])
        assert [p["is_receipt"] for p in cfg["printers"]] == [True], cfg["printers"]


def test_items_routed_to_printers_at_ingest(store, mgr, publish) -> None:
    _printer(mgr, "Front", "10.0.0.1")
    pizza = _printer(mgr, "Pizza", "10.0.0.2")
    wings = _printer(mgr, "Wings", "10.0.0.3")

    menu = mgr.post(f"{STORE_V1}/me/menu/menus", json={"name": "Main"}, timeout=5).json()
    # Category default printer → Wings; one item overrides to Pizza; one has neither.
    cat = mgr.post(f"{STORE_V1}/me/menu/menus/{menu['id']}/categories",
                   json={"name": "Food", "default_printer_id": wings["id"]}, timeout=5).json()
    pep = mgr.post(f"{STORE_V1}/me/menu/categories/{cat['id']}/items",
                   json={"name": "Pepperoni Pizza", "price_cents": 1500}, timeout=5).json()
    assert mgr.patch(f"{STORE_V1}/me/menu/items/{pep['id']}/printer",
                     json={"printer_id": pizza["id"]}, timeout=5).status_code == 204
    mgr.post(f"{STORE_V1}/me/menu/categories/{cat['id']}/items",
             json={"name": "Hot Wings", "price_cents": 900}, timeout=5)
    plain = mgr.post(f"{STORE_V1}/me/menu/menus/{menu['id']}/categories", json={"name": "Drinks"}, timeout=5).json()
    mgr.post(f"{STORE_V1}/me/menu/categories/{plain['id']}/items", json={"name": "Soda", "price_cents": 200}, timeout=5)

    def it(name: str, size: str = "") -> dict:
        return {"name": name, "size_name": size, "size_id": 1, "quantity": 1, "price": 5.0, "plu": "",
                "who": "", "group_id": "", "notes": "", "modifiers": []}

    o = store.order(items=[it("  pepperoni   PIZZA ", "Large"), it("hot wings"), it("Soda"), it("Mystery Dish")])
    with SSEStream(store.key) as s:
        assert s.next()["event"] == "store_config"
        publish(o)
        frame = s.wait_event("order", lambda d: d.get("source_event_id") == str(o["order_id"]))
        assert frame, "order never reached the SSE stream"

    stored = wait_order(store.key, o["order_id"])
    for items in (frame["items"], stored["items"]):
        by = {i["name"].strip().lower(): i for i in items}
        p = by["pepperoni   pizza"]
        assert p["routing"] == "matched" and p["printer_id"] == pizza["id"] and p["menu_item_id"] == pep["id"], p
        assert p["size"] == "Large", "size is split off the name"
        assert by["hot wings"]["routing"] == "matched" and by["hot wings"]["printer_id"] == wings["id"], "category default"
        assert by["soda"]["routing"] == "unassigned" and not by["soda"].get("printer_id"), by["soda"]
        assert by["mystery dish"]["routing"] == "unmatched" and not by["mystery dish"].get("menu_item_id")


def test_order_without_menu_is_unmatched(store, publish) -> None:
    o = store.order()
    publish(o)
    stored = wait_order(store.key, o["order_id"])
    item = stored["items"][0]
    assert item["name"] == "Test Pizza" and item["size"] == "12 Medium", item
    assert item["routing"] == "unmatched", item


def test_unmatched_names_report(store, mgr, publish) -> None:
    """Recent vendor item names that don't reach a kitchen printer, so the
    manager can fix menu names / printers."""
    pizza = _printer(mgr, "Pizza", "10.0.0.2")
    menu = mgr.post(f"{STORE_V1}/me/menu/menus", json={"name": "Main"}, timeout=5).json()
    cat = mgr.post(f"{STORE_V1}/me/menu/menus/{menu['id']}/categories",
                   json={"name": "Pizzas", "default_printer_id": pizza["id"]}, timeout=5).json()
    mgr.post(f"{STORE_V1}/me/menu/categories/{cat['id']}/items", json={"name": "Cheese Pizza", "price_cents": 1000}, timeout=5)
    bare = mgr.post(f"{STORE_V1}/me/menu/menus/{menu['id']}/categories", json={"name": "Sides"}, timeout=5).json()
    mgr.post(f"{STORE_V1}/me/menu/categories/{bare['id']}/items", json={"name": "Fries", "price_cents": 300}, timeout=5)

    def it(name: str) -> dict:
        return {"name": name, "size_name": "", "size_id": 1, "quantity": 1, "price": 5.0, "plu": "",
                "who": "", "group_id": "", "notes": "", "modifiers": []}

    o = store.order(items=[it("cheese pizza"), it("Fries"), it("Garlic Knots"), it("GARLIC knots")])
    publish(o)
    wait_order(store.key, o["order_id"])
    rep = mgr.get(f"{STORE_V1}/me/menu/unmatched", params={"days": 7}, timeout=5).json()
    got = {(r["name"].lower(), r["routing"], r["orders"]) for r in rep}
    assert got == {("garlic knots", "unmatched", 2), ("fries", "unassigned", 1)}, rep
