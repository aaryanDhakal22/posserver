"""Delivery maps (2.0.9): main geocodes + routes delivery orders in the
background and pushes RPC `order.geo` to the agent; the agent asks back with
`geo.pin` / `geo.trip`; the store pin + published map ride store_config.

Geo providers are faked: the test runs a small HTTP server (Census, Nominatim
and OSRM endpoints) on this host and points dev main at it through the
DEV-ONLY `POST /admin/v1/geo/test-providers` (main reaches the host as
`host.docker.internal` — the dev compose maps it to the Docker host gateway).
DELETE restores main's env providers afterwards. Dev only.

The test plays the agent: SSE `event: command` frames down, POST
/store/v1/agent/rpc up (replies + geo.pin / geo.trip requests).
"""

from __future__ import annotations

import json
import math
import os
import secrets
import threading
import time
import uuid
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit

import pytest
import requests

from conftest import ADMIN_V1, IS_DEV, STORE_V1, store_key, wait_order, wait_until
from sse import SSEStream

STORE = (40.6666, -74.1158)  # the test store's pin (Bayonne, Broadway)
FAKE_HOST = os.getenv("GEO_FAKE_HOST", "host.docker.internal")  # how main (in Docker) reaches this host


# ---------------------------------------------------------------------------
# Fake Census / Nominatim / OSRM
# ---------------------------------------------------------------------------


def _hav(a: tuple[float, float], b: tuple[float, float]) -> float:
    r = math.pi / 180
    dlat, dlng = (b[0] - a[0]) * r, (b[1] - a[1]) * r
    h = math.sin(dlat / 2) ** 2 + math.cos(a[0] * r) * math.cos(b[0] * r) * math.sin(dlng / 2) ** 2
    return 2 * 6371008.8 * math.asin(min(1.0, math.sqrt(h)))


def _secs(a, b) -> float:
    """Fake drive time: straight line at 8 m/s, plus a 5 s asymmetry so to/from differ."""
    return _hav(a, b) / 8.0 + (5 if a[0] < b[0] else 0)


class FakeGeo:
    """Answers like the real providers. Known addresses are registered by the
    test (`add`); anything else is no-match on both geocoders."""

    def __init__(self) -> None:
        self.known: dict[str, tuple[float, float]] = {}  # lowercased street -> (lat, lng)
        # lowercased street -> several Census matches [(matchedAddress, lat, lng)] (2.1.0 candidates)
        self.multi: dict[str, list[tuple[str, float, float]]] = {}
        self.census_hits = 0
        srv = ThreadingHTTPServer(("0.0.0.0", 0), self._handler())
        srv.daemon_threads = True
        self.srv = srv
        self.port = srv.server_address[1]
        threading.Thread(target=srv.serve_forever, daemon=True).start()

    def add(self, street: str, lat: float, lng: float) -> None:
        self.known[street.lower()] = (lat, lng)

    def add_matches(self, street: str, matches: list[tuple[str, float, float]]) -> None:
        """Census answers `street` with every match (best first)."""
        self.multi[street.lower()] = matches

    def close(self) -> None:
        self.srv.shutdown()

    def _handler(self):
        fake = self

        class H(BaseHTTPRequestHandler):
            def log_message(self, *_a):  # quiet
                pass

            def _json(self, code: int, body) -> None:
                raw = json.dumps(body).encode()
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def do_GET(self):  # noqa: N802
                u = urlsplit(self.path)
                q = {k: v[0] for k, v in parse_qs(u.query).items()}
                if u.path == "/census":
                    fake.census_hits += 1
                    street = q.get("address", "").split(",")[0].strip().lower()
                    hit = fake.known.get(street)
                    matches = [{"coordinates": {"x": lng, "y": lat}, "matchedAddress": m}
                               for m, lat, lng in fake.multi.get(street, [])]
                    if hit:
                        matches = [{"coordinates": {"x": hit[1], "y": hit[0]},
                                    "matchedAddress": street.upper() + ", BAYONNE, NJ, 07002"}]
                    return self._json(200, {"result": {"addressMatches": matches}})
                if u.path == "/nominatim":
                    return self._json(200, [])
                if u.path.startswith("/osrm/"):
                    parts = u.path.split("/")  # ['', 'osrm', service, 'v1', 'driving', coords]
                    service = parts[2]
                    pts = [tuple(reversed([float(x) for x in c.split(",")])) for c in parts[5].split(";")]
                    if service == "route":
                        a, b = pts[0], pts[1]
                        return self._json(200, {"code": "Ok", "routes": [
                            {"geometry": "fake_polyline", "distance": _hav(a, b) * 1.3, "duration": _secs(a, b)}]})
                    if service == "table":
                        src = [int(i) for i in q["sources"].split(";")]
                        dst = [int(i) for i in q["destinations"].split(";")]
                        return self._json(200, {"code": "Ok", "durations": [
                            [_secs(pts[i], pts[j]) for j in dst] for i in src]})
                    if service == "trip":
                        # Nearest neighbour from the first point (the store).
                        order, cur, left = [0], pts[0], list(range(1, len(pts)))
                        while left:
                            nxt = min(left, key=lambda i: _hav(cur, pts[i]))
                            order.append(nxt)
                            left.remove(nxt)
                            cur = pts[nxt]
                        pos = {wp: k for k, wp in enumerate(order)}
                        dist = sum(_hav(pts[order[k]], pts[order[k + 1]]) for k in range(len(order) - 1))
                        return self._json(200, {"code": "Ok",
                                                "trips": [{"geometry": "fake_trip", "distance": dist, "duration": dist / 8}],
                                                "waypoints": [{"waypoint_index": pos[i], "trips_index": 0} for i in range(len(pts))]})
                return self._json(404, {"code": "InvalidUrl"})

        return H


