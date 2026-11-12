"""The order screen's Pi/main gaps (2026-11-11, contract/main.md §2.5za quote,
§2.5zb terminal settings + counter orders):

  * double / triple toppings: a group with allow_multiple takes the same
    option again; each pick is priced on its half / whole and the quote
    reads "2× Pepperoni"; the order line has one child "2x Pepperoni" (ASCII:
    the printers' code page has no ×) at twice the price
  * custom lines (Quicc Add): a counter channel prices one at the store
    rate; a website refuses it (custom_line); a counter channel's order
    keeps it as a line with sku "custom"; the Pi's order.placed carries one
    the same way
  * a missed required group is a warning on the counter (line_warnings) and
    an error on a website
  * the terminal setting topping_max_count (default 3, 1-10), sent to the Pi
"""

from __future__ import annotations

import secrets
import uuid
from datetime import datetime, timezone

import pytest
import requests

from conftest import ADMIN_V1, API_V1, MAIN_BASE_URL, STORE_V1, store_key
from openapi import check

ME = f"{STORE_V1}/me"
APPLY = f"{ME}/menu/bank/apply"
CH_V1 = f"{MAIN_BASE_URL}/channel/v1"
SETTINGS = f"{ME}/terminal/settings"
CONFIG = f"{STORE_V1}/agent/terminal-config"


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


def _ok(r: requests.Response, status: int = 200):
    assert r.status_code == status, f"{r.status_code}: {r.text}"
    return r.json() if r.content else {}


def _channel(admin, store, kind: str) -> dict:
    key = _ok(admin.post(f"{ADMIN_V1}/channels", json={"location_id": store.location_id, "kind": kind, "name": kind.title()}, timeout=5), 201)["key"]
    return {"Authorization": f"Bearer {key}"}


def _doc() -> dict:
    return {
        "format": 1,
        "sizes": [{"name": "12 Medium", "short": "12\""}],
        "groups": [
            {"name": "Toppings", "max_select": 0, "allow_multiple": True, "portions": True, "options": [
                {"name": "Pepperoni", "price_cents": 150}, {"name": "Onion", "price_cents": 125}]},
            {"name": "Side", "required": True, "min_select": 1, "max_select": 1, "options": [
                {"name": "Fries", "price_cents": 0}, {"name": "Salad", "price_cents": 100}]}],
        "menus": [{"name": "Main", "categories": [{"name": "Food", "items": [
            {"name": "Cheese Pizza", "price_cents": 999, "sizes": [{"size": "12 Medium", "price_cents": 999}], "groups": ["Toppings"]},
            {"name": "Club Sandwich", "price_cents": 1099, "groups": ["Side"]}]}]}],
        "views": [], "deals": [],
    }


def _menu(mgr) -> dict:
    _ok(mgr.post(APPLY, json=_doc(), timeout=15))
    bank = _ok(mgr.get(f"{ME}/menu/bank", timeout=5))
    items = {it["name"]: it["id"] for m in bank["menus"] for c in m["categories"] for it in c["items"]}
    groups = bank["groups"].values() if isinstance(bank["groups"], dict) else bank["groups"]
    opts = {(g["name"], o["name"]): (g["id"], o["id"]) for g in groups for o in g["options"]}
    sizes = {z["name"]: z["id"] for z in bank["sizes"]}
    return {"items": items, "opts": opts, "sizes": sizes}


def _pick(m, group: str, option: str, portion: str = "") -> dict:
    gid, oid = m["opts"][(group, option)]
    return {"group_id": gid, "option_id": oid, "portion": portion}


def _quote(hdr, lines) -> dict:
    return check(requests.post(f"{CH_V1}/quote", json={"type": "pickup", "payment": "cash", "lines": lines}, headers=hdr, timeout=5), "quote")


