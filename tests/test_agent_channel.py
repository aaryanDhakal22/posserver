"""The agent-facing surface of main, with the test playing the agent:

  * device-keyed store-config (what the agent's setup screen verifies a token with)
  * the duplex RPC channel — main→agent `event: command` frames on SSE,
    agent→main POST /store/v1/agent/rpc (replies + unsolicited reports)
  * the advisory live-printer roster built from those reports
  * agent self-revoke
"""

import threading
import uuid
from datetime import datetime, timezone

import requests

from conftest import ADMIN_V1, API_V1, STORE_V1, store_key
from sse import SSEStream


def _rpc(api_key: str, envelope: dict) -> requests.Response:
    return requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(api_key), json=envelope, timeout=5)


def _report(printers: list[dict]) -> dict:
    return {
        "type": "printer.report",
        "id": str(uuid.uuid4()),
        "payload": {
            "reported_at": datetime.now(timezone.utc).isoformat(),
            "agent_version": "pytest",
            "printers": printers,
        },
    }


def test_store_config_is_device_scoped(store) -> None:
    r = requests.get(f"{STORE_V1}/store-config", headers=store_key(store.key), timeout=5)
    assert r.status_code == 200, r.text
    assert r.json()["location"]["id"] == store.location_id
    assert requests.get(f"{STORE_V1}/store-config", timeout=5).status_code == 401


def test_rpc_requires_device_key(admin) -> None:
    r = requests.post(f"{STORE_V1}/agent/rpc", json={"type": "ping", "id": "x"}, timeout=5)
    assert r.status_code == 401


def test_unknown_rpc_type_is_accepted_noop(store) -> None:
    r = _rpc(store.key, {"type": "some.future.thing", "id": str(uuid.uuid4())})
    assert r.status_code == 202, r.text


def test_ping_offline_agent_is_503(admin, store) -> None:
    r = admin.post(f"{ADMIN_V1}/agent/ping", json={"tenant_id": store.tenant_id, "location_id": store.location_id}, timeout=10)
    assert r.status_code == 503, r.text


def test_ping_round_trip_over_the_duplex_channel(admin, store) -> None:
    """Admin ping → main publishes `event: command` on our SSE → we POST the
    reply with the same id → main correlates it and answers the admin call."""
    with SSEStream(store.key) as s:
        assert s.next()["event"] == "store_config"
        result: dict = {}

        def ping() -> None:
            result["resp"] = admin.post(
                f"{ADMIN_V1}/agent/ping",
                json={"tenant_id": store.tenant_id, "location_id": store.location_id},
                timeout=10,
            )

        t = threading.Thread(target=ping)
        t.start()
        cmd = s.wait_event("command", lambda d: d.get("type") == "ping", timeout=5)
        assert cmd, "main never sent the ping command over SSE"
        reply = _rpc(store.key, {"type": "ping.reply", "id": cmd["id"], "payload": {"pong": True}})
        assert reply.status_code == 204, reply.text
        t.join(timeout=10)

        r = result["resp"]
        assert r.status_code == 200, r.text
        assert r.json()["payload"] == {"pong": True}


def test_printer_report_feeds_live_roster(store) -> None:
    printers = [{"name": "Pizza", "ip": "10.0.0.9", "up": True}, {"name": "Wings", "ip": "10.0.0.10", "up": False}]
    r = _rpc(store.key, _report(printers))
    assert r.status_code in (200, 202, 204), r.text

    live = store.manager_session().get(f"{STORE_V1}/me/printers/live", timeout=10)
    assert live.status_code == 200, live.text
    body = live.json()
    assert body["agent_online"] is False  # no SSE subscriber → served from the cache
    assert {(p["name"], p["up"]) for p in body["printers"]} == {("Pizza", True), ("Wings", False)}
    assert body["age_seconds"] >= 0


def test_live_roster_is_scoped(make_store) -> None:
    a, b = make_store(), make_store()
    _rpc(a.key, _report([{"name": "OnlyA", "ip": "10.0.0.1", "up": True}]))
    body = b.manager_session().get(f"{STORE_V1}/me/printers/live", timeout=10).json()
    assert all(p["name"] != "OnlyA" for p in body["printers"])


def test_agent_self_revoke_kills_the_key(store) -> None:
    h = store_key(store.key)
    assert requests.get(f"{STORE_V1}/store-config", headers=h, timeout=5).status_code == 200
    r = requests.post(f"{STORE_V1}/agent/revoke", headers=h, timeout=5)
    assert r.status_code == 204, r.text
    assert requests.get(f"{STORE_V1}/store-config", headers=h, timeout=5).status_code == 401


def test_store_config_is_read_only_for_agents(store) -> None:
    """Printers/location are edited only in /app/store; the device-keyed
    store-config is GET-only now."""
    r = requests.post(f"{STORE_V1}/store-config", headers=store_key(store.key),
                      json={"name": "x", "printers": []}, timeout=5)
    assert r.status_code in (404, 405), r.text


def test_agent_only_endpoints_refuse_other_device_kinds(store) -> None:
    """A manager-minted mobile/kiosk key must not read the agent's order stream
    (it would mark orders dispatched without the agent printing them) or post
    floor events / reports."""
    mgr = store.manager_session()
    r = mgr.post(f"{STORE_V1}/me/devices", json={"name": "kiosk", "kind": "kiosk"}, timeout=5)
    assert r.status_code == 201, r.text
    kiosk = store_key(r.json()["api_key"])
    assert requests.get(f"{STORE_V1}/store-config", headers=kiosk, timeout=5).status_code == 403
    assert requests.post(f"{STORE_V1}/agent/rpc", headers=kiosk, json={"type": "x", "id": "1"}, timeout=5).status_code == 403
    assert requests.get(f"{API_V1}/events/orders", headers=kiosk, timeout=5).status_code == 403
    # Order reads stay open to any device of the store.
    assert requests.get(f"{API_V1}/orders", headers=kiosk, timeout=5).status_code == 200
