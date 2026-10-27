"""POS deliveries + address candidates (2.1.0).

The agent asks main to locate a delivery main has no order for (a POS check
the store hub pulled): RPC `geo.locate {order_id (the agent's id), address}`.
main answers {ok} at once and later pushes the usual `order.geo` with that
order_id; the stop (a point only) joins every drive-time table of the store,
so AUTO plans online + POS deliveries on one matrix. `geo.forget` drops it.

An ambiguous address comes back `state:"check"` with up to 3 candidates; staff
pick one (`geo.choose {order_id, index}`) — for online orders and stops alike —
and the result is pushed again located / pinned with `confirmed_address`.

Same fake providers + agent-playing helpers as test_geo.py. Dev only.
"""

from __future__ import annotations

import uuid
from datetime import datetime, timezone

import pytest

from conftest import ADMIN_V1, wait_order, wait_until
from sse import SSEStream
from test_geo import _delivery, _next_geo, _pin_store, _rpc, _street, fake_geo  # noqa: F401 (fixture)

# One xdist worker for every test that swaps main's global geo providers.
pytestmark = pytest.mark.xdist_group("geo")


def _locate(key: str, order_id: str, street: str, zip_: str = "07002") -> dict:
    r = _rpc(key, "geo.locate", {"order_id": order_id, "address": {
        "line1": street, "line2": "", "city": "Bayonne", "state": "NJ", "postal_code": zip_}})
    assert r.status_code == 200, r.text
    return r.json()["payload"]


def _ambiguous(fake_geo, street: str) -> list[tuple[str, float, float]]:
    """The same house number on the street in three zips (input has 07002,
    but the top match is 07305 — so it is not confident)."""
    s = street.upper()
    matches = [(f"{s}, JERSEY CITY, NJ, 07305", 40.6950, -74.0850),
               (f"{s}, BAYONNE, NJ, 07002", 40.6690, -74.1143),
               (f"{s}, NEWARK, NJ, 07105", 40.7200, -74.1500),
               (f"{s}, ELIZABETH, NJ, 07201", 40.6600, -74.2100)]
    fake_geo.add_matches(street, matches)
    return matches


def test_geo_locate_without_an_order_and_shared_drive_times(admin, store, publish, fake_geo) -> None:
    _pin_store(admin, store)
    a_st, s_st, b_st = _street(), _street(), _street()
    fake_geo.add(a_st, 40.6690, -74.1143)
    fake_geo.add(s_st, 40.6600, -74.1200)
    fake_geo.add(b_st, 40.6720, -74.1100)
    stop = str(uuid.uuid4())  # the agent's own id: main has no order for it
    with SSEStream(store.key) as s:
        oa = _delivery(store, a_st)
        publish(oa)
        a_id = wait_order(store.key, oa["order_id"])["id"]
        assert _next_geo(s, a_id)["state"] == "located"

        assert _locate(store.key, stop, s_st) == {"ok": True}
        g = _next_geo(s, stop)
        assert g["state"] == "located" and g["quality"] == "exact"
        assert abs(g["lat"] - 40.6600) < 1e-6 and g["polyline"] == "fake_polyline" and g["store_to_s"] > 0
        assert "candidates" not in g and "confirmed_address" not in g and "confirmed" not in g
        pair = [t for t in g["times"] if t["other"] == a_id]
        assert len(pair) == 1 and pair[0]["to_s"] > 0 and pair[0]["from_s"] > 0

        # An online order located now has the stop in its table too.
        ob = _delivery(store, b_st)
        publish(ob)
        b_id = wait_order(store.key, ob["order_id"])["id"]
        g = _next_geo(s, b_id)
        others = {t["other"] for t in g["times"]}
        assert {a_id, stop} <= others

        # No order was created for the stop.
        r = admin.get(f"{ADMIN_V1}/orders", params={"tenant_id": store.tenant_id, "limit": 50}, timeout=5)
        assert r.status_code == 200, r.text
        ids = {o["id"] for o in r.json()}
        assert a_id in ids and b_id in ids and stop not in ids

        # Forget: the stop leaves every later table.
        r = _rpc(store.key, "geo.forget", {"order_id": stop})
        assert r.status_code == 200 and r.json()["payload"] == {"ok": True}
        oc = _delivery(store, b_st.replace("Street", "St"))
        publish(oc)
        c_id = wait_order(store.key, oc["order_id"])["id"]
        g = _next_geo(s, c_id)
        assert stop not in {t["other"] for t in g["times"]}

    # Bad payloads are 400s.
    assert _rpc(store.key, "geo.locate", {"order_id": "nope", "address": {}}).status_code == 400
    assert _rpc(store.key, "geo.locate", {"order_id": stop}).status_code == 400
    assert _rpc(store.key, "geo.forget", {}).status_code == 400


