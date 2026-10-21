"""Headless recovery + the delivery guarantee, with the test playing the agent:

  * POST /store/v1/agent/link — the store manager login links a blank Pi:
    mints an agent key, revokes (and disconnects) every older agent, hands
    the manager hash + runtime secrets; throttled per username
  * GET /store/v1/agent/restore — the open batch for a replacement Pi
  * order.ack — main re-sends an order until the agent acks it
  * POST /store/v1/agent/backup — nightly SQLite copies, newest 2 kept
  * agent.health → the admin fleet view; opening hours validation
"""

import secrets
import time
import uuid
from datetime import datetime, timedelta, timezone

import requests

from conftest import ADMIN_V1, STORE_V1, store_key, wait_order, wait_until
from sse import SSEStream


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _login(store) -> tuple[str, str]:
    """Give the throwaway store a manager login; returns (username, password)."""
    username, password = f"mgr{store.tag}", secrets.token_urlsafe(10)
    r = store.admin.patch(f"{ADMIN_V1}/tenants/{store.tenant_id}/credentials",
                          json={"username": username, "password": password}, timeout=5)
    assert r.status_code == 204, r.text
    return username, password


def _link(username: str, password: str, name: str = "pytest-pi") -> requests.Response:
    return requests.post(f"{STORE_V1}/agent/link",
                         json={"username": username, "password": password, "agent_name": name}, timeout=10)


def _rpc(key: str, typ: str, payload: dict) -> requests.Response:
    return requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(key),
                         json={"type": typ, "id": str(uuid.uuid4()), "payload": payload}, timeout=10)


def test_link_rejects_a_wrong_login(store) -> None:
    username, _ = _login(store)
    r = _link(username, "not-the-password")
    assert r.status_code == 401, r.text
    assert r.json()["code"] == "invalid_credentials"
    r = _link("nobody-" + store.tag, "x")
    assert r.status_code == 401, r.text


def test_link_throttles_per_username(store) -> None:
    username, password = _login(store)
    for _ in range(5):
        assert _link(username, "wrong").status_code == 401
    r = _link(username, password)
    assert r.status_code == 429, "the 6th try in the window is refused even with the right password"
    assert r.json()["code"] == "too_many_attempts"


def test_link_mints_revokes_old_agents_and_drops_their_stream(store) -> None:
    username, password = _login(store)
    old_key = store.key
    with SSEStream(old_key) as old:
        assert old.next()["event"] == "store_config"
        r = _link(username, password)
        assert r.status_code == 200, r.text
        body = r.json()
        new_key = body["api_key"]
        assert new_key and new_key != old_key
        assert body["tenant"]["id"] == store.tenant_id
        assert body["location"]["id"] == store.location_id
        assert body["manager"]["username"] == username
        assert body["manager"]["password_hash"].startswith("$2"), "a bcrypt hash (offline unlock)"
        assert store.device_id in body["revoked_agents"]
        assert isinstance(body["secrets"], dict)

        # The old agent's live stream is closed and its key is dead.
        wait_until(lambda: not old.alive(), timeout=10, what="old agent stream closed")
    assert requests.get(f"{STORE_V1}/store-config", headers=store_key(old_key), timeout=5).status_code == 401
    r = requests.get(f"{STORE_V1}/store-config", headers=store_key(new_key), timeout=5)
    assert r.status_code == 200 and r.json()["location"]["id"] == store.location_id

    cred = requests.get(f"{STORE_V1}/agent/credential", headers=store_key(new_key), timeout=5)
    assert cred.status_code == 200, cred.text
    assert cred.json() == body["manager"]


def test_restore_hands_back_the_open_batch(store, publish) -> None:
    username, password = _login(store)
    key = store.key
    a, b = store.order(), store.order()
    publish(a)
    publish(b)
    ida = wait_order(key, a["order_id"])["id"]
    idb = wait_order(key, b["order_id"])["id"]

    opened = (datetime.now(timezone.utc) - timedelta(hours=1)).isoformat()
    state = {"version": 1, "batch": {"id": 7, "opened_at": opened}, "kitchen_next": 1102, "last_prep_minutes": 25,
             "drivers": [{"id": "drv-1", "name": "Abdul", "position": 1, "status": "available"}],
             "open_shifts": [{"id": "sh-1", "driver_id": "drv-1", "driver_name": "Abdul", "batch_id": 7,
                              "clock_in": opened}]}
    r = _rpc(key, "agent.state", {"at": _now(), "state": state})
    assert r.status_code in (200, 204), r.text

    agent_status = {"state": "accepted", "arrival_date": _now(), "batch_id": 7, "kitchen_number": 1101,
                    "prep_minutes": 25, "accepted_at": _now()}
    ev = {"event_id": str(uuid.uuid4()), "type": "order.status", "order_id": ida, "status": "accepted",
          "at": _now(), "kitchen_number": 1101, "batch_id": 7, "agent_status": agent_status}
    r = _rpc(key, "floor.events", {"events": [ev]})
    assert r.status_code == 200, r.text

    # A replacement Pi links and restores.
    linked = _link(username, password).json()
    r = requests.get(f"{STORE_V1}/agent/restore", headers=store_key(linked["api_key"]), timeout=10)
    assert r.status_code == 200, r.text
    got = r.json()
    assert got["state"]["batch"]["id"] == 7 and got["state"]["kitchen_next"] == 1102
    assert got["state"]["drivers"][0]["name"] == "Abdul"
    by_id = {o["order"]["id"]: o for o in got["orders"]}
    assert ida in by_id and idb in by_id, "both open-batch orders come back"
    assert by_id[ida]["agent_status"]["state"] == "accepted"
    assert by_id[ida]["agent_status"]["kitchen_number"] == 1101
    assert by_id[ida]["agent_batch_id"] == 7
    assert by_id[idb].get("agent_status") is None, "never touched on the floor → rings again"

    # The old key can't read it any more.
    assert requests.get(f"{STORE_V1}/agent/restore", headers=store_key(key), timeout=5).status_code == 401


