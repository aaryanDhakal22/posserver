"""Pricing (main 3.0, archive/PORTS-PLAN.md step 4). The engine's arithmetic is
covered by the shared cases (Go + TS, internal/domain/pricing/testdata);
this is the HTTP side against a real store.

  * settings: defaults (6% tax, the website takes tips), save + read back,
    per-kind sets, refused values; a channel sees its kind's sets
  * deals: create every kind, refused shapes, another store's items, a
    duplicate code (409), replace, delete, another store can't touch them
  * /channel/v1/quote (OpenAPI-checked): sizes + options + per-item tax, a
    mix deal and a code, deals only on their channel kinds, sold out, order
    types from store status, the quote permission
  * /channel/v1/pricing: the context the TS engine previews with
"""

from __future__ import annotations

import pytest
import requests

from conftest import ADMIN_V1, MAIN_BASE_URL, STORE_V1
from openapi import check

ME = f"{STORE_V1}/me"
MENU = f"{ME}/menu"
CH_V1 = f"{MAIN_BASE_URL}/channel/v1"


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


def _ok(r: requests.Response, status: int = 200):
    assert r.status_code == status, f"{r.status_code}: {r.text}"
    return r.json() if r.content else None


def _channel(admin, store, kind: str = "web") -> dict:
    key = _ok(admin.post(f"{ADMIN_V1}/channels", json={"location_id": store.location_id, "kind": kind, "name": kind.title()}, timeout=5), 201)["key"]
    return {"Authorization": f"Bearer {key}"}


def _menu(mgr) -> dict:
    """Pizza (sizes 12/16, toppings priced per size), Wings (10% tax, needs a
    sauce), Soda (sold out later)."""
    m = _ok(mgr.post(f"{MENU}/menus", json={"name": "Main"}, timeout=5), 201)
    pz = _ok(mgr.post(f"{MENU}/menus/{m['id']}/categories", json={"name": "Pizza"}, timeout=5), 201)
    sd = _ok(mgr.post(f"{MENU}/menus/{m['id']}/categories", json={"name": "Sides"}, timeout=5), 201)
    pizza = _ok(mgr.post(f"{MENU}/categories/{pz['id']}/items", json={"name": "Cheese Pizza", "price_cents": 0}, timeout=5), 201)
    wings = _ok(mgr.post(f"{MENU}/categories/{sd['id']}/items", json={"name": "Wings", "price_cents": 1099}, timeout=5), 201)
    soda = _ok(mgr.post(f"{MENU}/categories/{sd['id']}/items", json={"name": "Soda", "price_cents": 199}, timeout=5), 201)
    _ok(mgr.patch(f"{MENU}/items/{wings['id']}", json={**wings, "tax_rate_bps": 1000}, timeout=5))
    s12 = _ok(mgr.post(f"{MENU}/sizes", json={"name": "12 Medium"}, timeout=5), 201)
    s16 = _ok(mgr.post(f"{MENU}/sizes", json={"name": "16 X-Large"}, timeout=5), 201)
    _ok(mgr.put(f"{MENU}/items/{pizza['id']}/bank", json={"sizes": [{"size_id": s12["id"], "price_cents": 1199},
                                                                     {"size_id": s16["id"], "price_cents": 1599}]}, timeout=5), 204)
    top = _ok(mgr.post(f"{MENU}/modifier-groups", json={"name": "Toppings", "max_select": 5}, timeout=5), 201)
    pep = _ok(mgr.post(f"{MENU}/modifier-groups/{top['id']}/options", json={"name": "Pepperoni", "price_delta_cents": 150}, timeout=5), 201)
    _ok(mgr.put(f"{MENU}/modifier-groups/{top['id']}/bank", json={"portions": True, "options": [
        {"option_id": pep["id"], "is_default": False, "kitchen_name": "", "prices": [{"size_id": s16["id"], "price_cents": 225}]}]}, timeout=5), 204)
    _ok(mgr.post(f"{MENU}/items/{pizza['id']}/modifier-groups", json={"group_id": top["id"]}, timeout=5), 204)
    sauce = _ok(mgr.post(f"{MENU}/modifier-groups", json={"name": "Sauce", "required": True, "min_select": 1, "max_select": 1}, timeout=5), 201)
    bbq = _ok(mgr.post(f"{MENU}/modifier-groups/{sauce['id']}/options", json={"name": "BBQ", "price_delta_cents": 50}, timeout=5), 201)
    _ok(mgr.post(f"{MENU}/items/{wings['id']}/modifier-groups", json={"group_id": sauce["id"]}, timeout=5), 204)
    return {"pizza": pizza, "wings": wings, "soda": soda, "s12": s12, "s16": s16, "top": top, "pep": pep, "sauce": sauce, "bbq": bbq,
            "cat_pizza": pz, "cat_sides": sd}


