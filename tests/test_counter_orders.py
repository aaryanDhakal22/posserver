"""Counter orders from the Pi (TERMINAL-PLAN.md 0d, contract/main.md §2.5zb).
The test plays the Pi: floor events over POST /store/v1/agent/rpc.

  * order.placed → an ordinary order under the Pi's id, source counter, in
    the Pi's state with its counter number; idempotent (event and order id);
    never sent back over SSE (no order frame, no redelivery)
  * later order.status events move it like any order
  * order.payment adds payments / refunds (idempotent by payment id), on
    any order of the store (online "pay at pickup" too)
  * an event main refused is looked at again when the Pi resends it
  * order.void cancels it with the reason, in any state
  * journal.entries → the staff journal (idempotent per entry), listed in
    Store › Staff
  * bad events are answered invalid
"""

from __future__ import annotations

import uuid
from queue import Empty
from datetime import datetime, timezone

import requests

from conftest import ADMIN_V1, API_V1, STORE_V1, store_key, wait_order, wait_until
from gatemark import intentional_sleep
from sse import SSEStream


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _uuid7() -> str:
    # Python 3.11 has no uuid7; any uuid works as an order id.
    return str(uuid.uuid4())


def _send(key: str, events: list[dict]) -> dict[str, str]:
    env = {"type": "floor.events", "id": str(uuid.uuid4()), "payload": {"events": events}}
    r = requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(key), json=env, timeout=10)
    assert r.status_code == 200, r.text
    return {x["event_id"]: x["result"] for x in r.json()["payload"]["results"]}


def _snapshot(oid: str, **kw) -> dict:
    snap = {
        "id": oid, "source": "counter", "source_event_id": f"ck-{oid}", "status": "accepted", "currency": "USD",
        "money": {"subtotal_cents": 2399, "tax_cents": 144, "tip_cents": 0, "fees": [],
                  "discounts": [{"type": "manual", "label": "Manager 10%", "amount_cents": 240}], "total_cents": 2303},
        "customer": {"display_name": "Sam Lee", "phone": "4105550100"},
        "fulfillment": {"type": "counter"},
        "items": [{"id": str(uuid.uuid4()), "position": 0, "name": "Cheese Pizza", "size": "16\" X-Large", "quantity": 1,
                   "unit_price_cents": 2399, "line_total_cents": 2399, "routing": "matched",
                   "children": [{"id": str(uuid.uuid4()), "position": 0, "name": "Pepperoni (1st half)", "quantity": 1,
                                 "unit_price_cents": 0, "line_total_cents": 0, "portion": "first", "mode": "add"}]}],
        "payments": [{"id": str(uuid.uuid4()), "method": "cash", "amount_cents": 2303, "at": _now()}],
        "notes": "", "placed_at": _now(), "kitchen_order_number": 12,
        "staff_id": str(uuid.uuid4()), "device_id": "terminal-1",
    }
    snap.update(kw)
    return snap


def _placed(snap: dict) -> dict:
    return {"event_id": str(uuid.uuid4()), "type": "order.placed", "order_id": snap["id"], "at": _now(),
            "batch_id": 3, "agent_status": {"state": snap.get("status", "accepted")}, "snapshot": snap}


def _get(key: str, oid: str) -> dict:
    r = requests.get(f"{API_V1}/orders/{oid}", headers=store_key(key), timeout=5)
    assert r.status_code == 200, r.text
    return r.json()


def _frame(s: SSEStream):
    try:
        return s.frames.get(timeout=0.5)
    except Empty:
        return None


