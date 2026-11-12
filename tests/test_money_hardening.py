"""Hardening C1 (HARDENING-PLAN.md §1–§2, 2026-11-12): main never loses or
silently rewrites the store's money.

  * a money event main can store is stored and FLAGGED, never answered
    invalid (the Pi's drawer has moved): GET /store/v1/me/reports/flags
  * payment idempotency: the same payment id / client_key with the same body
    is a duplicate, with another body a conflict (kept whole in the flags)
  * refunds over what was paid / the tender / the original / the person's
    limit, payments on cancelled orders, overpaying: flagged
  * a carried-over unpaid pickup (open across batches) is paid in a later
    batch; main checks each Z against its payments (main_check), flags a
    mismatch and checks again when a late payment lands
  * Z reports and closed drawers keep their history (never overwritten)
  * order.changed is forward-only (an older change is stale), a change after
    the batch's Z is flagged; a void can be a waste with the store's reason
  * a real store with money is never deleted (409, retire it); retired =
    devices revoked; whoami says device_revoked / unknown_key (F-4)
  * the store's terminal settings carry waste_reasons and void_reasons
"""

from __future__ import annotations

import uuid
from datetime import datetime, timedelta, timezone

import requests

from conftest import ADMIN_V1, API_V1, STORE_V1, store_key, wait_order

ME = f"{STORE_V1}/me"


def _now(delta_s: float = 0) -> str:
    return (datetime.now(timezone.utc) + timedelta(seconds=delta_s)).isoformat()


def _send(key: str, events: list[dict]) -> dict[str, str]:
    env = {"type": "floor.events", "id": str(uuid.uuid4()), "payload": {"events": events}}
    r = requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(key), json=env, timeout=10)
    assert r.status_code == 200, r.text
    return {x["event_id"]: x["result"] for x in r.json()["payload"]["results"]}


def _ok(r: requests.Response, status: int = 200) -> dict:
    assert r.status_code == status, f"{r.status_code}: {r.text}"
    return r.json()


def _snapshot(oid: str, total: int = 2303, payments: list | None = None, **kw) -> dict:
    snap = {
        "id": oid, "source": "counter", "source_event_id": f"ck-{oid}", "status": "accepted", "currency": "USD",
        "money": {"subtotal_cents": total, "tax_cents": 0, "tip_cents": 0, "fees": [], "discounts": [], "total_cents": total},
        "customer": {"display_name": "Sam Lee", "phone": "4105550100"}, "fulfillment": {"type": "pickup"},
        "items": [{"id": str(uuid.uuid4()), "position": 0, "name": "Cheese Pizza", "quantity": 1,
                   "unit_price_cents": total, "line_total_cents": total, "routing": "matched"}],
        "payments": payments or [], "notes": "", "placed_at": _now(), "kitchen_order_number": 12,
        "staff_id": str(uuid.uuid4()), "device_id": "terminal-1",
    }
    snap.update(kw)
    return snap


def _place(key: str, total: int = 2303, batch: int = 3, **kw) -> str:
    oid = str(uuid.uuid4())
    ev = {"event_id": str(uuid.uuid4()), "type": "order.placed", "order_id": oid, "at": _now(), "batch_id": batch,
          "agent_status": {"state": "accepted"}, "snapshot": _snapshot(oid, total, **kw)}
    assert _send(key, [ev]) == {ev["event_id"]: "applied"}
    return oid


def _pay(oid: str, amount: int, method: str = "cash", **kw) -> dict:
    p = {"id": str(uuid.uuid4()), "method": method, "amount_cents": amount, "tip_cents": 0, "at": _now(), **kw}
    return {"event_id": str(uuid.uuid4()), "type": "order.payment", "order_id": oid, "payment": p, "at": _now()}


def _flags(mgr) -> list[dict]:
    return _ok(mgr.get(f"{ME}/reports/flags", timeout=5))["flags"]


def _codes(mgr, subject: str) -> set[str]:
    return {f["code"] for f in _flags(mgr) if f["subject"] == subject}


