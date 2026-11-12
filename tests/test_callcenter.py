"""The call center's remote terminal (/callcenter/v1, contract/main.md §2.5zh,
CALLCENTER-PLAN.md). main signs the browser in with the store's login (a
long-lived device token), the agent with their 4-digit PIN (role callcenter
only), relays the allowed terminal calls to the store's Pi over the duplex
channel (RPC terminal.relay) and relays the Pi's terminal events back.

The test plays the Pi: it holds the store's stream, answers terminal.relay
commands and posts terminal.events reports.
"""

from __future__ import annotations

import json
import secrets
import threading
import uuid
from datetime import datetime
from queue import Empty, Queue
from zoneinfo import ZoneInfo

import requests

from conftest import ADMIN_V1, API_V1, MAIN_BASE_URL, STORE_V1, store_key, wait_until
from sse import SSEStream
from test_counter_orders import _placed, _send, _snapshot, _uuid7

CC = f"{MAIN_BASE_URL}/callcenter/v1"
NY = ZoneInfo("America/New_York")


# --- helpers ---------------------------------------------------------------------


def _login(store, name: str = "Desk 1") -> tuple[dict, dict]:
    """The store's login → (Bearer headers, the login answer)."""
    username, password = f"cc{store.tag}", secrets.token_urlsafe(10)
    r = store.admin.patch(f"{ADMIN_V1}/tenants/{store.tenant_id}/credentials",
                          json={"username": username, "password": password}, timeout=5)
    assert r.status_code == 204, r.text
    store.cc_login = (username, password)
    r = requests.post(f"{CC}/login", json={"username": username, "password": password, "device_name": name}, timeout=5)
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["device_token"].startswith("qccd_") and len(body["device_token"]) == 5 + 64
    return {"Authorization": f"Bearer {body['device_token']}"}, body


def _person(mgr, role: str, pin: str, first: str = "Cara", last: str = "Smith") -> dict:
    r = mgr.post(f"{STORE_V1}/me/staff", json={"first_name": first, "last_name": last, "role": role, "pin": pin}, timeout=5)
    assert r.status_code == 201, r.text
    return r.json()


def _pin(hdr: dict, pin: str) -> requests.Response:
    return requests.post(f"{CC}/session", headers=hdr, json={"pin": pin}, timeout=5)


class FakePi:
    """Holds the store's stream and answers terminal.relay like a Pi would:
    `answer(payload) -> (status, headers, body)`; every relay payload is kept."""

    def __init__(self, store, answer=None) -> None:
        self.key = store.key
        self.stream = SSEStream(self.key)
        self.seen: list[dict] = []
        self.wants: Queue = Queue()
        self.answer = answer or (lambda p: (200, {}, {"ok": True, "path": p["path"]}))
        self._stop = threading.Event()
        self._t = threading.Thread(target=self._run, daemon=True)
        self._t.start()

    def _run(self) -> None:
        while not self._stop.is_set():
            try:
                f = self.stream.frames.get(timeout=0.2)
            except Empty:
                continue
            if f["event"] != "command":
                continue
            env = json.loads(f["data"])
            if env["type"] == "terminal.events.want":
                self.wants.put(env["payload"])
            if env["type"] != "terminal.relay":
                continue
            self.seen.append(env["payload"])
            out = self.answer(env["payload"])
            if out is None:
                continue  # play a Pi that never answers
            if isinstance(out, dict):  # a raw reply payload (an odd Pi)
                payload = out
            else:
                status, headers, body = out
                payload = {"status": status, "headers": headers, "body": body}
            requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(self.key),
                          json={"type": "terminal.relay.reply", "id": env["id"], "payload": payload}, timeout=5)

    def report(self, events: list[dict]) -> None:
        r = requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(self.key),
                          json={"type": "terminal.events", "id": str(uuid.uuid4()), "payload": {"events": events}}, timeout=5)
        assert r.status_code in (200, 204), r.text

    def close(self) -> None:
        self._stop.set()
        self._t.join(timeout=2)
        self.stream.close()

    def __enter__(self) -> "FakePi":
        return self

    def __exit__(self, *_exc) -> None:
        self.close()


class CCEvents:
    """The browser's /callcenter/v1/events stream."""

    def __init__(self, hdr: dict) -> None:
        self.resp = requests.get(f"{CC}/events", headers={**hdr, "Accept": "text/event-stream"}, stream=True, timeout=60)
        assert self.resp.status_code == 200, self.resp.text
        self.frames: Queue = Queue()
        self._done = threading.Event()
        threading.Thread(target=self._read, daemon=True).start()

    def _read(self) -> None:
        event, data = "", []
        try:
            for raw in self.resp.iter_lines(decode_unicode=True):
                if raw == "":
                    if event or data:
                        self.frames.put((event, json.loads("\n".join(data) or "null")))
                    event, data = "", []
                elif raw.startswith("event:"):
                    event = raw[6:].strip()
                elif raw.startswith("data:"):
                    data.append(raw[5:].strip())
        except (requests.RequestException, AttributeError, ValueError):
            pass
        finally:
            self._done.set()

    def wait(self, event: str, predicate=lambda d: True, timeout: float = 15.0):
        end = datetime.now().timestamp() + timeout
        while datetime.now().timestamp() < end:
            try:
                name, data = self.frames.get(timeout=0.3)
            except Empty:
                continue
            if name == event and predicate(data):
                return data
        return None

    def ended(self) -> bool:
        return self._done.is_set()

    def close(self) -> None:
        import socket  # noqa: PLC0415
        sock = getattr(getattr(self.resp.raw, "_connection", None), "sock", None)
        if sock is not None:
            try:
                sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
        self.resp.close()