@pytest.fixture(scope="module")
def fake_geo(admin):
    if not IS_DEV:
        pytest.skip("the geo provider override exists on dev main only")
    fake = FakeGeo()
    base = f"http://{FAKE_HOST}:{fake.port}"
    r = admin.post(f"{ADMIN_V1}/geo/test-providers", json={
        "census_url": f"{base}/census", "nominatim_url": f"{base}/nominatim", "osrm_url": f"{base}/osrm"}, timeout=5)
    if r.status_code == 404:
        fake.close()
        pytest.skip("main has no geo test override (not APP_ENV=dev)")
    assert r.status_code == 200 and r.json()["routing"] is True, r.text
    yield fake
    admin.delete(f"{ADMIN_V1}/geo/test-providers", timeout=5)
    fake.close()


def _street() -> str:
    """A unique street per call so geo_cache never carries over between runs."""
    return f"{secrets.randbelow(900) + 100} Pytest{secrets.token_hex(3)} Street"


def _pin_store(admin, store) -> None:
    r = admin.put(f"{ADMIN_V1}/locations/{store.location_id}/pin", json={"lat": STORE[0], "lng": STORE[1]}, timeout=5)
    assert r.status_code == 200, r.text


def _delivery(store, street: str) -> dict:
    return store.order(service_type="delivery",
                       delivery_address={"street": street, "city": "Bayonne", "state": "NJ", "zip": "07002",
                                         "suite": "", "cross_streets": "", "buz": ""})


def _rpc(key: str, msg_type: str, payload: dict, msg_id: str | None = None) -> requests.Response:
    env = {"type": msg_type, "id": msg_id or str(uuid.uuid4()), "payload": payload}
    return requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(key), json=env, timeout=10)


def _next_geo(stream: SSEStream, order_id: str, *, timeout: float = 30.0, reply: dict | None = None) -> dict:
    """Wait for an order.geo command for order_id; reply to it (default ok)."""
    cmd = stream.wait_event("command", lambda d: d.get("type") == "order.geo" and d["payload"]["order_id"] == order_id,
                            timeout=timeout)
    assert cmd is not None, f"no order.geo for {order_id} within {timeout}s"
    r = _rpc(stream_key(stream), "order.geo.reply", reply or {"ok": True}, msg_id=cmd["id"])
    assert r.status_code == 204, r.text
    return cmd["payload"]


def stream_key(stream: SSEStream) -> str:
    return stream.resp.request.headers["X-STORE-API-KEY"]


# ---------------------------------------------------------------------------
# Store pin, published map, store_config
# ---------------------------------------------------------------------------