def test_doubles_custom_lines_and_required_warnings(admin, store, mgr) -> None:
    m = _menu(mgr)
    counter, web = _channel(admin, store, "counter"), _channel(admin, store, "web")
    pizza = {"item_id": m["items"]["Cheese Pizza"], "size_id": m["sizes"]["12 Medium"], "qty": 1, "options": [
        _pick(m, "Toppings", "Pepperoni"), _pick(m, "Toppings", "Pepperoni"),
        _pick(m, "Toppings", "Onion", "first"), _pick(m, "Toppings", "Onion", "first")]}
    club = {"item_id": m["items"]["Club Sandwich"], "qty": 1}
    custom = {"custom": True, "name": " Extra dough ball ", "price_cents": 250, "qty": 2, "note": "on the side"}

    # The counter: doubles priced per pick, the custom line at the store rate, the missed side a warning.
    q = _quote(counter, [pizza, club, custom])
    assert q["errors"] == [], q["errors"]
    assert q["lines"][0]["mods"] == ["12 Medium", "2× Pepperoni", "2× Onion (1st half)"]
    assert q["lines"][0]["unit_cents"] == 999 + 2 * 150 + 2 * 125
    assert q["lines"][2] == {**q["lines"][2], "name": "Extra dough ball", "unit_cents": 250, "total_cents": 500, "custom": True}
    assert q["warnings"] == ["Club Sandwich: choose a side."]
    assert q["line_warnings"] == [{"code": "option_rules", "message": "Club Sandwich: choose a side.", "line": 1}]

    # A website: the custom line is refused, the missed side is an error; doubles are fine.
    q = _quote(web, [pizza, club, custom])
    assert [(e["code"], e["line"]) for e in q["errors"]] == [("option_rules", 1), ("custom_line", 2)], q["errors"]
    assert "line_warnings" not in q and q["lines"][0]["unit_cents"] == 999 + 2 * 150 + 2 * 125

    # A counter channel's order keeps the custom line (sku custom) and one child per double.
    body = {"client_order_id": "t-" + secrets.token_hex(6), "type": "pickup", "lines": [pizza, custom],
            "payment": {"method": "cash", "token": ""}, "customer": {"name": "Ada Lovelace", "phone": "(410) 555-0101"}}
    r = requests.post(f"{CH_V1}/orders", json=body, headers=counter, timeout=10)
    o = check(r, "placeOrder")
    assert r.status_code == 201, r.text
    got = _ok(requests.get(f"{API_V1}/orders/{o['id']}", headers=store_key(store.key), timeout=5))
    pz, cl = got["items"]
    assert [(c["name"], c["unit_price_cents"]) for c in pz["children"]] == [("2x Pepperoni", 300), ("2x Onion (1st half)", 250)]
    assert pz["line_total_cents"] == 999 + 300 + 250
    assert (cl["name"], cl.get("sku"), cl["quantity"], cl["unit_price_cents"], cl["line_total_cents"]) == ("Extra dough ball", "custom", 2, 250, 500)
    assert cl.get("menu_item_id") is None and cl["routing"] == "unmatched" and cl["notes"] == "on the side"


def test_topping_max_count_setting(store, mgr) -> None:
    assert _ok(mgr.get(SETTINGS, timeout=5))["topping_max_count"] == 3
    assert _ok(mgr.put(SETTINGS, json={"topping_max_count": 2}, timeout=5))["topping_max_count"] == 2
    for bad in (0, 11, -1):
        r = mgr.put(SETTINGS, json={"topping_max_count": bad}, timeout=5)
        assert r.status_code == 400 and r.json()["code"] == "bad_settings", (bad, r.text)
    # A save that leaves it out keeps it; the Pi's terminal config carries it.
    assert _ok(mgr.put(SETTINGS, json={"auto_lock_minutes": 6}, timeout=5))["topping_max_count"] == 2
    cfg = _ok(requests.get(CONFIG, headers=store_key(store.key), timeout=5))
    assert cfg["settings"]["topping_max_count"] == 2


def test_pi_order_with_a_custom_line(store) -> None:
    """The Pi's order.placed with a custom line: kept as sent (sku custom, no menu item)."""
    oid, now = str(uuid.uuid4()), datetime.now(timezone.utc).isoformat()
    snap = {"id": oid, "source": "counter", "source_event_id": f"ck-{oid}", "status": "accepted", "currency": "USD",
            "money": {"subtotal_cents": 500, "tax_cents": 30, "tip_cents": 0, "fees": [], "discounts": [], "total_cents": 530},
            "customer": {"display_name": "", "phone": ""}, "fulfillment": {"type": "counter"},
            "items": [{"id": str(uuid.uuid4()), "position": 0, "sku": "custom", "name": "Extra dough ball", "quantity": 2,
                       "unit_price_cents": 250, "line_total_cents": 500, "routing": "unmatched", "notes": "on the side"}],
            "payments": [{"id": str(uuid.uuid4()), "method": "cash", "amount_cents": 530, "at": now}],
            "notes": "", "placed_at": now, "kitchen_order_number": 7, "staff_id": str(uuid.uuid4()), "device_id": "terminal-1"}
    ev = {"event_id": str(uuid.uuid4()), "type": "order.placed", "order_id": oid, "at": now, "batch_id": 1,
          "agent_status": {"state": "accepted"}, "snapshot": snap}
    env = {"type": "floor.events", "id": str(uuid.uuid4()), "payload": {"events": [ev]}}
    r = requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(store.key), json=env, timeout=10)
    assert r.status_code == 200 and r.json()["payload"]["results"][0]["result"] == "applied", r.text
    it = _ok(requests.get(f"{API_V1}/orders/{oid}", headers=store_key(store.key), timeout=5))["items"][0]
    assert (it["name"], it.get("sku"), it["quantity"], it["line_total_cents"]) == ("Extra dough ball", "custom", 2, 500)
    assert it.get("menu_item_id") is None
