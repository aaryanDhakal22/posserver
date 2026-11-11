"""The counter menu the Pi pulls + the SoftTouch import's gaps
(TERMINAL-PLAN.md 0d, contract/main.md §2.5zb).

  * GET /store/v1/agent/counter-menu: the counter view's build with its
    layout + the counter's pricing (items facts, settings, deals, open_prices),
    ETag → 304, agent keys only (test_staff)
  * menu.changed {version} pushed to a counter store's Pi when the menu moves
  * an option button's mode (add | no) in the layout, by ids and by names
  * no_tax / open_price item flags (bank document round trip, pricing facts,
    quotes: open price only where open_prices)
  * a view's `menus` rule shows only those menus
  * an online order never routes to a counter-only item
"""

from __future__ import annotations

import copy

import pytest
import requests

from conftest import STORE_V1, store_key, wait_order
from sse import SSEStream

MENU = f"{STORE_V1}/me/menu"
APPLY = f"{MENU}/bank/apply"
EXPORT = f"{MENU}/bank/document"
COUNTER_MENU = f"{STORE_V1}/agent/counter-menu"


def _ok(r: requests.Response, status: int = 200):
    assert r.status_code == status, f"{r.status_code}: {r.text}"
    return r.json() if r.content else {}


def _btn(row: int, col: int, action: dict, page: int = 0) -> dict:
    return {"page": page, "row": row, "col": col, "caption": "", "color": "", "font_color": "", "font_size": 0, "action": action}


def _doc() -> dict:
    sizes = ["12 Medium", "16 X-Large"]
    return {
        "format": 1,
        "sizes": [{"name": "12 Medium", "short": "12\""}, {"name": "16 X-Large", "short": "16\""}],
        "groups": [{"name": "Toppings (Counter)", "allow_multiple": True, "portions": True, "options": [
            {"name": "Pepperoni", "price_cents": 175}, {"name": "Onions", "price_cents": 150}]}],
        "menus": [
            {"name": "Main", "categories": [{"name": "Pizza", "items": [{"name": "Pepperoni Pizza", "price_cents": 1499}]}]},
            {"name": "Counter", "categories": [
                {"name": "Counter Pizza", "items": [
                    {"name": "Cheese Pizza", "price_cents": 999, "groups": ["Toppings (Counter)"],
                     "sizes": [{"size": s, "price_cents": p} for s, p in zip(sizes, (999, 1399))]}]},
                {"name": "Counter Extras", "items": [
                    {"name": "Advance Payment", "price_cents": 0, "open_price": True},
                    {"name": "Ice Cup", "price_cents": 0, "no_tax": True},
                    {"name": "Garlic Knots", "price_cents": 499, "no_tax": True}]}]},
        ],
        "views": [
            {"kind": "web", "name": "Web", "rules": {"markup_bps": 0, "round": "none", "require_photo": False,
                                                    "hidden_categories": ["Counter Pizza", "Counter Extras"]}, "entries": []},
            {"kind": "counter", "name": "Counter", "rules": {"markup_bps": 0, "round": "none", "require_photo": False,
                                                            "hidden_categories": [], "menus": ["Counter"]}, "entries": [],
             "layout": {
                 "tab_strip": {"rows": 1, "cols": 2},
                 "tabs": [{"name": "Pizza", "strip_row": 0, "strip_col": 0, "color": "", "font_color": "", "font_size": 0,
                           "grid": {"rows": 2, "cols": 2}, "pages": [{"name": "Page 1"}],
                           "buttons": [_btn(0, 0, {"type": "item", "item": "Cheese Pizza", "size": "12 Medium"}),
                                       _btn(0, 1, {"type": "item", "item": "Advance Payment"})]}],
                 "screens": [{"name": "Toppings", "prompt": "", "grid": {"rows": 2, "cols": 2}, "pages": [{"name": "Add"}],
                              "buttons": [_btn(0, 0, {"type": "option", "group": "Toppings (Counter)", "option": "Onions"}),
                                          _btn(0, 1, {"type": "option", "group": "Toppings (Counter)", "option": "Onions", "mode": "no"})]}],
                 "flows": {"Cheese Pizza": [{"screen": "Toppings", "required_count": 0, "forced": False, "prompt": ""}]},
                 "functions": {"nav": [], "employee": [], "manager": []},
                 "bottom_bar": {"none": {"rows": 0, "cols": 0, "buttons": []}, "line": {"rows": 0, "cols": 0, "buttons": []},
                                "modifier": {"rows": 0, "cols": 0, "buttons": []}}}},
        ],
        "deals": [],
    }