def test_store_pin_and_map_ride_store_config(admin, store) -> None:
    mgr = store.manager_session()
    r = mgr.put(f"{STORE_V1}/me/location/pin", json={"lat": STORE[0], "lng": STORE[1]}, timeout=5)
    assert r.status_code == 200, r.text
    loc = r.json()
    assert (loc["lat"], loc["lng"], loc["geo_source"], loc["delivery_radius_m"], loc["map"]) == (
        STORE[0], STORE[1], "manual", 8047, None)
    assert mgr.put(f"{STORE_V1}/me/location/pin", json={"lat": 200, "lng": 0}, timeout=5).status_code == 400
    assert mgr.put(f"{STORE_V1}/me/location/pin", json={"lat": 40.1}, timeout=5).status_code == 400

    with SSEStream(store.key) as s:
        cfg = s.wait_event("store_config", timeout=10)
        assert cfg["location"]["lat"] == STORE[0] and cfg["location"]["lng"] == STORE[1]
        assert cfg["location"]["delivery_radius_m"] == 8047 and cfg["location"]["map"] is None

        m = {"url": "https://utility.quiccpos.com/maps/t-20261022.pmtiles", "sha256": "ab" * 32,
             "version": "20261022", "bbox": [-74.30, 40.52, -73.93, 40.81]}
        r = admin.patch(f"{ADMIN_V1}/locations/{store.location_id}/map", json=m, timeout=5)
        assert r.status_code == 200, r.text
        assert r.json()["map"] == m
        pushed = s.wait_event("store_config", lambda d: d["location"]["map"] is not None, timeout=10)
        assert pushed and pushed["location"]["map"] == m

        # Bad input; then clearing publishes map null.
        for bad in ({**m, "sha256": "nothex"}, {**m, "bbox": [1, 2, 3]}, {**m, "version": ""}, {**m, "url": "ftp://x"}):
            assert admin.patch(f"{ADMIN_V1}/locations/{store.location_id}/map", json=bad, timeout=5).status_code == 400
        r = admin.patch(f"{ADMIN_V1}/locations/{store.location_id}/map", json={"url": ""}, timeout=5)
        assert r.status_code == 200 and r.json()["map"] is None
        assert s.wait_event("store_config", lambda d: d["location"]["map"] is None, timeout=10)

    # Admin pin + 404.
    r = admin.put(f"{ADMIN_V1}/locations/{store.location_id}/pin", json={"lat": 40.67, "lng": -74.12}, timeout=5)
    assert r.status_code == 200 and r.json()["lat"] == 40.67
    assert admin.put(f"{ADMIN_V1}/locations/{uuid.uuid4()}/pin", json={"lat": 40.6, "lng": -74.1}, timeout=5).status_code == 404
    assert mgr.get(f"{STORE_V1}/me/location", timeout=5).json()["geo_source"] == "manual"


def test_store_address_is_geocoded_on_save_unless_pinned(fake_geo, store) -> None:
    street = _street()
    fake_geo.add(street, 40.6701, -74.1102)
    mgr = store.manager_session()
    cur = mgr.get(f"{STORE_V1}/me/location", timeout=5).json()
    body = {k: cur[k] for k in ("name", "timezone", "daily_reset_hour", "phone", "config")}
    r = mgr.patch(f"{STORE_V1}/me/location", json={**body, "address_line1": street, "address_line2": "Bayonne, NJ 07002"}, timeout=5)
    assert r.status_code == 200, r.text
    loc = wait_until(lambda: (lambda l: l if l["lat"] else None)(mgr.get(f"{STORE_V1}/me/location", timeout=5).json()),
                     timeout=15, what="store geocoded")
    assert (loc["lat"], loc["lng"], loc["geo_source"]) == (40.6701, -74.1102, "geocoded")

    # A hand-placed pin is never moved by a later address save.
    assert mgr.put(f"{STORE_V1}/me/location/pin", json={"lat": 40.66, "lng": -74.11}, timeout=5).status_code == 200
    hits = fake_geo.census_hits
    assert mgr.patch(f"{STORE_V1}/me/location", json={**body, "address_line1": street, "address_line2": "Bayonne, NJ 07002"},
                     timeout=5).status_code == 200
    time.sleep(2)
    loc = mgr.get(f"{STORE_V1}/me/location", timeout=5).json()
    assert (loc["lat"], loc["lng"], loc["geo_source"]) == (40.66, -74.11, "manual")
    assert fake_geo.census_hits == hits


# ---------------------------------------------------------------------------
# order.geo
# ---------------------------------------------------------------------------


