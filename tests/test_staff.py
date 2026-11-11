"""The counter terminal's staff (TERMINAL-PLAN.md 0d, contract/main.md §2.5zb).

  * Store › Staff CRUD (/store/v1/me/staff): the PIN is 2–4 digits, unique
    among the store's active staff (checked against the hashes), never
    returned; role limits from the store's terminal settings, a person's own
    limits over them; turning someone back on needs a PIN
  * the terminal settings (/store/v1/me/terminal/settings), defaults + ranges
  * the Pi's GET /store/v1/agent/terminal-config: active staff with bcrypt
    hashes, a version every change bumps, ETag → 304, agent keys only
  * every change pushes the command terminal.changed {version} to the Pi
  * another store's person is a 404
"""

from __future__ import annotations

import json

import pytest
import requests

from conftest import STORE_V1, store_key
from sse import SSEStream

STAFF = f"{STORE_V1}/me/staff"
SETTINGS = f"{STORE_V1}/me/terminal/settings"
CONFIG = f"{STORE_V1}/agent/terminal-config"


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


def _ok(r: requests.Response, status: int = 200):
    assert r.status_code == status, f"{r.status_code}: {r.text}"
    return r.json() if r.content else {}


def _code(r: requests.Response, status: int) -> str:
    assert r.status_code == status, f"{r.status_code}: {r.text}"
    return r.json()["code"]


def _config(key: str) -> dict:
    return _ok(requests.get(CONFIG, headers=store_key(key), timeout=5))


def test_staff_crud_and_pins(mgr) -> None:
    sam = _ok(mgr.post(STAFF, json={"first_name": "  Sam ", "last_name": "Lee", "role": "cashier", "pin": "1234"}, timeout=10), 201)
    assert sam["first_name"] == "Sam" and sam["role"] == "cashier" and sam["active"] is True
    assert "pin" not in json.dumps(sam).replace('"pin_', ""), "the PIN never comes back"
    assert "pin_hash" not in sam
    assert sam["limits"] == {} and sam["effective_limits"] == {"refund_cents": 0, "discount_pct": 10}

    # PIN rules.
    assert _code(mgr.post(STAFF, json={"first_name": "Ann", "role": "cashier", "pin": "1234"}, timeout=10), 409) == "pin_taken"
    for pin in ("1", "12345", "12a", ""):
        assert _code(mgr.post(STAFF, json={"first_name": "Ann", "role": "cashier", "pin": pin}, timeout=10), 400) == "bad_pin"
    assert _code(mgr.post(STAFF, json={"first_name": "Ann", "role": "boss", "pin": "77"}, timeout=10), 400) == "bad_role"
    assert _code(mgr.post(STAFF, json={"first_name": " ", "role": "cashier", "pin": "77"}, timeout=10), 400) == "bad_name"
    assert _code(mgr.post(STAFF, json={"first_name": "Ann", "role": "cashier", "pin": "77",
                                       "limits": {"discount_pct": 101}}, timeout=10), 400) == "bad_limits"
    assert _code(mgr.post(STAFF, json={"first_name": "Ann", "role": "cashier", "pin": "77", "nope": 1}, timeout=10), 400) == "bad_request"

    ann = _ok(mgr.post(STAFF, json={"first_name": "Ann", "role": "manager", "pin": "77",
                                    "limits": {"refund_cents": 5000}}, timeout=10), 201)
    assert ann["effective_limits"] == {"refund_cents": 5000, "discount_pct": None}, "a manager is unlimited unless limited"

    # Edit: name, role, limits ({} = the role defaults again).
    sam = _ok(mgr.patch(f"{STAFF}/{sam['id']}", json={"last_name": "Leigh", "limits": {"discount_pct": 25}}, timeout=10))
    assert sam["last_name"] == "Leigh" and sam["effective_limits"] == {"refund_cents": 0, "discount_pct": 25}
    sam = _ok(mgr.patch(f"{STAFF}/{sam['id']}", json={"role": "driver", "limits": {}}, timeout=10))
    assert sam["effective_limits"] == {"refund_cents": 0, "discount_pct": 0}

    # Reset a PIN: not to a colleague's.
    assert _code(mgr.post(f"{STAFF}/{sam['id']}/pin", json={"pin": "77"}, timeout=10), 409) == "pin_taken"
    _ok(mgr.post(f"{STAFF}/{sam['id']}/pin", json={"pin": "4321"}, timeout=10), 204)
    # The old PIN is free now.
    bob = _ok(mgr.post(STAFF, json={"first_name": "Bob", "role": "cashier", "pin": "1234"}, timeout=10), 201)

    # Deactivating releases the PIN; turning back on needs one.
    _ok(mgr.patch(f"{STAFF}/{sam['id']}", json={"active": False}, timeout=10))
    _ok(mgr.post(STAFF, json={"first_name": "Cy", "role": "cashier", "pin": "4321"}, timeout=10), 201)
    assert _code(mgr.patch(f"{STAFF}/{sam['id']}", json={"active": True}, timeout=10), 400) == "pin_required"
    assert _code(mgr.patch(f"{STAFF}/{sam['id']}", json={"active": True, "pin": "4321"}, timeout=10), 409) == "pin_taken"
    sam = _ok(mgr.patch(f"{STAFF}/{sam['id']}", json={"active": True, "pin": "555"}, timeout=10))
    assert sam["active"] is True

    listed = _ok(mgr.get(STAFF, timeout=5))
    assert [p["first_name"] for p in listed] == ["Ann", "Bob", "Cy", "Sam"]
    assert all("pin_hash" not in p for p in listed)

    _ok(mgr.delete(f"{STAFF}/{bob['id']}", timeout=5), 204)
    assert _code(mgr.delete(f"{STAFF}/{bob['id']}", timeout=5), 404) == "not_found"
    assert _code(mgr.patch(f"{STAFF}/not-a-uuid", json={}, timeout=5), 400) == "bad_request"


