"""Golden chapter: what the Pi (agent) sees — the manager-login link, the
SSE handshake and command frames, every device-keyed /store/v1/agent/* route,
the tablet report key, and every RPC both ways (orders and geo live in
ch_orders / ch_geo)."""

from __future__ import annotations

import json
import threading
import time
import uuid
from typing import Any

import requests

from conftest import Store
from golden.recorder import Rec
from sse import SSEStream

T0 = "2026-11-02T15:00:00Z"
SQLITE = "application/vnd.sqlite3"


def run(rec: Rec, admin: requests.Session, _request) -> None:
    a = Store(admin)
    try:
        _run(rec, admin, a)
    finally:
        a.delete()


def _run(rec: Rec, admin: requests.Session, a: Store) -> None:
    rec.alias(a.tag, "tag")
    rec.alias(a.store_id, "store_id")
    user, pw = f"mgr{a.tag}", "golden-" + a.tag
    rec.alias(pw, "password")
    admin.patch(f"{rec.base}/admin/v1/tenants/{a.tenant_id}/credentials", json={"username": user, "password": pw}, timeout=5)
    mgr = requests.Session()
    mgr.post(f"{rec.base}/store/v1/session", json={"username": user, "password": pw}, timeout=5)
    mgr.post(f"{rec.base}/store/v1/me/printers", json={"name": "Pizza", "ip": "10.0.0.20"}, timeout=5)
    mgr.post(f"{rec.base}/store/v1/me/printers", json={"name": "Wings", "ip": "10.0.0.21"}, timeout=5)

    # The Pi links itself with the manager login.
    rec.call("link wrong password", "POST", "/store/v1/agent/link", json_body={"username": user, "password": "x", "agent_name": "pi"})
    key = rec.call("link", "POST", "/store/v1/agent/link",
                   json_body={"username": user, "password": pw, "agent_name": "golden-pi"}).json()["api_key"]
    h = {"X-STORE-API-KEY": key}

    def rpc(label: str, typ: str, payload: Any, msg_id: str | None = None) -> requests.Response:
        return rec.call(label, "POST", "/store/v1/agent/rpc", headers=h,
                        json_body={"type": typ, "id": msg_id or str(uuid.uuid4()), "payload": payload})

    rec.call("store-config", "GET", "/store/v1/store-config", headers=h)
    rec.call("restore (nothing yet)", "GET", "/store/v1/agent/restore", headers=h)
    rec.call("credential", "GET", "/store/v1/agent/credential", headers=h)
    rec.call("secrets", "GET", "/store/v1/agent/secrets", headers=h,
             mask=("pushover_app_token", "pushover_user_key", "pushover_boss_key", "pushover_admin_devices"))
    rec.call("backup bad", "POST", "/store/v1/agent/backup", headers={**h, "Content-Type": SQLITE}, data=b"not a database")
    rec.call("backup", "POST", "/store/v1/agent/backup", headers={**h, "Content-Type": SQLITE},
             data=b"SQLite format 3\x00" + bytes(1000))
    rec.call("app latest none", "GET", "/store/v1/agent/app/latest", headers=h, params={"app": "golden-none"})
    rec.call("app apk none", "GET", "/store/v1/agent/app/releases/1/apk", headers=h, params={"app": "golden-none"})

    # Tablet report tokens + a direct report + one relayed by the Pi.
    tok = rec.call("tablet token", "POST", "/store/v1/agent/tablet-tokens", headers=h,
                   json_body={"tablet_id": "tab-golden", "tablet_name": "Counter"}).json()["token"]
    rec.call("tablet token bad", "POST", "/store/v1/agent/tablet-tokens", headers=h, json_body={"tablet_id": ""})
    report = {"v": 1, "id": str(uuid.uuid4()), "created_at": T0, "kind": "guide", "scenario": "printer", "outcome": "fixed",
              "headline": "Kitchen printer was out of paper", "cause": "printer_paper", "fixed_by": "printer_paper", "note": "",
              "steps": [{"id": "paper", "at": T0, "answer": "yes", "result": "fixed"}],
              "checks": {"pi_port": {"status": "ok", "plain": "The Pi answers", "details": "200 in 12 ms"}},
              "tablet": {"name": "Counter", "device_id": "dev-1", "app_version": "2.0.3", "android_sdk": 34, "model": "golden",
                         "checks": {"notifications": True, "full_screen": True, "battery": True, "installs": True, "alarm_volume_pct": 80}},
              "pi": {"address": "192.168.1.219:80", "version": "2.4.2", "store": "golden", "last_diag": {}},
              "log": ["golden line 1"]}
    rq = {"X-Quibase-Report-Key": tok}
    rec.call("diag direct", "POST", "/store/v1/tablet/diag-reports", headers=rq, json_body=report)
    rec.call("diag direct duplicate", "POST", "/store/v1/tablet/diag-reports", headers=rq, json_body=report)
    rec.call("diag direct bad", "POST", "/store/v1/tablet/diag-reports", headers=rq, data="nope")
    rec.call("report key opens nothing else", "GET", "/api/v1/orders", headers={"X-STORE-API-KEY": tok})
    relayed = {**report, "id": str(uuid.uuid4()), "kind": "full_check", "scenario": None, "outcome": "ok", "headline": "All good"}
    pi_payload = {"tablet_id": "tab-pi", "tablet_name": "Counter (Pi)", "report": relayed,
                  "pi": {"version": "2.4.2", "health": {"sd_errors": 0, "under_voltage": False}}}
    rpc("diag via pi", "diag.report", pi_payload)
    rpc("diag via pi duplicate", "diag.report", pi_payload)
    rpc("diag via pi bad", "diag.report", {"tablet_id": "t", "report": {"v": 1, "id": "nope"}})
    reps = rec.call("admin diag list", "GET", "/admin/v1/diag-reports", session=admin, params={"tenant_id": a.tenant_id}).json()
    rec.call("admin diag get", "GET", f"/admin/v1/diag-reports/{reps[-1]['id']}", session=admin)
    rec.call("admin diag resolve", "PATCH", f"/admin/v1/diag-reports/{reps[-1]['id']}", session=admin, json_body={"resolved": True})
    rec.call("admin tablet tokens", "GET", "/admin/v1/tablet-tokens", session=admin, params={"location_id": a.location_id})
    rec.call("tablet token revoke", "DELETE", "/store/v1/agent/tablet-tokens/tab-golden", headers=h)
    rec.call("tablet token revoke unknown", "DELETE", "/store/v1/agent/tablet-tokens/tab-gone", headers=h)

    # Unsolicited reports.
    rpc("printer report", "printer.report", {"reported_at": T0, "agent_version": "2.4.2",
                                             "printers": [{"name": "Pizza", "ip": "10.0.0.20", "up": True},
                                                          {"name": "Wings", "ip": "10.0.0.21", "up": False}]})
    rpc("agent state", "agent.state", {"at": T0, "state": {"version": 1, "batch": {"id": 7, "opened_at": T0}, "kitchen_next": 1102,
                                                          "last_prep_minutes": 25,
                                                          "drivers": [{"id": "drv-1", "name": "Abdul", "position": 1, "status": "available"}],
                                                          "open_shifts": []}})
    rpc("agent health", "agent.health", {"at": T0, "device_id": "x", "version": "2.4.2", "disk_free_bytes": 123456789, "temp_c": 51.5})
    rec.call("admin agents health", "GET", "/admin/v1/agents/health", session=admin,
             filt=lambda rows: [r for r in rows if r.get("location_id") == a.location_id], mask=("last_seen_at", "age_seconds"))
    upd = str(uuid.uuid4())
    rpc("update status", "agent.update.status", {"id": upd, "action": "update", "requested_by": "pi-web", "force": False,
                                                 "state": "running", "from_version": "2.4.1", "to_version": "2.4.2",
                                                 "from_sha": "aa", "to_sha": "bb", "error": "", "started_at": T0, "finished_at": None})
    rpc("update status bad", "agent.update.status", {"id": "", "state": "ok"})
    rpc("unknown type", "some.future.thing", {"x": 1})
    rpc("bad payload", "agent.health", "not an object")
    rec.call("rpc not json", "POST", "/store/v1/agent/rpc", headers=h, data="nope")
    rec.call("reply nobody waits for", "POST", "/store/v1/agent/rpc", headers=h,
             json_body={"type": "ping.reply", "id": str(uuid.uuid4()), "payload": {"pong": True}})

    # The SSE stream: the handshake frame, then main → agent commands.
    with SSEStream(key) as s:
        first = s.next()
        rec.value("sse first frame event", first["event"])
        rec.value("sse store_config", first["data"])

        def ask(label: str, method: str, path: str, body: Any, cmd_type: str, reply: Any, *,
                session: requests.Session | None = None, params: dict | None = None, mask: tuple[str, ...] = ()) -> None:
            out: dict = {}

            def go() -> None:
                out["r"] = (session or admin).request(method, rec.base + path, json=body, params=params, timeout=20)

            t = threading.Thread(target=go)
            t.start()
            cmd = s.wait_event("command", lambda d: d.get("type") == cmd_type, timeout=10)
            rec.value(f"{label}: command frame", cmd)
            rpc(f"{label}: reply", f"{cmd_type}.reply", reply, msg_id=cmd["id"] if cmd else None)
            t.join(timeout=20)
            rec.record(f"{label}: answer", method, path, out["r"], params=params, mask=mask)

        scope = {"tenant_id": a.tenant_id, "location_id": a.location_id}
        ask("ping", "POST", "/admin/v1/agent/ping", scope, "ping", {"pong": True}, mask=("rtt_ms", "elapsed_ms"))
        ask("update", "POST", "/admin/v1/agent/update", {**scope, "force": True}, "agent.update",
            {"accepted": True, "state": "waiting_for_idle", "code": ""})
        ask("rollback", "POST", "/admin/v1/agent/rollback", scope, "agent.rollback",
            {"accepted": False, "state": "", "code": "no_previous"})
        ask("printers live", "GET", "/store/v1/me/printers/live", None, "printer.report.request",
            {"reported_at": T0, "agent_version": "2.4.2", "printers": [{"name": "Pizza", "ip": "10.0.0.20", "up": True}]},
            session=mgr, mask=("age_seconds",))
        rec.call("agent updates", "GET", "/admin/v1/agent-updates", session=admin, params={"location_id": a.location_id})

        # Printer follow + found printer: replies, then the re-pushed store_config.
        printers = {p["name"]: p for p in mgr.get(f"{rec.base}/store/v1/me/printers", timeout=5).json()}
        rpc("printer moved", "printer.moved", {"printer_id": printers["Pizza"]["id"], "old_ip": "10.0.0.20",
                                               "new_ip": "10.0.0.33", "mac": "AA:BB:CC:00:00:01"})
        rec.value("store_config after move", s.wait_event("store_config", lambda d: any(p["ip"] == "10.0.0.33" for p in d["printers"])))
        rpc("printer moved stale", "printer.moved", {"printer_id": printers["Pizza"]["id"], "old_ip": "10.0.0.20",
                                                     "new_ip": "10.0.0.34", "mac": "AA:BB:CC:00:00:01"})
        rpc("printer add", "printer.add", {"name": "Bar", "ip": "10.0.0.40", "mac": "AA:BB:CC:00:00:02"})
        rec.value("store_config after add", s.wait_event("store_config", lambda d: any(p["name"] == "Bar" for p in d["printers"])))
        rpc("printer add dup", "printer.add", {"name": "Bar", "ip": "10.0.0.41", "mac": ""})
        rec.call("ip changes", "GET", "/store/v1/me/printers/ip-changes", session=mgr, params={"limit": 20})

        # A manager edit pushes store_config live.
        bar = [p for p in mgr.get(f"{rec.base}/store/v1/me/printers", timeout=5).json() if p["name"] == "Bar"][0]
        mgr.patch(f"{rec.base}/store/v1/me/printers/{bar['id']}", json={"name": "Bar 2", "ip": "10.0.0.42"}, timeout=5)
        rec.value("store_config after manager edit", s.wait_event("store_config", lambda d: any(p["name"] == "Bar 2" for p in d["printers"])))

        # Revoke: the key dies and the stream closes.
        rec.call("revoke", "POST", "/store/v1/agent/revoke", headers=h)
        rec.call("revoked key", "GET", "/store/v1/store-config", headers=h)
        rec.value("stream closed", json.dumps(_closed(s)))


def _closed(s: SSEStream) -> bool:
    deadline = time.time() + 5
    while time.time() < deadline:
        if not s.alive():
            return True
        time.sleep(0.1)
    return False
