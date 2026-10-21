"""Tablet diagnostics (main 2.0.1), with the test playing the agent and the tablet:

  * POST/DELETE /store/v1/agent/tablet-tokens — the agent mints / revokes a
    tablet's write-only report token (agent keys only; rotate per tablet;
    max 10 active per location; a Pi re-link keeps them)
  * POST /store/v1/tablet/diag-reports — the tablet's direct upload with
    X-Quibase-Report-Key: 202 stored / 200 duplicate / 401 / 413 / 429
  * the report key opens nothing else (orders, SSE, RPC, admin, …)
  * RPC diag.report — a report relayed by the Pi (via=pi)
  * /admin/v1/diag-reports + /admin/v1/tablet-tokens — list / detail / resolve,
    token list / revoke

Reports here use non-paging outcomes (a guide that ended `fixed`, a full check
that was `ok`) so a run never pages the owner.
"""

import secrets
import uuid
from datetime import datetime, timezone

import requests

from conftest import ADMIN_V1, API_V1, STORE_V1, store_key

REPORT_HEADER = "X-Quibase-Report-Key"
REPORTS_URL = f"{STORE_V1}/tablet/diag-reports"


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _report(**over) -> dict:
    r = {
        "v": 1,
        "id": str(uuid.uuid4()),
        "created_at": _now(),
        "kind": "guide",
        "scenario": "printer",
        "outcome": "fixed",
        "headline": "Kitchen printer was out of paper",
        "cause": "printer_paper",
        "fixed_by": "printer_paper",
        "note": "",
        "steps": [{"id": "paper", "at": _now(), "answer": "yes", "result": "fixed"}],
        "checks": {"pi_port": {"status": "ok", "plain": "The Pi answers", "details": "200 in 12 ms"}},
        "tablet": {"name": "Counter", "device_id": "dev-1", "app_version": "1.3.0", "android_sdk": 34,
                   "model": "pytest", "checks": {"notifications": True, "full_screen": True, "battery": True,
                                                 "installs": True, "alarm_volume_pct": 80}},
        "pi": {"address": "192.168.1.219:8080", "version": "2.0.1", "store": "pytest", "last_diag": {}},
        "log": ["pytest line 1", "pytest line 2"],
    }
    r.update(over)
    return r


def _mint(key: str, tablet_id: str | None = None, name: str = "Counter") -> requests.Response:
    return requests.post(f"{STORE_V1}/agent/tablet-tokens", headers=store_key(key),
                         json={"tablet_id": tablet_id or "tab-" + secrets.token_hex(4), "tablet_name": name}, timeout=5)


def _token(store, tablet_id: str | None = None) -> str:
    r = _mint(store.key, tablet_id)
    assert r.status_code == 201, r.text
    return r.json()["token"]


def _post(token: str, body) -> requests.Response:
    kw = {"data": body} if isinstance(body, (str, bytes)) else {"json": body}
    return requests.post(REPORTS_URL, headers={REPORT_HEADER: token}, timeout=10, **kw)


def _rpc(key: str, typ: str, payload: dict) -> requests.Response:
    return requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(key),
                         json={"type": typ, "id": str(uuid.uuid4()), "payload": payload}, timeout=10)


# --- tokens ----------------------------------------------------------------------


def test_agent_mints_a_report_token(store) -> None:
    r = _mint(store.key, "tab-1", "Counter")
    assert r.status_code == 201, r.text
    body = r.json()
    assert body["token"].startswith("qrep_") and len(body["token"]) == 5 + 43
    uuid.UUID(body["id"])


def test_mint_needs_a_tablet_id(store) -> None:
    r = requests.post(f"{STORE_V1}/agent/tablet-tokens", headers=store_key(store.key),
                      json={"tablet_id": "  ", "tablet_name": "x"}, timeout=5)
    assert r.status_code == 400, r.text


