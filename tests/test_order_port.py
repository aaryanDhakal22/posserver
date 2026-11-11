"""The order port (main 3.0, archive/PORTS-PLAN.md step 5): POST /channel/v1/orders.

  * a cash pickup through the call center: 201, OpenAPI-checked; the Pi's SSE
    frame is an ordinary Snapshot (source = the channel kind, the bank's
    names, sizes + options as children, kitchen routing from the bank item,
    a pending cash payment, money that adds up); the same client_order_id
    again → 200, the same order, nothing new on the stream
  * GET one / list — own channel only
  * the test card (approve / decline) and what each channel may take
  * the store's status: paused, closed (a special day), a later time that is
    offered / one that isn't
  * price drift, cart problems (422 + issues), a combo (two kitchen lines + a
    combo discount, the total still the quote's)
  * delivery: the website refuses an address outside the area, the call
    center may send it (geo fake)
  * Brygid's SQS orders are linked to the store's built-in vendor channel
"""

from __future__ import annotations

import secrets
from datetime import datetime, timedelta
from zoneinfo import ZoneInfo

import pytest
import requests

from conftest import ADMIN_V1, MAIN_BASE_URL, STORE_V1, wait_order, wait_until
from openapi import check
from sse import SSEStream
from test_geo import _pin_store, _street, fake_geo  # noqa: F401 (fixture)

ME = f"{STORE_V1}/me"
MENU = f"{ME}/menu"
CH_V1 = f"{MAIN_BASE_URL}/channel/v1"
NY = ZoneInfo("America/New_York")


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


def _ok(r: requests.Response, status: int = 200):
    assert r.status_code == status, f"{r.status_code}: {r.text}"
    return r.json() if r.content else None


def _channel(admin, store, kind: str) -> dict:
    key = _ok(admin.post(f"{ADMIN_V1}/channels", json={"location_id": store.location_id, "kind": kind, "name": kind.title()}, timeout=5), 201)["key"]
    return {"Authorization": f"Bearer {key}"}


def _menu(mgr) -> dict:
    """Pizza (12/16, toppings per size, kitchen printer Pizza), Wings (Sides →
    printer Fryer, needs a sauce), and a Pizza + Wings combo."""
    pz_p = _ok(mgr.post(f"{STORE_V1}/me/printers", json={"name": "Pizza", "ip": "10.0.0.2"}, timeout=5), 201)
    fr_p = _ok(mgr.post(f"{STORE_V1}/me/printers", json={"name": "Fryer", "ip": "10.0.0.3"}, timeout=5), 201)
    m = _ok(mgr.post(f"{MENU}/menus", json={"name": "Main"}, timeout=5), 201)
    pz = _ok(mgr.post(f"{MENU}/menus/{m['id']}/categories", json={"name": "Pizza", "default_printer_id": pz_p["id"]}, timeout=5), 201)
    sd = _ok(mgr.post(f"{MENU}/menus/{m['id']}/categories", json={"name": "Sides", "default_printer_id": fr_p["id"]}, timeout=5), 201)
    pizza = _ok(mgr.post(f"{MENU}/categories/{pz['id']}/items", json={"name": "Cheese Pizza", "price_cents": 0}, timeout=5), 201)
    wings = _ok(mgr.post(f"{MENU}/categories/{sd['id']}/items", json={"name": "Wings", "price_cents": 1099}, timeout=5), 201)
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
    combo = _ok(mgr.post(f"{ME}/deals", json={"kind": "bundle", "name": "Pizza + Wings", "active": True, "price_cents": 2299, "slots": [
        {"label": "Pizza", "eligible": {"categories": [pz["id"]], "sizes": [s16["id"]]}, "included_options": 1},
        {"label": "Wings", "eligible": {"items": [wings["id"]]}, "included_options": 0}]}, timeout=5), 201)
    return {"pizza": pizza, "wings": wings, "s12": s12, "s16": s16, "top": top, "pep": pep, "sauce": sauce, "bbq": bbq,
            "combo": combo, "printer_pizza": pz_p, "printer_fryer": fr_p}


def _pizza(m, qty=1, size="s16", half="left") -> dict:
    return {"item_id": m["pizza"]["id"], "size_id": m[size]["id"], "qty": qty,
            "options": [{"group_id": m["top"]["id"], "option_id": m["pep"]["id"], "portion": half}]}