def _line(item_id: str, qty: int = 1, size: str = "", options=()) -> dict:
    return {"item_id": item_id, "size_id": size, "qty": qty, "options": [{"group_id": g, "option_id": o, "portion": p} for g, o, p in options]}


def _quote(hdr, lines, **cart) -> dict:
    r = requests.post(f"{CH_V1}/quote", json={"type": cart.pop("type", "pickup"), "lines": lines, **cart}, headers=hdr, timeout=5)
    return check(r, "quote")


def test_settings(admin, store, mgr) -> None:
    got = _ok(mgr.get(f"{ME}/pricing", timeout=5))
    assert got["store"]["tax_bps"] == 600 and got["store"]["stacking"] == "best" and got["kinds"]["web"]["tips"]["enabled"] is True
    body = {"store": {"tax_bps": 600, "stacking": "stack", "fees": {"service_fee_cents": 0, "service_fee_bps": 0, "delivery_fee_cents": 299,
                                                                   "free_delivery_over_cents": 3000, "delivery_min_cents": 1000},
                      "tips": {"enabled": False, "presets": [], "default_pct": 0}, "card_fee": {"mode": "none", "bps": 0}},
            "kinds": {"web": {"fees": {"service_fee_cents": 75, "service_fee_bps": 0, "delivery_fee_cents": 399, "free_delivery_over_cents": None,
                                       "delivery_min_cents": 1500},
                              "tips": {"enabled": True, "presets": [15, 18, 20], "default_pct": 18}}}}
    got = _ok(mgr.put(f"{ME}/pricing", json=body, timeout=5))
    assert got["store"]["stacking"] == "stack" and got["kinds"]["web"]["fees"]["service_fee_cents"] == 75 and "counter" not in got["kinds"]

    web = check(requests.get(f"{CH_V1}/pricing", headers=_channel(admin, store, "web"), timeout=5), "getPricing")
    assert web["settings"]["service_fee_cents"] == 75 and web["settings"]["delivery_fee_cents"] == 399 and web["settings"]["tips"]["enabled"]
    counter = check(requests.get(f"{CH_V1}/pricing", headers=_channel(admin, store, "counter"), timeout=5), "getPricing")
    assert counter["settings"]["service_fee_cents"] == 0 and counter["settings"]["free_delivery_over_cents"] == 3000
    assert counter["settings"]["stacking"] == "stack" and counter["timezone"] == "America/New_York" and counter["types"] == {"pickup": True, "delivery": True}

    for bad in ({**body, "store": {**body["store"], "tax_bps": 5000}}, {**body, "store": {**body["store"], "stacking": "all"}},
                {**body, "kinds": {"kiosk": {}}}, {**body, "kinds": {"web": {"tips": {"enabled": True, "presets": [150], "default_pct": 18}}}},
                {**body, "store": {**body["store"], "card_fee": {"mode": "tax", "bps": 0}}}):
        assert mgr.put(f"{ME}/pricing", json=bad, timeout=5).status_code == 400, bad


