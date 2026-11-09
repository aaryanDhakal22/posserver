"""Golden chapter: orders end to end as the Pi sees them — every vendor
fixture through SQS (v2 attributes, the v1 relay envelope, the store_name
fallback, a duplicate), kitchen routing stamps from the menu, the SSE order
frames, acks and redelivery, every order read, and the floor read model fed
by floor.events (statuses, drivers, shifts, runs, not delivered, restore)."""

from __future__ import annotations

import json
import uuid
from datetime import datetime, timedelta, timezone
from typing import Any

import requests

from conftest import RUN_OFFSET, Store, online_attrs, SQS_QUEUE_URL
from data import order_requests
from golden.recorder import Rec
from sse import SSEStream


def _recent() -> str:
    """An hour ago, for values main only reads inside a window around now."""
    return (datetime.now(timezone.utc) - timedelta(hours=1)).strftime("%Y-%m-%dT%H:%M:%SZ")


T = ["2026-11-02T15:00:00Z", "2026-11-02T15:01:00Z", "2026-11-02T15:02:00Z", "2026-11-02T15:03:00Z",
     "2026-11-02T15:04:00Z", "2026-11-02T15:05:00Z", "2026-11-02T15:06:00Z", "2026-11-02T15:07:00Z"]


def run(rec: Rec, admin: requests.Session, request) -> None:
    sqs = request.getfixturevalue("sqs")
    a = Store(admin)
    try:
        _run(rec, admin, a, sqs)
    finally:
        a.delete()


