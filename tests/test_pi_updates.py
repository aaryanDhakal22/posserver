"""2.0.6: Pi updates + printer auto-follow + found printers, with the test
playing the agent (SSE `event: command` down, POST /store/v1/agent/rpc up):

  * admin POST /admin/v1/agent/update + /rollback — recorded in agent_updates
    before the RPC; offline → 503 "unreachable", the agent's reply (accepted /
    waiting_for_idle / rejected code) stored; GET /admin/v1/agent-updates
  * agent.update.status reports upsert the row by id (Pi-started updates too),
    scoped to the reporting location
  * printer.moved / printer.add from the agent, the manager's IP edits, and
    GET /store/v1/me/printers/ip-changes
  * the live roster passes the agent's per-printer mac + found printers through
"""

import threading
import uuid
from datetime import datetime, timezone

import requests

from conftest import ADMIN_V1, STORE_V1, store_key
from sse import SSEStream


def _rpc(api_key: str, msg_type: str, payload: dict, msg_id: str | None = None) -> requests.Response:
    env = {"type": msg_type, "id": msg_id or str(uuid.uuid4()), "payload": payload}
    return requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(api_key), json=env, timeout=5)


def _scope(store) -> dict:
    return {"tenant_id": store.tenant_id, "location_id": store.location_id}


def _updates(admin, store) -> list[dict]:
    r = admin.get(f"{ADMIN_V1}/agent-updates", params={"location_id": store.location_id}, timeout=5)
    assert r.status_code == 200, r.text
    return r.json()


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


# --- admin update / rollback -------------------------------------------------


def test_update_offline_agent_is_503_and_recorded_unreachable(admin, store) -> None:
    r = admin.post(f"{ADMIN_V1}/agent/update", json={**_scope(store), "force": True}, timeout=15)
    assert r.status_code == 503, r.text
    body = r.json()
    assert body["code"] == "agent_offline" and body["state"] == "unreachable" and body["id"]

    rows = _updates(admin, store)
    assert len(rows) == 1
    row = rows[0]
    assert row["id"] == body["id"]
    assert (row["action"], row["state"], row["requested_by"], row["force"]) == ("update", "unreachable", "admin", True)
    assert row["error"]


def test_rollback_offline_is_503_and_keeps_console_as_requester(admin, store) -> None:
    r = admin.post(f"{ADMIN_V1}/agent/rollback", json={**_scope(store), "requested_by": "console"}, timeout=15)
    assert r.status_code == 503, r.text
    row = _updates(admin, store)[0]
    assert (row["action"], row["state"], row["requested_by"], row["force"]) == ("rollback", "unreachable", "console", False)


def test_update_refuses_bad_requests(admin, make_store) -> None:
    a, b = make_store(), make_store()
    # b's location under a's tenant → 404, nothing recorded.
    r = admin.post(f"{ADMIN_V1}/agent/update", json={"tenant_id": a.tenant_id, "location_id": b.location_id}, timeout=10)
    assert r.status_code == 404, r.text
    assert _updates(admin, b) == []
    r = admin.post(f"{ADMIN_V1}/agent/update", json={**_scope(a), "requested_by": "pi-web"}, timeout=10)
    assert r.status_code == 400, r.text
    assert requests.post(f"{ADMIN_V1}/agent/update", json=_scope(a), timeout=5).status_code == 401
    assert requests.get(f"{ADMIN_V1}/agent-updates", timeout=5).status_code == 401


def _ask_and_reply(admin, store, path: str, body: dict, cmd_type: str, reply: dict) -> tuple[requests.Response, dict]:
    """Admin call in a thread; we read the command off SSE and reply to it."""
    with SSEStream(store.key) as s:
        assert s.next()["event"] == "store_config"
        result: dict = {}
        t = threading.Thread(target=lambda: result.setdefault(
            "resp", admin.post(f"{ADMIN_V1}/agent/{path}", json={**_scope(store), **body}, timeout=15)))
        t.start()
        cmd = s.wait_event("command", lambda d: d.get("type") == cmd_type, timeout=10)
        assert cmd, f"main never sent {cmd_type}"
        r = _rpc(store.key, cmd_type + ".reply", reply, msg_id=cmd["id"])
        assert r.status_code == 204, r.text
        t.join(timeout=15)
    return result["resp"], cmd


