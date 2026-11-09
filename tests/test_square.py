"""Square (weborder stage 2), against the in-process fake Square that dev main
serves under /__fake/square (SQUARE_ENV unset on dev = fake).

  * a store connects Square (OAuth: connect link → the fake's page → main's
    callback → the SPA), the one card-taking location is picked, disconnect
  * GET /channel/v1/payments: the SDK's setup once connected
  * a card order is a hold: authorized, not charged; captured by the safety
    timer with a tip added afterwards that the hold can carry (raised)
  * a hold that can't grow: captured at the driver's clock-out, the late tip
    charged on the card saved for that order, the card turned off after
  * Close batch captures the batch; a cancelled order's hold is voided
  * declines, 3-D Secure, a store without Square (the test card), webhooks
  * disconnect is refused while holds are open
"""

from __future__ import annotations

import base64
import hashlib
import hmac
import json
import secrets
import uuid
from datetime import datetime, timedelta, timezone

import pytest
import requests

from conftest import ADMIN_V1, MAIN_BASE_URL, STORE_V1, store_key, wait_until
from openapi import check
from test_order_port import _channel, _menu, _ok, _order, _pizza, _place

FAKE = f"{MAIN_BASE_URL}/__fake/square"
CH_V1 = f"{MAIN_BASE_URL}/channel/v1"


@pytest.fixture(autouse=True)
def _fake_square_on():
    r = requests.get(f"{STORE_V1}/me/square", timeout=5)  # 401 without a session: the route exists
    if requests.get(f"{FAKE}/_fake/payments", timeout=5).status_code != 200:
        pytest.skip("main isn't running the fake Square (SQUARE_ENV=fake)")
    assert r.status_code == 401


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


def _connect(mgr) -> dict:
    url = _ok(mgr.post(f"{STORE_V1}/me/square/connect", timeout=5))["url"]
    assert url.startswith(FAKE + "/oauth2/authorize?")
    r = requests.get(url, timeout=10)  # the fake's page → main's callback → the SPA
    assert "square=connected" in r.url, r.url
    st = _ok(mgr.get(f"{STORE_V1}/me/square", timeout=10))
    assert st["connected"] and st["env"] == "fake" and st["square_location_id"]
    return st


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _floor(store, *events) -> dict[str, str]:
    env = {"type": "floor.events", "id": str(uuid.uuid4()), "payload": {"events": list(events)}}
    r = requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(store.key), json=env, timeout=10)
    assert r.status_code == 200, r.text
    return {x["event_id"]: x["result"] for x in r.json()["payload"]["results"]}


def _ev(type_: str, **kw) -> dict:
    return {"event_id": str(uuid.uuid4()), "type": type_, "at": _now(), **kw}


def _hold(admin, oid: str) -> dict:
    return _ok(admin.get(f"{ADMIN_V1}/card-holds/{oid}", timeout=5))


def _settled(admin, oid: str) -> dict:
    """The hold once the worker is done with it (the background worker may be
    the one that captures it, so poll rather than trust one tick)."""
    _ok(admin.post(f"{ADMIN_V1}/card-holds/tick", timeout=30))
    assert wait_until(lambda: _hold(admin, oid)["status"] != "held", timeout=20)
    return _hold(admin, oid)


def _fake_payment(pid: str) -> dict:
    return _ok(requests.get(f"{FAKE}/_fake/payments/{pid}", timeout=5))


def _card_order(admin, store, mgr, token: str, **kw):
    m = _menu(mgr)
    web = _channel(admin, store, "web")
    r = _place(web, _order([_pizza(m)], method="card", token=token, **kw))
    return r, web


def test_connect_and_disconnect(admin, store, mgr) -> None:
    st = _ok(mgr.get(f"{STORE_V1}/me/square", timeout=5))
    assert st["enabled"] and st["connected"] is False and st["state"] == "" and st["app_id"] == "sq0idp-fake"
    web = _channel(admin, store, "web")
    pay = check(requests.get(f"{CH_V1}/payments", headers=web, timeout=5), "getPayments")
    assert pay["card"] is None and pay["methods"] == ["card"] and pay["hold_hours"] == 24

    st = _connect(mgr)
    assert [loc["id"] for loc in st["locations"]] == [st["square_location_id"]] and st["holds_open"] == 0
    pay = check(requests.get(f"{CH_V1}/payments", headers=web, timeout=5), "getPayments")
    assert pay["card"] == {"provider": "square", "env": "fake", "app_id": "sq0idp-fake", "location_id": st["square_location_id"]}

    r = mgr.put(f"{STORE_V1}/me/square/location", json={"square_location_id": "L-not-mine"}, timeout=10)
    assert r.status_code == 400
    assert mgr.put(f"{STORE_V1}/me/square/location", json={"square_location_id": st["square_location_id"]}, timeout=10).status_code == 200

    # A forged or expired state never connects anything.
    r = requests.get(f"{MAIN_BASE_URL}/square/oauth/callback", params={"code": "x", "state": "nope"}, allow_redirects=False, timeout=5)
    assert r.status_code == 302 and "square=expired" in r.headers["Location"]
    # The owner said no on Square's page.
    url = _ok(mgr.post(f"{STORE_V1}/me/square/connect", timeout=5))["url"] + "&deny=1"
    assert "square=denied" in requests.get(url, timeout=10).url

    assert mgr.delete(f"{STORE_V1}/me/square", timeout=10).status_code == 204
    assert _ok(mgr.get(f"{STORE_V1}/me/square", timeout=5))["connected"] is False
    assert mgr.delete(f"{STORE_V1}/me/square", timeout=10).status_code == 409