def _agent(store, pin: str = "4321") -> tuple[dict, dict, requests.Session]:
    """A signed-in browser with an agent on it: (headers, the staff row, the manager session)."""
    mgr = store.manager_session()
    person = _person(mgr, "callcenter", pin)
    hdr, _ = _login(store)
    r = _pin(hdr, pin)
    assert r.status_code == 200, r.text
    return hdr, person, mgr


# --- store login, Me, the token's reach ----------------------------------------------


def test_login_me_and_logout(store) -> None:
    hdr, body = _login(store, name="  Elevate   desk 2 ")
    assert body["device"]["name"] == "Elevate desk 2"
    assert body["store"]["location_id"] == store.location_id and body["store"]["name"] == store.name

    r = requests.post(f"{CC}/login", json={"username": store.cc_login[0], "password": "nope"}, timeout=5)
    assert r.status_code == 401 and r.json()["code"] == "invalid_credentials", r.text
    assert "set-cookie" not in {k.lower() for k in r.headers}
    assert requests.post(f"{CC}/login", json={"username": ""}, timeout=5).status_code == 400

    r = requests.get(f"{CC}/me", headers=hdr, timeout=5)
    assert r.status_code == 200, r.text
    me = r.json()
    assert me["store"]["tenant_id"] == store.tenant_id and me["store"]["timezone"]
    assert me["open"] is True and me["closed_reason"] == "" and me["next_open_at"] is None  # no hours: always open
    assert me["pi"] == {"online": False}
    assert me["settings"] == {"idle_lock_minutes": 15}
    assert me["square"] is None  # not connected: no card entry
    assert me["device"]["name"] == "Elevate desk 2" and me["session"] is None
    assert isinstance(me["hours"], list) and len(me["hours"]) == 7
    # the login sets no cookie and no CORS header: the web app's server holds the token
    assert "set-cookie" not in {k.lower() for k in r.headers}
    assert "access-control-allow-origin" not in {k.lower() for k in r.headers}

    for bad in ({}, {"Authorization": "Bearer qccd_" + "0" * 64}, {"Authorization": "Bearer nope"}):
        r = requests.get(f"{CC}/me", headers=bad, timeout=5)
        assert r.status_code == 401 and r.json()["code"] == "device_required", r.text

    with store.online():
        assert wait_until(lambda: requests.get(f"{CC}/me", headers=hdr, timeout=5).json()["pi"]["online"], timeout=5)

    assert requests.post(f"{CC}/logout", headers=hdr, timeout=5).status_code == 204
    r = requests.get(f"{CC}/me", headers=hdr, timeout=5)
    assert r.status_code == 401 and r.json()["code"] == "device_required"


def test_idle_lock_minutes_is_a_store_setting(store) -> None:
    mgr = store.manager_session()
    hdr, _ = _login(store)
    r = mgr.put(f"{STORE_V1}/me/terminal/settings", json={"callcenter_idle_lock_minutes": 7}, timeout=5)
    assert r.status_code == 200 and r.json()["callcenter_idle_lock_minutes"] == 7, r.text
    assert requests.get(f"{CC}/me", headers=hdr, timeout=5).json()["settings"] == {"idle_lock_minutes": 7}
    for bad in (-1, 241):
        r = mgr.put(f"{STORE_V1}/me/terminal/settings", json={"callcenter_idle_lock_minutes": bad}, timeout=5)
        assert r.status_code == 400, (bad, r.text)


def test_device_token_opens_nothing_else(store) -> None:
    """A qccd_ token is the call center's only: no Store page, admin, device or
    agent route takes it."""
    hdr, _ = _login(store)
    tok = hdr["Authorization"].removeprefix("Bearer ")
    for method, url in (("GET", f"{STORE_V1}/me/staff"), ("GET", f"{STORE_V1}/me/terminal/settings"),
                        ("GET", f"{ADMIN_V1}/tenants"), ("GET", f"{API_V1}/events/orders"),
                        ("POST", f"{STORE_V1}/agent/rpc"), ("GET", f"{STORE_V1}/agent/terminal-config"),
                        ("GET", f"{STORE_V1}/me/callcenter/devices")):
        for h in (hdr, {"X-API-Key": tok}, {"Authorization": f"Bearer {tok}", "X-API-Key": tok}):
            r = requests.request(method, url, headers=h, json={}, timeout=5)
            assert r.status_code in (401, 403), (method, url, r.status_code, r.text)
        r = requests.request(method, url, cookies={"quicc_store_session": tok}, json={}, timeout=5)
        assert r.status_code in (401, 403), (method, url, r.status_code)


# --- the staff role + the PIN session --------------------------------------------------


