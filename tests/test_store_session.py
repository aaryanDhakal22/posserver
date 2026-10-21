"""Store-manager browser surface: cookie session login + the session-scoped
/store/v1/me/* routes (location, printers, non-agent devices). Scope comes from
the cookie — never from a URL id."""

import requests

from conftest import STORE_V1


def test_login_rejects_bad_credentials(store) -> None:
    store.manager_session()  # ensure the tenant has a login
    r = requests.post(f"{STORE_V1}/session", json={"username": f"mgr{store.tag}", "password": "wrong"}, timeout=5)
    assert r.status_code == 401, r.text


def test_me_requires_session(admin) -> None:
    for path in ("/session", "/me/location", "/me/printers", "/me/devices"):
        assert requests.get(f"{STORE_V1}{path}", timeout=5).status_code == 401, path


def test_whoami_and_location(store) -> None:
    mgr = store.manager_session()
    who = mgr.get(f"{STORE_V1}/session", timeout=5)
    assert who.status_code == 200, who.text
    assert who.json()["location"]["id"] == store.location_id
    loc = mgr.get(f"{STORE_V1}/me/location", timeout=5)
    assert loc.status_code == 200 and loc.json()["id"] == store.location_id


def test_printer_crud(store) -> None:
    mgr = store.manager_session()
    r = mgr.post(f"{STORE_V1}/me/printers", json={"name": "Wings", "ip": "10.0.0.7"}, timeout=5)
    assert r.status_code == 201, r.text
    pid = r.json()["id"]

    r = mgr.patch(f"{STORE_V1}/me/printers/{pid}", json={"name": "Wings", "ip": "10.0.0.8"}, timeout=5)
    assert r.status_code == 200, r.text
    assert r.json()["ip"] == "10.0.0.8"
    assert any(p["id"] == pid for p in mgr.get(f"{STORE_V1}/me/printers", timeout=5).json())

    assert mgr.delete(f"{STORE_V1}/me/printers/{pid}", timeout=5).status_code in (200, 204)
    assert not any(p["id"] == pid for p in mgr.get(f"{STORE_V1}/me/printers", timeout=5).json())


def test_cannot_touch_another_stores_printer(make_store) -> None:
    a, b = make_store(), make_store()
    pid = a.manager_session().post(f"{STORE_V1}/me/printers", json={"name": "A", "ip": "10.0.0.1"}, timeout=5).json()["id"]
    r = b.manager_session().patch(f"{STORE_V1}/me/printers/{pid}", json={"name": "hijack", "ip": "1.1.1.1"}, timeout=5)
    assert r.status_code == 404, r.text


def test_mint_non_agent_device_only(store) -> None:
    mgr = store.manager_session()
    r = mgr.post(f"{STORE_V1}/me/devices", json={"name": "phone", "kind": "mobile"}, timeout=5)
    assert r.status_code == 201, r.text
    assert r.json()["device"]["kind"] == "mobile" and r.json()["api_key"]
    r = mgr.post(f"{STORE_V1}/me/devices", json={"name": "sneaky", "kind": "agent"}, timeout=5)
    assert r.status_code == 400, "agents must be minted via /admin"


def test_logout_clears_session(store) -> None:
    mgr = store.manager_session()
    assert mgr.delete(f"{STORE_V1}/session", timeout=5).status_code == 204
    assert mgr.get(f"{STORE_V1}/me/location", timeout=5).status_code == 401


def test_location_timezone_must_be_real(store) -> None:
    """The agent uses the timezone for ticket times + the daily driver
    clock-out: a typo must be refused, not pushed live."""
    mgr = store.manager_session()
    loc = mgr.get(f"{STORE_V1}/me/location", timeout=5).json()
    body = {k: loc[k] for k in ("name", "timezone", "daily_reset_hour", "phone", "address_line1", "address_line2", "config")}
    r = mgr.patch(f"{STORE_V1}/me/location", json={**body, "timezone": "America/Nowhere"}, timeout=5)
    assert r.status_code >= 400, r.text
    r = mgr.patch(f"{STORE_V1}/me/location", json={**body, "timezone": "America/Chicago"}, timeout=5)
    assert r.status_code == 200 and r.json()["timezone"] == "America/Chicago", r.text