def test_unacked_orders_are_resent_until_acked(store, publish) -> None:
    with SSEStream(store.key) as s:
        assert s.next()["event"] == "store_config"
        o = store.order()
        publish(o)
        first = s.wait_event("order", lambda d: d.get("source_event_id") == str(o["order_id"]), timeout=30)
        assert first, "order never arrived"
        again = s.wait_event("order", lambda d: d.get("id") == first["id"], timeout=25)
        assert again, "an unacked order must be re-sent"

        r = _rpc(store.key, "order.ack", {"order_ids": [first["id"]]})
        assert r.status_code == 200, r.text
        assert r.json()["payload"]["acked"] == 1
        time.sleep(1)
        s.drain()
        assert s.wait_event("order", lambda d: d.get("id") == first["id"], timeout=15) is None, \
            "an acked order is not re-sent"


def test_backups_keep_the_newest_two(admin, store) -> None:
    h = {**store_key(store.key), "Content-Type": "application/vnd.sqlite3"}
    bad = requests.post(f"{STORE_V1}/agent/backup", headers=h, data=b"not a database", timeout=10)
    assert bad.status_code == 400, bad.text
    sizes = []
    for i in range(3):
        blob = b"SQLite format 3\x00" + bytes([i]) * (1000 + i)
        r = requests.post(f"{STORE_V1}/agent/backup", headers=h, data=blob, timeout=10)
        assert r.status_code == 201, r.text
        sizes.append(len(blob))
        time.sleep(0.05)
    r = admin.get(f"{ADMIN_V1}/agent-backups", params={"location_id": store.location_id}, timeout=10)
    assert r.status_code == 200, r.text
    rows = r.json()
    assert len(rows) == 2, rows
    assert sorted(x["size"] for x in rows) == sorted(sizes[1:])
    data = admin.get(f"{ADMIN_V1}/agent-backups/{rows[0]['id']}", timeout=10)
    assert data.status_code == 200 and data.content.startswith(b"SQLite format 3\x00")


def test_health_reports_reach_the_fleet_view(admin, store) -> None:
    r = _rpc(store.key, "agent.health", {"at": _now(), "device_id": store.device_id, "version": "pytest",
                                          "disk_free_bytes": 123456789, "temp_c": 51.5})
    assert r.status_code == 200, r.text
    fleet = admin.get(f"{ADMIN_V1}/agents/health", timeout=10)
    assert fleet.status_code == 200, fleet.text
    mine = [x for x in fleet.json() if x["location_id"] == store.location_id]
    assert mine and mine[0]["health"]["temp_c"] == 51.5


def test_opening_hours_are_validated(store) -> None:
    mgr = store.manager_session()
    loc = mgr.get(f"{STORE_V1}/me/location", timeout=5).json()
    body = {k: loc[k] for k in ("name", "timezone", "daily_reset_hour", "phone", "address_line1", "address_line2", "config")}
    good = {**body, "config": {**(body["config"] or {}),
                               "opening_hours": {"mon": [{"open": "11:00", "close": "23:00"}],
                                                 "fri": [{"open": "11:00", "close": "01:00"}]}}}
    r = mgr.patch(f"{STORE_V1}/me/location", json=good, timeout=5)
    assert r.status_code == 200, r.text
    assert r.json()["config"]["opening_hours"]["fri"][0]["close"] == "01:00"
    for bad_hours in ({"funday": []}, {"mon": [{"open": "25:00", "close": "23:00"}]},
                      {"mon": [{"open": "11:00", "close": "11:00"}]}):
        bad = {**body, "config": {**(body["config"] or {}), "opening_hours": bad_hours}}
        r = mgr.patch(f"{STORE_V1}/me/location", json=bad, timeout=5)
        assert r.status_code == 400, (bad_hours, r.status_code, r.text)