def _payments(key: str, oid: str) -> list[int]:
    o = _ok(requests.get(f"{API_V1}/orders/{oid}", headers=store_key(key), timeout=5))
    return sorted(p["amount_cents"] for p in o.get("payments") or [])


def test_payment_idempotency_and_conflicts(store, make_store) -> None:
    mgr = store.manager_session()
    oid = _place(store.key)
    first = _pay(oid, 1000, client_key="pay-key-1")
    assert _send(store.key, [first]) == {first["event_id"]: "applied"}

    # The same key + the same body under a new payment id (a retry after a
    # timeout): the same payment.
    retry = _pay(oid, 1000, client_key="pay-key-1")
    assert _send(store.key, [retry]) == {retry["event_id"]: "duplicate"}
    # The same key, another body: a conflict (nothing stored, kept in the flags).
    other = _pay(oid, 1500, client_key="pay-key-1")
    assert _send(store.key, [other]) == {other["event_id"]: "conflict"}
    # The same payment id on another order: a conflict too.
    oid2 = _place(store.key)
    stolen = {**_pay(oid2, 1000), "payment": {**first["payment"], "client_key": ""}}
    assert _send(store.key, [stolen]) == {stolen["event_id"]: "conflict"}
    # A Square payment recorded twice under two ids.
    sq1, sq2 = _pay(oid2, 800, "card", ref="sq_dup_1"), _pay(oid2, 800, "card", ref="sq_dup_1")
    assert _send(store.key, [sq1, sq2]) == {sq1["event_id"]: "applied", sq2["event_id"]: "conflict"}

    assert _payments(store.key, oid) == [1000] and _payments(store.key, oid2) == [800]
    conflicts = [f for f in _flags(mgr) if f["code"] == "payment_conflict"]
    assert len(conflicts) == 3, conflicts
    whys = sorted(f["detail"]["why"] for f in conflicts)
    assert whys == ["client_key used by another payment", "payment id holds another payment",
                    "processor reference recorded under another payment id"]
    assert all(f["detail"]["amount_cents"] in (1000, 1500, 800) for f in conflicts), "the money is kept whole"

    # Another store's Pi has its own keys.
    b = make_store()
    oid3 = _place(b.key)
    assert _send(b.key, [_pay(oid3, 1000, client_key="pay-key-1")]).popitem()[1] == "applied"


def test_money_main_disagrees_with_is_stored_and_flagged(store) -> None:
    mgr = store.manager_session()
    oid = _place(store.key, total=2000)
    pay = _pay(oid, 2000, "card", ref="sq_flags_1")
    over = _pay(oid, 500)  # more than the total
    assert set(_send(store.key, [pay, over]).values()) == {"applied"}
    # A cashier's cash back on a card sale nobody approved (the Pi's cash_for_card), then more back than was paid.
    cashier = _ok(mgr.post(f"{ME}/staff", json={"first_name": "Cara", "role": "cashier", "pin": "4820"}, timeout=10), 201)
    cash_back = _pay(oid, -600, staff_id=cashier["id"])
    too_much = _pay(oid, -5000, "card")
    assert set(_send(store.key, [cash_back, too_much]).values()) == {"applied"}
    # A refund of a named payment: more than it took; and one of no payment of this order.
    of_pay = _pay(oid, -2500, "card", refund_of=pay["payment"]["id"], approved_by="mgr-1")
    of_none = _pay(oid, -100, "card", refund_of=str(uuid.uuid4()), approved_by="mgr-1")
    assert set(_send(store.key, [of_pay, of_none]).values()) == {"applied"}
    assert _payments(store.key, oid) == [-5000, -2500, -600, -100, 500, 2000], "every one stored"

    codes = {}
    for f in _flags(mgr):
        codes.setdefault(f["subject"], set()).add(f["code"])
    sub = lambda e: f"payment:{e['payment']['id']}"  # noqa: E731
    assert codes[sub(over)] == {"overpaid"}
    assert codes[sub(cash_back)] == {"refund_exceeds_tender"}
    assert "refund_exceeds_paid" in codes[sub(too_much)]
    assert codes[sub(of_pay)] >= {"refund_exceeds_original"}
    assert "refund_of_unknown" in codes[sub(of_none)]

    # A payment on a voided order.
    v = {"event_id": str(uuid.uuid4()), "type": "order.void", "order_id": oid, "reason": "left", "at": _now()}
    late = _pay(oid, 300)
    assert _send(store.key, [v, late]) == {v["event_id"]: "applied", late["event_id"]: "applied"}
    assert "order_cancelled" in _codes(mgr, sub(late))


