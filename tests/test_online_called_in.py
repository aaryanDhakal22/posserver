"""Online orders = called-in orders (the owner, 2026-11-12; agent contract §2.10aa, main §2.5zb), main's side.

  * the store edits an online (vendor) or website order like its own: the Pi sends order.changed with the
    order's own source; main rewrites its lines + money (+ the address), keeps its source and its payments
    (a website card hold keeps its amount: the manager refunds by hand), and notes what the order owes its
    customer back (source_metadata.refund_owed_cents + a refund_owed flag: a note, never a payment)
  * a change in another channel's name (a counter snapshot for an online order, a web one for a vendor
    order) is still invalid
  * tips are card only: a cash / comp payment's tip (or one added afterwards) is stored and flagged cash_tip
  * a manager may refund an earlier day's order (not carried): the refund lands in today's batch (its Z);
    a cashier's with nobody approving is flagged refund_needs_manager (money-rules R24 / R25 / R33-R36)
"""

from __future__ import annotations

import uuid

import pytest
import requests

from conftest import API_V1, MAIN_BASE_URL, STORE_V1, store_key, wait_order
from test_money_hardening import _codes, _flags, _now, _ok, _pay, _place, _send

ME = f"{STORE_V1}/me"


def _get(key: str, oid: str) -> dict:
    return _ok(requests.get(f"{API_V1}/orders/{oid}", headers=store_key(key), timeout=5))


def _changed(o: dict, items: list[dict], money: dict, **kw) -> dict:
    """The Pi's order.changed snapshot of a channel order: the order as main sent it, with the new lines and
    money, the terminals' payments on it (none here), who changed it."""
    snap = {k: v for k, v in o.items() if k not in ("payments",)}
    snap.update(items=items, money=money, payments=[], staff_id=str(uuid.uuid4()), device_id="terminal-1")
    snap.update(kw)
    return snap


def _line(name: str, cents: int) -> dict:
    return {"id": str(uuid.uuid4()), "position": 9, "name": name, "quantity": 1, "unit_price_cents": cents,
            "line_total_cents": cents, "routing": "matched", "sku": "custom"}


def _money(sub: int, tax: int = 0) -> dict:
    return {"subtotal_cents": sub, "tax_cents": tax, "tip_cents": 0, "fees": [], "discounts": [], "total_cents": sub + tax}


def test_online_order_changed_by_the_store(store, publish) -> None:
    mgr = store.manager_session()
    v = store.order()
    publish(v)
    online = wait_order(store.key, v["order_id"])
    oid = online["id"]
    assert online["source"] == "online"
    before_pays = online.get("payments") or []
    orig = online["items"]
    sub = sum(i["line_total_cents"] for i in orig)

    # Added a line (the balance is collected at the counter as an order.payment, not here).
    wings = _line("Wings", 899)
    add = {"event_id": str(uuid.uuid4()), "type": "order.changed", "order_id": oid, "at": _now(1),
           "snapshot": _changed(online, [*orig, wings], _money(sub + 899)), "refund_owed_cents": 0, "refund_due": None}
    assert _send(store.key, [add]) == {add["event_id"]: "applied"}
    got = _get(store.key, oid)
    assert got["source"] == "online" and sorted(i["name"] for i in got["items"]) == sorted([*(i["name"] for i in orig), "Wings"])
    assert got["money"]["total_cents"] == sub + 899
    assert (got.get("payments") or []) == before_pays, "the channel's payment never moves"
    assert got["source_metadata"].get("refund_owed_cents") == 0

    # Removed the vendor's line (it arrived paid): the customer is owed it back. A note, not a payment.
    due = {"cents": sub, "total_owed_cents": sub, "lines": [{"name": orig[0]["name"], "qty": 1, "cents": sub}],
           "reference": str(v["order_id"]), "source": "online", "number": 1104, "staff_name": "Cara S.",
           "device_name": "Counter 1", "at": _now(2)}
    rm = {"event_id": str(uuid.uuid4()), "type": "order.changed", "order_id": oid, "at": _now(2),
          "snapshot": _changed(online, [wings], _money(899)), "refund_owed_cents": sub, "refund_due": due}
    assert _send(store.key, [rm]) == {rm["event_id"]: "applied"}
    got = _get(store.key, oid)
    assert [i["name"] for i in got["items"]] == ["Wings"] and got["money"]["total_cents"] == 899
    assert got["source_metadata"]["refund_owed_cents"] == sub
    assert (got.get("payments") or []) == before_pays
    owed = [f for f in _flags(mgr) if f["subject"] == f"order:{oid}" and f["code"] == "refund_owed"]
    assert len(owed) == 1, owed
    d = owed[0]["detail"]
    assert d["cents"] == sub and d["reference"] == str(v["order_id"]) and d["order_owed_cents"] == sub and d["lines"][0]["qty"] == 1

    # Another channel's snapshot for this order: invalid (a counter one, a website one).
    for src in ("counter", "web"):
        bad = {"event_id": str(uuid.uuid4()), "type": "order.changed", "order_id": oid, "at": _now(3),
               "snapshot": _changed(online, [wings], _money(899), source=src)}
        assert _send(store.key, [bad]) == {bad["event_id"]: "invalid"}, src
    # An older change after a newer one: stale (forward-only, as for ours).
    old = {**add, "event_id": str(uuid.uuid4()), "at": _now(-60)}
    assert _send(store.key, [old]) == {old["event_id"]: "stale"}