def _run(rec: Rec, admin: requests.Session, a: Store, sqs) -> None:
    rec.alias(a.tag, "tag")
    rec.alias(a.store_id, "store_id")
    user, pw = f"mgr{a.tag}", "golden-" + a.tag
    admin.patch(f"{rec.base}/admin/v1/tenants/{a.tenant_id}/credentials", json={"username": user, "password": pw}, timeout=5)
    mgr = requests.Session()
    mgr.post(f"{rec.base}/store/v1/session", json={"username": user, "password": pw}, timeout=5)
    for name, ip in (("Pizza", "10.0.0.1"), ("Kitchen", "10.0.0.2")):
        mgr.post(f"{rec.base}/store/v1/me/printers", json={"name": name, "ip": ip}, timeout=5)

    # A menu the fixtures route against: most items matched (item printer or
    # the category default), one item with no printer (unassigned), the rest
    # unmatched.
    names = sorted({it["name"] for f in order_requests.values() for it in f["items"]})
    rows = ["menu,category,item,price,printer"]
    for i, n in enumerate(names):
        if i % 4 == 3:
            continue  # unmatched
        printer = "" if i % 4 == 2 else ("Pizza" if i % 2 == 0 else "Kitchen")
        rows.append(f"Main,{'Pizza' if i % 2 == 0 else 'Kitchen'},\"{n}\",1.00,{printer}")
    mgr.post(f"{rec.base}/store/v1/me/menu/import", params={"dry_run": 0, "replace": 1}, data="\n".join(rows).encode(),
             headers={"Content-Type": "text/csv"}, timeout=10)

    key = requests.post(f"{rec.base}/store/v1/agent/link", json={"username": user, "password": pw, "agent_name": "golden-pi"},
                        timeout=10).json()["api_key"]
    h = {"X-STORE-API-KEY": key}

    def publish(body: Any, *, attrs: bool = True) -> None:
        sqs.send_message(QueueUrl=SQS_QUEUE_URL, MessageBody=body if isinstance(body, str) else json.dumps(body),
                         **({"MessageAttributes": online_attrs()} if attrs else {}))

    def rpc(label: str, typ: str, payload: Any) -> requests.Response:
        return rec.call(label, "POST", "/store/v1/agent/rpc", headers=h,
                        json_body={"type": typ, "id": str(uuid.uuid4()), "payload": payload})

    ids: dict[str, str] = {}
    base = RUN_OFFSET + 900_000_000 + int(a.tag, 16) % 1000 * 100

    with SSEStream(key) as s:
        rec.value("sse first frame", s.next()["event"])

        def frame(label: str, source_event_id: str) -> dict | None:
            f = s.wait_event("order", lambda d: d.get("source_event_id") == source_event_id, timeout=30)
            rec.value(label, f)
            return f

        # Every fixture, one at a time (frames arrive in publish order).
        for i, (name, fx) in enumerate(order_requests.items()):
            p = dict(fx)
            p["order_id"] = base + i
            p["store_id"], p["store_name"] = a.store_id, a.name
            rec.alias(p["order_id"], f"order_id:{name}")
            publish(p)
            f = frame(f"frame {name}", str(p["order_id"]))
            ids[name] = f["id"] if f else ""
            rec.call(f"by-source {name}", "GET", f"/api/v1/orders/by-source/online/{p['order_id']}", headers=h)
            rec.call(f"by-id {name}", "GET", f"/api/v1/orders/{ids[name]}", headers=h)

        # v1 relay envelope (prod online today): no attributes, JSON in a string.
        v1o = dict(order_requests["basic_pickup_order"], order_id=base + 50, store_id=a.store_id, store_name=a.name)
        rec.alias(v1o["order_id"], "order_id:v1")
        publish(json.dumps({"ID": str(v1o["order_id"]), "Payload": json.dumps(v1o), "DateCreated": "2026-11-02",
                            "CreatedAt": "2026-11-02T15:00:00Z"}), attrs=False)
        frame("frame v1 envelope", str(v1o["order_id"]))

        # store_name fallback (no store_id), then a duplicate of it.
        nm = dict(order_requests["basic_pickup_order"], order_id=base + 51, store_name=a.name)
        nm.pop("store_id", None)
        rec.alias(nm["order_id"], "order_id:by-name")
        publish(nm)
        fb = frame("frame by name", str(nm["order_id"]))
        publish(nm)
        dup = s.wait_event("order", lambda d: d.get("source_event_id") == str(nm["order_id"]) and d.get("id") != (fb or {}).get("id"),
                           timeout=4)
        rec.value("duplicate makes no new order", dup is None)

        # Acks: everything but one order; that one is re-sent until acked.
        all_ids = [x for x in ids.values() if x] + [(fb or {}).get("id", "")]
        keep = ids["basic_pickup_order"]
        rpc("ack", "order.ack", {"order_ids": [x for x in all_ids if x != keep]})
        rpc("ack bad", "order.ack", {"order_ids": ["nope"]})
        again = s.wait_event("order", lambda d: d.get("id") == keep, timeout=20)
        rec.value("unacked order re-sent", again)
        rpc("ack last", "order.ack", {"order_ids": [keep]})

        # Floor events on the delivery: accepted → ready → out → delivered;
        # a stale one, a duplicate, an invalid one; then not delivered.
        d = ids["delivery_with_address"]
        ev = lambda typ, **kw: {"event_id": str(uuid.uuid4()), "type": typ, **kw}  # noqa: E731
        acc = ev("order.status", order_id=d, status="accepted", at=T[1], kitchen_number=1100, estimated_ready_at=T[4], batch_id=7,
                 agent_status={"state": "accepted", "batch_id": 7, "kitchen_number": 1100, "prep_minutes": 25})
        rpc("floor accepted", "floor.events", {"events": [acc]})
        rpc("floor duplicate", "floor.events", {"events": [acc]})
        rpc("floor ready + stale + invalid", "floor.events", {"events": [
            ev("order.status", order_id=d, status="ready", at=T[3]),
            ev("order.status", order_id=d, status="accepted", at=T[2], kitchen_number=1199),
            ev("order.status", kitchen_number="not-a-number")]})
        rpc("floor out", "floor.events", {"events": [ev("order.status", order_id=d, status="on_delivery", at=T[4], driver_name="Sam")]})
        rec.call("order after out", "GET", f"/api/v1/orders/{d}", headers=h)
        rpc("floor not delivered retry", "floor.events", {"events": [ev("order.not_delivered", order_id=d, action="retry", at=T[5])]})
        rec.call("order after not delivered", "GET", f"/api/v1/orders/{d}", headers=h)
        rpc("floor out again + delivered", "floor.events", {"events": [
            ev("order.status", order_id=d, status="on_delivery", at=T[6], driver_name="Ana"),
            ev("order.status", order_id=d, status="delivered", at=T[7])]})
        rpc("floor scheduled + cancelled", "floor.events", {"events": [
            ev("order.status", order_id=ids["future_scheduled_delivery"], status="scheduled", at=T[1]),
            ev("order.status", order_id=ids["tax_exempt_corporate_order"], status="cancelled", at=T[1], auto=True),
            ev("order.status", order_id=ids["coupon_and_partial_payment"], status="nope", at=T[1])]})
        p = ids["basic_pickup_order"]
        rpc("floor pickup accepted + cancel", "floor.events", {"events": [
            ev("order.status", order_id=p, status="accepted", at=T[1], kitchen_number=1101),
            ev("order.not_delivered", order_id=p, action="cancel", at=T[2])]})
        rpc("floor unknown order", "floor.events", {"events": [ev("order.status", order_id=str(uuid.uuid4()), status="accepted", at=T[1])]})
        drv = str(uuid.uuid4())
        rec.alias(drv, "driver")
        rpc("floor roster + shift", "floor.events", {"events": [
            ev("drivers.roster", at=T[1], drivers=[{"id": drv, "name": "Sam", "position": 1, "status": "out"},
                                                   {"id": str(uuid.uuid4()), "name": "Ana", "position": 2, "status": "available"}]),
            # A recent clock-in: /me/floor lists the last 7 days of shifts, so a
            # fixed date would drop out of the read below a week later (it did, 2026-11-09).
            ev("driver.shift", shift_id=str(uuid.uuid4()), driver_id=drv, driver_name="Sam", clock_in=_recent(), at=T[1])]})
        rpc("floor run", "floor.events", {"events": [
            ev("delivery.run", at=T[4], batch_id=7, driver_id=drv, driver_name="Sam", plan_id=str(uuid.uuid4()),
               suggested_ids=[d], sent_ids=[d], stops=[{"order_id": d, "eta": T[5]}]),
            ev("delivery.run", at=T[4], sent_ids=[])]})
        rpc("floor bad payload", "floor.events", "not an object")
        no_echo = s.wait_event("order", lambda x: x.get("id") == d, timeout=3)
        rec.value("floor changes are not echoed to the agent", no_echo is None)

    # Reads after the floor moved.
    for name in ("delivery_with_address", "basic_pickup_order"):
        rec.call(f"by-id after floor {name}", "GET", f"/api/v1/orders/{ids[name]}", headers=h)
    rec.call("list", "GET", "/api/v1/orders", headers=h, params={"num": 200})
    rec.call("list page", "GET", "/api/v1/orders", headers=h, params={"offset": 2, "num": 3})
    rec.call("list status", "GET", "/api/v1/orders", headers=h, params={"status": "completed"})
    rec.call("list status accepted", "GET", "/api/v1/orders", headers=h, params={"status": "accepted"})
    rec.call("list q", "GET", "/api/v1/orders", headers=h, params={"q": "DAVIS"})
    rec.call("list sort", "GET", "/api/v1/orders", headers=h, params={"sort": "placed_at_asc"})
    rec.call("list from/to", "GET", "/api/v1/orders", headers=h, params={"from": "2026-03-01T00:00:00Z", "to": "2026-03-31T00:00:00Z"})
    rec.call("list bad offset", "GET", "/api/v1/orders", headers=h, params={"offset": "abc"})
    rec.call("list bad num", "GET", "/api/v1/orders", headers=h, params={"num": "xyz"})
    rec.call("by-source unknown", "GET", "/api/v1/orders/by-source/online/1", headers=h)
    rec.call("by-id bad", "GET", "/api/v1/orders/nope", headers=h)
    rec.call("me orders", "GET", "/store/v1/me/orders", session=mgr)
    rec.call("me floor", "GET", "/store/v1/me/floor", session=mgr)
    rec.call("me delivery runs", "GET", "/store/v1/me/delivery-runs", session=mgr)
    rec.call("admin delivery runs", "GET", "/admin/v1/delivery-runs", session=admin, params={"tenant_id": a.tenant_id, "location_id": a.location_id})
    rec.call("admin orders", "GET", "/admin/v1/orders", session=admin, params={"tenant_id": a.tenant_id})
    rec.call("admin orders by source", "GET", "/admin/v1/orders", session=admin, params={"source_event_id": str(base)})
    rec.call("menu unmatched", "GET", "/store/v1/me/menu/unmatched", session=mgr, params={"days": 7})
    rec.call("restore", "GET", "/store/v1/agent/restore", headers=h)
    rec.call("admin order delete", "DELETE", f"/admin/v1/orders/{ids['sample_real_order']}", session=admin)
    rec.call("deleted order", "GET", f"/api/v1/orders/{ids['sample_real_order']}", headers=h)