def test_mint_is_agent_keys_only(store) -> None:
    mgr = store.manager_session()
    r = mgr.post(f"{STORE_V1}/me/devices", json={"name": "phone", "kind": "mobile"}, timeout=5)
    assert r.status_code == 201, r.text
    mobile_key = r.json()["api_key"]
    assert _mint(mobile_key).status_code == 403
    r = requests.delete(f"{STORE_V1}/agent/tablet-tokens/tab-1", headers=store_key(mobile_key), timeout=5)
    assert r.status_code == 403
    assert requests.post(f"{STORE_V1}/agent/tablet-tokens", json={"tablet_id": "x"}, timeout=5).status_code == 401


def test_minting_again_rotates_the_tablets_token(store) -> None:
    old = _token(store, "tab-rot")
    other = _token(store, "tab-other")
    new = _token(store, "tab-rot")
    assert _post(old, _report()).status_code == 401, "the tablet's previous token is revoked"
    assert _post(new, _report()).status_code == 202
    assert _post(other, _report()).status_code == 202, "another tablet's token is untouched"


def test_agent_revokes_a_tablet_token(store) -> None:
    tok = _token(store, "tab-gone")
    for _ in range(2):  # idempotent
        r = requests.delete(f"{STORE_V1}/agent/tablet-tokens/tab-gone", headers=store_key(store.key), timeout=5)
        assert r.status_code == 204, r.text
    assert _post(tok, _report()).status_code == 401


def test_at_most_ten_active_tokens_per_location(store) -> None:
    toks = [_token(store, f"tab-{i}") for i in range(11)]
    assert _post(toks[0], _report()).status_code == 401, "the oldest is revoked past 10"
    assert _post(toks[1], _report()).status_code == 202
    assert _post(toks[10], _report()).status_code == 202


def test_relinking_the_pi_keeps_tablet_tokens(store) -> None:
    tok = _token(store, "tab-keep")
    username, password = f"mgr{store.tag}", secrets.token_urlsafe(10)
    r = store.admin.patch(f"{ADMIN_V1}/tenants/{store.tenant_id}/credentials",
                          json={"username": username, "password": password}, timeout=5)
    assert r.status_code == 204, r.text
    r = requests.post(f"{STORE_V1}/agent/link",
                      json={"username": username, "password": password, "agent_name": "pytest-pi"}, timeout=10)
    assert r.status_code == 200, r.text
    assert _post(tok, _report()).status_code == 202, "a new Pi must not cut the tablet off from main"


# --- direct upload -----------------------------------------------------------------


def test_report_is_stored_then_deduped(admin, store) -> None:
    tok = _token(store, "tab-dup")
    rep = _report()
    r = _post(tok, rep)
    assert r.status_code == 202, r.text
    assert r.json() == {"id": rep["id"], "stored": True, "duplicate": False}
    r = _post(tok, rep)
    assert r.status_code == 200, r.text
    assert r.json()["duplicate"] is True and r.json()["id"] == rep["id"]

    d = admin.get(f"{ADMIN_V1}/diag-reports/{rep['id']}", timeout=5).json()
    assert d["via"] == "direct" and d["tablet_id"] == "tab-dup" and d["tablet_name"] == "Counter"
    assert d["location_id"] == store.location_id and d["store_name"] == store.name
    assert d["report"]["tablet"]["android_sdk"] == 34


def test_bad_or_missing_report_key_is_401(store) -> None:
    assert _post("qrep_" + "A" * 43, _report()).status_code == 401
    assert _post("garbage", _report()).status_code == 401
    assert requests.post(REPORTS_URL, json=_report(), timeout=5).status_code == 401
    # The agent's own device key is not a report key.
    r = requests.post(REPORTS_URL, headers=store_key(store.key), json=_report(), timeout=5)
    assert r.status_code == 401


def test_malformed_report_is_400(store) -> None:
    tok = _token(store)
    assert _post(tok, "not json").status_code == 400
    assert _post(tok, _report(v=2)).status_code == 400
    assert _post(tok, _report(id="nope")).status_code == 400
    assert _post(tok, _report(kind="")).status_code == 400


def test_oversize_report_is_413(store) -> None:
    tok = _token(store)
    r = _post(tok, _report(note="x" * (300 * 1024)))
    assert r.status_code == 413, r.text