def test_callcenter_role_pin_rules_and_the_pis_snapshot(store) -> None:
    mgr = store.manager_session()
    for bad in ("12", "123", "12345", "12a4"):
        r = mgr.post(f"{STORE_V1}/me/staff", json={"first_name": "Cara", "role": "callcenter", "pin": bad}, timeout=5)
        assert r.status_code == 400 and r.json()["code"] == "bad_pin", (bad, r.text)
    p = _person(mgr, "callcenter", "4321")
    assert p["role"] == "callcenter"
    assert p["effective_limits"] == {"refund_cents": 0, "discount_pct": 0}
    cashier = _person(mgr, "cashier", "77", first="Sam", last="")

    # A 3-digit PIN can't be reset onto an agent; a cashier becoming one needs a new 4-digit PIN.
    r = mgr.post(f"{STORE_V1}/me/staff/{p['id']}/pin", json={"pin": "123"}, timeout=5)
    assert r.status_code == 400 and r.json()["code"] == "bad_pin", r.text
    r = mgr.patch(f"{STORE_V1}/me/staff/{cashier['id']}", json={"role": "callcenter"}, timeout=5)
    assert r.status_code == 400 and r.json()["code"] == "pin_required", r.text
    r = mgr.patch(f"{STORE_V1}/me/staff/{cashier['id']}", json={"role": "callcenter", "pin": "88"}, timeout=5)
    assert r.status_code == 400 and r.json()["code"] == "bad_pin", r.text
    r = mgr.patch(f"{STORE_V1}/me/staff/{cashier['id']}", json={"role": "callcenter", "pin": "8899"}, timeout=5)
    assert r.status_code == 200 and r.json()["role"] == "callcenter", r.text
    # PINs stay unique across every role.
    r = mgr.post(f"{STORE_V1}/me/staff", json={"first_name": "Dee", "role": "cashier", "pin": "4321"}, timeout=5)
    assert r.status_code == 409 and r.json()["code"] == "pin_taken", r.text

    # The Pi's snapshot carries the agents, never their PIN hash (main checks that PIN; no Pi can log one in).
    r = requests.get(f"{STORE_V1}/agent/terminal-config", headers=store_key(store.key), timeout=5)
    assert r.status_code == 200, r.text
    cfg = r.json()
    agents = [s for s in cfg["staff"] if s["role"] == "callcenter"]
    assert {s["id"] for s in agents} == {p["id"], cashier["id"]}
    assert all(s["pin_hash"] == "" and s["active"] for s in agents)
    assert cfg["settings"]["callcenter_idle_lock_minutes"] == 15


def test_pin_session(store) -> None:
    mgr = store.manager_session()
    agent = _person(mgr, "callcenter", "4321")
    _person(mgr, "cashier", "1111", first="Sam")
    _person(mgr, "manager", "2222", first="Kay")
    hdr, _ = _login(store)

    assert requests.get(f"{CC}/session", headers=hdr, timeout=5).json()["staff"] is None
    r = requests.put(f"{CC}/session", headers=hdr, timeout=5)
    assert r.status_code == 401 and r.json()["code"] == "login_required", r.text
    r = requests.get(f"{CC}/pi/pickups", headers=hdr, timeout=5)
    assert r.status_code == 401 and r.json()["code"] == "login_required", r.text

    for bad in ("", "12", "12345", "abcd"):
        r = _pin(hdr, bad)
        assert r.status_code == 400 and r.json()["code"] == "bad_pin", (bad, r.text)
    r = _pin(hdr, "9999")
    assert r.status_code == 401 and r.json()["code"] == "wrong_pin", r.text
    # A cashier's or a manager's PIN is refused exactly like a wrong one: the
    # pad never tells which store PINs exist.
    for other in ("1111", "2222"):
        r2 = _pin(hdr, other)
        assert (r2.status_code, r2.json()) == (r.status_code, r.json()), r2.text

    r = _pin(hdr, "4321")
    assert r.status_code == 200, r.text
    s = r.json()
    assert s["staff"] == {"id": agent["id"], "first_name": "Cara", "last_name": "Smith", "name": "Cara S.", "role": "callcenter"}
    assert s["role"] == "callcenter" and s["limits"] == {"refund_cents": 0, "discount_pct": 0}
    assert s["settings"]["idle_lock_minutes"] == 15 and s["expires_at"]

    got = requests.get(f"{CC}/session", headers=hdr, timeout=5).json()
    assert got["staff"]["id"] == agent["id"] and got["expires_at"]
    me = requests.get(f"{CC}/me", headers=hdr, timeout=5).json()
    assert me["session"]["staff"]["name"] == "Cara S."
    assert requests.put(f"{CC}/session", headers=hdr, timeout=5).status_code == 204

    # The manager sees the browser and who is on it.
    devs = mgr.get(f"{STORE_V1}/me/callcenter/devices", timeout=5).json()
    assert [(d["name"], d.get("staff_name")) for d in devs] == [("Desk 1", "Cara S.")]

    # Lock; then switched off = the session ends by itself.
    assert requests.delete(f"{CC}/session", headers=hdr, timeout=5).status_code == 204
    assert requests.get(f"{CC}/session", headers=hdr, timeout=5).json()["staff"] is None
    assert _pin(hdr, "4321").status_code == 200
    ev = CCEvents(hdr)
    try:
        assert ev.wait("hello") is not None
        r = mgr.patch(f"{STORE_V1}/me/staff/{agent['id']}", json={"active": False}, timeout=5)
        assert r.status_code == 200, r.text
        assert ev.wait("session.ended") == {"reason": "staff_changed"}
    finally:
        ev.close()
    assert requests.get(f"{CC}/session", headers=hdr, timeout=5).json()["staff"] is None
    r = _pin(hdr, "4321")
    assert r.status_code == 401 and r.json()["code"] == "wrong_pin", r.text