def test_website_card_order_changed_keeps_its_hold(admin, store) -> None:
    fake = f"{MAIN_BASE_URL}/__fake/square"
    if requests.get(f"{fake}/_fake/payments", timeout=5).status_code != 200:
        pytest.skip("main isn't running the fake Square (SQUARE_ENV=fake)")
    from test_square import _card_order, _connect, _ev, _floor, _hold, _settled

    mgr = store.manager_session()
    _connect(mgr)
    r, _web = _card_order(admin, store, mgr, "cnon:card-nonce-ok")
    o = r.json()
    assert r.status_code == 201, r.text
    oid, total = o["id"], o["money"]["total_cents"]
    held = _hold(admin, oid)["amount_cents"]
    acc = _ev("order.status", order_id=oid, status="accepted", kitchen_number=1101, agent_status={"state": "accepted"}, batch_id=5)
    assert _floor(store, acc) == {acc["event_id"]: "applied"}

    web = _get(store.key, oid)
    assert web["source"] == "web"
    cut = max(web["money"]["subtotal_cents"] - 300, 0)
    items = [{**web["items"][0], "line_total_cents": cut, "unit_price_cents": cut}]
    owed = total - cut
    ch = _ev("order.changed", order_id=oid, snapshot=_changed(web, items, _money(cut)), refund_owed_cents=owed,
             refund_due={"cents": owed, "total_owed_cents": owed, "lines": [], "reference": oid, "source": "web"})
    assert _floor(store, ch) == {ch["event_id"]: "applied"}
    assert _get(store.key, oid)["money"]["total_cents"] == cut
    assert _hold(admin, oid)["amount_cents"] == held, "the hold never follows the new total"

    closed = _ev("batch.closed", batch_id=5)
    assert _floor(store, closed) == {closed["event_id"]: "applied"}
    h = _settled(admin, oid)
    assert h["status"] == "captured" and h["captured_cents"] == total, "captured as authorized: the manager refunds by hand"


def test_cash_tip_is_flagged(store) -> None:
    mgr = store.manager_session()
    oid = _place(store.key, total=1500, batch=3)
    cash = _pay(oid, 1500, "cash", tip_cents=300, batch_id=3)
    assert _send(store.key, [cash]) == {cash["event_id"]: "applied"}, "stored as sent: the drawer moved"
    assert _codes(mgr, f"payment:{cash['payment']['id']}") == {"cash_tip"}

    oid2 = _place(store.key, total=1500, batch=3)
    card = _pay(oid2, 1500, "card", tip_cents=300, ref=f"sq-{uuid.uuid4().hex[:10]}", batch_id=3)
    later_cash = _pay(oid, 0, "cash", tip_cents=200, ref=cash["payment"]["id"], batch_id=3)  # a tip set afterwards
    assert set(_send(store.key, [card, later_cash]).values()) == {"applied"}
    assert _codes(mgr, f"payment:{card['payment']['id']}") == set()
    assert _codes(mgr, f"payment:{later_cash['payment']['id']}") == {"cash_tip"}


def test_refund_on_an_earlier_days_order(store) -> None:
    mgr = store.manager_session()
    cashier = _ok(mgr.post(f"{ME}/staff", json={"first_name": "Cara", "role": "cashier", "pin": "4831"}, timeout=10), 201)
    manager = _ok(mgr.post(f"{ME}/staff", json={"first_name": "Khawar", "role": "manager", "pin": "9831"}, timeout=10), 201)
    oid = _place(store.key, total=3000, batch=3)
    assert _send(store.key, [_pay(oid, 3000, batch_id=3)]).popitem()[1] == "applied"

    # The next day (batch 4): a manager's refund, one a manager approved, one nobody approved.
    own = {**_pay(oid, -500, staff_id=manager["id"], batch_id=4), "earlier_day": True}
    ok = {**_pay(oid, -400, staff_id=cashier["id"], approved_by=manager["id"], batch_id=4), "earlier_day": True}
    alone = {**_pay(oid, -300, staff_id=cashier["id"], batch_id=4), "earlier_day": True}
    # The flag may ride the payment too.
    alone2 = _pay(oid, -200, staff_id=cashier["id"], batch_id=4, earlier_day=True)
    assert set(_send(store.key, [own, ok, alone, alone2]).values()) == {"applied"}
    sub = lambda e: f"payment:{e['payment']['id']}"  # noqa: E731
    assert _codes(mgr, sub(own)) == set() and _codes(mgr, sub(ok)) == set()
    assert _codes(mgr, sub(alone)) == {"refund_needs_manager"}
    assert _codes(mgr, sub(alone2)) == {"refund_needs_manager"}
    assert sorted(p["amount_cents"] for p in _get(store.key, oid)["payments"]) == [-500, -400, -300, -200, 3000]

    # Today's Z (batch 4) holds the refunds: main's check agrees with the Pi's cash tender.
    z = {"id": str(uuid.uuid4()), "batch_id": 4, "business_date": "2026-11-13", "tenders": [
        {"tender": "cash", "count": 4, "collected_cents": 0, "refunded_cents": 1400, "tips_cents": 0, "net_cents": -1400}],
        "reconcile": {"difference_cents": 0}, "sales": {"total_cents": 0}}
    ev = {"event_id": str(uuid.uuid4()), "type": "z.report", "at": _now(), "report": z}
    assert _send(store.key, [ev]) == {ev["event_id"]: "applied"}
    got = _ok(mgr.get(f"{ME}/reports/z/{z['id']}", timeout=5))
    assert got["main_difference_cents"] == 0, got
    assert got["main_check"] == [{"tender": "cash", "pi_cents": -1400, "main_cents": -1400}], got["main_check"]