def _wings(m, qty=1) -> dict:
    return {"item_id": m["wings"]["id"], "qty": qty, "options": [{"group_id": m["sauce"]["id"], "option_id": m["bbq"]["id"]}]}


def _order(lines, method="cash", **kw) -> dict:
    body = {"client_order_id": kw.pop("client_order_id", "t-" + secrets.token_hex(6)), "type": kw.pop("type", "pickup"), "lines": lines,
            "payment": {"method": method, "token": kw.pop("token", "")},
            "customer": {"name": "Ada Lovelace", "phone": "(410) 555-0101", "email": "ada@example.com"}}
    body.update(kw)
    return body


def _place(hdr, body) -> requests.Response:
    return requests.post(f"{CH_V1}/orders", json=body, headers=hdr, timeout=10)


def test_place_cash_pickup(admin, store, mgr) -> None:
    m = _menu(mgr)
    op = _channel(admin, store, "operator")
    body = _order([_pizza(m, qty=2), _wings(m)], notes="Ring the bell")
    with SSEStream(store.key) as s:
        assert s.next()["event"] == "store_config"
        r = _place(op, body)
        o = check(r, "placeOrder")
        assert r.status_code == 201 and o["status"] == "received" and o["payment"] == {"method": "cash", "status": "due"}
        assert o["client_order_id"] == body["client_order_id"] and o["customer"]["name"] == "Ada Lovelace"
        frame = s.wait_event("order", lambda d: d.get("id") == o["id"])
        assert frame, "the order never reached the Pi's stream"

        # Placing it again: the same order, nothing new for the Pi.
        again = _place(op, body)
        assert again.status_code == 200 and check(again, "placeOrder")["id"] == o["id"]
        assert s.wait_event("order", lambda d: d.get("id") != o["id"], timeout=3) is None  # waits on purpose

    # The Pi got an ordinary Snapshot.
    assert frame["source"] == "operator" and frame["status"] == "received"
    pz, wg = frame["items"]
    assert (pz["name"], pz["size"], pz["quantity"], pz["unit_price_cents"]) == ("Cheese Pizza", "16 X-Large", 2, 1599)
    assert [(c["name"], c["quantity"], c["unit_price_cents"]) for c in pz["children"]] == [("Pepperoni (left half)", 2, 225)]
    assert pz["line_total_cents"] == 2 * 1824 and pz["printer_id"] == m["printer_pizza"]["id"] and pz["routing"] == "matched"
    assert pz["menu_item_id"] == m["pizza"]["id"]
    assert wg["printer_id"] == m["printer_fryer"]["id"] and wg["children"][0]["name"] == "BBQ"
    money = frame["money"]
    assert money["subtotal_cents"] == 2 * 1824 + 1149 and money["total_cents"] == o["money"]["total_cents"]
    assert money["total_cents"] == money["subtotal_cents"] + money["tax_cents"] + money.get("tip_cents", 0)
    assert frame["payments"] == [{"method": "cash", "amount_cents": money["total_cents"]}]
    assert frame["customer"]["phone"] == "4105550101" and frame["notes"] == "Ring the bell"

    # Read back: own channel only.
    got = check(requests.get(f"{CH_V1}/orders/{o['id']}", headers=op, timeout=5), "getOrder")
    assert got["id"] == o["id"] and got["lines"][0]["mods"] == ["Pepperoni (left half)"]
    listed = check(requests.get(f"{CH_V1}/orders", headers=op, timeout=5), "listOrders")
    assert [x["id"] for x in listed] == [o["id"]]
    other = _channel(admin, store, "counter")
    assert requests.get(f"{CH_V1}/orders/{o['id']}", headers=other, timeout=5).status_code == 404
    assert check(requests.get(f"{CH_V1}/orders", headers=other, timeout=5), "listOrders") == []
    # The Pi's own read API has it too (the device key).
    r = requests.get(f"{MAIN_BASE_URL}/api/v1/orders/{o['id']}", headers={"X-STORE-API-KEY": store.key}, timeout=5)
    assert r.status_code == 200 and r.json()["source"] == "operator", r.text


