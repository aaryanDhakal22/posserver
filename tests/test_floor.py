"""main as the floor read model: the agent pushes `floor.events` (order status
+ kitchen number + prep estimate + driver, and its driver roster) over the
duplex channel; main applies them forward-only, idempotently, scoped to the
reporting agent. The test plays the agent."""

import uuid
from datetime import datetime, timedelta, timezone

import requests

from conftest import STORE_V1, store_key, wait_order
from sse import SSEStream


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _status(order_id: str, status: str, **kw) -> dict:
    return {"event_id": str(uuid.uuid4()), "type": "order.status", "order_id": order_id,
            "status": status, "at": _now(), **kw}


def _send(key: str, events: list[dict]) -> requests.Response:
    env = {"type": "floor.events", "id": str(uuid.uuid4()), "payload": {"events": events}}
    return requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(key), json=env, timeout=10)


def _results(r: requests.Response) -> dict[str, str]:
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["type"] == "floor.events.reply"
    return {x["event_id"]: x["result"] for x in body["payload"]["results"]}


def test_status_events_apply_forward_only_and_idempotently(store, publish) -> None:
    o = store.order()
    publish(o)
    oid = wait_order(store.key, o["order_id"])["id"]
    eta = (datetime.now(timezone.utc) + timedelta(minutes=20)).isoformat()

    acc = _status(oid, "accepted", kitchen_number=1104, prep_minutes=20, estimated_ready_at=eta)
    ready = _status(oid, "ready")
    res = _results(_send(store.key, [acc, ready]))
    assert res == {acc["event_id"]: "applied", ready["event_id"]: "applied"}

    got = wait_order(store.key, o["order_id"])
    assert got["status"] == "ready" and got["kitchen_order_number"] == 1104 and got["estimated_ready_at"]

    # Retry of the same POST: duplicates. A late "accepted": stale (no going back).
    late = _status(oid, "accepted")
    res = _results(_send(store.key, [acc, late]))
    assert res == {acc["event_id"]: "duplicate", late["event_id"]: "stale"}
    assert wait_order(store.key, o["order_id"])["status"] == "ready"


def test_delivery_states_and_driver(store, publish) -> None:
    o = store.order()
    publish(o)
    oid = wait_order(store.key, o["order_id"])["id"]
    _results(_send(store.key, [_status(oid, "on_delivery", driver_name="Sam")]))  # skipping steps is fine
    got = wait_order(store.key, o["order_id"])
    assert got["status"] == "on_delivery" and got["driver_name"] == "Sam"
    _results(_send(store.key, [_status(oid, "delivered")]))
    assert wait_order(store.key, o["order_id"])["status"] == "completed"


def test_cancel_from_batch_close(store, publish) -> None:
    """An order nobody accepted is cancelled when the agent closes its batch;
    an accepted one can't be cancelled that way."""
    o, p = store.order(), store.order()
    publish(o)
    publish(p)
    oid = wait_order(store.key, o["order_id"])["id"]
    pid = wait_order(store.key, p["order_id"])["id"]
    _results(_send(store.key, [_status(pid, "accepted", kitchen_number=1100)]))
    c1, c2 = _status(oid, "cancelled", auto=True), _status(pid, "cancelled", auto=True)
    assert _results(_send(store.key, [c1, c2])) == {c1["event_id"]: "applied", c2["event_id"]: "stale"}
    assert wait_order(store.key, o["order_id"])["status"] == "cancelled"
    assert wait_order(store.key, p["order_id"])["status"] == "accepted"


def test_scope_unknown_and_invalid(make_store, publish) -> None:
    a, b = make_store(), make_store()
    o = a.order()
    publish(o)
    oid = wait_order(a.key, o["order_id"])["id"]
    foreign = _status(oid, "accepted")           # B's agent can't touch A's order
    missing = _status(str(uuid.uuid4()), "ready")
    bogus = _status(oid, "teleported")
    odd = {"event_id": str(uuid.uuid4()), "type": "something.new", "at": _now()}
    res = _results(_send(b.key, [foreign, missing, bogus, odd]))
    assert res == {foreign["event_id"]: "not_found", missing["event_id"]: "not_found",
                   bogus["event_id"]: "invalid", odd["event_id"]: "invalid"}
    assert wait_order(a.key, o["order_id"])["status"] == "received"


def test_status_change_is_not_echoed_to_the_agent(store, publish) -> None:
    o = store.order()
    publish(o)
    with SSEStream(store.key) as s:
        assert s.next()["event"] == "store_config"
        assert s.wait_event("order", lambda d: d.get("source_event_id") == str(o["order_id"]))
        oid = wait_order(store.key, o["order_id"])["id"]
        _results(_send(store.key, [_status(oid, "accepted", kitchen_number=1100)]))
        echo = s.wait_event("order", lambda d: d.get("id") == oid, timeout=4)
        assert echo is None, "main must not push the agent's own floor change back to it"


