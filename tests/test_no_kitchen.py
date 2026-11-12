""""No kitchen ticket" (owner 2026-11-12, migration 00041; contract/main.md
§2.5zb): an item (or a category, for its items without a printer of their
own) that gets no station ticket and isn't in "Also on this order"; it still
prints on the receipt.

  * the bank document carries `no_kitchen_ticket` on items + categories
    (export → apply is a no-op; false clears; absent keeps)
  * the counter menu's routes: {printer_id: "", no_kitchen: true}
  * an online order's line matched to such an item: routing "none", no printer
  * never in the unmatched-names report
  * Store › Menu's API: PATCH …/items/:id/printer and …/categories/:id take it
"""

from __future__ import annotations

import copy

import pytest
import requests

from conftest import STORE_V1, store_key, wait_order

MENU = f"{STORE_V1}/me/menu"
APPLY = f"{MENU}/bank/apply"
EXPORT = f"{MENU}/bank/document"
COUNTER_MENU = f"{STORE_V1}/agent/counter-menu"


def _ok(r: requests.Response, status: int = 200):
    assert r.status_code == status, f"{r.status_code}: {r.text}"
    return r.json() if r.content else {}


def _doc() -> dict:
    return {
        "format": 1, "sizes": [], "groups": [],
        "menus": [{"name": "Counter", "categories": [
            {"name": "Misc", "printer": "Wings", "items": [
                {"name": "Ranch", "price_cents": 75},
                {"name": "Coke 2 Liter", "price_cents": 399, "no_kitchen_ticket": True}]},
            {"name": "Drinks", "no_kitchen_ticket": True, "items": [
                {"name": "Pepsi Can", "price_cents": 150},
                {"name": "Hot Sauce Cup", "price_cents": 50, "printer": "Wings"}]}]}],
        "views": [{"kind": "counter", "name": "Counter", "rules": {"markup_bps": 0, "round": "none",
                                                                  "require_photo": False, "hidden_categories": []},
                   "entries": []}],
        "deals": [],
    }


@pytest.fixture
def nk_store(store):
    mgr = store.manager_session()
    printers = {}
    for name, ip in (("Front", "10.0.0.1"), ("Wings", "10.0.0.3")):
        printers[name] = _ok(mgr.post(f"{STORE_V1}/me/printers", json={"name": name, "ip": ip}, timeout=5), 201)["id"]
    res = _ok(mgr.post(APPLY, json=_doc(), timeout=20))
    assert res["applied"] is True, res
    yield store, mgr, printers
    mgr.close()


def _bank_items(doc: dict) -> dict:
    return {i["name"]: i for m in doc["menus"] for c in m["categories"] for i in c["items"]}


def test_bank_document_round_trip(nk_store) -> None:
    _, mgr, _ = nk_store
    doc = _ok(mgr.get(EXPORT, timeout=10))
    items = _bank_items(doc)
    cats = {c["name"]: c for m in doc["menus"] for c in m["categories"]}
    assert items["Coke 2 Liter"]["no_kitchen_ticket"] is True and items["Ranch"]["no_kitchen_ticket"] is False
    assert cats["Drinks"]["no_kitchen_ticket"] is True and cats["Misc"]["no_kitchen_ticket"] is False
    assert _ok(mgr.post(APPLY, params={"dry_run": 1}, json=doc, timeout=20))["changes"] == []

    d2 = copy.deepcopy(doc)
    for m in d2["menus"]:
        for c in m["categories"]:
            if c["name"] == "Drinks":
                c.pop("no_kitchen_ticket")  # absent keeps
            for i in c["items"]:
                if i["name"] == "Coke 2 Liter":
                    i["no_kitchen_ticket"] = False  # false clears
    res = _ok(mgr.post(APPLY, json=d2, timeout=20))
    assert 'item "Coke 2 Liter": no_kitchen_ticket' in res["changes"], res["changes"]
    again = _ok(mgr.get(EXPORT, timeout=10))
    assert _bank_items(again)["Coke 2 Liter"]["no_kitchen_ticket"] is False
    assert {c["name"]: c for m in again["menus"] for c in m["categories"]}["Drinks"]["no_kitchen_ticket"] is True