def test_wrong_pins_lock_the_pad_then_revoke_the_device(store) -> None:
    mgr = store.manager_session()
    _person(mgr, "callcenter", "4321")
    hdr, _ = _login(store)
    for _ in range(4):
        assert _pin(hdr, "0000").status_code == 401
    assert _pin(hdr, "0000").status_code == 401  # the 5th: the pad locks after it
    r = _pin(hdr, "4321")  # even the right PIN waits
    assert r.status_code == 429 and r.json()["code"] == "too_many_attempts", r.text
    assert 1 <= int(r.headers["Retry-After"]) <= 30

    # Another browser of the store isn't locked by it.
    hdr2 = {"Authorization": "Bearer " + requests.post(f"{CC}/login", json={
        "username": store.cc_login[0], "password": store.cc_login[1]}, timeout=5).json()["device_token"]}
    assert _pin(hdr2, "4321").status_code == 200

    # The manager revokes the locked browser: its token is gone.
    devs = mgr.get(f"{STORE_V1}/me/callcenter/devices", timeout=5).json()
    locked = next(d for d in devs if "staff_id" not in d)
    assert mgr.delete(f"{STORE_V1}/me/callcenter/devices/{locked['id']}", timeout=5).status_code == 204
    assert mgr.delete(f"{STORE_V1}/me/callcenter/devices/{locked['id']}", timeout=5).status_code == 404
    r = requests.get(f"{CC}/me", headers=hdr, timeout=5)
    assert r.status_code == 401 and r.json()["code"] == "device_required", r.text
    listed = {d["id"]: d for d in mgr.get(f"{STORE_V1}/me/callcenter/devices", timeout=5).json()}
    assert listed[locked["id"]].get("revoked_at")


def test_another_stores_manager_cannot_touch_the_device(store, make_store) -> None:
    hdr, body = _login(store)
    other = make_store().manager_session()
    assert other.get(f"{STORE_V1}/me/callcenter/devices", timeout=5).json() == []
    assert other.delete(f"{STORE_V1}/me/callcenter/devices/{body['device']['id']}", timeout=5).status_code == 404
    assert requests.get(f"{CC}/me", headers=hdr, timeout=5).status_code == 200


# --- the relay -------------------------------------------------------------------------


def test_relay_passes_the_call_and_the_pis_answer_through(store) -> None:
    hdr, person, _ = _agent(store)

    def answer(p: dict):
        if p["path"] == "menu":
            if p["headers"].get("If-None-Match") == '"m7"':
                return 304, {"ETag": '"m7"'}, None
            return 200, {"ETag": '"m7"'}, {"menu_version": 7, "counter_menu": {"big": "x" * 5000}}
        if p["path"] == "orders" and p["method"] == "POST":
            return 201, {}, {"order": {"id": "o1"}, "number": 12, "duplicate": False, "echo": p["body"]}
        if p["path"].endswith("/void"):
            return 409, {}, {"error": "paid", "code": "refund_first", "paid_cents": 500}
        if p["path"] == "customers":
            return 429, {"Retry-After": "7"}, {"error": "slow down", "code": "too_many_attempts"}
        if p["method"] == "DELETE":
            return 204, {}, None
        return 200, {}, {"orders": [], "query": p["query"]}

    with FakePi(store, answer) as pi:
        assert wait_until(lambda: requests.get(f"{CC}/me", headers=hdr, timeout=5).json()["pi"]["online"], timeout=5)

        r = requests.get(f"{CC}/pi/orders?open=1&x=a%20b", headers=hdr, timeout=10)
        assert r.status_code == 200 and r.json() == {"orders": [], "query": "open=1&x=a%20b"}, r.text
        sent = pi.seen[-1]
        me = requests.get(f"{CC}/me", headers=hdr, timeout=5).json()
        assert sent["device"] == me["device"]["id"] and sent["device_name"] == "Desk 1"
        assert sent["staff_id"] == person["id"]
        assert (sent["method"], sent["path"], sent["body"]) == ("GET", "orders", None)

        # A body rides as sent; the Pi's status and extra fields come back as they are.
        order = {"client_key": "cc-0001-abcd", "type": "delivery", "cart": {"lines": [{"item_id": "i1", "qty": 2}], "code": "SAVE5"},
                 "customer": {"name": "Sam", "phone": "4105550100", "address": {"line1": "1 Main St", "city": "Towson"}},
                 "payments": [{"tender": "manual_card", "amount_cents": 1500, "tip_cents": 200, "ref": "sq1", "last4": "1111"}]}
        r = requests.post(f"{CC}/pi/orders", headers=hdr, json=order, timeout=10)
        assert r.status_code == 201 and r.json()["echo"] == order and r.json()["number"] == 12, r.text
        r = requests.post(f"{CC}/pi/orders/o1/void", headers=hdr, json={"reason": "Customer cancelled"}, timeout=10)
        assert r.status_code == 409 and r.json() == {"error": "paid", "code": "refund_first", "paid_cents": 500}, r.text
        r = requests.get(f"{CC}/pi/customers?phone=410", headers=hdr, timeout=10)
        assert r.status_code == 429 and r.headers["Retry-After"] == "7", r.text
        r = requests.delete(f"{CC}/pi/drafts/d-1", headers=hdr, timeout=10)
        assert r.status_code == 204 and r.content == b""

        # The menu: uploaded once per version; main serves its copy on the Pi's 304.
        r = requests.get(f"{CC}/pi/menu", headers=hdr, timeout=10)
        assert r.status_code == 200 and r.headers["ETag"] == '"m7"' and r.json()["menu_version"] == 7, r.text
        assert r.headers.get("Content-Encoding") == "gzip"
        first = [p for p in pi.seen if p["path"] == "menu"]
        assert first[-1]["headers"] == {}
        r = requests.get(f"{CC}/pi/menu", headers=hdr, timeout=10)
        assert r.status_code == 200 and r.json()["counter_menu"]["big"] == "x" * 5000, r.text
        assert [p for p in pi.seen if p["path"] == "menu"][-1]["headers"] == {"If-None-Match": '"m7"'}
        r = requests.get(f"{CC}/pi/menu", headers={**hdr, "If-None-Match": '"m7"'}, timeout=10)
        assert r.status_code == 304 and r.content == b""