def test_refunds_over_the_persons_limit(store) -> None:
    mgr = store.manager_session()
    cashier = _ok(mgr.post(f"{ME}/staff", json={"first_name": "Cara", "role": "cashier", "pin": "4821"}, timeout=10), 201)
    oid = _place(store.key, total=5000)
    assert _send(store.key, [_pay(oid, 5000)]).popitem()[1] == "applied"
    small = _pay(oid, -1500, staff_id=cashier["id"])           # under $20: fine
    big = _pay(oid, -1000, staff_id=cashier["id"])             # $25 in all: over her $20
    card = _pay(oid, -200, "card", staff_id=cashier["id"])     # a cashier's card refund
    approved = _pay(oid, -900, staff_id=cashier["id"], approved_by="manager-7")
    assert set(_send(store.key, [small, big, card, approved]).values()) == {"applied"}
    sub = lambda e: f"payment:{e['payment']['id']}"  # noqa: E731
    assert _codes(mgr, sub(small)) == set()
    assert "over_staff_limit" in _codes(mgr, sub(big))
    assert "refund_needs_manager" in _codes(mgr, sub(card))
    assert _codes(mgr, sub(approved)) == set()


def test_comps_and_their_reversal_follow_the_pis_rules(store) -> None:
    """The staging page of 2026-11-12 (flow VOID D6): a cashier comped an order, then voided it, which reverses
    the comp (a negative comp payment) with nobody approving. The Pi allows it (a comp moves no money); main
    used to flag every non-cash refund by a non-manager refund_needs_manager. Now main checks the Pi's rules
    (contract/money-rules.json R12 / R13 / C01 / C03): a comp reversal is clean at any amount, a cashier's
    comp is clean, a driver's comp nobody approved is comp_needs_manager, and a manager's own limits are
    ignored (max refunds $25 in cash with a $5 limit of his own: clean)."""
    mgr = store.manager_session()
    cashier = _ok(mgr.post(f"{ME}/staff", json={"first_name": "Cara", "role": "cashier", "pin": "4824"}, timeout=10), 201)
    driver = _ok(mgr.post(f"{ME}/staff", json={"first_name": "Dev", "role": "driver", "pin": "4825"}, timeout=10), 201)
    boss = _ok(mgr.post(f"{ME}/staff", json={"first_name": "Max", "role": "manager", "pin": "4826",
                                             "limits": {"refund_cents": 500}}, timeout=10), 201)
    oid = _place(store.key, total=2500)
    comp = _pay(oid, 2500, "comp", staff_id=cashier["id"])
    back = _pay(oid, -2500, "comp", staff_id=cashier["id"])   # over her $20 cash limit: still free
    oid2 = _place(store.key, total=500)
    driver_comp = _pay(oid2, 500, "comp", staff_id=driver["id"])
    oid3 = _place(store.key, total=3000)
    cash = _pay(oid3, 3000, staff_id=cashier["id"])
    own = _pay(oid3, -2500, staff_id=boss["id"])
    assert set(_send(store.key, [comp, back, driver_comp, cash, own]).values()) == {"applied"}
    sub = lambda e: f"payment:{e['payment']['id']}"  # noqa: E731
    assert _codes(mgr, sub(comp)) == set() and _codes(mgr, sub(back)) == set()
    assert _codes(mgr, sub(driver_comp)) == {"comp_needs_manager"}
    assert _codes(mgr, sub(own)) == set()