def test_long_fields_are_capped_not_rejected(admin, store) -> None:
    tok = _token(store)
    rep = _report(headline="h" * 300, note="n" * 900, outcome="some_future_outcome", log=[f"l{i}" for i in range(400)])
    assert _post(tok, rep).status_code == 202
    d = admin.get(f"{ADMIN_V1}/diag-reports/{rep['id']}", timeout=5).json()
    assert len(d["headline"]) == 120 and len(d["report"]["note"]) == 500
    assert d["outcome"] == "some_future_outcome", "unknown enum values are kept"
    assert len(d["report"]["log"]) == 300 and d["report"]["log"][-1] == "l399"


def test_burst_from_one_tablet_is_429(store) -> None:
    tok = _token(store, "tab-burst")
    for i in range(30):
        r = _post(tok, _report())
        assert r.status_code == 202, f"report {i}: {r.status_code} {r.text}"
    r = _post(tok, _report())
    assert r.status_code == 429, r.text
    assert int(r.headers["Retry-After"]) >= 1
    assert r.json()["code"] == "rate_limited"


# --- the report key opens nothing else ------------------------------------------------


def test_report_key_cannot_read_orders_or_anything_else(store) -> None:
    tok = _token(store)
    as_report = {REPORT_HEADER: tok}
    as_device = store_key(tok)
    as_bearer = {"Authorization": f"Bearer {tok}"}
    checks = [
        ("GET", f"{API_V1}/orders"),
        ("GET", f"{API_V1}/orders/{uuid.uuid4()}"),
        ("GET", f"{API_V1}/events/orders"),
        ("GET", f"{STORE_V1}/store-config"),
        ("POST", f"{STORE_V1}/agent/rpc"),
        ("POST", f"{STORE_V1}/agent/tablet-tokens"),
        ("GET", f"{STORE_V1}/agent/restore"),
        ("GET", f"{STORE_V1}/agent/credential"),
        ("GET", f"{STORE_V1}/me/location"),
        ("GET", f"{STORE_V1}/operator/menu"),
        ("GET", f"{ADMIN_V1}/diag-reports"),
        ("GET", f"{ADMIN_V1}/tenants"),
    ]
    for method, url in checks:
        for headers in (as_report, as_device, as_bearer):
            r = requests.request(method, url, headers=headers, json={}, timeout=5, stream=True)
            r.close()
            assert r.status_code == 401, f"{method} {url} with {list(headers)[0]} → {r.status_code}"


# --- via the Pi (RPC) ------------------------------------------------------------------


def test_rpc_diag_report_is_stored_via_pi(admin, store) -> None:
    rep = _report(kind="full_check", scenario=None, outcome="ok", headline="All good")
    payload = {"tablet_id": "tab-pi", "tablet_name": "Counter (Pi)", "report": rep,
               "pi": {"version": "2.0.1-pytest", "health": {"sd_errors": 0, "under_voltage": False}}}
    r = _rpc(store.key, "diag.report", payload)
    assert r.status_code == 200, r.text
    env = r.json()
    assert env["type"] == "diag.report.reply"
    assert env["payload"] == {"id": rep["id"], "stored": True, "duplicate": False}

    again = _rpc(store.key, "diag.report", payload).json()["payload"]
    assert again == {"id": rep["id"], "stored": False, "duplicate": True}

    d = admin.get(f"{ADMIN_V1}/diag-reports/{rep['id']}", timeout=5).json()
    assert d["via"] == "pi" and d["tablet_id"] == "tab-pi" and d["tablet_name"] == "Counter (Pi)"
    assert d["agent_version"] == "2.0.1-pytest" and d["scenario"] == ""
    assert d["report"]["pi"]["health"] == {"sd_errors": 0, "under_voltage": False}
    assert d["report"]["pi"]["address"] == "192.168.1.219:8080", "the tablet's pi fields are kept"


def test_rpc_diag_report_rejects_a_bad_report(store) -> None:
    r = _rpc(store.key, "diag.report", {"tablet_id": "t", "report": {"v": 1, "id": "nope"}})
    assert r.status_code == 400, r.text
    r = _rpc(store.key, "diag.report", {"tablet_id": "t"})
    assert r.status_code == 400, r.text