def _deals(mgr, m) -> dict:
    defs = {
        "mix": {"kind": "mix", "name": "Any 2 pizzas", "active": True, "eligible": {"items": [], "categories": [m["cat_pizza"]["id"]], "sizes": []},
                "qty": 2, "each_cents": 1000, "channel_kinds": ["web", "operator"]},
        "nth": {"kind": "nth", "name": "2nd wings half off", "active": True, "eligible": {"items": [m["wings"]["id"]]}, "n": 2, "pct": 50},
        "code": {"kind": "code", "name": "Ten off", "active": True, "code": "save10", "pct": 10, "channel_kinds": ["web", "counter"]},
        "bundle": {"kind": "bundle", "name": "Combo", "active": True, "price_cents": 1999, "slots": [
            {"label": "Pizza", "eligible": {"categories": [m["cat_pizza"]["id"]], "sizes": [m["s16"]["id"]]}, "included_options": 1},
            {"label": "Wings", "eligible": {"items": [m["wings"]["id"]]}, "included_options": 0}]},
        "free_item": {"kind": "free_item", "name": "Free soda over $30", "active": False, "eligible": {"items": [m["soda"]["id"]]}, "min_subtotal_cents": 3000},
    }
    return {k: _ok(mgr.post(f"{ME}/deals", json=v, timeout=5), 201) for k, v in defs.items()}


def test_deals(admin, mgr, make_store) -> None:
    m = _menu(mgr)
    d = _deals(mgr, m)
    assert d["code"]["code"] == "SAVE10" and d["nth"]["channel_kinds"] == ["web", "operator", "counter"]
    assert [x["name"] for x in _ok(mgr.get(f"{ME}/deals", timeout=5))] == ["Any 2 pizzas", "2nd wings half off", "Ten off", "Combo", "Free soda over $30"]

    other = make_store().manager_session()
    try:
        om = _menu(other)
        foreign = {"kind": "mix", "name": "x", "active": True, "eligible": {"items": [om["pizza"]["id"]]}, "qty": 2, "each_cents": 100}
        assert mgr.post(f"{ME}/deals", json=foreign, timeout=5).status_code == 400
        assert other.delete(f"{ME}/deals/{d['mix']['id']}", timeout=5).status_code == 404
        assert other.put(f"{ME}/deals/{d['mix']['id']}", json=foreign, timeout=5).status_code == 404
        # Another store may use the same code.
        assert other.post(f"{ME}/deals", json={"kind": "code", "name": "Ten", "active": True, "code": "SAVE10", "amount_cents": 100}, timeout=5).status_code == 201
    finally:
        other.close()
    for bad in ({"kind": "mix", "name": "x", "eligible": {"items": [m["pizza"]["id"]]}, "qty": 1, "each_cents": 100},
                {"kind": "nth", "name": "x", "eligible": {"items": []}, "n": 2, "pct": 50},
                {"kind": "code", "name": "x", "code": "A", "pct": 10},
                {"kind": "code", "name": "x", "code": "BOTH", "pct": 10, "amount_cents": 100},
                {"kind": "bundle", "name": "x", "price_cents": 100, "slots": []},
                {"kind": "raffle", "name": "x"},
                {"kind": "mix", "name": "x", "eligible": {"items": [m["pizza"]["id"]]}, "qty": 2, "each_cents": 100, "channel_kinds": ["kiosk"]},
                {"kind": "mix", "name": "x", "eligible": {"items": [m["pizza"]["id"]]}, "qty": 2, "each_cents": 100, "schedule": {"days": ["funday"]}}):
        assert mgr.post(f"{ME}/deals", json=bad, timeout=5).status_code == 400, bad
    dup = mgr.post(f"{ME}/deals", json={"kind": "code", "name": "Again", "active": True, "code": "Save10", "amount_cents": 100}, timeout=5)
    assert dup.status_code == 409 and dup.json()["code"] == "code_taken"

    up = _ok(mgr.put(f"{ME}/deals/{d['mix']['id']}", json={**d["mix"], "each_cents": 900, "active": False}, timeout=5))
    assert up["each_cents"] == 900 and up["active"] is False and up["id"] == d["mix"]["id"]
    assert mgr.delete(f"{ME}/deals/{d['nth']['id']}", timeout=5).status_code == 204
    assert mgr.delete(f"{ME}/deals/{d['nth']['id']}", timeout=5).status_code == 404