def test_hold_then_safety_capture_with_a_raised_tip(admin, store, mgr) -> None:
    _connect(mgr)
    r, _web = _card_order(admin, store, mgr, "cnon:card-nonce-ok", tip_cents=300)
    o = check(r, "placeOrder")
    assert r.status_code == 201 and o["payment"]["method"] == "card"
    total = o["money"]["total_cents"]
    h = _hold(admin, o["id"])
    assert h["status"] == "held" and h["gateway"] == "square" and h["tip_cents"] == 300 and h["amount_cents"] == total - 300
    sq = _fake_payment(h["payment_ref"])
    assert sq["status"] == "APPROVED" and sq["total_money"]["amount"] == total and sq["tip_money"]["amount"] == 300
    # Paying again with the same client_order_id doesn't hold twice.
    made = _ok(requests.get(f"{FAKE}/_fake/payments", params={"reference_id": o["client_order_id"]}, timeout=5))["payments"]
    assert len(made) == 1

    # A tip written on the receipt, entered on Quibase (absolute; a replay is harmless).
    tip = _ev("order.tip", order_id=o["id"], tip_cents=500)
    assert _floor(store, tip) == {tip["event_id"]: "applied"}
    assert _floor(store, tip) == {tip["event_id"]: "duplicate"}
    bad = _ev("order.tip", order_id=o["id"], tip_cents=-1)
    assert _floor(store, bad) == {bad["event_id"]: "invalid"}

    # Nobody clocked out or closed the batch: main captures it 24 h later (made due now).
    h = _ok(admin.post(f"{ADMIN_V1}/card-holds/{o['id']}/due", timeout=30))
    assert h["status"] == "captured" and h["captured_cents"] == total + 500 and h["tip_note"] == "added tip on the card hold"
    sq = _fake_payment(h["payment_ref"])
    assert sq["status"] == "COMPLETED" and sq["tip_money"]["amount"] == 800 and sq["total_money"]["amount"] == total + 500

    # Captured: a later tip has no hold to go on.
    late = _ev("order.tip", order_id=o["id"], tip_cents=900)
    assert _floor(store, late) == {late["event_id"]: "stale"}


def test_late_tip_on_the_saved_card_at_clock_out(admin, store, mgr) -> None:
    _connect(mgr)
    r, _web = _card_order(admin, store, mgr, "cnon:card-nonce-no-edit")
    o = check(r, "placeOrder")
    total = o["money"]["total_cents"]
    sid, did = str(uuid.uuid4()), str(uuid.uuid4())
    clock_in = (datetime.now(timezone.utc) - timedelta(minutes=5)).isoformat()
    shift_in = _ev("driver.shift", shift_id=sid, driver_id=did, driver_name="Sam", clock_in=clock_in)
    run = _ev("delivery.run", batch_id=1, driver_id=did, driver_name="Sam", sent_ids=[o["id"]], stops=[])
    tip = _ev("order.tip", order_id=o["id"], tip_cents=400)
    assert set(_floor(store, shift_in, run, tip).values()) == {"applied"}
    assert _hold(admin, o["id"])["status"] == "held"  # nothing until the clock-out

    out = _ev("driver.shift", shift_id=sid, driver_id=did, driver_name="Sam", clock_in=clock_in, clock_out=_now())
    assert _floor(store, out) == {out["event_id"]: "applied"}
    h = _settled(admin, o["id"])
    assert h["capture_reason"] == "clock_out"
    assert h["status"] == "captured" and h["tip_note"] == "added tip charged separately" and h["tip_payment_ref"]
    assert h["captured_cents"] == total + 400
    assert _fake_payment(h["payment_ref"])["total_money"]["amount"] == total  # the hold couldn't grow
    tip_pay = _fake_payment(h["tip_payment_ref"])
    assert tip_pay["status"] == "COMPLETED" and tip_pay["total_money"]["amount"] == 400
    # The card was saved for this one tip and turned off right after.
    card = _ok(requests.get(f"{FAKE}/_fake/cards/{tip_pay['source_card_id']}", timeout=5))
    assert card["enabled"] is False