def test_carried_over_pickup_and_the_z_check(store) -> None:
    mgr = store.manager_session()
    # Batch 3: an unpaid pickup stays on the Pickup list; the batch closes.
    oid = _place(store.key, total=1800, batch=3)
    z3 = {"id": str(uuid.uuid4()), "batch_id": 3, "business_date": "2026-11-12", "tenders": [],
          "reconcile": {"difference_cents": 0}, "sales": {"total_cents": 0}}
    ev = {"event_id": str(uuid.uuid4()), "type": "z.report", "at": _now(), "report": z3}
    assert _send(store.key, [ev]) == {ev["event_id"]: "applied"}
    # Batch 4: it is paid (the Pi says which batch the money is in).
    paid = _pay(oid, 1800, batch_id=4)
    ready = {"event_id": str(uuid.uuid4()), "type": "order.status", "order_id": oid, "status": "delivered", "at": _now()}
    assert set(_send(store.key, [paid, ready]).values()) == {"applied"}
    # Batch 4's Z: its cash matches main's payments; nothing flagged. A
    # waste section (a void-waste) is stored whole like the rest.
    waste = {"count": 1, "cents": 1299, "reasons": [{"reason": "Never picked up", "count": 1, "cents": 1299}]}
    z4 = {"id": str(uuid.uuid4()), "batch_id": 4, "business_date": "2026-11-13", "tenders": [
        {"tender": "cash", "count": 1, "collected_cents": 1800, "refunded_cents": 0, "tips_cents": 0, "net_cents": 1800}],
        "reconcile": {"difference_cents": 0}, "sales": {"total_cents": 1800}, "waste": waste}
    ev4 = {"event_id": str(uuid.uuid4()), "type": "z.report", "at": _now(), "report": z4}
    assert _send(store.key, [ev4]) == {ev4["event_id"]: "applied"}
    got = _ok(mgr.get(f"{ME}/reports/z/{z4['id']}", timeout=5))
    assert got["report"]["waste"] == waste and got["flagged"] is False and got["main_difference_cents"] == 0
    assert got["main_check"] == [{"tender": "cash", "pi_cents": 1800, "main_cents": 1800}]
    assert _ok(mgr.get(f"{ME}/reports/z/{z3['id']}", timeout=5))["flagged"] is False

    # Batch 5's Z says $10 cash main never got: flagged; the late payment
    # arrives and main's check agrees again.
    oid5 = _place(store.key, total=1000, batch=5)
    z5 = {"id": str(uuid.uuid4()), "batch_id": 5, "business_date": "2026-11-14", "tenders": [
        {"tender": "cash", "count": 1, "collected_cents": 1000, "refunded_cents": 0, "tips_cents": 0, "net_cents": 1000}],
        "reconcile": {"difference_cents": 0}, "sales": {"total_cents": 1000}}
    ev5 = {"event_id": str(uuid.uuid4()), "type": "z.report", "at": _now(), "report": z5}
    assert _send(store.key, [ev5]) == {ev5["event_id"]: "applied"}
    got = _ok(mgr.get(f"{ME}/reports/z/{z5['id']}", timeout=5))
    assert got["flagged"] is True and got["main_difference_cents"] == -1000
    assert "z_mismatch" in _codes(mgr, "z:5")
    assert _send(store.key, [_pay(oid5, 1000)]).popitem()[1] == "applied"
    got = _ok(mgr.get(f"{ME}/reports/z/{z5['id']}", timeout=5))
    assert got["flagged"] is False and got["main_difference_cents"] == 0

    # A Z that doesn't balance on the Pi's own numbers is flagged too.
    z6 = {"id": str(uuid.uuid4()), "batch_id": 6, "business_date": "2026-11-15", "tenders": [],
          "reconcile": {"difference_cents": 5}}
    ev6 = {"event_id": str(uuid.uuid4()), "type": "z.report", "at": _now(), "report": z6}
    assert _send(store.key, [ev6]) == {ev6["event_id"]: "applied"}
    assert _ok(mgr.get(f"{ME}/reports/z/{z6['id']}", timeout=5))["flagged"] is True
    assert "z_unbalanced" in _codes(mgr, "z:6")