def test_delivery_order_is_located_routed_and_pushed_with_times(admin, store, publish, fake_geo) -> None:
    _pin_store(admin, store)
    a_st, b_st = _street(), _street()
    fake_geo.add(a_st, 40.6690, -74.1143)
    fake_geo.add(b_st, 40.6600, -74.1200)
    with SSEStream(store.key) as s:
        oa = _delivery(store, a_st)
        publish(oa)
        a_id = wait_order(store.key, oa["order_id"])["id"]
        g = _next_geo(s, a_id)
        assert g["state"] == "located" and g["quality"] == "exact"
        assert abs(g["lat"] - 40.6690) < 1e-6 and abs(g["lng"] + 74.1143) < 1e-6
        assert g["distance_m"] > 0 and g["duration_s"] > 0 and g["polyline"] == "fake_polyline"
        assert g["outside_area"] is False and g["store_to_s"] > 0 and g["to_store_s"] > 0
        assert g.get("times", []) == []

        ob = _delivery(store, b_st)
        publish(ob)
        b_id = wait_order(store.key, ob["order_id"])["id"]
        g = _next_geo(s, b_id)
        assert g["state"] == "located"
        pair = [t for t in g["times"] if t["other"] == a_id]
        assert len(pair) == 1 and pair[0]["to_s"] > 0 and pair[0]["from_s"] > 0
        assert pair[0]["to_s"] != pair[0]["from_s"]  # both directions, not a copy

        # Acked: not pushed again.
        assert s.wait_event("command", lambda d: d.get("type") == "order.geo", timeout=6) is None

    # A pickup order gets no geo work at all.
    with SSEStream(store.key) as s:
        p = store.order()
        publish(p)
        pid = wait_order(store.key, p["order_id"])["id"]
        assert s.wait_event("command", lambda d: d.get("type") == "order.geo" and d["payload"]["order_id"] == pid,
                            timeout=6) is None


def test_outside_area_and_not_found(admin, store, publish, fake_geo) -> None:
    _pin_store(admin, store)
    far = _street()
    fake_geo.add(far, 40.7357, -74.1724)  # Newark, ~9 km out (> 5 mi)
    with SSEStream(store.key) as s:
        o = _delivery(store, far)
        publish(o)
        oid = wait_order(store.key, o["order_id"])["id"]
        g = _next_geo(s, oid)
        assert g["state"] == "located" and g["outside_area"] is True

        nf = _delivery(store, _street())  # nobody knows it
        publish(nf)
        nid = wait_order(store.key, nf["order_id"])["id"]
        g = _next_geo(s, nid)
        assert g["state"] == "not_found"
        assert "lat" not in g and "quality" not in g and g["polyline"] == ""


def test_pin_is_cached_for_the_address_and_repushed(admin, store, publish, fake_geo) -> None:
    _pin_store(admin, store)
    street = _street()  # unknown to the fake: not_found first
    with SSEStream(store.key) as s:
        o = _delivery(store, street)
        publish(o)
        oid = wait_order(store.key, o["order_id"])["id"]
        assert _next_geo(s, oid)["state"] == "not_found"

        r = _rpc(store.key, "geo.pin", {"order_id": oid, "lat": 40.6650, "lng": -74.1180})
        assert r.status_code == 200, r.text
        assert r.json()["payload"] == {"ok": True}
        g = _next_geo(s, oid)
        assert g["state"] == "located" and g["quality"] == "pinned"
        assert (g["lat"], g["lng"]) == (40.6650, -74.1180) and g["polyline"] == "fake_polyline"

        # The next order to the same address (spelled differently) lands on the pin.
        o2 = _delivery(store, street.replace("Street", "St."))
        publish(o2)
        o2id = wait_order(store.key, o2["order_id"])["id"]
        g = _next_geo(s, o2id)
        assert g["state"] == "located" and g["quality"] == "pinned" and (g["lat"], g["lng"]) == (40.6650, -74.1180)

    # Bad / foreign pins.
    assert _rpc(store.key, "geo.pin", {"order_id": oid, "lat": 400, "lng": 0}).status_code == 400
    r = _rpc(store.key, "geo.pin", {"order_id": str(uuid.uuid4()), "lat": 40.6, "lng": -74.1})
    assert r.status_code == 200 and r.json()["payload"] == {"ok": False, "code": "not_found"}


def test_unacked_geo_is_repushed_when_the_agent_reconnects(admin, store, publish, fake_geo) -> None:
    _pin_store(admin, store)
    street = _street()
    fake_geo.add(street, 40.6680, -74.1130)
    o = _delivery(store, street)
    publish(o)  # no agent connected: the push is held
    oid = wait_order(store.key, o["order_id"])["id"]
    time.sleep(4)  # located + push attempted (offline) by now
    with SSEStream(store.key) as s:
        g = _next_geo(s, oid, timeout=20)
        assert g["state"] == "located" and g["quality"] == "exact"