def test_close_batch_captures_and_cancel_voids(admin, store, mgr) -> None:
    _connect(mgr)
    m = _menu(mgr)
    web = _channel(admin, store, "web")
    a = check(_place(web, _order([_pizza(m)], method="card", token="cnon:card-nonce-ok")), "placeOrder")
    b = check(_place(web, _order([_pizza(m)], method="card", token="cnon:card-nonce-ok")), "placeOrder")
    acc_a = _ev("order.status", order_id=a["id"], status="accepted", kitchen_number=1101, agent_status={"state": "accepted"}, batch_id=7)
    # b is never accepted: cancelled by the Pi (cancel after accept is stage 3).
    cancel_b = _ev("order.status", order_id=b["id"], status="cancelled")
    assert set(_floor(store, acc_a, cancel_b).values()) == {"applied"}
    # The cancelled order's hold is released (in the background).
    assert wait_until(lambda: _hold(admin, b["id"])["status"] == "cancelled", timeout=10)
    assert _fake_payment(_hold(admin, b["id"])["payment_ref"])["status"] == "CANCELED"

    closed = _ev("batch.closed", batch_id=7)
    assert _floor(store, closed) == {closed["event_id"]: "applied"}
    h = _settled(admin, a["id"])
    assert h["capture_reason"] == "batch_close"
    assert h["status"] == "captured" and _fake_payment(h["payment_ref"])["status"] == "COMPLETED"
    # What the manager sees.
    holds = _ok(mgr.get(f"{STORE_V1}/me/card-holds", timeout=5))["holds"]
    assert {x["order_id"]: x["status"] for x in holds} == {a["id"]: "captured", b["id"]: "cancelled"}


def test_declines_and_stores_without_square(admin, store, mgr, make_store) -> None:
    _connect(mgr)
    m = _menu(mgr)
    web = _channel(admin, store, "web")
    r = _place(web, _order([_pizza(m)], method="card", token="cnon:card-nonce-declined"))
    assert r.status_code == 402 and check(r, "placeOrder")["code"] == "payment_declined"
    # 3-D Secure: the storefront verifies the buyer, then sends verification_token.
    r = _place(web, _order([_pizza(m)], method="card", token="cnon:card-nonce-verification"))
    assert r.status_code == 402 and check(r, "placeOrder")["code"] == "verification_required"

    # A store with no Square: the test card (dev / staging), no SDK setup, no hold.
    other = make_store()
    o_mgr = other.manager_session()
    m2 = _menu(o_mgr)
    web2 = _channel(admin, other, "web")
    assert check(requests.get(f"{CH_V1}/payments", headers=web2, timeout=5), "getPayments")["card"] is None
    o = check(_place(web2, _order([_pizza(m2)], method="card", token="tok_visa")), "placeOrder")
    assert o["payment"] == {"method": "card", "status": "paid"}
    assert admin.get(f"{ADMIN_V1}/card-holds/{o['id']}", timeout=5).status_code == 404


def _sign(body: bytes) -> str:
    mac = hmac.new(b"fake-webhook-key", b"http://localhost:1323/square/webhook" + body, hashlib.sha256)
    return base64.b64encode(mac.digest()).decode()


def test_webhooks_and_disconnect_with_open_holds(admin, store, mgr) -> None:
    st = _connect(mgr)
    r, _web = _card_order(admin, store, mgr, "cnon:card-nonce-ok")
    o = check(r, "placeOrder")
    h = _hold(admin, o["id"])
    # Open holds: disconnecting would strand them.
    r = mgr.delete(f"{STORE_V1}/me/square", timeout=10)
    assert r.status_code == 409 and r.json()["code"] == "holds_open"

    body = json.dumps({"merchant_id": st["merchant_id"], "type": "payment.updated", "event_id": secrets.token_hex(8),
                       "data": {"type": "payment", "id": h["payment_ref"],
                                "object": {"payment": {"id": h["payment_ref"], "status": "CANCELED"}}}}).encode()
    hook = f"{MAIN_BASE_URL}/square/webhook"
    r = requests.post(hook, data=body, headers={"X-Square-Hmacsha256-Signature": "bad", "Content-Type": "application/json"}, timeout=5)
    assert r.status_code == 401
    r = requests.post(hook, data=body, headers={"X-Square-Hmacsha256-Signature": _sign(body), "Content-Type": "application/json"}, timeout=5)
    assert r.status_code == 200, r.text
    assert _hold(admin, o["id"])["status"] == "cancelled"  # voided at Square

    # Access revoked in Square: the store stops taking cards.
    body = json.dumps({"merchant_id": st["merchant_id"], "type": "oauth.authorization.revoked", "event_id": secrets.token_hex(8),
                       "data": {"type": "revocation", "id": "x", "object": {}}}).encode()
    r = requests.post(hook, data=body, headers={"X-Square-Hmacsha256-Signature": _sign(body)}, timeout=5)
    assert r.status_code == 200
    assert _ok(mgr.get(f"{STORE_V1}/me/square", timeout=5))["state"] == "revoked"
    web = _channel(admin, store, "web")
    assert check(requests.get(f"{CH_V1}/payments", headers=web, timeout=5), "getPayments")["card"] is None
