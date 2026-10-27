"""2.2.3: Pushover targeting for the driver-route release.

- `GET /store/v1/agent/secrets` (agent keys only, no-store): main's current
  Pushover secrets, so a linked Pi picks up a change without re-linking.
- The store owner's alert phones: location config `pushover_owner_devices`
  (comma list of Pushover device names), saved with `PATCH /me/location`,
  validated + normalized by main (400 `bad_device`), pushed to the agent in
  `store_config`.
- `POST /store/v1/me/alerts/test`: one test Pushover from main to those phones.

Never sends a real Pushover: the alert-test cases here all answer before
sending (no devices / not configured), and the not-configured case skips when
the target env has Pushover keys.
"""

from __future__ import annotations

import pytest
import requests

from conftest import STORE_V1, store_key
from sse import SSEStream

SECRET_KEYS = {"pushover_app_token", "pushover_user_key", "pushover_boss_key", "pushover_admin_devices"}


def _secrets(key: str) -> requests.Response:
    return requests.get(f"{STORE_V1}/agent/secrets", headers=store_key(key), timeout=5)


def _location_body(mgr: requests.Session) -> dict:
    loc = mgr.get(f"{STORE_V1}/me/location", timeout=5).json()
    return {k: loc[k] for k in ("name", "timezone", "daily_reset_hour", "phone", "address_line1", "address_line2", "config")}


def _save_devices(mgr: requests.Session, devices) -> requests.Response:
    body = _location_body(mgr)
    body["config"] = {**(body["config"] or {}), "pushover_owner_devices": devices}
    return mgr.patch(f"{STORE_V1}/me/location", json=body, timeout=5)


def test_agent_secrets_for_agent_keys_only(store) -> None:
    r = _secrets(store.key)
    assert r.status_code == 200, r.text
    assert set(r.json()) == SECRET_KEYS, "exactly the four Pushover fields, always present"
    assert all(isinstance(v, str) for v in r.json().values())
    assert "no-store" in r.headers.get("Cache-Control", "")

    # A manager-minted kiosk key is a device of the store but not the agent.
    mgr = store.manager_session()
    kiosk = mgr.post(f"{STORE_V1}/me/devices", json={"name": "kiosk", "kind": "kiosk"}, timeout=5)
    assert kiosk.status_code == 201, kiosk.text
    assert _secrets(kiosk.json()["api_key"]).status_code == 403
    # No key / a wrong key / the manager's cookie.
    assert requests.get(f"{STORE_V1}/agent/secrets", timeout=5).status_code == 401
    assert _secrets("not-a-key").status_code == 401
    assert mgr.get(f"{STORE_V1}/agent/secrets", timeout=5).status_code == 401


def test_owner_devices_saved_normalized_and_pushed(store) -> None:
    mgr = store.manager_session()
    before = _location_body(mgr)
    with SSEStream(store.key) as s:
        assert s.next()["event"] == "store_config"
        r = _save_devices(mgr, " OwnerPhone , Tablet_2,OwnerPhone,, ")
        assert r.status_code == 200, r.text
        assert r.json()["config"]["pushover_owner_devices"] == "OwnerPhone,Tablet_2"
        pushed = s.wait_event(
            "store_config",
            lambda d: d["location"]["config"].get("pushover_owner_devices") == "OwnerPhone,Tablet_2",
            timeout=10,
        )
        assert pushed, "the agent did not get the owner devices in store_config"

    # The rest of the location stays as it was.
    after = _location_body(mgr)
    assert {k: after[k] for k in before if k != "config"} == {k: before[k] for k in before if k != "config"}

    # "" clears.
    r = _save_devices(mgr, "")
    assert r.status_code == 200, r.text
    assert r.json()["config"]["pushover_owner_devices"] == ""


@pytest.mark.parametrize("bad", ["my phone", "x" * 26, "ok,phone!", 42])
def test_owner_devices_bad_name_is_400(store, bad) -> None:
    mgr = store.manager_session()
    assert _save_devices(mgr, "Keep-Me").status_code == 200
    r = _save_devices(mgr, bad)
    assert r.status_code == 400, r.text
    assert r.json()["code"] == "bad_device"
    assert _location_body(mgr)["config"]["pushover_owner_devices"] == "Keep-Me", "a refused save changes nothing"


def test_alert_test_needs_saved_devices(store) -> None:
    mgr = store.manager_session()
    r = mgr.post(f"{STORE_V1}/me/alerts/test", timeout=10)
    assert r.status_code == 400, r.text
    assert r.json()["code"] == "no_devices"
    assert _save_devices(mgr, "").status_code == 200
    r = mgr.post(f"{STORE_V1}/me/alerts/test", timeout=10)
    assert r.status_code == 400 and r.json()["code"] == "no_devices", r.text


def test_alert_test_not_configured(store) -> None:
    sec = _secrets(store.key).json()
    if sec["pushover_app_token"] and sec["pushover_user_key"]:
        pytest.skip("this env has Pushover keys: the test would send a real Pushover")
    mgr = store.manager_session()
    assert _save_devices(mgr, "OwnerPhone").status_code == 200
    r = mgr.post(f"{STORE_V1}/me/alerts/test", timeout=10)
    assert r.status_code == 503, r.text
    assert r.json()["code"] == "not_configured"


def test_alert_test_needs_a_session(admin) -> None:
    assert requests.post(f"{STORE_V1}/me/alerts/test", timeout=5).status_code == 401
