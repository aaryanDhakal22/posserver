"""General refund (the owner, 2026-11-12; agent contract §2.10x, contract/main.md
§2.5zb "refund.general"): cash a terminal gave back with no order.

  * stored as an unlinked counter refund (no order, the Pi's batch, the reason)
  * idempotent like order.payment: the same id / client_key + body = duplicate,
    another body = conflict (kept in the flags, nothing stored)
  * a manager every time: a non-manager's general refund nobody approved is flagged (refund_needs_manager)
  * main's Z check counts it with the batch's cash (the Pi's cash net is less
    the general refund): a Z that has it matches, one that misses it doesn't
  * a store with only a general refund still counts as holding money (S-1)
"""

from __future__ import annotations

import uuid

import requests

from conftest import ADMIN_V1
from test_money_hardening import ME, _codes, _now, _ok, _send


def _general(amount: int, batch: int = 3, **kw) -> dict:
    reason = kw.pop("reason", "Spilled drink")
    p = {"id": str(uuid.uuid4()), "method": "cash", "amount_cents": -amount, "tip_cents": 0, "at": _now(),
         "batch_id": batch, **kw}
    return {"event_id": str(uuid.uuid4()), "type": "refund.general", "at": _now(), "payment": p,
            "reason": reason, "batch_id": batch, "kind": "general"}


def _z(batch: int, cash_net: int) -> dict:
    zid = str(uuid.uuid4())
    report = {"id": zid, "batch_id": batch, "business_date": "2026-11-12", "tenders": [
        {"tender": "cash", "count": 0, "collected_cents": 0, "refunded_cents": -cash_net, "tips_cents": 0, "net_cents": cash_net}],
        "reconcile": {"difference_cents": 0}, "sales": {"total_cents": 0},
        "general_refunds": {"count": 1, "cents": -cash_net}}
    return {"event_id": str(uuid.uuid4()), "type": "z.report", "at": _now(), "report": report}


def test_general_refund_stored_idempotent_and_in_the_z_check(store) -> None:
    mgr = store.manager_session()
    first = _general(500, batch=8, client_key="gen-key-1")
    assert _send(store.key, [first]) == {first["event_id"]: "applied"}
    # The same event again, and the same key + body under a new id: duplicates.
    again = {**first, "event_id": str(uuid.uuid4())}
    retry = _general(500, batch=8, client_key="gen-key-1")
    assert _send(store.key, [again, retry]) == {again["event_id"]: "duplicate", retry["event_id"]: "duplicate"}
    # The same key, another amount: a conflict (nothing stored).
    other = _general(900, batch=8, client_key="gen-key-1")
    assert _send(store.key, [other]) == {other["event_id"]: "conflict"}
    assert "payment_conflict" in _codes(mgr, f"refund:{other['payment']['id']}")
    # Not a refund: invalid (kept and retried by the Pi; never stored as money).
    bad = _general(-300, batch=8)
    assert _send(store.key, [bad]) == {bad["event_id"]: "invalid"}

    # Batch 8's Z: cash net -$5.00 (the general refund) = main's payments.
    z = _z(8, -500)
    assert _send(store.key, [z]) == {z["event_id"]: "applied"}
    got = _ok(mgr.get(f"{ME}/reports/z/{z['report']['id']}", timeout=5))
    assert got["flagged"] is False and got["main_difference_cents"] == 0, got
    assert got["main_check"] == [{"tender": "cash", "pi_cents": -500, "main_cents": -500}]
    assert got["report"]["general_refunds"] == {"count": 1, "cents": 500}

    # A late general refund of batch 8 (after its Z): the check moves and flags it.
    late = _general(200, batch=8)
    assert _send(store.key, [late]) == {late["event_id"]: "applied"}
    got = _ok(mgr.get(f"{ME}/reports/z/{z['report']['id']}", timeout=5))
    assert got["main_difference_cents"] == -200 and got["flagged"] is True, got
    assert "z_mismatch" in _codes(mgr, "z:8")


def test_a_general_refund_nobody_approved_is_flagged(store) -> None:
    """A manager every time (the owner, 2026-11-12): a cashier's general refund with no approval is stored and
    flagged refund_needs_manager, the one flag whatever the amount (contract/money-rules.json G01 / G02); a
    manager's own or an approved one is clean."""
    mgr = store.manager_session()
    cashier = _ok(mgr.post(f"{ME}/staff", json={"first_name": "Cara", "role": "cashier", "pin": "4822"}, timeout=10), 201)
    boss = _ok(mgr.post(f"{ME}/staff", json={"first_name": "Mia", "role": "manager", "pin": "4823"}, timeout=10), 201)
    small = _general(500, staff_id=cashier["id"])                          # nobody approved
    big = _general(2500, staff_id=cashier["id"])                            # nobody approved, over her limit
    approved = _general(9000, staff_id=cashier["id"], approved_by=boss["id"])
    own = _general(700, staff_id=boss["id"])                                # a manager's own
    assert set(_send(store.key, [small, big, approved, own]).values()) == {"applied"}
    sub = lambda e: f"refund:{e['payment']['id']}"  # noqa: E731
    assert _codes(mgr, sub(small)) == {"refund_needs_manager"}
    assert _codes(mgr, sub(big)) == {"refund_needs_manager"}
    assert _codes(mgr, sub(approved)) == set()
    assert _codes(mgr, sub(own)) == set()


def test_a_real_store_with_only_a_general_refund_holds_money(admin, make_store) -> None:
    real = make_store(test=False)
    ev = _general(700)
    assert _send(real.key, [ev]) == {ev["event_id"]: "applied"}
    r = admin.delete(f"{ADMIN_V1}/tenants/{real.tenant_id}", timeout=10)
    body = r.json()
    assert r.status_code == 409 and body["code"] == "has_money_records" and body["money"]["payments"] == 1, body
    # The teardown purges it (dev only): the unlinked row goes with the store.
