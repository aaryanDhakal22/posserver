"""Square on the counter terminal (agent contract §2.10r, main contract §2.5e), against dev main's fake
Square: the Pi's RPCs square.session / square.keyed_payment / square.refund with the store's key.

  * square.session: the store's token + Square location + expiry, in_person; not linked = an answer
  * square.keyed_payment: MOTO (seller_keyed_in), tip, reference_id; the same key = the same payment;
    declines are answers (card_declined + Square's code); Square away = 500 (outcome unknown)
  * square.refund: partial, the same key = the same refund, over the rest = refund_failed
  * Store › Settings › Payments says in_person
"""

from __future__ import annotations

import uuid

import pytest
import requests

from conftest import STORE_V1, store_key
from test_square import FAKE, _connect, _fake_payment


@pytest.fixture(autouse=True)
def _fake_square_on():
    if requests.get(f"{FAKE}/_fake/payments", timeout=5).status_code != 200:
        pytest.skip("main isn't running the fake Square (SQUARE_ENV=fake)")


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


def _rpc(store, typ: str, payload: dict) -> tuple[int, dict]:
    env = {"type": typ, "id": str(uuid.uuid4()), "payload": payload}
    r = requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(store.key), json=env, timeout=30)
    body = r.json() if r.content else {}
    return r.status_code, (body.get("payload") if r.status_code == 200 else body)


def _keyed(nonce: str, key: str, cents: int, tip: int = 0) -> dict:
    return {"nonce": nonce, "amount_cents": cents, "tip_cents": tip, "idempotency_key": key, "reference_id": f"ck-{key}"[:40]}


def test_not_linked_is_an_answer(store) -> None:
    for typ, body in (("square.session", {}), ("square.keyed_payment", _keyed("cnon:card-nonce-ok", "k-nl", 100)),
                      ("square.refund", {"payment_id": "pay_x", "amount_cents": 100, "idempotency_key": "r-nl"})):
        code, p = _rpc(store, typ, body)
        assert code == 200 and p["ok"] is False and p["code"] == "square_not_linked", (typ, code, p)


def test_session_keyed_payment_and_refund(store, mgr) -> None:
    st = _connect(mgr)
    me = mgr.get(f"{STORE_V1}/me/square", timeout=10).json()
    assert me["in_person"] is True and "PAYMENTS_WRITE_IN_PERSON" in me["scopes"], me

    code, s = _rpc(store, "square.session", {})
    assert code == 200 and s["ok"] and s["access_token"] and s["location_id"] == st["square_location_id"], s
    assert s["environment"] == "fake" and s["expires_at"] and s["in_person"] is True and s["app_id"], s

    key = f"att-{uuid.uuid4().hex[:8]}-k1"
    code, k = _rpc(store, "square.keyed_payment", _keyed("cnon:card-nonce-ok", key, 1777, 300))
    assert code == 200 and k["ok"], k
    pay = k["payment"]
    assert pay["status"] == "COMPLETED" and pay["last4"] == "1111" and pay["entry"] == "keyed" and pay["receipt_number"], pay
    at = _fake_payment(pay["id"])
    assert at["seller_keyed_in"] is True and at["amount_money"]["amount"] == 1777 and at["tip_money"]["amount"] == 300, at
    assert at["location_id"] == st["square_location_id"], at
    # A lost answer: the same key = the same payment.
    code, again = _rpc(store, "square.keyed_payment", _keyed("cnon:card-nonce-ok", key, 1777, 300))
    assert code == 200 and again["payment"]["id"] == pay["id"], again

    # A decline is an answer.
    code, d = _rpc(store, "square.keyed_payment", _keyed("cnon:card-nonce-rejected-cvv", f"{key}2", 1000))
    assert code == 200 and not d["ok"] and d["code"] == "card_declined" and d["square_code"] == "CVV_FAILURE", d

    # Square away: 500 (the Pi says 503, outcome unknown); only this merchant's call fails.
    r = requests.post(f"{FAKE}/_fake/fail", json={"op": "create", "status": 500, "code": "INTERNAL_SERVER_ERROR",
                                                  "merchant": st["merchant_id"]}, timeout=5)
    assert r.status_code == 204
    code, _ = _rpc(store, "square.keyed_payment", _keyed("cnon:card-nonce-ok", f"{key}3", 500))
    assert code == 500

    # Refunds: part, the same key again, more than is left.
    rk = f"ref-{uuid.uuid4().hex[:8]}"
    code, ref = _rpc(store, "square.refund", {"payment_id": pay["id"], "amount_cents": 1000, "idempotency_key": rk, "reason": "Wrong order"})
    assert code == 200 and ref["ok"] and ref["refund_id"] and ref["status"] in ("PENDING", "COMPLETED"), ref
    code, ref2 = _rpc(store, "square.refund", {"payment_id": pay["id"], "amount_cents": 1000, "idempotency_key": rk})
    assert code == 200 and ref2["refund_id"] == ref["refund_id"], ref2
    assert _fake_payment(pay["id"])["refunded_money"]["amount"] == 1000
    code, over = _rpc(store, "square.refund", {"payment_id": pay["id"], "amount_cents": 1078, "idempotency_key": rk + "x"})
    assert code == 200 and not over["ok"] and over["code"] == "refund_failed", over