def test_a_closed_drawer_keeps_its_history(store) -> None:
    mgr = store.manager_session()
    d = {"id": str(uuid.uuid4()), "device_id": "till-1", "device_name": "Till 1", "staff_id": str(uuid.uuid4()),
         "staff_name": "Cara S.", "batch_id": 7, "opened_at": "2026-11-12T15:00:00Z", "start_cents": 10000,
         "closed_at": "2026-11-12T21:00:00Z", "closed_by": "x", "close_reason": "counted", "counted_cents": 15000,
         "expected_cents": 15000, "over_short_cents": 0}
    first = {"event_id": str(uuid.uuid4()), "type": "drawer.closed", "at": _now(), "drawer": d}
    same = {**first, "event_id": str(uuid.uuid4())}
    recount = {"event_id": str(uuid.uuid4()), "type": "drawer.closed", "at": _now(),
               "drawer": {**d, "counted_cents": 14900, "over_short_cents": -100}}
    assert set(_send(store.key, [first, same, recount]).values()) == {"applied"}
    got = _ok(mgr.get(f"{ME}/drawers/{d['id']}", timeout=5))
    assert got["drawer"]["counted_cents"] == 14900
    assert len(got["revisions"]) == 1 and got["revisions"][0]["before"]["counted_cents"] == 15000
    assert "drawer_recount" in _codes(mgr, f"drawer:{d['id']}")


def test_order_changed_forward_only_and_after_z(store) -> None:
    mgr = store.manager_session()
    oid = _place(store.key, total=1000, batch=8)
    newer = {"event_id": str(uuid.uuid4()), "type": "order.changed", "order_id": oid, "at": _now(5),
             "snapshot": _snapshot(oid, 1500)}
    older = {"event_id": str(uuid.uuid4()), "type": "order.changed", "order_id": oid, "at": _now(-5),
             "snapshot": _snapshot(oid, 1200)}
    assert _send(store.key, [newer, older]) == {newer["event_id"]: "applied", older["event_id"]: "stale"}
    assert _ok(requests.get(f"{API_V1}/orders/{oid}", headers=store_key(store.key), timeout=5))["money"]["total_cents"] == 1500

    # A change whose money doesn't add up: applied (the kitchen has it) and flagged.
    off = _snapshot(oid, 1500)
    off["money"]["total_cents"] = 1499
    odd = {"event_id": str(uuid.uuid4()), "type": "order.changed", "order_id": oid, "at": _now(10), "snapshot": off}
    assert _send(store.key, [odd]) == {odd["event_id"]: "applied"}
    assert "money_mismatch" in _codes(mgr, f"order:{oid}")

    # After its batch's Z: applied (a carried-over order may still change) and flagged.
    z = {"id": str(uuid.uuid4()), "batch_id": 8, "business_date": "2026-11-12", "reconcile": {"difference_cents": 0}}
    zev = {"event_id": str(uuid.uuid4()), "type": "z.report", "at": _now(), "report": z}
    after = {"event_id": str(uuid.uuid4()), "type": "order.changed", "order_id": oid, "at": _now(15),
             "snapshot": _snapshot(oid, 1700)}
    assert _send(store.key, [zev, after]) == {zev["event_id"]: "applied", after["event_id"]: "applied"}
    assert "changed_after_z" in _codes(mgr, f"order:{oid}")


def test_void_waste(store) -> None:
    oid = _place(store.key, total=1299)
    v = {"event_id": str(uuid.uuid4()), "type": "order.void", "order_id": oid, "kind": "waste",
         "reason": "customer never came", "waste_reason": "Never picked up", "staff_id": str(uuid.uuid4()), "at": _now()}
    assert _send(store.key, [v]) == {v["event_id"]: "applied"}
    assert _ok(requests.get(f"{API_V1}/orders/{oid}", headers=store_key(store.key), timeout=5))["status"] == "cancelled"


