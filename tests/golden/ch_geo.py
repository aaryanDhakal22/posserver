"""Golden chapter: delivery maps as the Pi sees them — order.geo pushes for
located / not found / ambiguous deliveries, geo.choose, geo.pin, geo.trip,
POS stops (geo.locate / geo.forget), geo.search and location.radius — with
main pointed at fake Census / Nominatim / OSRM (dev only, xdist group geo)."""

from __future__ import annotations

import json
import secrets
import uuid
from typing import Any

import pytest
import requests

from conftest import IS_DEV, SQS_QUEUE_URL, Store, online_attrs
from golden.recorder import Rec
from sse import SSEStream
from test_geo import FAKE_HOST, STORE, FakeGeo


def run(rec: Rec, admin: requests.Session, request) -> None:
    if not IS_DEV:
        pytest.skip("the geo provider override exists on dev main only")
    sqs = request.getfixturevalue("sqs")
    fake = FakeGeo()
    a = Store(admin)
    try:
        base = f"http://{FAKE_HOST}:{fake.port}"
        rec.alias(base, "fake")
        rec.call("install fake providers", "POST", "/admin/v1/geo/test-providers", session=admin,
                 json_body={"census_url": f"{base}/census", "nominatim_url": f"{base}/nominatim", "osrm_url": f"{base}/osrm"})
        _run(rec, admin, a, sqs, fake)
    finally:
        rec.call("remove fake providers", "DELETE", "/admin/v1/geo/test-providers", session=admin)
        a.delete()
        fake.close()