@pytest.fixture
def counter_store(store):
    mgr = store.manager_session()
    res = _ok(mgr.post(APPLY, json=_doc(), timeout=20))
    assert res["applied"] is True, res
    yield store, mgr
    mgr.close()


def _items_by_name(build: dict) -> dict:
    return {i["name"]: i for c in build["categories"] for i in c["items"]}


def test_counter_menu_for_the_pi(counter_store) -> None:
    store, mgr = counter_store
    r = requests.get(COUNTER_MENU, headers=store_key(store.key), timeout=10)
    body = _ok(r)
    assert r.headers["ETag"] == f'"v{body["menu_version"]}"'
    assert requests.get(COUNTER_MENU, headers={**store_key(store.key), "If-None-Match": r.headers["ETag"]},
                        timeout=5).status_code == 304

    build = body["build"]
    assert build["view"]["kind"] == "counter" and build["view"]["id"] == body["view_id"]
    items = _items_by_name(build)
    assert set(items) == {"Cheese Pizza", "Advance Payment", "Ice Cup", "Garlic Knots"}, "the menus rule: Counter only"
    # The layout, with the option button's mode.
    no_btn = build["layout"]["screens"][0]["buttons"][1]["action"]
    assert no_btn["type"] == "option" and no_btn["mode"] == "no" and no_btn["label"] == "Onions"
    assert "mode" not in build["layout"]["screens"][0]["buttons"][0]["action"]

    pricing = body["pricing"]
    assert pricing["open_prices"] is True and pricing["settings"]["tax_bps"] == 600 and pricing["timezone"]
    facts = pricing["items"]
    assert facts[items["Advance Payment"]["id"]].get("open_price") is True
    assert facts[items["Ice Cup"]["id"]].get("no_tax") is True and "open_price" not in facts[items["Ice Cup"]["id"]]
    assert "no_tax" not in facts[items["Cheese Pizza"]["id"]]

    # The web view never shows the Counter menu.
    views = {v["kind"]: v for v in _ok(mgr.get(f"{MENU}/views", timeout=5))}
    web = _ok(mgr.get(f"{MENU}/views/{views['web']['id']}/build", timeout=10))
    assert set(_items_by_name(web)) == {"Pepperoni Pizza"}
    assert views["counter"]["rules"]["menus"], views["counter"]["rules"]