def test_relay_allowlist(store) -> None:
    hdr, _, _ = _agent(store)
    allowed = [("GET", "menu"), ("GET", "status"), ("GET", "session"), ("POST", "quote"), ("GET", "orders"),
               ("GET", "orders/abc"), ("PUT", "orders/abc/cart"), ("POST", "orders/abc/payments"),
               ("POST", "orders/abc/void"), ("POST", "orders/abc/time"), ("POST", "orders/abc/reprint"),
               ("GET", "pickups"), ("GET", "checks"), ("GET", "checks/abc"), ("GET", "drivers"), ("GET", "floor"),
               ("GET", "drafts"), ("PUT", "drafts/d1"), ("DELETE", "drafts/d1"), ("GET", "customers"),
               ("POST", "customers"), ("PATCH", "customers/c1"), ("POST", "customers/c1/addresses"),
               ("GET", "customers/c1/orders"), ("GET", "addresses"), ("POST", "journal")]
    refused = [("POST", "login"), ("POST", "logout"), ("POST", "manager-check"), ("POST", "refunds"),
               ("POST", "orders/abc/waste"), ("GET", "unsettled"), ("POST", "orders/abc/picked-up"),
               ("GET", "drawer"), ("GET", "drawers"), ("POST", "drawer/open"), ("POST", "drawer/pay-out"),
               ("POST", "drawer/close"), ("GET", "z"), ("GET", "reports/till"), ("GET", "reports/batches"),
               ("POST", "reports/drivers/print"), ("POST", "orders/abc/payments/p1/tip"), ("POST", "close"),
               ("POST", "deliveries/send-out"), ("POST", "drivers/d1/returned"), ("GET", "setup"), ("POST", "print"),
               ("GET", "square/session"), ("POST", "square/test-refund"), ("GET", "app/latest"), ("GET", "app/apk"),
               ("GET", "journal"), ("GET", "driver/mine"), ("POST", "driver/claim"), ("GET", "phones"),
               ("POST", "phone-charges"), ("POST", "orders/abc/phone-charges"),
               # an allowed path with another method, and paths that try to leave /api/terminal/
               ("DELETE", "orders/abc"), ("POST", "pickups"), ("PUT", "customers/c1"), ("GET", "orders/abc/cart"),
               ("GET", "../devices"), ("GET", "orders/../../devices"), ("GET", "orders/a%2Fb"), ("GET", "menu/"),
               ("GET", "orders//"), ("POST", "floor")]
    with FakePi(store) as pi:
        assert wait_until(lambda: requests.get(f"{CC}/me", headers=hdr, timeout=5).json()["pi"]["online"], timeout=5)
        for method, path in allowed:
            body = {"client_key": "k-12345678", "type": "pickup"} if method != "GET" else None
            r = requests.request(method, f"{CC}/pi/{path}", headers=hdr, json=body, timeout=10)
            assert r.status_code == 200 and r.json()["path"] == path, (method, path, r.status_code, r.text)
        n = len(pi.seen)
        for method, path in refused:
            req = requests.Request(method, f"{CC}/pi/{path}", headers=hdr, json={} if method != "GET" else None).prepare()
            req.url = f"{CC}/pi/{path}"  # as written: requests would fold the dots away
            r = requests.Session().send(req, timeout=10)
            if path in ("menu/", "orders//"):  # trailing slashes: the same route
                assert r.status_code == 200, r.text
                n += 1
                continue
            assert r.status_code in (403, 404), (method, path, r.status_code, r.text)
            if r.status_code == 403:
                assert r.json()["code"] == "not_allowed_for_callcenter" and r.json()["what"] == "route", (method, path, r.text)
        assert len(pi.seen) == n, "a refused call reached the Pi"


