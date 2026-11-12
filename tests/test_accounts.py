"""Website customer accounts + email receipts (weborder stage 2), on dev's
fakes: sign-in codes are logged (read back with GET /admin/v1/dev/signin-code),
mail is kept in memory (GET /admin/v1/dev/mail), Square is the in-process fake.

  * sign-in: code → verify → session (90 days); wrong code; limits; call
    only after the text; logout; only the website takes sessions
  * profile, addresses (first = default, default moves, limits), delete
  * guest web orders with the phone join the account; history; reorder
    (re-priced, a gone item dropped)
  * saved cards: save at checkout (Square), pay with the saved card, another
    customer's card refused, delete a card / the account turns cards off
  * receipts: placed (From = the store, Reply-To = the store's email, the
    tracking link on the store's host), none without an email, the final one
    after a late tip on the card, the cancelled one; a refused address fails
"""

from __future__ import annotations

import secrets
import uuid

import pytest
import requests

from conftest import ADMIN_V1, MAIN_BASE_URL, STORE_V1, wait_until
from openapi import check
from test_order_port import _channel, _menu, _ok, _order, _pizza, _place
from test_square import FAKE, _connect, _ev, _fake_payment, _floor, _hold, _settled

CH_V1 = f"{MAIN_BASE_URL}/channel/v1"


@pytest.fixture(autouse=True)
def _dev_fakes(admin):
    if admin.get(f"{ADMIN_V1}/dev/mail", params={"to": "x@example.com"}, timeout=5).status_code != 200:
        pytest.skip("main isn't running the dev fakes (sign-in codes / mail)")


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


def _ip() -> str:
    """A customer's IP as the storefront forwards it (each test its own, so
    the per-IP sign-in limit never trips across parallel tests and runs)."""
    return f"10.{secrets.randbelow(256)}.{secrets.randbelow(256)}.{secrets.randbelow(254) + 1}"


def _phone() -> str:
    return "410" + str(secrets.randbelow(9000000) + 1000000)


def _sign_in(admin, web: dict, phone: str, name: str = "Ada Lovelace") -> tuple[dict, dict]:
    """→ (the session answer, headers with the session)."""
    web = {**web, "X-Customer-IP": _ip()}
    r = requests.post(f"{CH_V1}/account/code", json={"phone": phone}, headers=web, timeout=5)
    sent = check(r, "sendSignInCode")
    assert r.status_code == 202 and sent["channel"] == "sms" and sent["sent_to"].endswith(phone[-4:])
    code = _ok(admin.get(f"{ADMIN_V1}/dev/signin-code", params={"phone": phone}, timeout=5))["code"]
    s = check(requests.post(f"{CH_V1}/account/verify", json={"phone": phone, "code": code, "name": name}, headers=web, timeout=5),
              "verifySignInCode")
    assert s["session"].startswith("qcs_")
    web.pop("X-Customer-IP")
    return s, {**web, "X-Customer-Session": s["session"]}


def _mail(admin, to: str) -> list[dict]:
    return _ok(admin.get(f"{ADMIN_V1}/dev/mail", params={"to": to}, timeout=5))["messages"]


def _receipts(admin, oid: str) -> dict[str, str]:
    _ok(admin.post(f"{ADMIN_V1}/dev/receipts/tick", timeout=30))
    return {r["kind"]: r["status"] for r in _ok(admin.get(f"{ADMIN_V1}/orders/{oid}/receipts", timeout=5))["receipts"]}


