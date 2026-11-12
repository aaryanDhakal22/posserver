"""A counter delivery's new address is located again (the owner's flow-audit follow-up, 2026-11-12;
TERMINAL-FLOWS CHG-14): the Pi's order.changed with another street runs the same geo path as intake
(Census → Nominatim, OSRM drive times) and main pushes order.geo again, so the Pi's map, drive time,
pools and AUTO see the new address. Same fake providers + agent-playing helpers as test_geo.py. Dev only.
"""

from __future__ import annotations

import uuid

import pytest

from sse import SSEStream
from test_counter_orders import _now, _placed, _send, _snapshot
from test_geo import _next_geo, _pin_store, _street, fake_geo  # noqa: F401 (fixture)

# One xdist worker for every test that swaps main's global geo providers.
pytestmark = pytest.mark.xdist_group("geo")


def _addr(street: str) -> dict:
    return {"line1": street, "city": "Bayonne", "state": "NJ", "postal_code": "07002", "country": "US"}


def _delivery_snapshot(oid: str, street: str) -> dict:
    money = {"subtotal_cents": 1299, "tax_cents": 78, "tip_cents": 0,
             "fees": [{"type": "delivery", "label": "Delivery fee", "amount_cents": 400, "driver_cents": 300}],
             "discounts": [], "total_cents": 1777}
    item = {"id": str(uuid.uuid4()), "position": 0, "name": "Cheese Pizza", "quantity": 1, "unit_price_cents": 1299,
            "line_total_cents": 1299, "routing": "matched"}
    return _snapshot(oid, fulfillment={"type": "delivery", "address": _addr(street)}, items=[item], money=money, payments=[])


def test_changed_delivery_address_is_located_again(admin, store, fake_geo) -> None:
    _pin_store(admin, store)
    first, second = _street(), _street()
    fake_geo.add(first, 40.6690, -74.1143)
    fake_geo.add(second, 40.6600, -74.1200)
    oid = str(uuid.uuid4())
    snap = _delivery_snapshot(oid, first)
    with SSEStream(store.key) as s:
        ev = _placed(snap)
        assert _send(store.key, [ev]) == {ev["event_id"]: "applied"}
        g = _next_geo(s, oid)
        assert g["state"] == "located" and abs(g["lat"] - 40.6690) < 1e-6, g

        # The customer gave the wrong address: the Pi's order.changed carries the new one.
        moved = {**snap, "fulfillment": {"type": "delivery", "address": _addr(second)}}
        ch = {"event_id": str(uuid.uuid4()), "type": "order.changed", "order_id": oid, "at": _now(), "snapshot": moved}
        assert _send(store.key, [ch]) == {ch["event_id"]: "applied"}
        g = _next_geo(s, oid)
        assert g["state"] == "located" and abs(g["lat"] - 40.6600) < 1e-6 and abs(g["lng"] + 74.1200) < 1e-6, g
        assert g["store_to_s"] > 0 and g["to_store_s"] > 0 and g["distance_m"] > 0
        assert fake_geo.census_for(second) >= 1

        # Only the apartment / a note moved: the same place, located from the cache (no new geocoder call).
        calls = fake_geo.census_for(second)
        notes = {**moved, "fulfillment": {"type": "delivery", "address": {**_addr(second), "line2": "Apt 4", "notes": "back door"}}}
        ch2 = {"event_id": str(uuid.uuid4()), "type": "order.changed", "order_id": oid, "at": _now(), "snapshot": notes}
        assert _send(store.key, [ch2]) == {ch2["event_id"]: "applied"}
        assert fake_geo.census_for(second) == calls