def test_relay_refuses_what_the_call_center_may_not_carry(store) -> None:
    hdr, _, _ = _agent(store)
    place = {"client_key": "cc-0002-abcd", "type": "pickup", "cart": {"lines": [{"item_id": "i1", "qty": 1}]}}
    cases = [
        ("POST", "orders", {**place, "type": "counter"}, 403, "type"),
        ("POST", "orders", {k: v for k, v in place.items() if k != "type"}, 403, "type"),
        ("POST", "orders", {**place, "payments": [{"tender": "cash", "amount_cents": 500}]}, 403, "tender"),
        ("POST", "orders", {**place, "payments": [{"tender": "comp", "amount_cents": 500}]}, 403, "comp"),
        ("POST", "orders", {**place, "manager_pin": "9876"}, 403, "manager"),
        ("POST", "orders", {**place, "approvals": ["a1"]}, 403, "manager"),
        ("POST", "orders", {**place, "cart": {"lines": [], "discount": {"pct": 10, "reason": "x"}}}, 403, "discount"),
        ("POST", "orders", {**place, "cart": {"lines": [{"custom": True, "name": "Thing", "price_cents": 500, "qty": 1}]}}, 403, "open_price"),
        ("POST", "orders", {**place, "cart": {"lines": [{"item_id": "i1", "qty": 1, "price_cents": 100}]}}, 403, "price_change"),
        ("POST", "quote", {"type": "pickup", "cart": {"lines": [], "discount": {"amount_cents": 100, "reason": "x"}}}, 403, "discount"),
        ("PUT", "orders/o1/cart", {"client_key": "c-12345678", "cart": {"lines": [], "discount": {"pct": 5, "reason": "x"}}}, 403, "discount"),
        ("PUT", "orders/o1/cart", {"client_key": "c-12345678", "cart": {"lines": []}, "manager_pin": "1"}, 403, "manager"),
        ("POST", "orders/o1/payments", {"tender": "cash", "amount_cents": 500}, 403, "tender"),
        ("POST", "orders/o1/payments", {"tender": "card", "amount_cents": 500}, 403, "tender"),
        ("POST", "orders/o1/payments", {"tender": "manual_card", "amount_cents": 500, "refund": True, "reason": "x"}, 403, "refund"),
        ("POST", "orders/o1/void", {"reason": "x", "manager_pin": "9876"}, 403, "manager"),
    ]
    with FakePi(store) as pi:
        assert wait_until(lambda: requests.get(f"{CC}/me", headers=hdr, timeout=5).json()["pi"]["online"], timeout=5)
        for method, path, body, status, what in cases:
            r = requests.request(method, f"{CC}/pi/{path}", headers=hdr, json=body, timeout=10)
            assert r.status_code == status and r.json()["code"] == "not_allowed_for_callcenter" and r.json()["what"] == what, \
                (path, body, r.status_code, r.text)
        # No order without its key, no charge without its key: a retry must be safe.
        r = requests.post(f"{CC}/pi/orders", headers=hdr, json={k: v for k, v in place.items() if k != "client_key"}, timeout=10)
        assert r.status_code == 400 and r.json()["code"] == "client_key_required", r.text
        r = requests.post(f"{CC}/pi/square/keyed-payments", headers=hdr, json={"nonce": "cnon:ok", "amount_cents": 500}, timeout=10)
        assert r.status_code == 400 and r.json()["code"] == "idempotency_key_required", r.text
        r = requests.post(f"{CC}/pi/quote", headers=hdr, data="not json", timeout=10)
        assert r.status_code == 400, r.text
        assert pi.seen == [], "a refused call reached the Pi"

        # What is allowed: a typed card, a coupon code, lines an order already holds on a change, a quote of them.
        ok = [
            ("POST", "orders", {**place, "cart": {"lines": [{"item_id": "i1", "qty": 1}], "code": "SAVE5"},
                                "payments": [{"tender": "manual_card", "amount_cents": 500, "ref": "sq"}]}),
            ("POST", "orders/o1/payments", {"tender": "manual_card", "amount_cents": 500, "tip_cents": 100, "client_key": "p-1"}),
            ("PUT", "orders/o1/cart", {"client_key": "c-12345678", "cart": {"lines": [
                {"custom": True, "name": "Kept", "price_cents": 500, "qty": 1}, {"item_id": "i1", "qty": 1, "price_cents": 900}]}}),
            ("POST", "quote", {"type": "delivery", "cart": {"lines": [{"custom": True, "name": "Kept", "price_cents": 500, "qty": 1}]}}),
            ("POST", "square/keyed-payments", {"nonce": "cnon:ok", "amount_cents": 500, "idempotency_key": "k1"}),
            ("POST", "orders/o1/void", {"reason": "Customer cancelled"}),
        ]
        for method, path, body in ok:
            r = requests.request(method, f"{CC}/pi/{path}", headers=hdr, json=body, timeout=10)
            assert r.status_code == 200, (path, r.status_code, r.text)
        assert len(pi.seen) == len(ok)

    big = {"client_key": "c-12345678", "cart": {"lines": []}, "note": "x" * (300 * 1024)}
    r = requests.put(f"{CC}/pi/orders/o1/cart", headers=hdr, json=big, timeout=10)
    assert r.status_code == 413, r.text


def test_store_offline(store) -> None:
    """No Pi stream, or a reply that isn't a relay answer (an older Pi): 503
    store_offline. A Pi that is busy: 503 store_busy."""
    hdr, _, _ = _agent(store)
    r = requests.get(f"{CC}/pi/pickups", headers=hdr, timeout=10)
    assert r.status_code == 503 and r.json()["code"] == "store_offline", r.text
    r = requests.post(f"{CC}/pi/orders", headers=hdr, json={"client_key": "cc-0003-abcd", "type": "pickup", "cart": {"lines": []}}, timeout=10)
    assert r.status_code == 503 and r.json()["code"] == "store_offline", r.text
    # Lists of main's own still answer.
    assert requests.get(f"{CC}/me", headers=hdr, timeout=5).json()["pi"] == {"online": False}

    def odd(p: dict):
        if p["path"] == "pickups":
            return 503, {}, {"error": "busy", "code": "busy"}
        if p["path"] == "checks":
            return {"ok": False, "code": "main_unsupported"}  # not a relay answer
        return 503, {}, {"error": "printers", "code": "printing_unavailable"}

    with FakePi(store, odd):
        assert wait_until(lambda: requests.get(f"{CC}/me", headers=hdr, timeout=5).json()["pi"]["online"], timeout=5)
        r = requests.get(f"{CC}/pi/checks", headers=hdr, timeout=10)
        assert r.status_code == 503 and r.json()["code"] == "store_offline", r.text
        # A Pi whose relay is full for a moment isn't offline: its own code, and "try again in a second".
        r = requests.get(f"{CC}/pi/pickups", headers=hdr, timeout=10)
        assert r.status_code == 503 and r.json()["code"] == "store_busy" and r.headers["Retry-After"] == "1", r.text
        # The Pi's own 503s pass through as they are.
        r = requests.post(f"{CC}/pi/orders/o1/reprint", headers=hdr, timeout=10)
        assert r.status_code == 503 and r.json()["code"] == "printing_unavailable", r.text