def test_payments(admin, store, mgr) -> None:
    m = _menu(mgr)
    web, op = _channel(admin, store, "web"), _channel(admin, store, "operator")
    # The website takes card only (its default); the test card works on dev.
    r = _place(web, _order([_pizza(m)], method="cash"))
    assert r.status_code == 403 and check(r, "placeOrder")["code"] == "payment_not_allowed"
    o = check(_place(web, _order([_pizza(m)], method="card", token="tok_visa")), "placeOrder")
    assert o["payment"] == {"method": "card", "status": "paid"}
    r = _place(web, _order([_pizza(m)], method="card", token="tok_decline"))
    assert r.status_code == 402 and check(r, "placeOrder")["code"] == "payment_declined"
    r = _place(op, _order([_pizza(m)], method="bitcoin"))
    assert r.status_code == 400 and check(r, "placeOrder")["code"] == "bad_request"
    # A vendor feed may place orders, but its view only maps the vendor's
    # names (no menu of its own), so the bank's items aren't on it.
    vendor = _channel(admin, store, "vendor")
    r = _place(vendor, _order([_pizza(m)]))
    assert r.status_code == 422 and check(r, "placeOrder")["code"] == "cart_invalid"


def test_bad_orders(admin, store, mgr) -> None:
    m = _menu(mgr)
    op = _channel(admin, store, "operator")
    for patch, code in (({"client_order_id": "has spaces"}, "bad_request"), ({"type": "dinein"}, "bad_request"),
                        ({"customer": {"name": "", "phone": "4105550101"}}, "bad_request"),
                        ({"customer": {"name": "A", "phone": "555-0101"}}, "bad_request"),
                        ({"customer": {"name": "A", "phone": "4105550101", "email": "nope"}}, "bad_request"),
                        ({"type": "delivery"}, "bad_request"), ({"lines": []}, "bad_request")):
        r = _place(op, {**_order([_pizza(m)]), **patch})
        assert r.status_code == 400 and check(r, "placeOrder")["code"] == code, (patch, r.text)

    # Cart problems: no sauce, an unknown item.
    r = _place(op, _order([{"item_id": m["wings"]["id"], "qty": 1}, {"item_id": "nope", "qty": 1}]))
    err = check(r, "placeOrder")
    assert r.status_code == 422 and err["code"] == "cart_invalid" and [i["code"] for i in err["issues"]] == ["option_rules", "unknown_item"]

    # The price moved since the channel showed it: refused, refresh.
    q = check(requests.post(f"{CH_V1}/quote", json={"type": "pickup", "lines": [_pizza(m)]}, headers=op, timeout=5), "quote")
    r = _place(op, _order([_pizza(m)], expected_total_cents=q["total_cents"] - 1))
    assert r.status_code == 409 and check(r, "placeOrder")["code"] == "price_changed"
    assert _place(op, _order([_pizza(m)], expected_total_cents=q["total_cents"])).status_code == 201

    # A vendor channel without the orders permission can't.
    ch = _ok(admin.post(f"{ADMIN_V1}/channels", json={"location_id": store.location_id, "kind": "web", "name": "Menu only",
                                                       "permissions": {"menu": True, "quote": True, "orders": False, "track": False, "payments": ["card"]}}, timeout=5), 201)
    r = _place({"Authorization": f"Bearer {ch['key']}"}, _order([_pizza(m)], method="card", token="tok_visa"))
    assert r.status_code == 403 and check(r, "placeOrder")["code"] == "forbidden"


