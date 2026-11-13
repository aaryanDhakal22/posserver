"""The counter terminal's staff (TERMINAL-PLAN.md 0d, contract/main.md §2.5zb).

  * Store › Staff CRUD (/store/v1/me/staff): the PIN is 2–4 digits, unique
    among the store's active staff (checked against the hashes), never
    returned; role limits from the store's terminal settings, a person's own
    limits over them; turning someone back on needs a PIN
  * the terminal settings (/store/v1/me/terminal/settings), defaults + ranges
  * the Pi's GET /store/v1/agent/terminal-config: active staff with bcrypt
    hashes, switched-off staff with active false and no hash, a version every
    change bumps, ETag → 304, agent keys only
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
    assert sam["limits"] == {} and sam["effective_limits"] == {"refund_cents": 2000, "discount_pct": 20}

    # PIN rules.
    assert _code(mgr.post(STAFF, json={"first_name": "Ann", "role": "cashier", "pin": "1234"}, timeout=10), 409) == "pin_taken"
    for pin in ("1", "12345", "12a", ""):
        assert _code(mgr.post(STAFF, json={"first_name": "Ann", "role": "cashier", "pin": pin}, timeout=10), 400) == "bad_pin"
    assert _code(mgr.post(STAFF, json={"first_name": "Ann", "role": "boss", "pin": "77"}, timeout=10), 400) == "bad_role"
    assert _code(mgr.post(STAFF, json={"first_name": " ", "role": "cashier", "pin": "77"}, timeout=10), 400) == "bad_name"
    assert _code(mgr.post(STAFF, json={"first_name": "Ann", "role": "cashier", "pin": "77",
                                       "limits": {"discount_pct": 101}}, timeout=10), 400) == "bad_limits"
    assert _code(mgr.post(STAFF, json={"first_name": "Ann", "role": "cashier", "pin": "77", "nope": 1}, timeout=10), 400) == "bad_request"

    # A manager's PIN is exactly 4 digits (the store security round, 2026-11-12; the Pi refuses a shorter one).
    for pin in ("77", "777"):
        r = mgr.post(STAFF, json={"first_name": "Ann", "role": "manager", "pin": pin}, timeout=10)
        assert _code(r, 400) == "bad_pin" and "4 digits" in r.json()["error"], r.text
    ann = _ok(mgr.post(STAFF, json={"first_name": "Ann", "role": "manager", "pin": "7711",
                                    "limits": {"refund_cents": 5000}}, timeout=10), 201)
    assert _code(mgr.post(f"{STAFF}/{ann['id']}/pin", json={"pin": "771"}, timeout=10), 400) == "bad_pin"
    assert _code(mgr.patch(f"{STAFF}/{ann['id']}", json={"pin": "71"}, timeout=10), 400) == "bad_pin"
    # A PIN sent with a change to manager is a manager's PIN (4 digits).
    assert _code(mgr.patch(f"{STAFF}/{sam['id']}", json={"role": "manager", "pin": "123"}, timeout=10), 400) == "bad_pin"
    # A manager is unlimited, their own limits kept but never applied (the Pi's rule; contract/money-rules.json E07).
    assert ann["limits"] == {"refund_cents": 5000}
    assert ann["effective_limits"] == {"refund_cents": None, "discount_pct": None}, "a manager is always unlimited"

    # Edit: name, role, limits ({} = the role defaults again).
    sam = _ok(mgr.patch(f"{STAFF}/{sam['id']}", json={"last_name": "Leigh", "limits": {"discount_pct": 25}}, timeout=10))
    assert sam["last_name"] == "Leigh" and sam["effective_limits"] == {"refund_cents": 2000, "discount_pct": 25}
    sam = _ok(mgr.patch(f"{STAFF}/{sam['id']}", json={"role": "driver", "limits": {}}, timeout=10))
    assert sam["effective_limits"] == {"refund_cents": 0, "discount_pct": 0}

    # Reset a PIN: not to a colleague's.
    assert _code(mgr.post(f"{STAFF}/{sam['id']}/pin", json={"pin": "7711"}, timeout=10), 409) == "pin_taken"
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


APP_DEFAULTS = {"default_tab": "info", "show_history": False, "button_font": "barlow", "cashier_price_drop_pct": 20,
                "click_sound": False, "prices_on_buttons": False, "out_of_area_margin_mi": 1, "name_on_paid_counter": False,
                "card_tips": False, "card_tip_percents": [15, 18, 20], "employee_meal_pct": 20,
                # terminal Stage 2
                "cashier_payout_cents": 2000, "default_pickup_minutes": 15, "area_codes": ["410", "443", "667"]}


def _known(got: dict, want: dict) -> dict:
    """The settings this test knows (a field added later has its own test)."""
    return {k: got.get(k) for k in want}


def test_terminal_settings(mgr) -> None:
    first = {"cashier_refund_cents": 2000, "cashier_discount_pct": 20, "auto_lock_minutes": 5, "counter_start": 1,
             "receipt_auto_done_seconds": 15, "auto_close_batch_at": "04:00", **APP_DEFAULTS}
    assert _known(_ok(mgr.get(SETTINGS, timeout=5)), first) == first
    want = {"cashier_refund_cents": 2000, "cashier_discount_pct": 15, "auto_lock_minutes": 3, "counter_start": 100,
            "receipt_auto_done_seconds": 0, "auto_close_batch_at": "03:30"}
    assert _known(_ok(mgr.put(SETTINGS, json=want, timeout=5)), {**want, **APP_DEFAULTS}) == {**want, **APP_DEFAULTS}
    assert _known(_ok(mgr.get(SETTINGS, timeout=5)), {**want, **APP_DEFAULTS}) == {**want, **APP_DEFAULTS}
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


def test_terminal_app_settings(store, mgr) -> None:
    """The terminal app's settings (TERMINAL-PLAN 2026-11-11): saved, ranged, kept when a save leaves them out, sent to the Pi."""
    mine = {"default_tab": "takeout", "show_history": True, "button_font": "barlow_semi_condensed", "cashier_price_drop_pct": 25,
            "click_sound": True, "prices_on_buttons": True, "out_of_area_margin_mi": 1.5, "name_on_paid_counter": True,
            "card_tips": True, "card_tip_percents": [10, 15, 20, 25], "employee_meal_pct": 40}
    saved = _ok(mgr.put(SETTINGS, json=mine, timeout=5))
    assert {k: saved[k] for k in mine} == mine and saved["auto_lock_minutes"] == 5
    # An older client's full save of the fields it knows keeps every new one.
    older = {k: saved[k] for k in ("cashier_refund_cents", "cashier_discount_pct", "auto_lock_minutes", "counter_start",
                                   "receipt_auto_done_seconds")}
    after = _ok(mgr.put(SETTINGS, json={**older, "auto_lock_minutes": 7}, timeout=5))
    assert {k: after[k] for k in mine} == mine and after["auto_lock_minutes"] == 7
    for bad in ({"default_tab": "drawer"}, {"button_font": "comic"}, {"cashier_price_drop_pct": 101}, {"out_of_area_margin_mi": 11},
                {"out_of_area_margin_mi": -1}, {"card_tip_percents": []}, {"card_tip_percents": [1, 2, 3, 4, 5, 6, 7]},
                {"card_tip_percents": [15, 101]}, {"employee_meal_pct": -1}):
        assert _code(mgr.put(SETTINGS, json=bad, timeout=5), 400) == "bad_settings", bad
    assert _code(mgr.put(SETTINGS, json={"card_tips": "yes"}, timeout=5), 400) == "bad_request"
    # The Pi's terminal config carries them as saved.
    cfg = _config(store.key)
    assert {k: cfg["settings"][k] for k in mine} == {**mine}
    # Every font the terminal ships is a choice (contract §2.5zb).
    for font in ("barlow", "barlow_condensed", "barlow_semi_condensed", "system", "fira_sans_condensed",
                 "ibm_plex_sans_condensed", "atkinson_hyperlegible"):
        assert _ok(mgr.put(SETTINGS, json={"button_font": font}, timeout=5))["button_font"] == font