def _run(rec: Rec, admin: requests.Session, a: Store, sqs, fake: FakeGeo) -> None:
    rec.alias(a.tag, "tag")
    rec.alias(a.store_id, "store_id")
    user, pw = f"mgr{a.tag}", "golden-" + a.tag
    admin.patch(f"{rec.base}/admin/v1/tenants/{a.tenant_id}/credentials", json={"username": user, "password": pw}, timeout=5)
    admin.put(f"{rec.base}/admin/v1/locations/{a.location_id}/pin", json={"lat": STORE[0], "lng": STORE[1]}, timeout=5)
    key = requests.post(f"{rec.base}/store/v1/agent/link", json={"username": user, "password": pw, "agent_name": "golden-pi"},
                        timeout=10).json()["api_key"]
    h = {"X-STORE-API-KEY": key}
    tag = secrets.token_hex(3)
    rec.alias(tag, "street-tag")
    streets = {k: f"{n} Golden{tag} {k.title()} Street" for k, n in
               (("known", 101), ("other", 205), ("ambiguous", 310), ("missing", 404), ("stop", 515))}
    fake.add(streets["known"], 40.6650, -74.1180)
    fake.add(streets["other"], 40.6700, -74.1100)
    fake.add(streets["stop"], 40.6600, -74.1200)
    up = streets["ambiguous"].upper()  # top match in another zip: not confident → check
    fake.add_matches(streets["ambiguous"], [(f"{up}, JERSEY CITY, NJ, 07305", 40.6950, -74.0850), (f"{up}, BAYONNE, NJ, 07002", 40.6690, -74.1143),
                                            (f"{up}, NEWARK, NJ, 07105", 40.7200, -74.1500), (f"{up}, ELIZABETH, NJ, 07201", 40.6600, -74.2100)])

    def rpc(label: str, typ: str, payload: Any, msg_id: str | None = None) -> requests.Response:
        return rec.call(label, "POST", "/store/v1/agent/rpc", headers=h,
                        json_body={"type": typ, "id": msg_id or str(uuid.uuid4()), "payload": payload})

    with SSEStream(key) as s:
        rec.value("sse first frame", s.next()["event"])

        def geo(label: str, order_id: str, reply: dict | None = None) -> dict | None:
            cmd = s.wait_event("command", lambda d: d.get("type") == "order.geo" and d["payload"].get("order_id") == order_id,
                               timeout=30)
            rec.value(label, _sorted_times(cmd))
            if cmd:
                rpc(f"{label}: reply", "order.geo.reply", reply or {"ok": True}, msg_id=cmd["id"])
            return cmd

        def deliver(name: str, street: str) -> str:
            o = a.order(service_type="delivery", delivery_address={"street": street, "city": "Bayonne", "state": "NJ",
                                                                   "zip": "07002", "suite": "", "cross_streets": "", "buz": ""})
            rec.alias(o["order_id"], f"order_id:{name}")
            sqs.send_message(QueueUrl=SQS_QUEUE_URL, MessageBody=json.dumps(o), MessageAttributes=online_attrs())
            # keep_others: its order.geo push can come before this order frame on a fast machine
            f = s.wait_event("order", lambda d: d.get("source_event_id") == str(o["order_id"]), timeout=30,
                             keep_others=True)
            return f["id"] if f else ""

        known = deliver("known", streets["known"])
        geo("located", known)
        other = deliver("other", streets["other"])
        geo("located other", other)
        missing = deliver("missing", streets["missing"])
        geo("not found", missing)
        amb = deliver("ambiguous", streets["ambiguous"])
        geo("ambiguous: check", amb)
        rpc("choose bad index", "geo.choose", {"order_id": amb, "index": 5})
        rpc("choose", "geo.choose", {"order_id": amb, "index": 1})
        geo("chosen: pinned + confirmed", amb)
        rpc("pin", "geo.pin", {"order_id": known, "lat": 40.6651, "lng": -74.1181})
        geo("pinned", known)
        rpc("pin bad", "geo.pin", {"order_id": known, "lat": 400, "lng": 0})
        rpc("pin unknown", "geo.pin", {"order_id": str(uuid.uuid4()), "lat": 40.6, "lng": -74.1})
        rpc("trip", "geo.trip", {"order_ids": [other, known, missing]})
        rpc("trip bad", "geo.trip", {"order_ids": []})

        stop = str(uuid.uuid4())  # the agent's id for a POS check
        rec.alias(stop, "pos-stop")
        rpc("locate stop", "geo.locate", {"order_id": stop, "address": {"line1": streets["stop"], "line2": "", "city": "Bayonne",
                                                                         "state": "NJ", "postal_code": "07002"}})
        geo("stop located", stop)
        rpc("locate bad", "geo.locate", {"order_id": stop})
        rpc("trip with stop", "geo.trip", {"order_ids": [stop, known]})
        rpc("forget stop", "geo.forget", {"order_id": stop})
        rpc("forget bad", "geo.forget", {})

        rpc("search", "geo.search", {"order_id": missing, "query": streets["other"]})
        geo("search result", missing)
        rec.call("search too soon", "POST", "/store/v1/agent/rpc", headers=h, mask=("retry_after_ms",),
                 json_body={"type": "geo.search", "id": str(uuid.uuid4()), "payload": {"order_id": missing, "query": streets["other"]}})

        rpc("radius", "location.radius", {"radius_m": 500})
        rec.value("store_config after radius", s.wait_event("store_config", lambda d: d.get("location", {}).get("delivery_radius_m") == 500,
                                                              timeout=10))
        geo("re-flagged outside area", other)
        rpc("radius bad", "location.radius", {"radius_m": 10})

    rec.call("me location after radius", "GET", "/store/v1/me/location", session=_mgr(rec, user, pw))


def _mgr(rec: Rec, user: str, pw: str) -> requests.Session:
    s = requests.Session()
    s.post(f"{rec.base}/store/v1/session", json={"username": user, "password": pw}, timeout=5)
    return s


def _sorted_times(cmd: dict | None) -> dict | None:
    """order.geo's drive-time list arrives in no fixed order (main 2.2.3);
    the agent reads it as a set, so compare it sorted by its times."""
    times = (cmd or {}).get("payload", {}).get("times")
    if isinstance(times, list):
        cmd["payload"]["times"] = sorted(times, key=lambda t: (t.get("to_s", 0), t.get("from_s", 0)))
    return cmd
