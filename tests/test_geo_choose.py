"""Address candidates (2.1.0) and what is left of 2.1.0's POS deliveries.

An ambiguous address comes back `state:"check"` with up to 3 candidates; staff
pick one (`geo.choose {order_id, index}`) and the result is pushed again
located / pinned with `confirmed_address`.

SoftTouch's POS deliveries (`geo.locate` / `geo.forget`, external stops, and
the anonymous `{opaque_id, source:"pos"}` stops of a `delivery.run`) were
retired with SoftTouch (housekeeping H3): main answers the two RPCs as an
unknown type (202, nothing done) and refuses a run with an opaque stop.

Same fake providers + agent-playing helpers as test_geo.py. Dev only.
"""

from __future__ import annotations

import uuid
from datetime import datetime, timezone

import pytest

from conftest import ADMIN_V1, wait_order
from sse import SSEStream
from test_geo import _delivery, _next_geo, _pin_store, _rpc, _street, fake_geo  # noqa: F401 (fixture)

# One xdist worker for every test that swaps main's global geo providers.
pytestmark = pytest.mark.xdist_group("geo")


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


def test_choose_unknown_order_and_bad_payloads(store) -> None:
    r = _rpc(store.key, "geo.choose", {"order_id": str(uuid.uuid4()), "index": 0})
    assert r.status_code == 200 and r.json()["payload"] == {"ok": False, "code": "not_found"}
    stop = str(uuid.uuid4())
    assert _rpc(store.key, "geo.choose", {"order_id": stop}).status_code == 400
    assert _rpc(store.key, "geo.choose", {"order_id": stop, "index": -1}).status_code == 400


def test_retired_pos_rpcs_are_a_no_op(store) -> None:
    """A Pi that still sends SoftTouch's geo.locate / geo.forget gets the
    unknown-type answer (202, no body): nothing stored, nothing pushed."""
    stop = str(uuid.uuid4())
    r = _rpc(store.key, "geo.locate", {"order_id": stop, "address": {
        "line1": _street(), "line2": "", "city": "Bayonne", "state": "NJ", "postal_code": "07002"}})
    assert r.status_code == 202 and r.text == ""
    r = _rpc(store.key, "geo.forget", {"order_id": stop})
    assert r.status_code == 202 and r.text == ""
    r = _rpc(store.key, "geo.pin", {"order_id": stop, "lat": 40.6655, "lng": -74.1170})
    assert r.status_code == 200 and r.json()["payload"] == {"ok": False, "code": "not_found"}


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def test_delivery_run_refuses_pos_stops(admin, store) -> None:
    from test_geo import _floor

    online = str(uuid.uuid4())
    pos = {"opaque_id": "pos:0123456789ab", "source": "pos", "placed_at": _now(), "sent_at": _now(),
           "out_at": _now(), "delivered_at": None, "distance_m": 2400}
    plain = {"event_id": str(uuid.uuid4()), "type": "delivery.run", "at": _now(), "batch_id": 3,
             "driver_id": str(uuid.uuid4()), "driver_name": "Sam", "plan_id": "",
             "suggested_ids": [], "sent_ids": [online], "stops": [{"order_id": online, "eta": _now()}]}
    mixed = {**plain, "event_id": str(uuid.uuid4()), "stops": [{"order_id": online, "eta": _now()}, pos]}
    pos_only = {"event_id": str(uuid.uuid4()), "type": "delivery.run", "at": _now(), "batch_id": 3,
                "driver_id": str(uuid.uuid4()), "driver_name": "Ann", "sent_ids": [], "stops": [pos]}
    assert _floor(store.key, [plain, mixed, pos_only]) == {
        plain["event_id"]: "applied", mixed["event_id"]: "invalid", pos_only["event_id"]: "invalid"}

    r = admin.get(f"{ADMIN_V1}/delivery-runs", params={"tenant_id": store.tenant_id, "location_id": store.location_id}, timeout=5)
    assert r.status_code == 200, r.text
    assert {x["id"] for x in r.json()} == {plain["event_id"]}