def test_store_closed(store) -> None:
    hdr, _, mgr = _agent(store)
    today = datetime.now(NY).date().isoformat()
    r = mgr.post(f"{STORE_V1}/me/hours-exceptions", json={"date": today, "closed": True, "note": "Closed today"}, timeout=5)
    assert r.status_code in (200, 201), r.text
    me = requests.get(f"{CC}/me", headers=hdr, timeout=5).json()
    assert me["open"] is False and me["closed_reason"] == "hours", me

    with FakePi(store) as pi:
        assert wait_until(lambda: requests.get(f"{CC}/me", headers=hdr, timeout=5).json()["pi"]["online"], timeout=5)
        for method, path, body in (
            ("POST", "orders", {"client_key": "cc-0004-abcd", "type": "pickup", "cart": {"lines": []}}),
            ("PUT", "orders/o1/cart", {"client_key": "c-12345678", "cart": {"lines": []}}),
            ("POST", "square/keyed-payments", {"nonce": "cnon:ok", "amount_cents": 500, "idempotency_key": "k1"}),
        ):
            r = requests.request(method, f"{CC}/pi/{path}", headers=hdr, json=body, timeout=10)
            assert r.status_code == 409 and r.json()["code"] == "store_closed" and r.json()["open"] is False, (path, r.text)
        assert pi.seen == [], "a closed store's order reached the Pi"
        # Lists, quotes, customers, void, reprint, order time stay.
        for method, path in (("GET", "pickups"), ("GET", "checks"), ("POST", "quote"), ("GET", "customers"),
                             ("POST", "orders/o1/void"), ("POST", "orders/o1/reprint"), ("POST", "orders/o1/time")):
            r = requests.request(method, f"{CC}/pi/{path}", headers=hdr, json={"reason": "x"} if method == "POST" else None, timeout=10)
            assert r.status_code == 200, (path, r.text)

    # A pause (the website's) doesn't close the call center: the app shows the note.
    exc = r = mgr.get(f"{STORE_V1}/me/ordering", timeout=5).json()
    for e in exc.get("exceptions", []):
        assert mgr.delete(f"{STORE_V1}/me/hours-exceptions/{e['id']}", timeout=5).status_code == 204
    assert mgr.post(f"{STORE_V1}/me/pause", json={"preset": "indefinite", "note": "Slammed"}, timeout=5).status_code == 200
    me = requests.get(f"{CC}/me", headers=hdr, timeout=5).json()
    assert (me["open"], me["paused"], me["pause_note"]) == (True, True, "Slammed"), me


# --- events ------------------------------------------------------------------------------


def test_events_relay_the_pis_terminal_events(store, make_store) -> None:
    hdr, _ = _login(store)  # no agent needed: a locked screen keeps its stream
    other_hdr, _ = _login(make_store())
    ev, other = CCEvents(hdr), CCEvents(other_hdr)
    try:
        assert ev.wait("hello") == {"pi_online": False, "open": True, "closed_reason": ""}
        assert other.wait("hello") is not None
        with FakePi(store) as pi:
            assert ev.wait("store", lambda d: d["pi_online"]) is not None
            # main asked the Pi for its terminal events (again once it was back).
            assert pi.wants.get(timeout=10)["seconds"] >= 60
            pi.report([{"event": "terminal.order.placed", "data": {"order": {"id": "o1"}, "device_id": "callcenter:x"}, "at": "2026-11-12T20:00:00Z"},
                       {"event": "terminal.draft.taken", "data": {"id": "d1", "by": "t1"}},
                       {"event": "order.updated", "data": {"id": "o1"}}])
            assert ev.wait("terminal.order.placed") == {"order": {"id": "o1"}, "device_id": "callcenter:x"}
            assert ev.wait("terminal.draft.taken") == {"id": "d1", "by": "t1"}
            assert ev.wait("order.updated") == {"id": "o1"}
        # The Pi left: after the reconnect grace the app hears it.
        assert ev.wait("store", lambda d: not d["pi_online"], timeout=20) is not None
        # Another store's browser heard none of it.
        assert other.wait("terminal.order.placed", timeout=1) is None
    finally:
        ev.close()
        other.close()


def test_revoking_a_device_ends_its_stream(store) -> None:
    hdr, body = _login(store)
    mgr = store.manager_session()
    ev = CCEvents(hdr)
    try:
        assert ev.wait("hello") is not None
        assert mgr.delete(f"{STORE_V1}/me/callcenter/devices/{body['device']['id']}", timeout=5).status_code == 204
        assert ev.wait("session.ended") == {"reason": "device_revoked"}
        assert wait_until(ev.ended, timeout=5)
    finally:
        ev.close()


# --- the report + the audit trail ----------------------------------------------------------