def test_store_status(admin, store, mgr) -> None:
    m = _menu(mgr)
    op = _channel(admin, store, "operator")
    _ok(mgr.post(f"{ME}/pause", json={"preset": "30m", "note": "Back at 7"}, timeout=5))
    r = _place(op, _order([_pizza(m)]))
    assert r.status_code == 409 and check(r, "placeOrder")["code"] == "store_paused" and r.json()["error"] == "Back at 7"
    _ok(mgr.delete(f"{ME}/pause", timeout=5))

    today = datetime.now(NY).date()
    _ok(mgr.post(f"{ME}/hours-exceptions", json={"date": today.isoformat(), "closed": True}, timeout=5))
    r = _place(op, _order([_pizza(m)]))
    assert r.status_code == 409 and check(r, "placeOrder")["code"] == "store_closed"

    # For later: a time the store offers is taken, any other isn't.
    tomorrow = (today + timedelta(days=1)).isoformat()
    times = _ok(requests.get(f"{CH_V1}/store/times", params={"type": "pickup", "date": tomorrow}, headers=op, timeout=5))["times"]
    o = check(_place(op, _order([_pizza(m)], promised_at=times[10])), "placeOrder")
    assert datetime.fromisoformat(o["promised_at"].replace("Z", "+00:00")) == datetime.fromisoformat(times[10].replace("Z", "+00:00"))
    odd = (datetime.fromisoformat(times[10].replace("Z", "+00:00")) + timedelta(minutes=7)).isoformat()
    r = _place(op, _order([_pizza(m)], promised_at=odd))
    assert r.status_code == 409 and check(r, "placeOrder")["code"] == "bad_time"


def test_combo(admin, store, mgr) -> None:
    m = _menu(mgr)
    op = _channel(admin, store, "operator")
    line = {"item_id": "", "deal_id": m["combo"]["id"], "qty": 1, "picks": [
        {"item_id": m["pizza"]["id"], "size_id": m["s16"]["id"], "options": [{"group_id": m["top"]["id"], "option_id": m["pep"]["id"]}]},
        {"item_id": m["wings"]["id"], "options": [{"group_id": m["sauce"]["id"], "option_id": m["bbq"]["id"]}]}]}
    q = check(requests.post(f"{CH_V1}/quote", json={"type": "pickup", "lines": [line]}, headers=op, timeout=5), "quote")
    assert q["lines"][0]["unit_cents"] == 2299 + 50  # the pepperoni is included, the BBQ isn't
    with SSEStream(store.key) as s:
        assert s.next()["event"] == "store_config"
        o = check(_place(op, _order([line])), "placeOrder")
        frame = s.wait_event("order", lambda d: d.get("id") == o["id"])
    assert o["money"]["total_cents"] == q["total_cents"]
    names = [(i["name"], i["printer_id"], i["notes"]) for i in frame["items"]]
    assert names == [("Cheese Pizza", m["printer_pizza"]["id"], "Combo: Pizza + Wings"), ("Wings", m["printer_fryer"]["id"], "Combo: Pizza + Wings")]
    assert frame["items"][0]["children"][0]["unit_price_cents"] == 0  # the included topping
    disc = frame["money"]["discounts"]
    assert disc == [{"type": "deal", "label": "Combo: Pizza + Wings", "amount_cents": (1599 + 0) + 1149 - 2349}] or \
        sum(d["amount_cents"] for d in disc) == frame["money"]["subtotal_cents"] - q["subtotal_cents"]


def test_brygid_orders_get_the_builtin_channel(admin, store, publish) -> None:
    o = store.order()
    publish(o)
    wait_order(store.key, o["order_id"])
    chans = wait_until(lambda: [c for c in admin.get(f"{ADMIN_V1}/channels", params={"location_id": store.location_id}, timeout=5).json()
                                if c["builtin"]], timeout=10, what="the built-in vendor channel")
    assert chans[0]["kind"] == "vendor" and chans[0]["name"] == "Brygid" and chans[0]["active"] is False


@pytest.mark.xdist_group("geo")
def test_delivery_area(admin, store, mgr, fake_geo) -> None:
    m = _menu(mgr)
    _pin_store(admin, store)
    web, op = _channel(admin, store, "web"), _channel(admin, store, "operator")
    near, far = _street(), _street()
    fake_geo.add(near, 40.6690, -74.1143)
    fake_geo.add(far, 40.9000, -74.1143)
    addr = lambda st: {"line1": st, "city": "Bayonne", "state": "NJ", "zip": "07002"}  # noqa: E731
    r = _place(web, _order([_pizza(m)], method="card", token="tok_visa", type="delivery", address=addr(far)))
    assert r.status_code == 422 and check(r, "placeOrder")["code"] == "outside_area"
    o = check(_place(web, _order([_pizza(m)], method="card", token="tok_visa", type="delivery", address=addr(near))), "placeOrder")
    assert o["type"] == "delivery"
    # The call center may send it anyway (the tablet flags it).
    assert _place(op, _order([_pizza(m)], type="delivery", address=addr(far))).status_code == 201