def test_driver_roster_and_store_floor_view(store, publish) -> None:
    o = store.order()
    publish(o)
    oid = wait_order(store.key, o["order_id"])["id"]
    roster = {"event_id": str(uuid.uuid4()), "type": "drivers.roster", "at": _now(), "drivers": [
        {"id": str(uuid.uuid4()), "name": "Sam", "position": 1, "status": "out"},
        {"id": str(uuid.uuid4()), "name": "Ana", "position": 2, "status": "available"}]}
    _results(_send(store.key, [roster, _status(oid, "accepted", kitchen_number=1107)]))

    mgr = store.manager_session()
    floor = mgr.get(f"{STORE_V1}/me/floor", timeout=5).json()
    assert [(d["name"], d["status"]) for d in floor["drivers"]] == [("Sam", "out"), ("Ana", "available")]
    assert floor["last_sync_at"]
    orders = mgr.get(f"{STORE_V1}/me/orders", timeout=5).json()
    mine = next(x for x in orders if x["id"] == oid)
    assert mine["status"] == "accepted" and mine["kitchen_order_number"] == 1107

    # A newer roster replaces the old one wholesale.
    roster2 = {"event_id": str(uuid.uuid4()), "type": "drivers.roster", "at": _now(), "drivers": [
        {"id": str(uuid.uuid4()), "name": "Zed", "position": 1, "status": "available"}]}
    _results(_send(store.key, [roster2]))
    assert [d["name"] for d in mgr.get(f"{STORE_V1}/me/floor", timeout=5).json()["drivers"]] == ["Zed"]


def test_old_agents_without_floor_events_are_fine(store) -> None:
    """main never invents floor state: no events → nothing synced."""
    floor = store.manager_session().get(f"{STORE_V1}/me/floor", timeout=5).json()
    assert floor == {"drivers": [], "last_sync_at": None, "shifts": []}


def test_driver_shifts_are_recorded(store) -> None:
    """driver.shift events (clock-in, then the clock-out for the same shift)
    become one shift record per shift for reports."""
    sid, did = str(uuid.uuid4()), str(uuid.uuid4())
    cin = datetime.now(timezone.utc) - timedelta(hours=3)
    opened = {"event_id": str(uuid.uuid4()), "type": "driver.shift", "shift_id": sid, "driver_id": did,
              "driver_name": "Juma", "clock_in": cin.isoformat(), "at": _now()}
    assert _results(_send(store.key, [opened])) == {opened["event_id"]: "applied"}
    closed = {**opened, "event_id": str(uuid.uuid4()), "clock_out": _now(), "auto": True}
    bad = {"event_id": str(uuid.uuid4()), "type": "driver.shift", "shift_id": "nope", "at": _now()}
    assert _results(_send(store.key, [closed, bad])) == {closed["event_id"]: "applied", bad["event_id"]: "invalid"}
    assert _results(_send(store.key, [closed])) == {closed["event_id"]: "duplicate"}, "retries are safe"
    shifts = store.manager_session().get(f"{STORE_V1}/me/floor", timeout=5).json()["shifts"]
    assert len(shifts) == 1 and shifts[0]["driver_name"] == "Juma" and shifts[0]["clock_out"] and shifts[0]["auto_out"], shifts


def test_bad_payloads_get_answers_not_retry_loops(store) -> None:
    """A malformed payload is a 400 (the agent won't retry the same bytes
    forever); an undecodable event inside a good batch is answered `invalid`."""
    env = {"type": "floor.events", "id": str(uuid.uuid4()), "payload": "not an object"}
    r = requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(store.key), json=env, timeout=5)
    assert r.status_code == 400, r.text
    weird = {"event_id": str(uuid.uuid4()), "type": "order.status", "kitchen_number": "not-a-number"}
    assert _results(_send(store.key, [weird])) == {weird["event_id"]: "invalid"}


def test_driver_ids_are_per_location(make_store) -> None:
    """A Pi re-linked to another store keeps its driver ids: the mirrors are
    keyed per location, so both stores keep their own copy."""
    a, b = make_store(), make_store()
    did, sid = str(uuid.uuid4()), str(uuid.uuid4())
    for st, name in ((a, "Sam A"), (b, "Sam B")):
        roster = {"event_id": str(uuid.uuid4()), "type": "drivers.roster", "at": _now(),
                  "drivers": [{"id": did, "name": name, "position": 1, "status": "available"}]}
        shift = {"event_id": str(uuid.uuid4()), "type": "driver.shift", "shift_id": sid, "driver_id": did,
                 "driver_name": name, "clock_in": _now(), "at": _now()}
        assert set(_results(_send(st.key, [roster, shift])).values()) == {"applied"}
    for st, name in ((a, "Sam A"), (b, "Sam B")):
        fl = st.manager_session().get(f"{STORE_V1}/me/floor", timeout=5).json()
        assert [d["name"] for d in fl["drivers"]] == [name]
        assert [s["driver_name"] for s in fl["shifts"]] == [name]