@pytest.mark.parametrize("body", [{"amount_cents": 100, "idempotency_key": "k"}, _keyed("cnon:card-nonce-ok", "", 100),
                                  _keyed("cnon:card-nonce-ok", "k", 0)])
def test_bad_keyed_payment_is_400(store, body) -> None:
    code, _ = _rpc(store, "square.keyed_payment", body)
    assert code == 400


def test_general_card_refund_unlinked_and_the_fallback(store, mgr) -> None:
    """Batch J (agent §2.10x, main §2.5e): square.general_refund = Square's unlinked refund to the card field's token
    (the same key = the same refund; a seller Square refuses = unlinked_unsupported; Square away = 500, not known);
    square.card_payments = the store's recent card payments with that last 4 that still hold the amount, then an
    ordinary linked square.refund on the one the manager picked."""
    code, nl = _rpc(store, "square.general_refund", {"nonce": "cnon:card-nonce-ok", "amount_cents": 100, "idempotency_key": "g-nl"})
    assert code == 200 and nl["ok"] is False and nl["code"] == "square_not_linked", nl
    st = _connect(mgr)
    key = f"gr{uuid.uuid4().hex[:20]}"
    body = {"nonce": "cnon:card-nonce-ok", "amount_cents": 650, "idempotency_key": key, "reason": "Overcharged"}
    code, g = _rpc(store, "square.general_refund", body)
    assert code == 200 and g["ok"] and g["unlinked"] is True and g["status"] == "PENDING" and g["last4"] == "1111", g
    code, g2 = _rpc(store, "square.general_refund", body)
    assert code == 200 and g2["refund_id"] == g["refund_id"], g2
    code, un = _rpc(store, "square.general_refund", {**body, "nonce": "cnon:unlinked-refund-unsupported", "idempotency_key": key + "u"})
    assert code == 200 and not un["ok"] and un["code"] == "unlinked_unsupported", un
    r = requests.post(f"{FAKE}/_fake/fail", json={"op": "refund", "status": 503, "code": "TEMPORARY_ERROR",
                                                  "merchant": st["merchant_id"]}, timeout=5)
    assert r.status_code == 204
    code, _ = _rpc(store, "square.general_refund", {**body, "idempotency_key": key + "t"})
    assert code == 500  # the Pi answers 503: not known, nothing recorded
    code, bad = _rpc(store, "square.general_refund", {**body, "nonce": ""})
    assert code == 400

    # The fallback: a typed card's payment of $20, listed for last 4 1111 and $6.50, refunded linked.
    pk = f"att-{uuid.uuid4().hex[:8]}-k1"
    code, k = _rpc(store, "square.keyed_payment", _keyed("cnon:card-nonce-ok", pk, 2000))
    assert code == 200 and k["ok"], k
    code, lst = _rpc(store, "square.card_payments", {"last4": "1111", "amount_cents": 650})
    assert code == 200 and lst["ok"], lst
    mine = [p for p in lst["payments"] if p["square_payment_id"] == k["payment"]["id"]]
    assert mine and mine[0]["refundable_cents"] == 2000 and mine[0]["entry"] == "keyed", lst
    code, ref = _rpc(store, "square.refund", {"payment_id": mine[0]["square_payment_id"], "amount_cents": 650,
                                              "idempotency_key": f"lr{uuid.uuid4().hex[:20]}", "reason": "Overcharged"})
    assert code == 200 and ref["ok"], ref
    assert _fake_payment(k["payment"]["id"])["refunded_money"]["amount"] == 650
    code, bad = _rpc(store, "square.card_payments", {"last4": "11", "amount_cents": 650})
    assert code == 400