def test_sign_in_profile_addresses(admin, store) -> None:
    web = {**_channel(admin, store, "web"), "X-Customer-IP": _ip()}
    phone = _phone()
    # A wrong code first.
    _ok(requests.post(f"{CH_V1}/account/code", json={"phone": phone}, headers=web, timeout=5), 202)
    r = requests.post(f"{CH_V1}/account/verify", json={"phone": phone, "code": "000000x"}, headers=web, timeout=5)
    assert r.status_code == 422 and check(r, "verifySignInCode")["code"] == "bad_code"
    # A call only after the text had its chance.
    r = requests.post(f"{CH_V1}/account/code", json={"phone": phone, "channel": "call"}, headers=web, timeout=5)
    assert r.status_code == 429 and r.json()["code"] == "call_too_soon"
    r = requests.post(f"{CH_V1}/account/code", json={"phone": "12"}, headers=web, timeout=5)
    assert r.status_code == 400 and r.json()["code"] == "bad_phone"

    s, me = _sign_in(admin, web, phone)
    assert s["new"] is True and s["customer"]["phone"] == phone and s["customer"]["name"] == "Ada Lovelace"
    acct = check(requests.get(f"{CH_V1}/account", headers=me, timeout=5), "getAccount")
    assert acct["addresses"] == [] and acct["cards"] == [] and acct["email"] == ""
    # The same phone again: the same account.
    s2, _ = _sign_in(admin, web, phone, name="Someone Else")
    assert s2["new"] is False and s2["customer"]["id"] == s["customer"]["id"] and s2["customer"]["name"] == "Ada Lovelace"

    r = requests.patch(f"{CH_V1}/account", json={"email": "not-an-email"}, headers=me, timeout=5)
    assert r.status_code == 400
    acct = check(requests.patch(f"{CH_V1}/account", json={"name": "Ada L.", "email": "ada@example.com"}, headers=me, timeout=5), "updateAccount")
    assert acct["name"] == "Ada L." and acct["email"] == "ada@example.com"

    home = check(requests.post(f"{CH_V1}/account/addresses", json={"label": "Home", "line1": "931 Taylor Ave", "zip": "21286",
                                                                    "notes": "gate 12"}, headers=me, timeout=5), "addAddress")
    assert home["default"] is True  # the first one
    work = check(requests.post(f"{CH_V1}/account/addresses", json={"label": "Work", "line1": "1 Main St", "city": "Towson",
                                                                    "default": True}, headers=me, timeout=5), "addAddress")
    assert work["default"] is True
    acct = requests.get(f"{CH_V1}/account", headers=me, timeout=5).json()
    assert [(a["label"], a["default"]) for a in acct["addresses"]] == [("Work", True), ("Home", False)]
    assert requests.post(f"{CH_V1}/account/addresses", json={"line1": "x"}, headers=me, timeout=5).status_code == 400
    upd = check(requests.put(f"{CH_V1}/account/addresses/{home['id']}", json={"label": "Home", "line1": "931 Taylor Ave", "zip": "21286",
                                                                              "default": True}, headers=me, timeout=5), "putAddress")
    assert upd["default"] is True
    assert requests.delete(f"{CH_V1}/account/addresses/{work['id']}", headers=me, timeout=5).status_code == 204
    assert requests.delete(f"{CH_V1}/account/addresses/{work['id']}", headers=me, timeout=5).status_code == 404

    # Signed out: the session stops.
    assert requests.post(f"{CH_V1}/account/logout", headers=me, timeout=5).status_code == 204
    r = requests.get(f"{CH_V1}/account", headers=me, timeout=5)
    assert r.status_code == 401 and r.json()["code"] == "session_invalid"
    r = requests.get(f"{CH_V1}/account", headers=web, timeout=5)
    assert r.status_code == 401 and r.json()["code"] == "sign_in"


def test_limits_and_only_the_website(admin, store, make_store) -> None:
    web = {**_channel(admin, store, "web"), "X-Customer-IP": _ip()}
    phone = _phone()
    for _ in range(3):
        _ok(requests.post(f"{CH_V1}/account/code", json={"phone": phone}, headers=web, timeout=5), 202)
    r = requests.post(f"{CH_V1}/account/code", json={"phone": phone}, headers=web, timeout=5)
    assert r.status_code == 429 and r.json()["code"] == "too_many"
    # The call center can't sign customers in, nor use their sessions.
    op = _channel(admin, store, "operator")
    assert requests.post(f"{CH_V1}/account/code", json={"phone": _phone()}, headers={**op, "X-Customer-IP": _ip()}, timeout=5).status_code == 403
    _, me = _sign_in(admin, web, _phone())
    r = requests.get(f"{CH_V1}/account", headers={**op, "X-Customer-Session": me["X-Customer-Session"]}, timeout=5)
    assert r.status_code == 403
    # Another store's website doesn't know the session.
    other = make_store()
    web2 = _channel(admin, other, "web")
    r = requests.get(f"{CH_V1}/account", headers={**web2, "X-Customer-Session": me["X-Customer-Session"]}, timeout=5)
    assert r.status_code == 401 and r.json()["code"] == "session_invalid"