def test_placed_is_stored_under_the_pis_id_and_never_echoed(store, admin, publish) -> None:
    oid = _uuid7()
    snap = _snapshot(oid)
    with SSEStream(store.key) as s:
        assert s.next()["event"] == "store_config"
        ev = _placed(snap)
        assert _send(store.key, [ev]) == {ev["event_id"]: "applied"}

        o = _get(store.key, oid)
        assert o["id"] == oid and o["source"] == "counter" and o["status"] == "accepted"
        assert o["kitchen_order_number"] == 12 and o["fulfillment"]["type"] == "counter"
        assert o["money"]["total_cents"] == 2303 and o["money"]["discounts"][0]["label"] == "Manager 10%"
        kid = o["items"][0]["children"][0]
        assert kid["portion"] == "first" and kid["mode"] == "add"
        assert o["payments"] == [{"method": "cash", "amount_cents": 2303}]
        assert o["source_metadata"]["device_id"] == "terminal-1" and o["source_metadata"]["staff_id"] == snap["staff_id"]

        # Replays: the same event, and a new event for the same order.
        again = _placed(snap)
        assert _send(store.key, [ev, again]) == {ev["event_id"]: "duplicate", again["event_id"]: "duplicate"}

        # The Pi made it: no order frame. An online order published after it
        # arrives; nothing for the counter order, also not after main's
        # redelivery window.
        online = store.order()
        publish(online)
        assert s.wait_event("order", lambda d: d.get("source_event_id") == str(online["order_id"]), keep_others=True)
        intentional_sleep(11, "main's redelivery timer (ORDER_REDELIVER_AFTER, 10 s) must not re-send a counter order")
        seen = [f for f in s.kept] + [f for f in iter(lambda: _frame(s), None)]
        assert not [f for f in seen if f["event"] == "order" and oid in f["data"]], "a counter order was sent back to the Pi"

    # The admin list sees it as an ordinary order.
    rows = admin.get(f"{ADMIN_V1}/orders", params={"tenant_id": store.tenant_id}, timeout=5).json()
    rows = rows.get("orders", rows) if isinstance(rows, dict) else rows
    assert any(r["id"] == oid for r in rows)


def test_status_payment_void(store) -> None:
    oid = _uuid7()
    snap = _snapshot(oid, payments=[], fulfillment={"type": "pickup"}, customer={"display_name": "Ana", "phone": "4105550111"})
    ev = _placed(snap)
    assert _send(store.key, [ev]) == {ev["event_id"]: "applied"}
    assert not _get(store.key, oid).get("payments"), "pay later: no payment yet"

    # A later floor event moves it like any order.
    ready = {"event_id": str(uuid.uuid4()), "type": "order.status", "order_id": oid, "status": "ready", "at": _now()}
    assert _send(store.key, [ready])[ready["event_id"]] == "applied"
    assert _get(store.key, oid)["status"] == "ready"

    # Pay at pickup: card with a tip; then a partial refund.
    pay = {"id": str(uuid.uuid4()), "method": "card", "amount_cents": 2303, "tip_cents": 300, "last4": "4242",
           "auth_code": "A1", "ref": "sq_pay_1", "at": _now(), "staff_id": snap["staff_id"]}
    e1 = {"event_id": str(uuid.uuid4()), "type": "order.payment", "order_id": oid, "payment": pay, "at": _now()}
    refund = {**pay, "id": str(uuid.uuid4()), "amount_cents": -500, "tip_cents": 0}
    e2 = {"event_id": str(uuid.uuid4()), "type": "order.payment", "order_id": oid, "payment": refund, "at": _now()}
    e3 = {**e1, "event_id": str(uuid.uuid4())}  # the same payment id again
    assert _send(store.key, [e1, e2, e3]) == {e1["event_id"]: "applied", e2["event_id"]: "applied", e3["event_id"]: "duplicate"}
    pays = _get(store.key, oid)["payments"]
    assert sorted(p["amount_cents"] for p in pays) == [-500, 2303]
    assert {p["method"] for p in pays} == {"card"} and any(p.get("last4") == "4242" for p in pays)

    # Bad payments / unknown order: only what main can't store is invalid
    # (an unknown tender, a negative tip); a zero amount with no tip is
    # stored and flagged (hardening M-2).
    bad = [
        {"event_id": str(uuid.uuid4()), "type": "order.payment", "order_id": oid, "payment": {**pay, "id": str(uuid.uuid4()), "method": "iou"}},
        {"event_id": str(uuid.uuid4()), "type": "order.payment", "order_id": oid,
         "payment": {**pay, "id": str(uuid.uuid4()), "tip_cents": -1}},
    ]
    zero = {"event_id": str(uuid.uuid4()), "type": "order.payment", "order_id": oid,
            "payment": {**pay, "id": str(uuid.uuid4()), "amount_cents": 0, "tip_cents": 0, "ref": ""}}
    nf = {"event_id": str(uuid.uuid4()), "type": "order.payment", "order_id": str(uuid.uuid4()), "payment": {**pay, "id": str(uuid.uuid4())}}
    res = _send(store.key, [*bad, zero, nf])
    assert [res[b["event_id"]] for b in bad] == ["invalid", "invalid"] and res[nf["event_id"]] == "not_found"
    assert res[zero["event_id"]] == "applied"
    # A tip added to a card payment afterwards (agent §2.10p): a zero amount with a tip is taken.
    tip = {"event_id": str(uuid.uuid4()), "type": "order.payment", "order_id": oid,
           "payment": {**pay, "id": str(uuid.uuid4()), "amount_cents": 0, "tip_cents": 200, "ref": pay["id"]}}
    assert _send(store.key, [tip])[tip["event_id"]] == "applied"

    # Void after it was picked up: cancelled with the reason; twice = stale.
    done = {"event_id": str(uuid.uuid4()), "type": "order.status", "order_id": oid, "status": "delivered", "at": _now()}
    assert _send(store.key, [done])[done["event_id"]] == "applied"
    v1 = {"event_id": str(uuid.uuid4()), "type": "order.void", "order_id": oid, "reason": "customer changed their mind",
          "staff_id": snap["staff_id"], "at": _now()}
    v2 = {**v1, "event_id": str(uuid.uuid4())}
    v3 = {**v1, "event_id": str(uuid.uuid4()), "reason": ""}  # no reason: stored all the same (stale: voided already)
    assert _send(store.key, [v1, v2, v3]) == {v1["event_id"]: "applied", v2["event_id"]: "stale", v3["event_id"]: "stale"}
    assert _get(store.key, oid)["status"] == "cancelled"