def test_waste_reasons_setting(store) -> None:
    mgr = store.manager_session()
    s = _ok(mgr.get(f"{ME}/terminal/settings", timeout=5))
    s = s.get("settings", s)
    assert s["waste_reasons"] == ["Never picked up", "Made wrong / remade", "Customer complaint", "Burnt / dropped"]
    saved = _ok(mgr.put(f"{ME}/terminal/settings", json={"waste_reasons": ["  Cold ", "cold", "Dropped"]}, timeout=5))
    saved = saved.get("settings", saved)
    assert saved["waste_reasons"] == ["Cold", "Dropped"]
    assert mgr.put(f"{ME}/terminal/settings", json={"waste_reasons": []}, timeout=5).status_code == 400
    cfg = _ok(requests.get(f"{STORE_V1}/agent/terminal-config", headers=store_key(store.key), timeout=5))
    assert cfg["settings"]["waste_reasons"] == ["Cold", "Dropped"]


def test_void_reasons_setting(store) -> None:
    """The owner's void reasons (2026-11-12, round 2): picked on the terminal, never typed; X = this setting."""
    mgr = store.manager_session()
    s = _ok(mgr.get(f"{ME}/terminal/settings", timeout=5))
    s = s.get("settings", s)
    assert s["void_reasons"] == ["Customer cancelled", "Wrong order", "Duplicate", "Took too long", "Other"]
    saved = _ok(mgr.put(f"{ME}/terminal/settings", json={"void_reasons": [" Changed  mind ", "changed mind", "Other"]}, timeout=5))
    saved = saved.get("settings", saved)
    assert saved["void_reasons"] == ["Changed mind", "Other"]
    assert saved["waste_reasons"] == s["waste_reasons"]  # left out = kept
    assert mgr.put(f"{ME}/terminal/settings", json={"void_reasons": []}, timeout=5).status_code == 400
    cfg = _ok(requests.get(f"{STORE_V1}/agent/terminal-config", headers=store_key(store.key), timeout=5))
    assert cfg["settings"]["void_reasons"] == ["Changed mind", "Other"]


def test_a_real_store_with_money_is_never_deleted(admin, make_store) -> None:
    real = make_store(test=False)
    assert _ok(admin.get(f"{ADMIN_V1}/tenants", timeout=5)) and real.tenant_id
    oid = _place(real.key, total=1000)
    plain = _place(real.key, total=500)  # no money on this one
    assert _send(real.key, [_pay(oid, 1000)]).popitem()[1] == "applied"

    r = admin.delete(f"{ADMIN_V1}/orders/{oid}", timeout=5)
    assert r.status_code == 409 and r.json()["code"] == "has_money_records"
    assert admin.delete(f"{ADMIN_V1}/orders/{plain}", timeout=5).status_code == 204
    r = admin.delete(f"{ADMIN_V1}/orders", params={"tenant_id": real.tenant_id}, timeout=5)
    assert r.status_code == 409 and r.json()["code"] == "has_money_records"
    r = admin.delete(f"{ADMIN_V1}/tenants/{real.tenant_id}", timeout=10)
    body = r.json()
    assert r.status_code == 409 and body["code"] == "has_money_records" and body["money"]["payments"] == 1
    assert _payments(real.key, oid) == [1000], "nothing went"

    # Retired instead: kept with its money, its devices revoked; the Pi hears
    # device_revoked (and only then may it unlink, F-4).
    key = real.key
    who = _ok(requests.get(f"{STORE_V1}/agent/whoami", headers=store_key(key), timeout=5))
    assert who["revoked"] is False and who["kind"] == "agent" and who["location_id"] == real.location_id
    t = _ok(admin.post(f"{ADMIN_V1}/tenants/{real.tenant_id}/retire", timeout=10))
    assert t["retired_at"] and t["test_store"] is False
    r = requests.get(f"{STORE_V1}/agent/whoami", headers=store_key(key), timeout=5)
    assert r.status_code == 401 and r.json()["code"] == "device_revoked" and r.json()["revoked"] is True
    real._stream = None  # its stream was cut with the key