def test_geo_locate_ambiguous_then_choose(admin, store, fake_geo) -> None:
    _pin_store(admin, store)
    street = _street()
    matches = _ambiguous(fake_geo, street)
    stop = str(uuid.uuid4())
    with SSEStream(store.key) as s:
        assert _locate(store.key, stop, street) == {"ok": True}
        g = _next_geo(s, stop)
        assert g["state"] == "check" and "lat" not in g and "quality" not in g and "confirmed" not in g
        cands = g["candidates"]
        assert len(cands) == 3
        assert set(cands[0]) == {"label", "line1", "city", "state", "postal_code", "lat", "lng"}
        assert cands[1]["city"] == "Bayonne" and cands[1]["postal_code"] == "07002" and cands[1]["state"] == "NJ"
        assert cands[1]["label"].endswith("Bayonne, NJ 07002") and abs(cands[1]["lat"] - matches[1][1]) < 1e-6

        r = _rpc(store.key, "geo.choose", {"order_id": stop, "index": 5})
        assert r.status_code == 200 and r.json()["payload"] == {"ok": False, "code": "bad_index"}
        r = _rpc(store.key, "geo.choose", {"order_id": stop, "index": 1})
        assert r.status_code == 200 and r.json()["payload"] == {"ok": True}
        g = _next_geo(s, stop)
        assert g["state"] == "located" and g["quality"] == "pinned"
        assert (g["lat"], g["lng"]) == (matches[1][1], matches[1][2])
        assert g["confirmed_address"] == {"line1": cands[1]["line1"], "city": "Bayonne", "state": "NJ", "postal_code": "07002"}
        assert "candidates" not in g and g["confirmed"] is True

        # The same address again (another POS check) lands on the pick.
        again = str(uuid.uuid4())
        _locate(store.key, again, street)
        g = _next_geo(s, again)
        assert g["state"] == "located" and g["quality"] == "pinned" and (g["lat"], g["lng"]) == (matches[1][1], matches[1][2])
        assert "confirmed" not in g  # a pinned cache hit is not a confirmation

    r = _rpc(store.key, "geo.choose", {"order_id": str(uuid.uuid4()), "index": 0})
    assert r.status_code == 200 and r.json()["payload"] == {"ok": False, "code": "not_found"}
    assert _rpc(store.key, "geo.choose", {"order_id": stop}).status_code == 400
    assert _rpc(store.key, "geo.choose", {"order_id": stop, "index": -1}).status_code == 400


def test_online_order_check_then_choose(admin, store, publish, fake_geo) -> None:
    _pin_store(admin, store)
    street = _street()
    matches = _ambiguous(fake_geo, street)
    with SSEStream(store.key) as s:
        o = _delivery(store, street)
        publish(o)
        oid = wait_order(store.key, o["order_id"])["id"]
        g = _next_geo(s, oid)
        assert g["state"] == "check" and len(g["candidates"]) == 3 and "lat" not in g

        r = _rpc(store.key, "geo.choose", {"order_id": oid, "index": 2})
        assert r.status_code == 200 and r.json()["payload"] == {"ok": True}
        g = _next_geo(s, oid)
        assert g["state"] == "located" and g["quality"] == "pinned"
        assert (g["lat"], g["lng"]) == (matches[2][1], matches[2][2])
        assert g["confirmed_address"]["city"] == "Newark" and g["confirmed_address"]["postal_code"] == "07105"
        assert g["confirmed"] is True

        # A later pin replaces the pick (and drops the confirmed address).
        r = _rpc(store.key, "geo.pin", {"order_id": oid, "lat": 40.6650, "lng": -74.1180})
        assert r.status_code == 200 and r.json()["payload"] == {"ok": True}
        g = _next_geo(s, oid)
        assert g["quality"] == "pinned" and (g["lat"], g["lng"]) == (40.6650, -74.1180) and "confirmed_address" not in g
        assert g["confirmed"] is True