def test_void_only_for_counter_orders_payments_for_any(store, make_store, publish) -> None:
    """A void is for counter orders only; a payment the terminal took is kept
    on any order of the store (an online "pay at pickup" order paid in cash at
    the counter), refunds as negative ones, idempotent by payment id."""
    o = store.order()
    publish(o)
    online = wait_order(store.key, o["order_id"])
    oid = online["id"]
    before = online.get("payments") or []
    v = {"event_id": str(uuid.uuid4()), "type": "order.void", "order_id": oid, "reason": "x", "at": _now()}
    pay = {"id": str(uuid.uuid4()), "method": "cash", "amount_cents": 1234, "tip_cents": 100, "at": _now(), "staff_id": str(uuid.uuid4())}
    p = {"event_id": str(uuid.uuid4()), "type": "order.payment", "order_id": oid, "payment": pay}
    refund = {"event_id": str(uuid.uuid4()), "type": "order.payment", "order_id": oid,
              "payment": {**pay, "id": str(uuid.uuid4()), "amount_cents": -200, "tip_cents": 0}}
    again = {**p, "event_id": str(uuid.uuid4())}  # the same payment id
    assert _send(store.key, [v, p, refund, again]) == {v["event_id"]: "invalid", p["event_id"]: "applied",
                                                       refund["event_id"]: "applied", again["event_id"]: "duplicate"}
    after = _get(store.key, oid)
    assert after["status"] != "cancelled"
    assert sorted(x["amount_cents"] for x in after["payments"]) == sorted([*(x["amount_cents"] for x in before), 1234, -200])
    assert after["money"] == online["money"] and after["items"] == online["items"], "the snapshot never moves"

    # Another store's Pi can't pay this order.
    other = make_store()
    foreign = {"event_id": str(uuid.uuid4()), "type": "order.payment", "order_id": oid,
               "payment": {**pay, "id": str(uuid.uuid4())}}
    assert _send(other.key, [foreign]) == {foreign["event_id"]: "not_found"}


