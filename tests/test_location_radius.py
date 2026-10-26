"""2.2.0: the delivery radius set from the tablet through the Pi — RPC
`location.radius {radius_m}` (agent → main, POST /store/v1/agent/rpc).
main validates it (500–80467 m, else 400), saves it on the calling agent's
location (the same `delivery_radius_m` the store page shows), re-flags
`outside_area` on the location's open located deliveries — re-pushing
`order.geo` only for the orders whose flag changed — pushes `store_config`,
and replies `{radius_m}`. The test plays the agent.
"""

from __future__ import annotations

import time
import uuid

import requests

from conftest import STORE_V1, store_key, wait_order
from sse import SSEStream
from test_geo import _delivery, _next_geo, _pin_store, _street, fake_geo  # noqa: F401  (fixture)


def _radius(key: str, payload) -> requests.Response:
    env = {"type": "location.radius", "id": str(uuid.uuid4()), "payload": payload}
    return requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(key), json=env, timeout=10)


def _my_radius(store) -> int:
    r = store.manager_session().get(f"{STORE_V1}/me/location", timeout=5)
    assert r.status_code == 200, r.text
    return r.json()["delivery_radius_m"]


def test_radius_round_trip_pushes_store_config(make_store) -> None:
    a, b = make_store(), make_store()
    before_b = _my_radius(b)
    with SSEStream(a.key) as s:
        assert s.next()["event"] == "store_config"
        r = _radius(a.key, {"radius_m": 6000})
        assert r.status_code == 200, r.text
        assert r.json()["type"] == "location.radius.reply"
        assert r.json()["payload"] == {"radius_m": 6000}
        pushed = s.wait_event("store_config", lambda d: d["location"]["delivery_radius_m"] == 6000, timeout=10)
        assert pushed, "the new radius was not pushed to the agent"
    assert _my_radius(a) == 6000  # the store page's field
    assert _my_radius(b) == before_b  # scoped to the calling agent's location


def test_radius_is_validated(store) -> None:
    before = _my_radius(store)
    for payload in [{}, {"radius_m": 499}, {"radius_m": 80468}, {"radius_m": 1200.5}, {"radius_m": "far"}]:
        r = _radius(store.key, payload)
        assert r.status_code == 400, (payload, r.status_code, r.text)
    assert _radius(store.key, {"radius_m": 500}).status_code == 200
    assert _radius(store.key, {"radius_m": 80467}).status_code == 200
    assert _my_radius(store) == 80467 != before


def test_radius_reflags_open_deliveries(admin, store, publish, fake_geo) -> None:  # noqa: F811
    _pin_store(admin, store)
    near, far = _street(), _street()
    fake_geo.add(near, 40.6756, -74.1158)  # ~1 km north of the store
    fake_geo.add(far, 40.7357, -74.1724)   # ~9 km out: outside the default 5 mi
    with SSEStream(store.key) as s:
        o1, o2 = _delivery(store, near), _delivery(store, far)
        publish(o1)
        publish(o2)
        nid = wait_order(store.key, o1["order_id"])["id"]
        fid = wait_order(store.key, o2["order_id"])["id"]
        assert _next_geo(s, nid)["outside_area"] is False
        assert _next_geo(s, fid)["outside_area"] is True

        # 15 km: the far one is inside now — pushed again; the near one isn't.
        r = _radius(store.key, {"radius_m": 15000})
        assert r.status_code == 200 and r.json()["payload"] == {"radius_m": 15000}, r.text
        g = _next_geo(s, fid)
        assert g["state"] == "located" and g["outside_area"] is False
        again = s.wait_event("command", lambda d: d.get("type") == "order.geo" and d["payload"]["order_id"] == nid,
                             timeout=4)
        assert again is None, "an order whose flag didn't change was pushed again"

        # 600 m: both are outside; both are pushed.
        assert _radius(store.key, {"radius_m": 600}).status_code == 200
        assert _next_geo(s, nid)["outside_area"] is True
        assert _next_geo(s, fid)["outside_area"] is True