def test_test_stores_go_with_their_money(admin, make_store) -> None:
    t = make_store()
    oid = _place(t.key, total=1000)
    assert _send(t.key, [_pay(oid, 1000)]).popitem()[1] == "applied"
    assert admin.delete(f"{ADMIN_V1}/orders/{oid}", timeout=5).status_code == 204
    assert admin.delete(f"{ADMIN_V1}/tenants/{t.tenant_id}", timeout=10).status_code == 204
    assert admin.delete(f"{ADMIN_V1}/tenants/{t.tenant_id}", timeout=10).status_code == 204, "twice is fine"


def test_whoami_and_auth_codes(admin, store) -> None:
    hdr = store_key(store.key)
    who = _ok(requests.get(f"{STORE_V1}/agent/whoami", headers=hdr, timeout=5))
    assert who["device_id"] == store.device_id == who["agent_id"] and who["tenant_id"] == store.tenant_id
    assert who["tenant"] == store.name

    # A key main never saw: unknown_key (the Pi keeps its link); the legacy
    # 401 body stays as it was unless the caller asks for codes.
    unknown = store_key("qagt_" + uuid.uuid4().hex)
    r = requests.get(f"{STORE_V1}/agent/whoami", headers=unknown, timeout=5)
    assert r.status_code == 401 and r.json()["code"] == "unknown_key" and r.json()["revoked"] is False
    r = requests.get(f"{STORE_V1}/store-config", headers=unknown, timeout=5)
    assert r.status_code == 401 and r.json() == {"error": "invalid API key"}
    r = requests.get(f"{STORE_V1}/store-config", headers={**unknown, "X-Quicc-Auth-Codes": "1"}, timeout=5)
    assert r.status_code == 401 and r.json() == {"error": "invalid API key", "code": "unknown_key"}
    assert requests.get(f"{STORE_V1}/agent/whoami", timeout=5).json()["code"] == "missing_key"

    # Revoked: device_revoked, on whoami and (asked) on any agent route.
    second = store.mint_key("second")
    assert admin.post(f"{ADMIN_V1}/devices/{second['device_id']}/revoke", timeout=5).status_code in (200, 204)
    r = requests.get(f"{STORE_V1}/agent/whoami", headers=store_key(second["api_key"]), timeout=5)
    assert r.status_code == 401 and r.json()["code"] == "device_revoked" and r.json()["device_id"] == second["device_id"]
    r = requests.get(f"{STORE_V1}/store-config", headers={**store_key(second["api_key"]), "X-Quicc-Auth-Codes": "1"}, timeout=5)
    assert r.json()["code"] == "device_revoked"
    # Deleted (hard): its tombstone still says device_revoked.
    third = store.mint_key("third")
    assert admin.delete(f"{ADMIN_V1}/devices/{third['device_id']}", timeout=5).status_code in (200, 204)
    r = requests.get(f"{STORE_V1}/agent/whoami", headers=store_key(third["api_key"]), timeout=5)
    assert r.status_code == 401 and r.json()["code"] == "device_revoked"


def test_channel_with_orders_is_kept(admin, store) -> None:
    from test_order_port import _channel, _menu, _order, _pizza, _place as place  # noqa: PLC0415

    mgr = store.manager_session()
    store.online()
    m = _menu(mgr)
    op = _channel(admin, store, "operator")
    o = _ok(place(op, _order([_pizza(m)])), 201)
    chans = _ok(admin.get(f"{ADMIN_V1}/channels", params={"location_id": store.location_id}, timeout=5))
    chans = chans.get("channels", chans) if isinstance(chans, dict) else chans
    cid = next(c["id"] for c in chans if c["kind"] == "operator" and not c.get("builtin"))
    r = admin.delete(f"{ADMIN_V1}/channels/{cid}", timeout=5)
    assert r.status_code == 409 and r.json()["code"] == "channel_in_use", r.text
    assert o["id"]



