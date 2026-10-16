"""Auth surfaces on main/: device keys (X-STORE-API-KEY), the admin bearer,
and the X-Trace-Id echo."""

import requests

from conftest import ADMIN_API_KEY, ADMIN_V1, API_V1, store_key


def test_orders_without_key_401(admin) -> None:
    assert requests.get(f"{API_V1}/orders", timeout=5).status_code == 401


def test_orders_with_bogus_key_401(admin) -> None:
    r = requests.get(f"{API_V1}/orders", headers=store_key("qpos_definitely-not-real"), timeout=5)
    assert r.status_code == 401


def test_orders_with_valid_key_200(store) -> None:
    r = requests.get(f"{API_V1}/orders", headers=store_key(store.key), timeout=5)
    assert r.status_code == 200, r.text
    assert r.json() == []  # brand-new store has no orders


def test_revoked_key_401(admin, store) -> None:
    h = store_key(store.key)
    assert requests.get(f"{API_V1}/orders", headers=h, timeout=5).status_code == 200
    r = admin.post(f"{ADMIN_V1}/devices/{store.device_id}/revoke", timeout=5)
    assert r.status_code == 204, r.text
    assert requests.get(f"{API_V1}/orders", headers=h, timeout=5).status_code == 401


def test_admin_requires_bearer(admin) -> None:
    assert requests.get(f"{ADMIN_V1}/overview", timeout=5).status_code == 401
    r = requests.get(f"{ADMIN_V1}/overview", headers={"Authorization": "Bearer wrong"}, timeout=5)
    assert r.status_code == 401


def test_admin_key_is_not_a_device_key(admin) -> None:
    r = requests.get(f"{API_V1}/orders", headers=store_key(ADMIN_API_KEY), timeout=5)
    assert r.status_code == 401


def test_response_carries_trace_id(store) -> None:
    r = requests.get(f"{API_V1}/orders", headers=store_key(store.key), timeout=5)
    trace_id = r.headers.get("X-Trace-Id", "")
    assert len(trace_id) == 32, f"expected a 32-hex trace id, got {trace_id!r}"
    int(trace_id, 16)