def test_update_round_trip_then_status_reports(admin, store) -> None:
    resp, cmd = _ask_and_reply(admin, store, "update", {"force": True}, "agent.update",
                               {"accepted": True, "state": "waiting_for_idle", "code": ""})
    assert resp.status_code == 200, resp.text
    body = resp.json()
    assert body["accepted"] is True and body["state"] == "waiting_for_idle" and body["code"] == ""
    assert cmd["payload"] == {"id": body["id"], "force": True, "requested_by": "admin"}

    row = _updates(admin, store)[0]
    assert row["id"] == body["id"] and row["state"] == "waiting_for_idle"

    # The agent reports every state change for the same id.
    status = {"id": body["id"], "action": "update", "requested_by": "admin", "force": True,
              "state": "running", "from_version": "2.0.5", "to_version": "2.0.6",
              "from_sha": "aa", "to_sha": "bb", "error": "", "started_at": _now(), "finished_at": None}
    r = _rpc(store.key, "agent.update.status", status)
    assert r.status_code == 200, r.text
    assert r.json()["payload"] == {"ok": True}
    r = _rpc(store.key, "agent.update.status", {**status, "state": "ok", "finished_at": _now()})
    assert r.status_code == 200, r.text

    rows = _updates(admin, store)
    assert len(rows) == 1
    assert (rows[0]["state"], rows[0]["from_version"], rows[0]["to_version"]) == ("ok", "2.0.5", "2.0.6")


def test_update_rejected_by_the_agent_is_stored(admin, store) -> None:
    resp, _ = _ask_and_reply(admin, store, "update", {}, "agent.update",
                             {"accepted": False, "state": "", "code": "not_after_close"})
    assert resp.status_code == 200, resp.text
    body = resp.json()
    assert (body["accepted"], body["state"], body["code"]) == (False, "rejected", "not_after_close")
    row = _updates(admin, store)[0]
    assert (row["state"], row["error"], row["force"]) == ("rejected", "not_after_close", False)


def test_rollback_round_trip(admin, store) -> None:
    resp, cmd = _ask_and_reply(admin, store, "rollback", {}, "agent.rollback",
                               {"accepted": True, "state": "requested", "code": ""})
    assert resp.status_code == 200, resp.text
    assert cmd["payload"] == {"id": resp.json()["id"], "requested_by": "admin"}
    assert _updates(admin, store)[0]["state"] == "requested"


def test_pi_started_update_is_inserted_and_scoped(admin, make_store) -> None:
    a, b = make_store(), make_store()
    uid = str(uuid.uuid4())
    status = {"id": uid, "action": "update", "requested_by": "pi-web", "force": False, "state": "failed",
              "from_version": "2.0.5", "to_version": "2.0.6", "error": "manifest signature", "started_at": _now()}
    assert _rpc(a.key, "agent.update.status", status).status_code == 200
    row = _updates(admin, a)[0]
    assert (row["id"], row["requested_by"], row["state"], row["error"]) == (uid, "pi-web", "failed", "manifest signature")

    # Another store's agent can't move a's row (and doesn't get one of its own).
    assert _rpc(b.key, "agent.update.status", {**status, "state": "ok", "error": ""}).status_code == 200
    assert _updates(admin, a)[0]["state"] == "failed"
    assert _updates(admin, b) == []

    # The unfiltered list has it too.
    r = admin.get(f"{ADMIN_V1}/agent-updates", params={"limit": 200}, timeout=5)
    assert r.status_code == 200 and any(u["id"] == uid for u in r.json())

    bad = _rpc(a.key, "agent.update.status", {"id": "", "state": "ok"})
    assert bad.status_code == 400, bad.text


# --- printers: auto-follow, add from the Pi, IP history ----------------------


def _add_printer(mgr, name: str, ip: str) -> dict:
    r = mgr.post(f"{STORE_V1}/me/printers", json={"name": name, "ip": ip}, timeout=5)
    assert r.status_code == 201, r.text
    return r.json()


def _ip_changes(mgr) -> list[dict]:
    r = mgr.get(f"{STORE_V1}/me/printers/ip-changes", params={"limit": 20}, timeout=5)
    assert r.status_code == 200, r.text
    return r.json()


def test_printer_moved_follows_and_logs(store) -> None:
    mgr = store.manager_session()
    p = _add_printer(mgr, "Pizza", "10.0.0.20")
    assert p["mac"] == ""

    with SSEStream(store.key) as s:
        assert s.next()["event"] == "store_config"
        r = _rpc(store.key, "printer.moved",
                 {"printer_id": p["id"], "old_ip": "10.0.0.20", "new_ip": "10.0.0.33", "mac": "AA:BB:CC:00:00:01"})
        assert r.status_code == 200, r.text
        assert r.json()["type"] == "printer.moved.reply"
        assert r.json()["payload"] == {"applied": True, "reason": ""}
        pushed = s.wait_event("store_config", lambda d: any(x["ip"] == "10.0.0.33" for x in d["printers"]), timeout=10)
        assert pushed, "the moved printer was not pushed to the agent"
        assert any(x.get("mac") == "aa:bb:cc:00:00:01" for x in pushed["printers"])

    mine = {x["id"]: x for x in mgr.get(f"{STORE_V1}/me/printers", timeout=5).json()}
    assert (mine[p["id"]]["ip"], mine[p["id"]]["mac"]) == ("10.0.0.33", "aa:bb:cc:00:00:01")
    changes = _ip_changes(mgr)
    assert len(changes) == 1
    c = changes[0]
    assert (c["printer_id"], c["printer_name"], c["old_ip"], c["new_ip"], c["source"]) == \
        (p["id"], "Pizza", "10.0.0.20", "10.0.0.33", "agent")
    assert c["mac"] == "aa:bb:cc:00:00:01" and c["at"]

    # The Pi retrying the same move: applied, no second history line.
    r = _rpc(store.key, "printer.moved", {"printer_id": p["id"], "old_ip": "10.0.0.20", "new_ip": "10.0.0.33", "mac": ""})
    assert r.json()["payload"] == {"applied": True, "reason": ""}
    assert len(_ip_changes(mgr)) == 1

    # A move from an IP the printer no longer has (the manager changed it) is stale.
    r = _rpc(store.key, "printer.moved", {"printer_id": p["id"], "old_ip": "10.0.0.20", "new_ip": "10.0.0.44", "mac": ""})
    assert r.json()["payload"] == {"applied": False, "reason": "stale_old_ip"}


