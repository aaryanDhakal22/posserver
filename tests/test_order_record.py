"""What Store › Orders' panel shows about how an order went (the owner, 2026-11-13; contract/main.md §2.5zn,
agent contract §2.10ag). The test plays the Pi: floor events over POST /store/v1/agent/rpc; the store's manager
reads GET /store/v1/me/orders/:id and the day close.

  * order.printed → the order's `prints` (each job: mode, at, tickets with kind / printer / result / printed_on /
    by / attempts / error), oldest first; a replay is a duplicate; another store's (or an unknown) order is
    not_found; a malformed job is invalid
  * a card payment's brand (`payment.brand`, any spelling Square uses) → `payments[].card_brand`; none from an
    older Pi; a cash payment never has one; the day close lists the batch's card payments with their brand
  * who took it: order.placed's `device_name` / `staff_name` (else, an older Pi, the same keys in
    source_metadata.terminal) → the order's source_metadata
"""

from __future__ import annotations

import uuid
from datetime import datetime, timezone

import requests

from conftest import STORE_V1, store_key

ME = f"{STORE_V1}/me"


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _send(key: str, events: list[dict]) -> dict[str, str]:
    env = {"type": "floor.events", "id": str(uuid.uuid4()), "payload": {"events": events}}
    r = requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(key), json=env, timeout=10)
    assert r.status_code == 200, r.text
    return {x["event_id"]: x["result"] for x in r.json()["payload"]["results"]}


def _ok(r: requests.Response, status: int = 200) -> dict:
    assert r.status_code == status, f"{r.status_code}: {r.text}"
    return r.json()


def _placed(oid: str, payments: list[dict], batch: int = 4, **kw) -> dict:
    snap = {
        "id": oid, "source": "counter", "source_event_id": f"ck-{oid}", "status": "accepted", "currency": "USD",
        "money": {"subtotal_cents": 2000, "tax_cents": 120, "tip_cents": 0, "fees": [], "discounts": [], "total_cents": 2120},
        "customer": {"display_name": "Sam Lee"}, "fulfillment": {"type": "counter"},
        "items": [{"id": str(uuid.uuid4()), "position": 0, "name": "Cheese Pizza", "quantity": 1, "unit_price_cents": 2000,
                   "line_total_cents": 2000, "routing": "matched"}],
        "payments": payments, "notes": "", "placed_at": _now(), "kitchen_order_number": 7,
        "staff_id": str(uuid.uuid4()), "device_id": "terminal-1",
    }
    snap.update(kw)
    return {"event_id": str(uuid.uuid4()), "type": "order.placed", "order_id": oid, "at": _now(), "batch_id": batch,
            "agent_status": {"state": "accepted"}, "snapshot": snap}


def _printed(oid: str, tickets: list[dict], mode: str = "kitchen") -> dict:
    return {"event_id": str(uuid.uuid4()), "type": "order.printed", "order_id": oid, "mode": mode, "at": _now(),
            "tickets": tickets}


def _pay(method: str, cents: int, **kw) -> dict:
    return {"id": str(uuid.uuid4()), "method": method, "amount_cents": cents, "at": _now(), **kw}


def test_print_results_reach_the_order(make_store) -> None:
    store, other = make_store(), make_store()
    mgr = store.manager_session()
    oid = str(uuid.uuid4())
    assert set(_send(store.key, [_placed(oid, [_pay("cash", 2120)])]).values()) == {"applied"}
    first = _printed(oid, [
        {"kind": "station", "printer": "Pizza", "result": "printed", "printed_on": "Pizza", "attempts": 1, "at": _now()},
        {"kind": "station", "printer": "Wings", "result": "rerouted", "printed_on": "Front", "by": "auto", "attempts": 2,
         "at": _now()},
    ])
    later = _printed(oid, [
        {"kind": "reprint", "printer": "Front", "result": "failed", "attempts": 3, "error": "printer unreachable", "at": _now()},
        {"kind": "receipt", "printer": "", "result": "skipped", "attempts": 0, "error": "no printers configured"},
    ], mode="receipt")
    res = _send(store.key, [first, later])
    assert res == {first["event_id"]: "applied", later["event_id"]: "applied"}, res
    # Idempotent on the event id.
    assert _send(store.key, [first]) == {first["event_id"]: "duplicate"}

    d = _ok(mgr.get(f"{ME}/orders/{oid}", timeout=5))
    prints = d["prints"]
    assert [p["mode"] for p in prints] == ["kitchen", "receipt"], prints
    assert prints[0]["event_id"] == first["event_id"]
    t = prints[0]["tickets"]
    assert t[0] == {"kind": "station", "printer": "Pizza", "result": "printed", "printed_on": "Pizza", "attempts": 1,
                    "at": t[0]["at"]}, t[0]
    assert t[1]["result"] == "rerouted" and t[1]["printed_on"] == "Front" and t[1]["by"] == "auto" and t[1]["attempts"] == 2
    t = prints[1]["tickets"]
    assert t[0]["result"] == "failed" and t[0]["error"] == "printer unreachable" and t[0]["attempts"] == 3
    # A ticket without its own time takes the job's.
    assert t[1]["result"] == "skipped" and t[1]["printer"] == "" and t[1]["at"]

    # Another store's order, an unknown order: not_found. Malformed jobs: invalid.
    stray = _printed(oid, [{"kind": "station", "printer": "Pizza", "result": "printed", "printed_on": "Pizza", "attempts": 1}])
    assert _send(other.key, [stray]) == {stray["event_id"]: "not_found"}
    unknown = _printed(str(uuid.uuid4()), [{"kind": "station", "printer": "P", "result": "printed", "attempts": 1}])
    assert _send(store.key, [unknown]) == {unknown["event_id"]: "not_found"}
    bad = [
        _printed(oid, []),
        _printed(oid, [{"kind": "station", "printer": "P", "result": "maybe"}]),
        _printed(oid, [{"kind": "station", "printer": "P", "result": "rerouted"}]),  # rerouted to nowhere
        _printed(oid, [{"printer": "P", "result": "printed"}]),  # no kind
        _printed(oid, [{"kind": "station", "result": "printed"}], mode=""),
        {**_printed(oid, [{"kind": "station", "result": "printed"}]), "order_id": "not-a-uuid"},
    ]
    assert set(_send(store.key, bad).values()) == {"invalid"}
    assert len(_ok(mgr.get(f"{ME}/orders/{oid}", timeout=5))["prints"]) == 2
    # Another store's manager never sees the order.
    assert other.manager_session().get(f"{ME}/orders/{oid}", timeout=5).status_code == 404