def test_trip_orders_the_stops(admin, store, publish, fake_geo) -> None:
    _pin_store(admin, store)
    # Three stops due north of the store at increasing distance.
    coords = [(40.6766, -74.1158), (40.6866, -74.1158), (40.6716, -74.1158)]
    ids = []
    with SSEStream(store.key) as s:
        for lat, lng in coords:
            st = _street()
            fake_geo.add(st, lat, lng)
            o = _delivery(store, st)
            publish(o)
            oid = wait_order(store.key, o["order_id"])["id"]
            assert _next_geo(s, oid)["state"] == "located"
            ids.append(oid)
        nf = _delivery(store, _street())
        publish(nf)
        nfid = wait_order(store.key, nf["order_id"])["id"]
        assert _next_geo(s, nfid)["state"] == "not_found"

    r = _rpc(store.key, "geo.trip", {"order_ids": [ids[1], nfid, ids[0], ids[2]]})
    assert r.status_code == 200, r.text
    p = r.json()["payload"]
    assert p["ok"] is True
    assert p["order_ids"] == [ids[2], ids[0], ids[1], nfid]  # nearest first; unlocated last
    assert p["unlocated"] == [nfid] and p["polyline"] == "fake_trip" and p["distance_m"] > 0 and p["duration_s"] > 0

    assert _rpc(store.key, "geo.trip", {"order_ids": []}).status_code == 400
    assert _rpc(store.key, "geo.trip", {"order_ids": ["nope"]}).status_code == 400


# ---------------------------------------------------------------------------
# Floor events: delivery.run + same-state pool updates
# ---------------------------------------------------------------------------


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _floor(key: str, events: list[dict]) -> dict[str, str]:
    r = _rpc(key, "floor.events", {"events": events})
    assert r.status_code == 200, r.text
    return {x["event_id"]: x["result"] for x in r.json()["payload"]["results"]}


def test_delivery_run_is_stored_idempotently(admin, store) -> None:
    driver = str(uuid.uuid4())
    run = {"event_id": str(uuid.uuid4()), "type": "delivery.run", "at": _now(), "batch_id": 7,
           "driver_id": driver, "driver_name": "Sam", "plan_id": str(uuid.uuid4()),
           "suggested_ids": ["o2"], "sent_ids": ["o1", "o2"],
           "stops": [{"order_id": "o1", "eta": _now()}, {"order_id": "o2", "eta": _now()}]}
    bad = {"event_id": str(uuid.uuid4()), "type": "delivery.run", "at": _now(), "sent_ids": []}
    assert _floor(store.key, [run, bad]) == {run["event_id"]: "applied", bad["event_id"]: "invalid"}
    assert _floor(store.key, [run]) == {run["event_id"]: "duplicate"}

    r = admin.get(f"{ADMIN_V1}/delivery-runs", params={"tenant_id": store.tenant_id, "location_id": store.location_id}, timeout=5)
    assert r.status_code == 200, r.text
    rows = r.json()
    assert len(rows) == 1
    got = rows[0]
    assert (got["id"], got["driver_id"], got["driver_name"], got["batch_id"], got["plan_id"]) == (
        run["event_id"], driver, "Sam", 7, run["plan_id"])
    assert got["suggested_ids"] == ["o2"] and got["sent_ids"] == ["o1", "o2"] and len(got["stops"]) == 2
    mgr = store.manager_session()
    assert [x["id"] for x in mgr.get(f"{STORE_V1}/me/delivery-runs", timeout=5).json()] == [run["event_id"]]


def test_same_state_pool_update_is_accepted(store, publish) -> None:
    o = store.order()
    publish(o)
    oid = wait_order(store.key, o["order_id"])["id"]
    acc = {"event_id": str(uuid.uuid4()), "type": "order.status", "order_id": oid, "status": "accepted",
           "kitchen_number": 1100, "at": _now(), "agent_status": {"state": "accepted"}, "batch_id": 1}
    assert _floor(store.key, [acc]) == {acc["event_id"]: "applied"}
    pool = str(uuid.uuid4())
    time.sleep(0.01)
    same = {"event_id": str(uuid.uuid4()), "type": "order.status", "order_id": oid, "status": "accepted",
            "at": _now(), "agent_status": {"state": "accepted", "pool_driver_id": pool}, "batch_id": 1}
    assert _floor(store.key, [same]) == {same["event_id"]: "stale"}  # no state change, no error

    st = {"at": _now(), "state": {"batch": {"id": 1, "opened_at": _now()}}}
    assert _rpc(store.key, "agent.state", st).status_code == 200
    r = requests.get(f"{STORE_V1}/agent/restore", headers=store_key(store.key), timeout=5)
    assert r.status_code == 200, r.text
    mine = [x for x in r.json()["orders"] if x["order"]["id"] == oid]
    assert mine and mine[0]["agent_status"]["pool_driver_id"] == pool
    assert wait_order(store.key, o["order_id"])["status"] == "accepted"