def test_guest_orders_join_history_and_reorder(admin, store, mgr) -> None:
    m = _menu(mgr)
    web = _channel(admin, store, "web")
    phone = _phone()
    guest = check(_place(web, _order([_pizza(m, qty=2)], method="card", token="tok_visa",
                                     customer={"name": "Ada", "phone": phone, "email": ""})), "placeOrder")
    s, me = _sign_in(admin, web, phone)
    assert s["joined_orders"] == 1
    mine = check(_place(me, _order([_pizza(m), {"item_id": m["wings"]["id"], "qty": 1, "options": [
        {"group_id": m["sauce"]["id"], "option_id": m["bbq"]["id"]}]}], method="card", token="tok_visa",
        customer={"name": "Ada", "phone": phone, "email": ""})), "placeOrder")
    hist = check(requests.get(f"{CH_V1}/account/orders", headers=me, timeout=5), "listAccountOrders")["orders"]
    assert [o["id"] for o in hist] == [mine["id"], guest["id"]]

    re = check(requests.post(f"{CH_V1}/account/orders/{guest['id']}/reorder", json={}, headers=me, timeout=10), "reorder")
    assert re["dropped"] == [] and re["lines"][0]["qty"] == 2 and re["quote"]["total_cents"] > 0
    # Wings leave the menu: a reorder of the second order drops them.
    _ok(mgr.delete(f"{STORE_V1}/me/menu/items/{m['wings']['id']}", timeout=5), 204)
    re = check(requests.post(f"{CH_V1}/account/orders/{mine['id']}/reorder", json={}, headers=me, timeout=10), "reorder")
    assert len(re["lines"]) == 1 and re["dropped"] == ["Wings"]
    # Not your order.
    _, other = _sign_in(admin, web, _phone())
    assert requests.post(f"{CH_V1}/account/orders/{guest['id']}/reorder", json={}, headers=other, timeout=5).status_code == 404
    assert check(requests.get(f"{CH_V1}/account/orders", headers=other, timeout=5), "listAccountOrders")["orders"] == []


def test_saved_cards(admin, store, mgr) -> None:
    _connect(mgr)
    m = _menu(mgr)
    web = _channel(admin, store, "web")
    phone = _phone()
    # Saving needs a signed-in customer.
    guest = _order([_pizza(m)])
    guest["payment"] = {"method": "card", "token": "cnon:card-nonce-ok", "save_card": True}
    r = _place(web, guest)
    assert r.status_code == 400 and check(r, "placeOrder")["code"] == "bad_request"

    _, me = _sign_in(admin, web, phone)
    body = _order([_pizza(m)], customer={"name": "Ada", "phone": phone, "email": ""})
    body["payment"] = {"method": "card", "token": "cnon:card-nonce-ok", "save_card": True}
    o = check(_place(me, body), "placeOrder")
    cards = requests.get(f"{CH_V1}/account", headers=me, timeout=5).json()["cards"]
    assert len(cards) == 1 and cards[0]["last4"] == "1111" and cards[0]["brand"] == "VISA" and cards[0]["exp_year"] == 2030
    assert _fake_payment(_hold(admin, o["id"])["payment_ref"])["status"] == "APPROVED"

    # One tap: pay with the saved card (still a hold).
    body = _order([_pizza(m)], customer={"name": "Ada", "phone": phone, "email": ""})
    body["payment"] = {"method": "card", "card_id": cards[0]["id"]}
    o2 = check(_place(me, body), "placeOrder")
    sq = _fake_payment(_hold(admin, o2["id"])["payment_ref"])
    assert sq["status"] == "APPROVED" and sq["source_card_id"]
    card_ref = sq["source_card_id"]

    # Another customer can't use it.
    _, other = _sign_in(admin, web, _phone())
    body = _order([_pizza(m)])
    body["payment"] = {"method": "card", "card_id": cards[0]["id"]}
    r = _place(other, body)
    assert r.status_code == 422 and check(r, "placeOrder")["code"] == "card_not_found"

    # Forget it: turned off at Square too.
    assert requests.delete(f"{CH_V1}/account/cards/{cards[0]['id']}", headers=me, timeout=5).status_code == 204
    assert _ok(requests.get(f"{FAKE}/_fake/cards/{card_ref}", timeout=5))["enabled"] is False
    assert requests.get(f"{CH_V1}/account", headers=me, timeout=5).json()["cards"] == []

    # Save another, then delete the account: the card goes, the orders stay.
    body = _order([_pizza(m)], customer={"name": "Ada", "phone": phone, "email": ""})
    body["payment"] = {"method": "card", "token": "cnon:card-nonce-no-edit", "save_card": True}
    o3 = check(_place(me, body), "placeOrder")
    ref3 = _fake_payment(_hold(admin, o3["id"])["payment_ref"])["source_card_id"]
    assert requests.delete(f"{CH_V1}/account", headers=me, timeout=10).status_code == 204
    assert _ok(requests.get(f"{FAKE}/_fake/cards/{ref3}", timeout=5))["enabled"] is False
    assert requests.get(f"{CH_V1}/account", headers=me, timeout=5).status_code == 401
    assert requests.get(f"{CH_V1}/orders/{o3['id']}", headers=web, timeout=5).status_code == 200