def test_printer_moved_is_scoped(make_store) -> None:
    a, b = make_store(), make_store()
    p = _add_printer(a.manager_session(), "Pizza", "10.0.0.20")
    r = _rpc(b.key, "printer.moved", {"printer_id": p["id"], "old_ip": "10.0.0.20", "new_ip": "10.0.0.66", "mac": ""})
    assert r.status_code == 200, r.text
    assert r.json()["payload"] == {"applied": False, "reason": "not_found"}
    assert _rpc(b.key, "printer.moved", {"printer_id": p["id"], "old_ip": "", "new_ip": ""}).status_code == 400


def test_printer_add_from_the_pi(store) -> None:
    r = _rpc(store.key, "printer.add", {"name": "Kitchen", "ip": "10.0.0.50", "mac": "aa:bb:cc:00:00:50"})
    assert r.status_code == 200, r.text
    reply = r.json()["payload"]
    assert reply["ok"] is True and reply["name"] == "Kitchen" and reply["ip"] == "10.0.0.50" and reply["id"]

    mgr = store.manager_session()
    printers = mgr.get(f"{STORE_V1}/me/printers", timeout=5).json()
    assert len(printers) == 1
    assert printers[0]["is_receipt"] is True  # the first printer is the receipt printer
    assert printers[0]["mac"] == "aa:bb:cc:00:00:50"

    for payload, code in [({"name": "kitchen", "ip": "10.0.0.51"}, "name_taken"),
                          ({"name": "", "ip": "10.0.0.51"}, "bad_request"),
                          ({"name": "Bar"}, "bad_request")]:
        r = _rpc(store.key, "printer.add", payload)
        assert r.status_code == 200, r.text
        assert r.json()["payload"] == {"ok": False, "code": code}, payload
    assert len(mgr.get(f"{STORE_V1}/me/printers", timeout=5).json()) == 1


def test_manager_ip_edit_is_logged(store) -> None:
    mgr = store.manager_session()
    p = _add_printer(mgr, "Wings", "10.0.0.60")
    assert mgr.patch(f"{STORE_V1}/me/printers/{p['id']}", json={"name": "Wings 2", "ip": "10.0.0.60"}, timeout=5).status_code == 200
    assert _ip_changes(mgr) == []  # a rename isn't an IP change
    assert mgr.patch(f"{STORE_V1}/me/printers/{p['id']}", json={"name": "Wings 2", "ip": "10.0.0.61"}, timeout=5).status_code == 200
    changes = _ip_changes(mgr)
    assert len(changes) == 1
    assert (changes[0]["old_ip"], changes[0]["new_ip"], changes[0]["source"], changes[0]["printer_name"]) == \
        ("10.0.0.60", "10.0.0.61", "manager", "Wings 2")
    assert requests.get(f"{STORE_V1}/me/printers/ip-changes", timeout=5).status_code == 401


def test_live_roster_passes_mac_and_found_through(store) -> None:
    report = {"reported_at": _now(), "agent_version": "2.0.6",
              "printers": [{"name": "Pizza", "ip": "10.0.0.9", "mac": "aa:bb:cc:00:00:09", "up": True}],
              "found": [{"ip": "10.0.0.77", "mac": "aa:bb:cc:00:00:77"}]}
    r = _rpc(store.key, "printer.report", report)
    assert r.status_code in (200, 202, 204), r.text
    body = store.manager_session().get(f"{STORE_V1}/me/printers/live", timeout=10).json()
    assert body["printers"][0]["mac"] == "aa:bb:cc:00:00:09"
    assert body["found"] == [{"ip": "10.0.0.77", "mac": "aa:bb:cc:00:00:77"}]


def test_live_roster_found_is_empty_for_old_agents(store) -> None:
    r = _rpc(store.key, "printer.report", {"reported_at": _now(), "agent_version": "2.0.5",
                                           "printers": [{"name": "Pizza", "ip": "10.0.0.9", "up": True}]})
    assert r.status_code in (200, 202, 204), r.text
    body = store.manager_session().get(f"{STORE_V1}/me/printers/live", timeout=10).json()
    assert body["found"] == []