def test_terminal_settings(mgr) -> None:
    assert _ok(mgr.get(SETTINGS, timeout=5)) == {"cashier_refund_cents": 0, "cashier_discount_pct": 10, "auto_lock_minutes": 5,
                                                 "counter_start": 1, "receipt_auto_done_seconds": 15, "auto_close_batch_at": "04:00"}
    want = {"cashier_refund_cents": 2000, "cashier_discount_pct": 15, "auto_lock_minutes": 3, "counter_start": 100,
            "receipt_auto_done_seconds": 0, "auto_close_batch_at": "03:30"}
    assert _ok(mgr.put(SETTINGS, json=want, timeout=5)) == want
    assert _ok(mgr.get(SETTINGS, timeout=5)) == want
    # A client that doesn't know auto_close_batch_at keeps the saved one; "" = off.
    older = {k: v for k, v in want.items() if k != "auto_close_batch_at"}
    assert _ok(mgr.put(SETTINGS, json=older, timeout=5))["auto_close_batch_at"] == "03:30"
    assert _ok(mgr.put(SETTINGS, json={**want, "auto_close_batch_at": ""}, timeout=5))["auto_close_batch_at"] == ""
    _ok(mgr.put(SETTINGS, json=want, timeout=5))
    for bad in ({"auto_lock_minutes": 0}, {"counter_start": 0}, {"cashier_discount_pct": 101}, {"receipt_auto_done_seconds": 601},
                {"auto_close_batch_at": "4:00"}, {"auto_close_batch_at": "24:00"}):
        assert _code(mgr.put(SETTINGS, json={**want, **bad}, timeout=5), 400) == "bad_settings"
    assert _code(mgr.put(SETTINGS, json={**want, "extra": 1}, timeout=5), 400) == "bad_request"
    # A cashier's effective limits follow the settings.
    p = _ok(mgr.post(STAFF, json={"first_name": "Dee", "role": "cashier", "pin": "11"}, timeout=10), 201)
    assert p["effective_limits"] == {"refund_cents": 2000, "discount_pct": 15}