def test_drawer_and_phone_settings(store, mgr) -> None:
    """cashier_payout_cents, default_pickup_minutes, area_codes (terminal Stage 2): defaults, ranges, kept when a save
    leaves them out, trimmed + deduped, sent to the Pi."""
    st = _ok(mgr.get(SETTINGS, timeout=5))
    assert (st["cashier_payout_cents"], st["default_pickup_minutes"], st["area_codes"]) == (2000, 15, ["410", "443", "667"])
    assert _config(store.key)["settings"]["area_codes"] == ["410", "443", "667"]
    saved = _ok(mgr.put(SETTINGS, json={"cashier_payout_cents": 3500, "default_pickup_minutes": 20,
                                        "area_codes": ["410", " 202", "410", "301"]}, timeout=5))
    assert (saved["cashier_payout_cents"], saved["default_pickup_minutes"], saved["area_codes"]) == (3500, 20, ["410", "202", "301"])
    # A save that leaves them out keeps them; null area codes = the defaults.
    kept = _ok(mgr.put(SETTINGS, json={"auto_lock_minutes": 6}, timeout=5))
    assert (kept["cashier_payout_cents"], kept["default_pickup_minutes"], kept["area_codes"]) == (3500, 20, ["410", "202", "301"])
    assert _ok(mgr.put(SETTINGS, json={"area_codes": None}, timeout=5))["area_codes"] == ["410", "443", "667"]
    for bad in ({"cashier_payout_cents": -1}, {"cashier_payout_cents": 100_001}, {"default_pickup_minutes": -1},
                {"default_pickup_minutes": 241}, {"area_codes": []}, {"area_codes": ["41"]}, {"area_codes": ["110"]},
                {"area_codes": ["4100"]}, {"area_codes": [str(200 + i) for i in range(21)]}):
        assert _code(mgr.put(SETTINGS, json=bad, timeout=5), 400) == "bad_settings", bad
    cfg = _config(store.key)["settings"]
    assert (cfg["cashier_payout_cents"], cfg["default_pickup_minutes"], cfg["area_codes"]) == (3500, 20, ["410", "443", "667"])