# --- admin ---------------------------------------------------------------------------


def test_admin_list_detail_and_resolve(admin, make_store) -> None:
    a, b = make_store(), make_store()
    tok_a = _token(a)
    r1 = _report(kind="full_check", outcome="ok", headline="check A")
    r2 = _report(headline="guide A")
    for rep in (r1, r2):
        assert _post(tok_a, rep).status_code == 202
    assert _post(_token(b), _report(headline="guide B")).status_code == 202

    rows = admin.get(f"{ADMIN_V1}/diag-reports", params={"tenant_id": a.tenant_id}, timeout=5).json()
    assert [x["id"] for x in rows] == [r2["id"], r1["id"]], "newest first, one tenant only"
    assert "report" not in rows[0], "the list carries summaries"
    assert rows[0]["store_name"] == a.name and rows[0]["via"] == "direct"

    rows = admin.get(f"{ADMIN_V1}/diag-reports",
                     params={"tenant_id": a.tenant_id, "kind": "full_check", "outcome": "ok"}, timeout=5).json()
    assert [x["id"] for x in rows] == [r1["id"]]
    rows = admin.get(f"{ADMIN_V1}/diag-reports", params={"tenant_id": a.tenant_id, "limit": 1}, timeout=5).json()
    assert len(rows) == 1

    # The 8-char prefix a page links with opens the report.
    d = admin.get(f"{ADMIN_V1}/diag-reports/{r1['id'][:8]}", timeout=5)
    assert d.status_code == 200 and d.json()["id"] == r1["id"]
    assert d.json()["report"]["checks"]["pi_port"]["status"] == "ok"
    assert admin.get(f"{ADMIN_V1}/diag-reports/{uuid.uuid4()}", timeout=5).status_code == 404

    r = admin.patch(f"{ADMIN_V1}/diag-reports/{r1['id']}", json={"resolved": True}, timeout=5)
    assert r.status_code == 200 and r.json()["resolved_at"], r.text
    rows = admin.get(f"{ADMIN_V1}/diag-reports", params={"tenant_id": a.tenant_id, "unresolved": "true"}, timeout=5).json()
    assert [x["id"] for x in rows] == [r2["id"]]
    r = admin.patch(f"{ADMIN_V1}/diag-reports/{r1['id']}", json={"resolved": False}, timeout=5)
    assert r.status_code == 200 and r.json()["resolved_at"] is None
    assert admin.patch(f"{ADMIN_V1}/diag-reports/{r1['id']}", json={}, timeout=5).status_code == 400
    assert admin.patch(f"{ADMIN_V1}/diag-reports/{uuid.uuid4()}", json={"resolved": True}, timeout=5).status_code == 404


def test_admin_routes_need_the_admin_key(store) -> None:
    for url in (f"{ADMIN_V1}/diag-reports", f"{ADMIN_V1}/tablet-tokens"):
        assert requests.get(url, timeout=5).status_code == 401


def test_admin_lists_and_revokes_tablet_tokens(admin, store) -> None:
    r = _mint(store.key, "tab-admin", "Front")
    tok, tok_id = r.json()["token"], r.json()["id"]
    rows = admin.get(f"{ADMIN_V1}/tablet-tokens", params={"location_id": store.location_id}, timeout=5).json()
    assert [x["id"] for x in rows] == [tok_id]
    row = rows[0]
    assert row["tablet_id"] == "tab-admin" and row["tablet_name"] == "Front" and row["revoked_at"] is None
    assert tok.startswith(row["key_prefix"]) and len(row["key_prefix"]) < len(tok)
    assert "token_hash" not in row and "token" not in row

    assert admin.post(f"{ADMIN_V1}/tablet-tokens/{tok_id}/revoke", timeout=5).status_code == 204
    assert _post(tok, _report()).status_code == 401
    rows = admin.get(f"{ADMIN_V1}/tablet-tokens", params={"location_id": store.location_id}, timeout=5).json()
    assert rows[0]["revoked_at"]
    assert admin.post(f"{ADMIN_V1}/tablet-tokens/{uuid.uuid4()}/revoke", timeout=5).status_code == 404