def test_a_refused_event_is_looked_at_again(store) -> None:
    """The Pi keeps a refused payment / counter order queued: the same event
    sent again is checked again (not answered duplicate), so it applies once
    the order is there."""
    oid = _uuid7()
    pay = {"id": str(uuid.uuid4()), "method": "cash", "amount_cents": 500, "at": _now()}
    p = {"event_id": str(uuid.uuid4()), "type": "order.payment", "order_id": oid, "payment": pay}
    assert _send(store.key, [p]) == {p["event_id"]: "not_found"}
    assert _send(store.key, [p]) == {p["event_id"]: "not_found"}, "a refused event is not a duplicate"
    placed = _placed(_snapshot(oid, payments=[]))
    assert _send(store.key, [placed, p]) == {placed["event_id"]: "applied", p["event_id"]: "applied"}
    assert _send(store.key, [p]) == {p["event_id"]: "duplicate"}
    assert [x["amount_cents"] for x in _get(store.key, oid)["payments"]] == [500]


def test_bad_placed_events(store, make_store) -> None:
    def ev(**kw):
        return _placed(_snapshot(_uuid7(), **kw))

    cases = [ev(source="online"), ev(items=[]), ev(status="scheduled"), ev(fulfillment={"type": "dine_in"}),
             ev(money={"subtotal_cents": -100, "tax_cents": 0, "tip_cents": 0, "total_cents": 0})]
    nosnap = {"event_id": str(uuid.uuid4()), "type": "order.placed", "order_id": _uuid7(), "at": _now()}
    mismatch = _placed(_snapshot(_uuid7()))
    mismatch["order_id"] = _uuid7()
    res = _send(store.key, [*cases, nosnap, mismatch])
    assert set(res.values()) == {"invalid"}, res

    # Money that doesn't add up is the Pi's sale all the same: stored and
    # flagged, never refused (hardening M-2 / M-13).
    off = ev(money={"subtotal_cents": 2399, "tax_cents": 0, "tip_cents": 0, "total_cents": 999}, payments=[])
    assert _send(store.key, [off]) == {off["event_id"]: "applied"}
    assert _get(store.key, off["order_id"])["money"]["total_cents"] == 999
    flags = store.manager_session().get(f"{STORE_V1}/me/reports/flags", timeout=5).json()["flags"]
    assert any(f["code"] == "money_mismatch" and f["subject"] == f"order:{off['order_id']}" for f in flags), flags

    # The same id from another store's Pi: its own scope, a different order — refused as a duplicate.
    oid = _uuid7()
    first = _placed(_snapshot(oid))
    assert _send(store.key, [first])[first["event_id"]] == "applied"
    other = make_store()
    dup = _placed(_snapshot(oid))
    assert _send(other.key, [dup])[dup["event_id"]] == "duplicate"
    r = requests.get(f"{API_V1}/orders/{oid}", headers=store_key(other.key), timeout=5)
    assert r.status_code == 404, "never another store's order"


def test_journal_entries(store) -> None:
    mgr = store.manager_session()
    staff_id = str(uuid.uuid4())
    entries = [
        {"id": str(uuid.uuid4()), "at": "2026-11-11T17:00:00Z", "staff_id": staff_id, "device_id": "terminal-1",
         "action": "login", "order_id": None, "detail": {}},
        {"id": str(uuid.uuid4()), "at": "2026-11-11T17:05:00Z", "staff_id": staff_id, "device_id": "terminal-1",
         "action": "no_sale", "detail": {"drawer": 1}},
    ]
    e1 = {"event_id": str(uuid.uuid4()), "type": "journal.entries", "at": _now(), "entries": entries}
    e2 = {"event_id": str(uuid.uuid4()), "type": "journal.entries", "at": _now(), "entries": entries[1:]}  # a replayed entry
    assert _send(store.key, [e1, e2]) == {e1["event_id"]: "applied", e2["event_id"]: "applied"}
    rows = wait_until(lambda: (r := mgr.get(f"{STORE_V1}/me/staff/journal", timeout=5).json()) and r)
    assert [r["action"] for r in rows] == ["no_sale", "login"], "newest first, each entry once"
    assert rows[0]["detail"] == {"drawer": 1} and rows[0]["staff_id"] == staff_id and rows[1]["order_id"] is None
    older = mgr.get(f"{STORE_V1}/me/staff/journal", params={"before": "2026-11-11T17:01:00Z"}, timeout=5).json()
    assert [r["action"] for r in older] == ["login"]

    bad = [
        {"event_id": str(uuid.uuid4()), "type": "journal.entries", "entries": []},
        {"event_id": str(uuid.uuid4()), "type": "journal.entries", "entries": [{**entries[0], "id": "x"}]},
        {"event_id": str(uuid.uuid4()), "type": "journal.entries", "entries": [{**entries[0], "id": str(uuid.uuid4()), "action": ""}]},
        {"event_id": str(uuid.uuid4()), "type": "journal.entries", "entries": [{**entries[0], "id": str(uuid.uuid4()), "detail": [1]}]},
    ]
    assert set(_send(store.key, bad).values()) == {"invalid"}
    assert len(mgr.get(f"{STORE_V1}/me/staff/journal", timeout=5).json()) == 2