def test_counter_menu_routes(nk_store) -> None:
    store, mgr, printers = nk_store
    body = _ok(requests.get(COUNTER_MENU, headers=store_key(store.key), timeout=10))
    ids = {i["name"]: i["id"] for c in body["build"]["categories"] for i in c["items"]}
    routes = body["routes"]
    for n in ("Coke 2 Liter", "Pepsi Can"):  # own flag; the category's (no own printer)
        assert routes[ids[n]] == {"printer_id": "", "no_kitchen": True}, (n, routes.get(ids[n]))
    for n in ("Ranch", "Hot Sauce Cup"):  # category default; own printer beats the category's flag
        r = routes[ids[n]]
        assert r["printer_id"] == printers["Wings"] and r["name"] == "Wings" and not r.get("no_kitchen"), (n, r)

    # Store › Menu: the item printer route takes the flag; the version moves.
    v0 = body["menu_version"]
    _ok(mgr.patch(f"{MENU}/items/{ids['Ranch']}/printer", json={"printer_id": None, "no_kitchen_ticket": True}, timeout=5), 204)
    body = _ok(requests.get(COUNTER_MENU, headers=store_key(store.key), timeout=10))
    assert body["menu_version"] > v0 and body["routes"][ids["Ranch"]] == {"printer_id": "", "no_kitchen": True}
    # Absent keeps it; false clears it.
    _ok(mgr.patch(f"{MENU}/items/{ids['Ranch']}/printer", json={"printer_id": None}, timeout=5), 204)
    assert _ok(requests.get(COUNTER_MENU, headers=store_key(store.key), timeout=10))["routes"][ids["Ranch"]]["no_kitchen"] is True
    _ok(mgr.patch(f"{MENU}/items/{ids['Ranch']}/printer", json={"printer_id": None, "no_kitchen_ticket": False}, timeout=5), 204)
    assert _ok(requests.get(COUNTER_MENU, headers=store_key(store.key), timeout=10))["routes"][ids["Ranch"]]["printer_id"] == printers["Wings"]

    # A category: PATCH takes it (absent keeps it).
    menus = _ok(mgr.get(f"{MENU}/menus", timeout=5))
    cats = {c["name"]: c for c in _ok(mgr.get(f"{MENU}/menus/{menus[0]['id']}/categories", timeout=5))}
    drinks = cats["Drinks"]
    assert drinks["no_kitchen_ticket"] is True
    _ok(mgr.patch(f"{MENU}/categories/{drinks['id']}", json={"name": "Drinks", "description": "", "available": True,
                                                               "default_printer_id": None}, timeout=5))
    cats = {c["name"]: c for c in _ok(mgr.get(f"{MENU}/menus/{menus[0]['id']}/categories", timeout=5))}
    assert cats["Drinks"]["no_kitchen_ticket"] is True, "absent keeps"
    _ok(mgr.patch(f"{MENU}/categories/{drinks['id']}", json={"name": "Drinks", "description": "", "available": True,
                                                               "default_printer_id": None, "no_kitchen_ticket": False}, timeout=5))
    routes = _ok(requests.get(COUNTER_MENU, headers=store_key(store.key), timeout=10))["routes"]
    assert ids["Pepsi Can"] not in routes, "no printer and no flag: no route (every station)"


def test_online_lines_are_routed_none(nk_store, publish) -> None:
    store, mgr, printers = nk_store

    def it(name: str) -> dict:
        return {"name": name, "size_name": "", "size_id": 1, "quantity": 1, "price": 2.0, "plu": "",
                "who": "", "group_id": "", "notes": "", "modifiers": []}

    o = store.order(items=[it("coke 2 liter"), it("Pepsi Can"), it("Ranch"), it("Hot Sauce Cup")])
    publish(o)
    stored = wait_order(store.key, o["order_id"])
    by = {i["name"].lower(): i for i in stored["items"]}
    for n in ("coke 2 liter", "pepsi can"):
        assert by[n]["routing"] == "none" and not by[n].get("printer_id") and by[n].get("menu_item_id"), by[n]
    for n in ("ranch", "hot sauce cup"):
        assert by[n]["routing"] == "matched" and by[n]["printer_id"] == printers["Wings"], by[n]
    rep = _ok(mgr.get(f"{MENU}/unmatched", params={"days": 7}, timeout=5))
    assert not {r["name"].lower() for r in rep} & {"coke 2 liter", "pepsi can"}, rep