def test_online_order_with_a_duplicate_match_is_located(admin, store, publish, fake_geo) -> None:
    """The geocoder returning one house twice (~20 m apart, "ST" vs "STREET")
    is not ambiguous: a normal online order is located as before 2.1.0."""
    _pin_store(admin, store)
    street = _street()
    s_ = street.upper()
    fake_geo.add_matches(street, [(f"{s_.replace(' STREET', ' ST')}, BAYONNE, NJ, 07002", 40.6690, -74.1143),
                                  (f"{s_}, BAYONNE, NJ, 07002", 40.6692, -74.1143)])
    with SSEStream(store.key) as s:
        o = _delivery(store, street)
        publish(o)
        oid = wait_order(store.key, o["order_id"])["id"]
        g = _next_geo(s, oid)
        assert g["state"] == "located" and g["quality"] == "exact" and (g["lat"], g["lng"]) == (40.6690, -74.1143)
        assert "candidates" not in g and "confirmed" not in g


def test_geo_pin_an_external_stop(admin, store, fake_geo) -> None:
    _pin_store(admin, store)
    stop = str(uuid.uuid4())
    with SSEStream(store.key) as s:
        _locate(store.key, stop, _street())  # unknown to every geocoder
        g = _next_geo(s, stop)
        assert g["state"] == "not_found" and "lat" not in g and "confirmed" not in g
        r = _rpc(store.key, "geo.pin", {"order_id": stop, "lat": 40.6655, "lng": -74.1170})
        assert r.status_code == 200 and r.json()["payload"] == {"ok": True}
        g = _next_geo(s, stop)
        assert g["state"] == "located" and g["quality"] == "pinned" and (g["lat"], g["lng"]) == (40.6655, -74.1170)
        assert g["polyline"] == "fake_polyline" and g["confirmed"] is True

    # geo.trip orders external stops with online orders.
    r = _rpc(store.key, "geo.trip", {"order_ids": [stop]})
    assert r.status_code == 200 and r.json()["payload"]["order_ids"] == [stop] and r.json()["payload"]["unlocated"] == []


def test_geo_locate_result_is_repushed_when_the_agent_reconnects(admin, store, fake_geo) -> None:
    _pin_store(admin, store)
    street = _street()
    fake_geo.add(street, 40.6680, -74.1130)
    stop = str(uuid.uuid4())
    assert _locate(store.key, stop, street) == {"ok": True}  # no agent connected: the push is held
    # Located + routed (the push to the offline agent follows at once).
    wait_until(lambda: fake_geo.routed_from(40.6680, -74.1130), timeout=20, what="stop located and routed")
    with SSEStream(store.key) as s:
        g = _next_geo(s, stop, timeout=20)
        assert g["state"] == "located" and g["quality"] == "exact"


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def test_delivery_run_with_pos_stops(admin, store) -> None:
    from test_geo import _floor

    online = str(uuid.uuid4())
    pos = {"opaque_id": "pos:0123456789ab", "source": "pos", "placed_at": _now(), "sent_at": _now(),
           "out_at": _now(), "delivered_at": None, "distance_m": 2400}
    mixed = {"event_id": str(uuid.uuid4()), "type": "delivery.run", "at": _now(), "batch_id": 3,
             "driver_id": str(uuid.uuid4()), "driver_name": "Sam", "plan_id": "",
             "suggested_ids": [], "sent_ids": [online], "stops": [{"order_id": online, "eta": _now()}, pos]}
    pos_only = {"event_id": str(uuid.uuid4()), "type": "delivery.run", "at": _now(), "batch_id": 3,
                "driver_id": str(uuid.uuid4()), "driver_name": "Ann", "sent_ids": [], "stops": [pos]}
    bad = {"event_id": str(uuid.uuid4()), "type": "delivery.run", "at": _now(), "sent_ids": [],
           "stops": [{"eta": _now()}]}
    assert _floor(store.key, [mixed, pos_only, bad]) == {
        mixed["event_id"]: "applied", pos_only["event_id"]: "applied", bad["event_id"]: "invalid"}

    r = admin.get(f"{ADMIN_V1}/delivery-runs", params={"tenant_id": store.tenant_id, "location_id": store.location_id}, timeout=5)
    assert r.status_code == 200, r.text
    runs = {x["id"]: x for x in r.json()}
    assert set(runs) == {mixed["event_id"], pos_only["event_id"]}
    assert runs[pos_only["event_id"]]["sent_ids"] == [] and runs[pos_only["event_id"]]["stops"] == [pos]
    assert runs[mixed["event_id"]]["stops"][1] == pos