def test_bank_document_round_trip(counter_store) -> None:
    _, mgr = counter_store
    doc = _ok(mgr.get(EXPORT, timeout=10))
    items = {i["name"]: i for m in doc["menus"] for c in m["categories"] for i in c["items"]}
    assert items["Advance Payment"].get("open_price") is True and items["Ice Cup"].get("no_tax") is True
    assert "no_tax" not in items["Cheese Pizza"] and "open_price" not in items["Cheese Pizza"]
    counter = [v for v in doc["views"] if v["kind"] == "counter"][0]
    assert counter["rules"]["menus"] == ["Counter"]
    assert counter["layout"]["screens"][0]["buttons"][1]["action"]["mode"] == "no"
    # Export → apply changes nothing.
    res = _ok(mgr.post(APPLY, params={"dry_run": 1}, json=doc, timeout=20))
    assert res["changes"] == [], res["changes"]

    # false clears a flag; absent keeps it; a bad mode / menu name is a 400.
    d2 = copy.deepcopy(doc)
    for m in d2["menus"]:
        for c in m["categories"]:
            for i in c["items"]:
                if i["name"] == "Ice Cup":
                    i["no_tax"] = False
                if i["name"] == "Advance Payment":
                    i.pop("open_price")
    res = _ok(mgr.post(APPLY, json=d2, timeout=20))
    assert 'item "Ice Cup": no_tax' in res["changes"], res["changes"]
    again = {i["name"]: i for m in _ok(mgr.get(EXPORT, timeout=10))["menus"] for c in m["categories"] for i in c["items"]}
    assert "no_tax" not in again["Ice Cup"] and again["Advance Payment"].get("open_price") is True

    d3 = copy.deepcopy(doc)
    [v for v in d3["views"] if v["kind"] == "counter"][0]["layout"]["screens"][0]["buttons"][1]["action"]["mode"] = "only"
    r = mgr.post(APPLY, json=d3, timeout=20)
    assert r.status_code == 400 and "mode" in r.text, r.text
    d4 = copy.deepcopy(doc)
    [v for v in d4["views"] if v["kind"] == "counter"][0]["rules"]["menus"] = ["Nope"]
    r = mgr.post(APPLY, json=d4, timeout=20)
    assert r.status_code == 400 and "rules.menus[0]" in r.text, r.text


def test_pricing_context_open_prices(counter_store) -> None:
    store, mgr = counter_store
    build = _ok(requests.get(COUNTER_MENU, headers=store_key(store.key), timeout=10))["build"]
    items = _items_by_name(build)

    # The store's quote preview runs main's engine on the counter's context.
    ctx = _ok(mgr.get(f"{STORE_V1}/me/pricing/context", params={"kind": "counter"}, timeout=10))
    assert ctx.get("open_prices") is True
    web_ctx = _ok(mgr.get(f"{STORE_V1}/me/pricing/context", params={"kind": "web"}, timeout=10))
    assert not web_ctx.get("open_prices")
    adv = items["Advance Payment"]["id"]
    assert ctx["items"][adv]["open_price"] is True


def test_menu_changed_is_pushed(counter_store) -> None:
    store, mgr = counter_store
    with SSEStream(store.key) as s:
        # On connect main pushes the current version once (it may have restarted).
        first = s.wait_event("command", lambda d: d.get("type") == "menu.changed", timeout=15)
        assert first, "menu.changed was not pushed on connect"
        cur = _ok(requests.get(COUNTER_MENU, headers=store_key(store.key), timeout=10))["menu_version"]
        assert first["payload"]["version"] <= cur
        # A menu edit moves the version → a new push.
        doc = _doc()
        doc["menus"][1]["categories"][1]["items"][2]["price_cents"] = 549
        _ok(mgr.post(APPLY, json=doc, timeout=20))
        newer = s.wait_event("command", lambda d: d.get("type") == "menu.changed" and d["payload"]["version"] > cur, timeout=15)
        assert newer, "menu.changed was not pushed after an edit"


def test_online_orders_never_route_to_counter_only_items(counter_store, publish) -> None:
    store, mgr = counter_store
    printers = _ok(mgr.post(f"{STORE_V1}/me/printers", json={"name": "Kitchen", "ip": "10.0.0.9"}, timeout=5), 201)
    assert printers

    def it(name: str) -> dict:
        return {"name": name, "size_name": "", "size_id": 1, "quantity": 1, "price": 5.0, "plu": "",
                "who": "", "group_id": "", "notes": "", "modifiers": []}

    o = store.order(items=[it("Cheese Pizza"), it("Pepperoni Pizza"), it("Garlic Knots")])
    publish(o)
    got = {i["name"]: i for i in wait_order(store.key, o["order_id"])["items"]}
    assert got["Cheese Pizza"]["routing"] == "unmatched" and not got["Cheese Pizza"].get("menu_item_id")
    assert got["Garlic Knots"]["routing"] == "unmatched"
    assert got["Pepperoni Pizza"]["routing"] in ("matched", "unassigned") and got["Pepperoni Pizza"]["menu_item_id"]
