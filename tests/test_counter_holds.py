"""Counter delivery holds (agent contract §2.10ao, main contract §2.5e / §2.5za, the owner 2026-11-14): a
delivery's card taken at the counter (the call center, Centurion's Manual Entry, Centurion's reader) is
authorized only, so the driver's typed card tip goes on it; main's card hold machine captures it with the
tip. The test plays the Pi (RPCs + floor events) against dev main's fake Square.

  * square.keyed_payment {hold: true} = APPROVED (not COMPLETED), delay_action COMPLETE; hold false = COMPLETED
  * order.placed / order.payment with a payment marked held → a card hold (gateway square_counter, 24 h)
  * square.tip_adjust keeps the typed tip on the hold; Close batch / the driver's clock-out captures
    amount + tip; the 24 h safety capture still fires
  * a held payment on an order that isn't a delivery is captured at once
  * a tender void (square.refund of the whole hold) cancels it, never refunds it; an order void cancels it
"""

from __future__ import annotations

import uuid
from datetime import datetime, timedelta, timezone

import pytest
import requests

from conftest import ADMIN_V1, STORE_V1, store_key, wait_until
from test_counter_orders import _now, _placed, _send, _snapshot
from test_square import FAKE, _connect, _fake_payment, _hold, _ok, _settled


@pytest.fixture(autouse=True)
def _fake_square_on():
    if requests.get(f"{FAKE}/_fake/payments", timeout=5).status_code != 200:
        pytest.skip("main isn't running the fake Square (SQUARE_ENV=fake)")


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


TOTAL = 1777


def _rpc(store, typ: str, payload: dict) -> dict:
    env = {"type": typ, "id": str(uuid.uuid4()), "payload": payload}
    r = requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(store.key), json=env, timeout=30)
    assert r.status_code == 200, r.text
    return r.json()["payload"]


def _keyed(store, hold: bool, cents: int = TOTAL) -> dict:
    k = f"att-{uuid.uuid4().hex[:10]}-k1"
    a = _rpc(store, "square.keyed_payment", {"nonce": "cnon:card-nonce-ok", "amount_cents": cents, "tip_cents": 0,
                                             "idempotency_key": k, "reference_id": f"ck-{k}"[:40], "hold": hold})
    assert a["ok"], a
    return a["payment"]


def _order(kind: str, pay: dict, held: bool = True) -> tuple[str, dict]:
    oid = str(uuid.uuid4())
    money = {"subtotal_cents": 1299, "tax_cents": 78, "tip_cents": 0, "discounts": [], "total_cents": TOTAL,
             "fees": [{"type": "delivery", "label": "Delivery fee", "amount_cents": 400, "driver_cents": 300}]}
    ful: dict = {"type": kind}
    if kind == "delivery":
        ful["address"] = {"line1": "931 Taylor Ave", "city": "Towson", "state": "MD", "postal_code": "21286", "country": "US"}
    else:
        money = {**money, "fees": [], "total_cents": 1377}
    item = {"id": str(uuid.uuid4()), "position": 0, "name": "Cheese Pizza", "quantity": 1, "unit_price_cents": 1299,
            "line_total_cents": 1299, "routing": "matched"}
    p = {"id": str(uuid.uuid4()), "method": "manual_card", "amount_cents": pay["amount_cents"], "tip_cents": 0, "ref": pay["id"],
         "last4": "1111", "brand": "visa", "client_key": f"sq-{uuid.uuid4().hex[:12]}", "at": _now()}
    if held:
        p["held"] = True
    return oid, _snapshot(oid, fulfillment=ful, items=[item], money=money, payments=[p])


def _place(store, snap: dict) -> None:
    ev = _placed(snap)
    assert _send(store.key, [ev]) == {ev["event_id"]: "applied"}


def _ev(type_: str, **kw) -> dict:
    return {"event_id": str(uuid.uuid4()), "type": type_, "at": _now(), **kw}


def test_delivery_keyed_card_is_a_hold_captured_with_the_typed_tip_at_close(admin, store, mgr) -> None:
    _connect(mgr)
    pay = _keyed(store, hold=True)
    assert pay["status"] == "APPROVED" and pay["held"] is True, pay
    sq = _fake_payment(pay["id"])
    assert sq["status"] == "APPROVED" and sq["delay_action"] == "COMPLETE", sq

    oid, snap = _order("delivery", pay)
    _place(store, snap)
    h = _hold(admin, oid)
    assert h["gateway"] == "square_counter" and h["status"] == "held" and h["amount_cents"] == TOTAL, h
    due = datetime.fromisoformat(h["due_at"].replace("Z", "+00:00"))
    assert timedelta(hours=23) < due - datetime.now(timezone.utc) <= timedelta(hours=24), h["due_at"]

    # The press of Close batch: the Pi's square.tip_adjust (the typed $4.00) is kept on the hold ...
    t = _rpc(store, "square.tip_adjust", {"square_payment_id": pay["id"], "tip_cents": 400, "idempotency_key": "dt-" + oid[:30]})
    assert t["ok"] and t["tip_cents"] == 400, t
    assert _hold(admin, oid)["added_tip_cents"] == 400
    # ... then batch.closed captures amount + tip.
    closed = _ev("batch.closed", batch_id=3)
    assert _send(store.key, [closed]) == {closed["event_id"]: "applied"}
    h = _settled(admin, oid)
    assert h["status"] == "captured" and h["capture_reason"] == "batch_close" and h["captured_cents"] == TOTAL + 400, h
    sq = _fake_payment(pay["id"])
    assert sq["status"] == "COMPLETED" and sq["tip_money"]["amount"] == 400 and sq["total_money"]["amount"] == TOTAL + 400, sq

    # Captured: a later tip edit is not_adjustable (batch F's page), never a second capture.
    late = _rpc(store, "square.tip_adjust", {"square_payment_id": pay["id"], "tip_cents": 500, "idempotency_key": "dt-late-" + oid[:20]})
    assert not late["ok"] and late["code"] == "not_adjustable", late