def test_card_brand_and_taken_by(store) -> None:
    mgr = store.manager_session()
    card, keyed, cash = (_pay("card", 1000, last4="4242", brand="VISA"),
                         _pay("manual_card", 1000, last4="0005", brand="American Express"),
                         _pay("cash", 120, brand="visa"))
    oid = str(uuid.uuid4())
    ev = _placed(oid, [card, keyed, cash], device_name="Counter 1", staff_name="Carlos M.")
    assert set(_send(store.key, [ev]).values()) == {"applied"}
    # A Square app payment later, through order.payment; and one from an older Pi (no brand).
    app = _pay("square_app", 0, tip_cents=300, last4="1111", brand="mastercard")
    old = _pay("card", 0, tip_cents=100, last4="9999")
    res = _send(store.key, [{"event_id": str(uuid.uuid4()), "type": "order.payment", "order_id": oid, "at": _now(),
                             "batch_id": 4, "payment": p} for p in (app, old)])
    assert set(res.values()) == {"applied"}, res

    d = _ok(mgr.get(f"{ME}/orders/{oid}", timeout=5))
    brands = {p["id"]: p.get("card_brand", "") for p in d["payments"]}
    assert brands == {card["id"]: "visa", keyed["id"]: "amex", cash["id"]: "", app["id"]: "mastercard", old["id"]: ""}, brands
    meta = d["order"]["source_metadata"]
    assert meta["device_name"] == "Counter 1" and meta["staff_name"] == "Carlos M." and meta["device_id"] == "terminal-1", meta
    # The list carries it too (the Orders table's "Taken by").
    listed = next(o for o in _ok(mgr.get(f"{ME}/orders", params={"sort": "placed"}, timeout=5)) if o["id"] == oid)
    assert listed["source_metadata"]["device_name"] == "Counter 1"

    # An older Pi: the names only inside source_metadata.terminal.
    oid2 = str(uuid.uuid4())
    ev2 = _placed(oid2, [_pay("cash", 2120)], source_metadata={"terminal": {"device_name": "Till 2", "staff_name": "Dana L."}})
    assert set(_send(store.key, [ev2]).values()) == {"applied"}
    meta = _ok(mgr.get(f"{ME}/orders/{oid2}", timeout=5))["order"]["source_metadata"]
    assert meta["device_name"] == "Till 2" and meta["staff_name"] == "Dana L.", meta
    # Older still: no name at all (the panel shows the device id).
    oid3 = str(uuid.uuid4())
    assert set(_send(store.key, [_placed(oid3, [_pay("cash", 2120)])]).values()) == {"applied"}
    meta = _ok(mgr.get(f"{ME}/orders/{oid3}", timeout=5))["order"]["source_metadata"]
    assert "device_name" not in meta and meta["device_id"] == "terminal-1", meta

    # The day close lists the batch's card payments with their brand.
    z = {"id": str(uuid.uuid4()), "version": 1, "batch_id": 4, "business_date": "2026-11-13", "timezone": "America/New_York",
         "opened_at": "2026-11-13T15:00:00Z", "closed_at": "2026-11-13T23:00:00Z", "close_kind": "manual", "forced": False,
         "counts": {"orders": 3}, "sales": {"total_cents": 6360}, "tenders": [],
         "reconcile": {"difference_cents": 0}}
    zr = {"event_id": str(uuid.uuid4()), "type": "z.report", "at": _now(), "report": z}
    assert _send(store.key, [zr]) == {zr["event_id"]: "applied"}
    full = _ok(mgr.get(f"{ME}/reports/z/{z['id']}", timeout=5))
    cards = {c["id"]: c for c in full["card_payments"]}
    assert set(cards) == {card["id"], keyed["id"], app["id"], old["id"]}, full["card_payments"]
    assert cards[card["id"]]["card_brand"] == "visa" and cards[card["id"]]["last4"] == "4242"
    assert cards[card["id"]]["order_id"] == oid and cards[card["id"]]["order_number"] == 7
    assert cards[keyed["id"]]["method"] == "manual_card" and cards[keyed["id"]]["card_brand"] == "amex"
    assert "card_brand" not in cards[old["id"]]