def test_terminal_config_for_the_pi(store, mgr) -> None:
    empty = _config(store.key)
    assert empty["version"] == 0 and empty["staff"] == [] and empty["settings"]["auto_lock_minutes"] == 5
    assert empty["settings"]["auto_close_batch_at"] == "04:00"  # the Pi's batch auto-close, on by default

    with SSEStream(store.key) as s:
        p = _ok(mgr.post(STAFF, json={"first_name": "Eve", "last_name": "Ray", "role": "cashier", "pin": "2468"}, timeout=10), 201)
        cmd = s.wait_event("command", lambda d: d.get("type") == "terminal.changed")
        assert cmd, "terminal.changed was not pushed"
        cfg = _config(store.key)
        assert cmd["payload"] == {"version": cfg["version"]} and cfg["version"] >= 1

        (eve,) = cfg["staff"]
        assert eve["id"] == p["id"] and eve["role"] == "cashier" and eve["first_name"] == "Eve"
        assert eve["pin_hash"].startswith("$2a$08$") and len(eve["pin_hash"]) == 60
        assert eve["effective_limits"] == {"refund_cents": 0, "discount_pct": 10}

        # A settings save is a change too.
        st = _ok(mgr.get(SETTINGS, timeout=5))
        _ok(mgr.put(SETTINGS, json={**st, "auto_lock_minutes": 9}, timeout=5))
        cmd = s.wait_event("command", lambda d: d.get("type") == "terminal.changed" and d["payload"]["version"] > cfg["version"])
        assert cmd, "a settings save pushes terminal.changed"

    cfg = _config(store.key)
    assert cfg["settings"]["auto_lock_minutes"] == 9
    # ETag → 304.
    r = requests.get(CONFIG, headers=store_key(store.key), timeout=5)
    etag = r.headers["ETag"]
    assert etag == f'"t{cfg["version"]}"'
    assert requests.get(CONFIG, headers={**store_key(store.key), "If-None-Match": etag}, timeout=5).status_code == 304

    # An inactive person's hash leaves the Pi.
    _ok(mgr.patch(f"{STAFF}/{p['id']}", json={"active": False}, timeout=10))
    assert _config(store.key)["staff"] == []


def test_terminal_config_is_agent_only(store, mgr) -> None:
    assert requests.get(CONFIG, timeout=5).status_code == 401
    assert mgr.get(CONFIG, timeout=5).status_code == 401
    kiosk = _ok(mgr.post(f"{STORE_V1}/me/devices", json={"name": "kiosk", "kind": "kiosk"}, timeout=5), 201)
    key = kiosk.get("api_key") or kiosk.get("key")
    assert requests.get(CONFIG, headers=store_key(key), timeout=5).status_code == 403
    assert requests.get(f"{STORE_V1}/agent/counter-menu", headers=store_key(key), timeout=5).status_code == 403


def test_another_stores_staff(make_store) -> None:
    a, b = make_store(), make_store()
    ma, mb = a.manager_session(), b.manager_session()
    p = _ok(ma.post(STAFF, json={"first_name": "Fay", "role": "cashier", "pin": "99"}, timeout=10), 201)
    # Same PIN at another store is fine.
    _ok(mb.post(STAFF, json={"first_name": "Gus", "role": "cashier", "pin": "99"}, timeout=10), 201)
    assert _code(mb.patch(f"{STAFF}/{p['id']}", json={"first_name": "X"}, timeout=5), 404) == "not_found"
    assert _code(mb.delete(f"{STAFF}/{p['id']}", timeout=5), 404) == "not_found"
    assert [x["first_name"] for x in _ok(mb.get(STAFF, timeout=5))] == ["Gus"]
    assert [x["first_name"] for x in _config(a.key)["staff"]] == ["Fay"]