def test_channel_quote(admin, store, mgr) -> None:
    m = _menu(mgr)
    d = _deals(mgr, m)
    web, counter = _channel(admin, store, "web"), _channel(admin, store, "counter")

    # 16" pizza + pepperoni (16" price) ×2, wings with BBQ (taxed at 10%).
    lines = [_line(m["pizza"]["id"], 2, m["s16"]["id"], [(m["top"]["id"], m["pep"]["id"], "whole")]),
             _line(m["wings"]["id"], 1, options=[(m["sauce"]["id"], m["bbq"]["id"], "")])]
    q = _quote(counter, lines)
    assert [x["unit_cents"] for x in q["lines"]] == [1824, 1149] and q["subtotal_cents"] == 4797
    assert q["discounts"] == [] and q["errors"] == []  # the mix deal isn't on the counter
    assert q["tax_cents"] == round(3648 * 0.06) + round(1149 * 0.10) == 334
    assert q["lines"][0]["mods"] == ["16 X-Large", "Pepperoni"]

    q = _quote(web, lines, code="save10")
    labels = {x["label"]: x["amount_cents"] for x in q["discounts"]}
    assert labels == {"Any 2 pizzas": 1648}, q["discounts"]  # (1824-1000)×2 beats 10% (480)
    assert "saves more than your code" in q["warnings"][0]

    # A bundle line on the web; the operator kind gets it too.
    q = _quote(web, [{"item_id": "", "deal_id": d["bundle"]["id"], "qty": 1, "picks": [
        {"item_id": m["pizza"]["id"], "size_id": m["s16"]["id"], "options": [{"group_id": m["top"]["id"], "option_id": m["pep"]["id"]}]},
        {"item_id": m["wings"]["id"], "options": [{"group_id": m["sauce"]["id"], "option_id": m["bbq"]["id"]}]}]}])
    assert q["lines"][0]["unit_cents"] == 1999 + 50 and q["lines"][0]["regular_cents"] == 1824 + 1149 and q["errors"] == []

    # Problems: no sauce, an unknown item, a sold-out soda, delivery switched off.
    _ok(mgr.patch(f"{MENU}/items/{m['soda']['id']}", json={**m["soda"], "in_stock": False}, timeout=5))
    _ok(mgr.put(f"{ME}/ordering", json={"pickup_enabled": True, "delivery_enabled": False, "pickup_eta_minutes": 20,
                                        "delivery_eta_minutes": 45, "schedule_days": 7}, timeout=5))
    q = _quote(counter, [_line(m["wings"]["id"]), _line("nope"), _line(m["soda"]["id"])], type="delivery")
    codes = [e["code"] for e in q["errors"]]
    assert codes == ["option_rules", "unknown_item", "sold_out", "type_off"], q["errors"]
    assert q["errors"][0]["line"] == 0 and q["errors"][3]["line"] is None

    # A vendor feed doesn't price carts; a bad body is a 400.
    vendor = _channel(admin, store, "vendor")
    r = requests.post(f"{CH_V1}/quote", json={"type": "pickup", "lines": []}, headers=vendor, timeout=5)
    assert r.status_code == 403 and check(r, "quote")["code"] == "forbidden"
    assert requests.get(f"{CH_V1}/pricing", headers=vendor, timeout=5).status_code == 403
    r = requests.post(f"{CH_V1}/quote", data="{not json", headers={**web, "Content-Type": "application/json"}, timeout=5)
    assert r.status_code == 400 and check(r, "quote")["code"] == "bad_request"

    # The context the TS engine previews with.
    ctx = check(requests.get(f"{CH_V1}/pricing", headers=web, timeout=5), "getPricing")
    assert ctx["items"][m["wings"]["id"]] == {"category_id": m["cat_sides"]["id"], "tax_bps": 1000}
    assert {x["name"] for x in ctx["deals"]} == {"Any 2 pizzas", "2nd wings half off", "Ten off", "Combo"}  # the inactive free soda isn't
    menu = requests.get(f"{CH_V1}/menu", headers=web, timeout=5).json()
    assert ctx["menu_version"] == menu["version"]