def test_drawer_start_setting(store, mgr) -> None:
    """drawer_start_cents (the owner, 2026-11-12: "cash left in each drawer at opening", default $100): saved,
    ranged, kept when a save leaves it out, sent to the Pi."""
    assert _ok(mgr.get(SETTINGS, timeout=5))["drawer_start_cents"] == 10000
    assert _config(store.key)["settings"]["drawer_start_cents"] == 10000
    assert _ok(mgr.put(SETTINGS, json={"drawer_start_cents": 15000}, timeout=5))["drawer_start_cents"] == 15000
    assert _ok(mgr.put(SETTINGS, json={"auto_lock_minutes": 6}, timeout=5))["drawer_start_cents"] == 15000
    for bad in (-1, 1_000_001):
        assert _code(mgr.put(SETTINGS, json={"drawer_start_cents": bad}, timeout=5), 400) == "bad_settings", bad
    assert _config(store.key)["settings"]["drawer_start_cents"] == 15000


def test_quibase_counter_min_setting(store, mgr) -> None:
    """quibase_counter_min_cents (the owner's X, 2026-11-13, agent 2.5.4: a terminal order shows on Quibase only over $X
    with a kitchen line; default $5): saved, ranged, 0 allowed, kept when a save leaves it out, sent to the Pi."""
    assert _ok(mgr.get(SETTINGS, timeout=5))["quibase_counter_min_cents"] == 500
    assert _config(store.key)["settings"]["quibase_counter_min_cents"] == 500
    assert _ok(mgr.put(SETTINGS, json={"quibase_counter_min_cents": 750}, timeout=5))["quibase_counter_min_cents"] == 750
    assert _ok(mgr.put(SETTINGS, json={"auto_lock_minutes": 6}, timeout=5))["quibase_counter_min_cents"] == 750
    for bad in (-1, 100_001):
        assert _code(mgr.put(SETTINGS, json={"quibase_counter_min_cents": bad}, timeout=5), 400) == "bad_settings", bad
    assert _config(store.key)["settings"]["quibase_counter_min_cents"] == 750
    assert _ok(mgr.put(SETTINGS, json={"quibase_counter_min_cents": 0}, timeout=5))["quibase_counter_min_cents"] == 0
    assert _config(store.key)["settings"]["quibase_counter_min_cents"] == 0