def test_report_per_agent_per_day(store) -> None:
    mgr = store.manager_session()
    cara = _person(mgr, "callcenter", "4321")
    dee = _person(mgr, "callcenter", "5432", first="Dee", last="")
    cashier = _person(mgr, "cashier", "11", first="Sam")

    def place(staff: dict, kind: str, total: int, *, device_kind: str = "callcenter", card: int = 0, tip: int = 0) -> str:
        oid = _uuid7()
        pays = []
        if card:
            pays = [{"id": str(uuid.uuid4()), "method": "manual_card", "amount_cents": card, "tip_cents": tip,
                     "at": datetime.now(NY).isoformat()}]
        snap = _snapshot(oid, staff_id=staff["id"], device_id="callcenter" if device_kind else "terminal-1",
                         device_kind=device_kind, taken_by=f"Call center - {staff['first_name']}" if device_kind else "",
                         fulfillment={"type": kind} if kind != "delivery" else {
                             "type": "delivery", "address": {"line1": "1 Main St", "city": "Towson", "state": "MD", "postal_code": "21204"}},
                         money={"subtotal_cents": total, "tax_cents": 0, "tip_cents": 0, "fees": [], "discounts": [], "total_cents": total},
                         payments=pays)
        snap["items"][0].update(unit_price_cents=total, line_total_cents=total)
        res = _send(store.key, [_placed(snap)])
        assert set(res.values()) == {"applied"}, res
        return oid

    a = place(cara, "pickup", 1000)
    place(cara, "delivery", 2500, card=2500, tip=300)
    place(dee, "delivery", 4000)
    place(cashier, "pickup", 9900, device_kind="")  # a store terminal's order: not in the report
    res = _send(store.key, [{"event_id": str(uuid.uuid4()), "type": "order.void", "order_id": a,
                             "at": datetime.now(NY).isoformat(), "reason": "Customer cancelled", "staff_id": cara["id"]}])
    assert set(res.values()) == {"applied"}, res

    today = datetime.now(NY).date().isoformat()
    r = mgr.get(f"{STORE_V1}/me/reports/callcenter", timeout=5)
    assert r.status_code == 200, r.text
    rep = r.json()
    assert (rep["from"], rep["to"], rep["timezone"]) == (today, today, "America/New_York")
    rows = {x["staff_name"]: x for x in rep["rows"]}
    assert rows["Cara S."] == {"day": today, "staff_id": cara["id"], "staff_name": "Cara S.", "orders": 2, "voided": 1,
                               "pickup": 0, "delivery": 1, "sales_cents": 2500, "card_cents": 2500, "tips_cents": 300}
    assert rows["Dee"] == {"day": today, "staff_id": dee["id"], "staff_name": "Dee", "orders": 1, "voided": 0,
                           "pickup": 0, "delivery": 1, "sales_cents": 4000, "card_cents": 0, "tips_cents": 0}
    assert set(rows) == {"Cara S.", "Dee"}
    assert rep["totals"] == {"orders": 3, "voided": 1, "sales_cents": 6500, "card_cents": 2500, "tips_cents": 300}

    assert mgr.get(f"{STORE_V1}/me/reports/callcenter?from=2020-01-01&to=2020-01-31", timeout=5).json()["rows"] == []
    for bad in ("from=nope", "from=2026-11-12&to=2026-11-01", "from=2026-01-01&to=2026-11-01"):
        assert mgr.get(f"{STORE_V1}/me/reports/callcenter?{bad}", timeout=5).status_code == 400, bad
    # The order keeps who took it.
    got = requests.get(f"{API_V1}/orders/{a}", headers=store_key(store.key), timeout=5)
    assert got.status_code == 200, got.text


def test_audit_rows(store) -> None:
    hdr, person, mgr = _agent(store)
    assert _pin(hdr, "0000").status_code == 401
    with FakePi(store):
        assert wait_until(lambda: requests.get(f"{CC}/me", headers=hdr, timeout=5).json()["pi"]["online"], timeout=5)
        assert requests.post(f"{CC}/pi/refunds", headers=hdr, json={}, timeout=10).status_code == 403
        assert requests.post(f"{CC}/pi/quote", headers=hdr, json={"cart": {"lines": []}}, timeout=10).status_code == 200
    assert requests.delete(f"{CC}/session", headers=hdr, timeout=5).status_code == 204

    def actions() -> list[dict]:
        r = mgr.get(f"{STORE_V1}/me/reports/audit?limit=200", timeout=5)
        assert r.status_code == 200, r.text
        return [e for e in r.json()["entries"] if e["actor_kind"] == "callcenter" or e["entity"] == "callcenter_devices"]

    assert wait_until(lambda: {"callcenter.login", "callcenter.session.start", "callcenter.pin.failed", "callcenter.refused",
                               "callcenter.session.end"} <= {e["action"] for e in actions()}, timeout=5), \
        sorted({e["action"] for e in actions()})
    rows = actions()
    refused = next(e for e in rows if e["action"] == "callcenter.refused")
    assert refused["detail"]["params"]["path"] == "refunds" and refused["detail"]["params"]["staff_id"] == person["id"]
    # A relayed quote leaves no audit row (the Pi's journal has what agents do to orders).
    assert not [e for e in rows if "quote" in json.dumps(e.get("detail") or {})]
    # The device row's own changes are in the database's trail, the token masked.
    dev = [e for e in rows if e["entity"] == "callcenter_devices"]
    assert dev and "qccd_" not in json.dumps(dev)