def test_order_changed_replaces_lines_and_money(store, publish) -> None:
    """A kitchen change after SEND (agent §2.10d): order.changed carries the
    whole new snapshot; main replaces the lines + money, keeps payments,
    status and number."""
    oid = _uuid7()
    snap = _snapshot(oid)
    ev = _placed(snap)
    assert _send(store.key, [ev]) == {ev["event_id"]: "applied"}
    wings = {"id": str(uuid.uuid4()), "position": 1, "name": "Wings", "size": "10 pc", "quantity": 2,
             "unit_price_cents": 1000, "line_total_cents": 2000, "routing": "unassigned"}
    changed = {**snap, "items": [*snap["items"], wings],
               "money": {**snap["money"], "subtotal_cents": 4399, "tax_cents": 264, "total_cents": 4423}}
    ch = {"event_id": str(uuid.uuid4()), "type": "order.changed", "order_id": oid, "at": _now(), "snapshot": changed,
          "staff_id": snap["staff_id"]}
    again = {**ch, "event_id": str(uuid.uuid4())}
    res = _send(store.key, [ch])
    assert res == {ch["event_id"]: "applied"}
    assert _send(store.key, [ch]) == {ch["event_id"]: "duplicate"}
    got = _get(store.key, oid)
    assert sorted(i["name"] for i in got["items"]) == ["Cheese Pizza", "Wings"]
    assert got["money"]["total_cents"] == 4423 and got["money"]["subtotal_cents"] == 4399
    assert got["kitchen_order_number"] == 12 and got["status"] == "accepted"
    assert [p["amount_cents"] for p in got.get("payments") or []] == [2303]
    # The same change again under a new event id: applied again (idempotent content).
    assert _send(store.key, [again]) == {again["event_id"]: "applied"}
    assert len(_get(store.key, oid)["items"]) == 2

    # Negative money (it can't be stored), a snapshot for another id, an unknown order: refused.
    # (Money that only doesn't add up is applied and flagged: test_money_hardening.py.)
    bad_money = {**ch, "event_id": str(uuid.uuid4()), "snapshot": {**changed, "money": {**changed["money"], "total_cents": -1}}}
    other_id = {**ch, "event_id": str(uuid.uuid4()), "order_id": str(uuid.uuid4())}
    unknown = _snapshot(_uuid7())
    nf = {"event_id": str(uuid.uuid4()), "type": "order.changed", "order_id": unknown["id"], "at": _now(), "snapshot": unknown}
    res = _send(store.key, [bad_money, other_id, nf])
    assert res == {bad_money["event_id"]: "invalid", other_id["event_id"]: "invalid", nf["event_id"]: "not_found"}

    # Not a counter order / a voided one: invalid.
    o = store.order()
    publish(o)
    online = wait_order(store.key, o["order_id"])["id"]
    foreign = {**ch, "event_id": str(uuid.uuid4()), "order_id": online, "snapshot": {**changed, "id": online}}
    v = {"event_id": str(uuid.uuid4()), "type": "order.void", "order_id": oid, "reason": "left", "at": _now()}
    late = {**ch, "event_id": str(uuid.uuid4())}
    res = _send(store.key, [foreign, v, late])
    assert res == {foreign["event_id"]: "invalid", v["event_id"]: "applied", late["event_id"]: "invalid"}