def test_close_unpaid_counter_action_setting(store, mgr) -> None:
    """close_unpaid_counter_action (the owner's X, 2026-11-13, agent contract 2.10s §4: what a forced close of the day
    and the auto-close do with an unpaid counter check): waste by default, carry the other choice, anything else
    refused, kept when a save leaves it out, sent to the Pi in terminal-config."""
    assert _ok(mgr.get(SETTINGS, timeout=5))["close_unpaid_counter_action"] == "waste"
    assert _config(store.key)["settings"]["close_unpaid_counter_action"] == "waste"
    assert _ok(mgr.put(SETTINGS, json={"close_unpaid_counter_action": "carry"}, timeout=5))["close_unpaid_counter_action"] == "carry"
    assert _ok(mgr.put(SETTINGS, json={"auto_lock_minutes": 6}, timeout=5))["close_unpaid_counter_action"] == "carry"
    assert _config(store.key)["settings"]["close_unpaid_counter_action"] == "carry"
    for bad in ("keep", "Carry", 1):
        assert _code(mgr.put(SETTINGS, json={"close_unpaid_counter_action": bad}, timeout=5), 400) in ("bad_settings", "bad_request"), bad
    assert _config(store.key)["settings"]["close_unpaid_counter_action"] == "carry"
    assert _ok(mgr.put(SETTINGS, json={"close_unpaid_counter_action": "waste"}, timeout=5))["close_unpaid_counter_action"] == "waste"
    assert _config(store.key)["settings"]["close_unpaid_counter_action"] == "waste"


def test_driver_claim_setting_retired(store, mgr) -> None:
    """driver_claim_after_minutes is gone (the owner, 2026-11-12: drivers claim once an order is past its promised
    time on the Pi; migration 00042 dropped it from saved rows): not served, not sent to the Pi, refused on a save."""
    assert "driver_claim_after_minutes" not in _ok(mgr.get(SETTINGS, timeout=5))
    assert "driver_claim_after_minutes" not in _config(store.key)["settings"]
    assert _code(mgr.put(SETTINGS, json={"driver_claim_after_minutes": 30}, timeout=5), 400) == "bad_request"


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
        assert eve["effective_limits"] == {"refund_cents": 2000, "discount_pct": 20}

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

    assert eve["active"] is True

    # Switched off: still listed (the Pi ends the session as `deactivated`, agent §2.10u), active false, and the
    # hash never leaves main. Deleted: gone from the list (the Pi says `removed`).
    _ok(mgr.patch(f"{STAFF}/{p['id']}", json={"active": False}, timeout=10))
    (off,) = _config(store.key)["staff"]
    assert off["id"] == p["id"] and off["active"] is False and off["pin_hash"] == "", off
    _ok(mgr.delete(f"{STAFF}/{p['id']}", timeout=10), 204)
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


def test_owner_defaults_for_every_store(store, mgr) -> None:
    """The owner's defaults (2026-11-12, every store): auto-lock 5 min, a cashier discounts up to 20 % and lowers a
    price up to 20 %, the employee meal is 20 % off; a delivery costs $4 = $1 store + $3 driver. The Pi gets them."""
    owner = {"auto_lock_minutes": 5, "cashier_discount_pct": 20, "cashier_price_drop_pct": 20, "employee_meal_pct": 20}
    assert _known(_ok(mgr.get(SETTINGS, timeout=5)), owner) == owner
    assert _known(_config(store.key)["settings"], owner) == owner
    fees = _ok(mgr.get(f"{STORE_V1}/me/pricing", timeout=5))["store"]["fees"]
    assert (fees["delivery_fee_cents"], fees["delivery_driver_cents"]) == (400, 300)