def test_tips_of_our_own_orders(store, admin, publish) -> None:
    """main keeps the tips of the Pi's own orders (the owner's flow-audit follow-up, 2026-11-12): a card payment's
    pre-tip and a tip added to it afterwards ride on its payment (`tip_cents`), a driver's cash tip at the door
    arrives as order.tip {kind: door} (absolute) and is kept as the order's one `door_tip` payment. The order read
    lists each payment's tip and the admin list adds them up; main's Z check counts the card tips (the Pi's card
    tender holds them) and never the door tip (the driver keeps it)."""
    mgr = store.manager_session()
    oid = _place(store.key, total=1777, batch=8)
    card = _pay(oid, 1777, "manual_card", tip_cents=300, last4="4242", ref=f"sq-{uuid.uuid4().hex[:10]}", batch_id=8)
    later = _pay(oid, 0, "manual_card", tip_cents=200, ref=card["payment"]["id"], batch_id=8)
    door = {"event_id": str(uuid.uuid4()), "type": "order.tip", "order_id": oid, "tip_cents": 300, "kind": "door", "at": _now()}
    assert _send(store.key, [card, later, door]) == {card["event_id"]: "applied", later["event_id"]: "applied",
                                                     door["event_id"]: "applied"}
    # The door tip is absolute: a corrected one replaces it; the same event again is a duplicate.
    door2 = {**door, "event_id": str(uuid.uuid4()), "tip_cents": 400, "at": _now(1)}
    assert _send(store.key, [door2]) == {door2["event_id"]: "applied"}
    assert _send(store.key, [door2]) == {door2["event_id"]: "duplicate"}

    o = _ok(requests.get(f"{API_V1}/orders/{oid}", headers=store_key(store.key), timeout=5))
    pays = sorted((p["method"], p["amount_cents"], p.get("tip_cents", 0)) for p in o["payments"])
    assert pays == [("door_tip", 0, 400), ("manual_card", 0, 200), ("manual_card", 1777, 300)], pays
    assert o["money"]["total_cents"] == 1777 and o["money"]["tip_cents"] == 0  # tips never in the order's total
    rows = _ok(admin.get(f"{ADMIN_V1}/orders", params={"tenant_id": store.tenant_id}, timeout=5))
    (row,) = [r for r in rows if r["id"] == oid]
    assert row["tip_cents"] == 900 and row["total_cents"] == 1777

    # The Z check: the Pi's manual-card tender nets amount + tips (2277); the door tip is in no tender.
    z = {"id": str(uuid.uuid4()), "batch_id": 8, "business_date": "2026-11-12", "tenders": [
        {"tender": "manual_card", "count": 2, "collected_cents": 1777, "refunded_cents": 0, "tips_cents": 500, "net_cents": 2277}],
        "reconcile": {"difference_cents": 0}, "sales": {"total_cents": 1777}}
    ev = {"event_id": str(uuid.uuid4()), "type": "z.report", "at": _now(), "report": z}
    assert _send(store.key, [ev]) == {ev["event_id"]: "applied"}
    got = _ok(mgr.get(f"{ME}/reports/z/{z['id']}", timeout=5))
    assert got["flagged"] is False and got["main_difference_cents"] == 0, got
    assert got["main_check"] == [{"tender": "manual_card", "pi_cents": 2277, "main_cents": 2277}]

    # Only a counter order takes a door tip (a vendor order: invalid, vendor tips aren't synced yet); an unknown
    # order is not_found; a negative tip is invalid.
    vendor = store.order()
    publish(vendor)
    vid = wait_order(store.key, vendor["order_id"])["id"]
    bad = [{**door, "event_id": str(uuid.uuid4()), "order_id": vid},
           {**door, "event_id": str(uuid.uuid4()), "order_id": str(uuid.uuid4())},
           {**door, "event_id": str(uuid.uuid4()), "tip_cents": -1}]
    res = _send(store.key, bad)
    assert [res[b["event_id"]] for b in bad] == ["invalid", "not_found", "invalid"]