def test_pickup_keyed_card_is_still_completed(admin, store, mgr) -> None:
    _connect(mgr)
    pay = _keyed(store, hold=False)
    assert pay["status"] == "COMPLETED" and not pay.get("held"), pay
    oid, snap = _order("pickup", pay, held=False)
    _place(store, snap)
    assert admin.get(f"{ADMIN_V1}/card-holds/{oid}", timeout=5).status_code == 404


def test_a_held_card_on_a_pickup_is_captured_at_once(admin, store, mgr) -> None:
    _connect(mgr)
    pay = _keyed(store, hold=True)
    oid, snap = _order("pickup", pay)  # the client said delivery, the order is a pickup
    _place(store, snap)
    h = _settled(admin, oid)
    assert h["status"] == "captured" and h["capture_reason"] == "not_delivery", h
    assert _fake_payment(pay["id"])["status"] == "COMPLETED"


def test_clock_out_captures_his_counter_holds_with_the_tip(admin, store, mgr) -> None:
    _connect(mgr)
    pay = _keyed(store, hold=True)
    oid, snap = _order("delivery", pay)
    _place(store, snap)
    sid, did = str(uuid.uuid4()), str(uuid.uuid4())
    clock_in = (datetime.now(timezone.utc) - timedelta(minutes=5)).isoformat()
    shift_in = _ev("driver.shift", shift_id=sid, driver_id=did, driver_name="Sam", clock_in=clock_in)
    run = _ev("delivery.run", batch_id=3, driver_id=did, driver_name="Sam", sent_ids=[oid], stops=[])
    assert set(_send(store.key, [shift_in, run]).values()) == {"applied"}
    t = _rpc(store, "square.tip_adjust", {"square_payment_id": pay["id"], "tip_cents": 300, "idempotency_key": "dt-co-" + oid[:20]})
    assert t["ok"], t
    assert _hold(admin, oid)["status"] == "held"  # nothing until the clock-out
    out = _ev("driver.shift", shift_id=sid, driver_id=did, driver_name="Sam", clock_in=clock_in, clock_out=_now())
    assert _send(store.key, [out]) == {out["event_id"]: "applied"}
    h = _settled(admin, oid)
    assert h["status"] == "captured" and h["capture_reason"] == "clock_out" and h["captured_cents"] == TOTAL + 300, h
    sq = _fake_payment(pay["id"])
    assert sq["status"] == "COMPLETED" and sq["total_money"]["amount"] == TOTAL + 300, sq


def test_tender_void_cancels_the_hold_never_refunds_it(admin, store, mgr) -> None:
    _connect(mgr)
    pay = _keyed(store, hold=True)
    oid, snap = _order("delivery", pay)
    _place(store, snap)
    # The Pi's tender void: square.refund of the whole hold.
    r = _rpc(store, "square.refund", {"payment_id": pay["id"], "amount_cents": TOTAL, "idempotency_key": "tv-" + oid[:30],
                                      "reason": "Tender voided"})
    assert r["ok"] and r["voided"] is True and r["status"] == "CANCELED", r
    sq = _fake_payment(pay["id"])
    assert sq["status"] == "CANCELED" and sq["refunded_money"]["amount"] == 0, sq
    assert _hold(admin, oid)["status"] == "cancelled"
    # The Pi's refund payment follows; Close batch never touches the cancelled hold.
    refund = {"id": str(uuid.uuid4()), "method": "manual_card", "amount_cents": -TOTAL, "tip_cents": 0, "ref": pay["id"],
              "refund_of": snap["payments"][0]["id"], "kind": "tender_void", "client_key": f"tv-{uuid.uuid4().hex[:12]}", "at": _now()}
    e = _ev("order.payment", order_id=oid, payment=refund)
    closed = _ev("batch.closed", batch_id=3)
    assert set(_send(store.key, [e, closed]).values()) == {"applied"}
    _ok(admin.post(f"{ADMIN_V1}/card-holds/tick", timeout=30))
    assert _hold(admin, oid)["status"] == "cancelled"
    assert _fake_payment(pay["id"])["status"] == "CANCELED"


def test_order_void_cancels_its_hold(admin, store, mgr) -> None:
    _connect(mgr)
    pay = _keyed(store, hold=True)
    oid, snap = _order("delivery", pay)
    _place(store, snap)
    v = _ev("order.void", order_id=oid, reason="customer cancelled", staff_id=snap["staff_id"])
    assert _send(store.key, [v]) == {v["event_id"]: "applied"}
    _ok(admin.post(f"{ADMIN_V1}/card-holds/tick", timeout=30))
    assert wait_until(lambda: _hold(admin, oid)["status"] == "cancelled", timeout=15)
    assert _fake_payment(pay["id"])["status"] == "CANCELED"


def test_safety_capture_after_24_hours_still_fires(admin, store, mgr) -> None:
    _connect(mgr)
    pay = _keyed(store, hold=True)
    oid, snap = _order("delivery", pay)
    _place(store, snap)
    h = _ok(admin.post(f"{ADMIN_V1}/card-holds/{oid}/due", timeout=30))
    if h["status"] == "held":
        h = _settled(admin, oid)
    assert h["status"] == "captured" and h["capture_reason"] == "" and h["captured_cents"] == TOTAL, h
    assert _fake_payment(pay["id"])["status"] == "COMPLETED"