def test_receipts(admin, store, mgr) -> None:
    _connect(mgr)
    store.online()  # the Pi is there (it plays the floor below)
    m = _menu(mgr)
    # The storefront's way in, so receipts link to the store's host.
    pk = _ok(admin.post(f"{ADMIN_V1}/platform-keys", json={"name": "test receipts"}, timeout=5), 201)
    host = f"t-{store.tag}.quiccpos.com"
    _ok(admin.post(f"{ADMIN_V1}/locations/{store.location_id}/site-domains", json={"host": host}, timeout=5), 201)
    web = {"Authorization": f"Bearer {pk['key']}", "X-Site-Host": host}
    try:
        _ok(mgr.put(f"{STORE_V1}/me/site/contact", json={"reply_email": "owner@example.com"}, timeout=5))
        assert mgr.put(f"{STORE_V1}/me/site/contact", json={"reply_email": "nope"}, timeout=5).status_code == 400
        # The web channel takes cash too here (as Lombardi's will).
        ch = [c for c in admin.get(f"{ADMIN_V1}/channels", params={"location_id": store.location_id}, timeout=5).json()
              if c["kind"] == "web" and c["builtin"]][0]
        _ok(admin.patch(f"{ADMIN_V1}/channels/{ch['id']}", json={"permissions": {"menu": True, "quote": True, "orders": True,
                                                                                 "track": True, "payments": ["card", "cash"]}}, timeout=5))

        email = f"ada-{secrets.token_hex(4)}@example.com"
        cash = check(_place(web, _order([_pizza(m)], customer={"name": "Ada Lovelace", "phone": "4105550101", "email": email})), "placeOrder")
        assert _receipts(admin, cash["id"]) == {"placed": "sent"}
        msg = _mail(admin, email)[-1]
        assert msg["FromName"] == store.name and msg["FromAddress"] == "orders@quiccpos.com" and msg["ReplyTo"] == "owner@example.com"
        assert msg["Subject"].startswith(f"Your {store.name} order #")
        assert f"https://{host}/track/{cash['track_token']}" in msg["Text"] and "Pay at the store" in msg["Text"]
        assert "Cheese Pizza" in msg["HTML"] and "Follow your order" in msg["HTML"]

        # No email: no receipt.
        none = check(_place(web, _order([_pizza(m)], customer={"name": "Bo", "phone": "4105550102", "email": ""})), "placeOrder")
        assert _receipts(admin, none["id"]) == {}

        # A card hold + a late tip that can't ride on it: the final receipt shows what was charged.
        email2 = f"cy-{secrets.token_hex(4)}@example.com"
        body = _order([_pizza(m)], method="card", token="cnon:card-nonce-no-edit", customer={"name": "Cy", "phone": "4105550103", "email": email2})
        card = check(_place(web, body), "placeOrder")
        assert _receipts(admin, card["id"]) == {"placed": "sent"}
        assert "is authorized for" in _mail(admin, email2)[-1]["Text"]
        tip = _ev("order.tip", order_id=card["id"], tip_cents=400)
        _floor(store, tip)
        _ok(admin.post(f"{ADMIN_V1}/card-holds/{card['id']}/capture", timeout=30))
        assert _settled(admin, card["id"])["status"] == "captured"
        assert wait_until(lambda: _receipts(admin, card["id"]).get("final") == "sent", timeout=15)
        final = _mail(admin, email2)[-1]
        assert final["Subject"].startswith("Your final receipt") and "including a $4.00 tip added after your order" in final["Text"]

        # Cancelled before anyone accepted it: the hold is released, the customer told.
        email3 = f"di-{secrets.token_hex(4)}@example.com"
        body = _order([_pizza(m)], method="card", token="cnon:card-nonce-ok", customer={"name": "Di", "phone": "4105550104", "email": email3})
        gone = check(_place(web, body), "placeOrder")
        _floor(store, _ev("order.status", order_id=gone["id"], status="cancelled"))
        assert wait_until(lambda: _receipts(admin, gone["id"]).get("final") == "sent", timeout=15)
        last = _mail(admin, email3)[-1]
        assert "was cancelled" in last["Subject"] and "you weren't charged" in last["Text"]

        # An address the mail service refuses: given up, not retried forever.
        bad = check(_place(web, _order([_pizza(m)], customer={"name": "Ed", "phone": "4105550105", "email": "fail@example.com"})), "placeOrder")
        assert _receipts(admin, bad["id"]) == {"placed": "failed"}
    finally:
        admin.post(f"{ADMIN_V1}/platform-keys/{pk['platform_key']['id']}/revoke", timeout=5)


def test_unknown_session_on_orders_is_refused(admin, store, mgr) -> None:
    m = _menu(mgr)
    web = _channel(admin, store, "web")
    r = _place({**web, "X-Customer-Session": "qcs_" + uuid.uuid4().hex}, _order([_pizza(m)], method="card", token="tok_visa"))
    assert r.status_code == 401 and r.json()["code"] == "session_invalid"
