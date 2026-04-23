"""Auth flow tests for main/.

Verifies the admin passcode can set an API key, and that the key gates the
order routes. Relies on ADMIN_PASSCODE being present (Doppler provides it);
skips otherwise so CI without admin creds still passes other suites.
"""

import os
import secrets

import pytest
import requests

from conftest import BASE_URL, ADMIN_PASSCODE


requires_admin = pytest.mark.skipif(
    not ADMIN_PASSCODE, reason="ADMIN_PASSCODE env var not set"
)


@requires_admin
def test_set_key_with_passcode_succeeds() -> None:
    key = "test-" + secrets.token_hex(8)
    resp = requests.post(
        f"{BASE_URL}/auth/key",
        headers={"X-Admin-Passcode": ADMIN_PASSCODE},
        json={"key": key},
        timeout=5,
    )
    assert resp.status_code == 200, resp.text


@requires_admin
def test_set_key_wrong_passcode_rejected() -> None:
    resp = requests.post(
        f"{BASE_URL}/auth/key",
        headers={"X-Admin-Passcode": "definitely-not-the-passcode"},
        json={"key": "anything"},
        timeout=5,
    )
    assert resp.status_code in (401, 403)


def test_orders_without_api_key_rejected() -> None:
    resp = requests.get(f"{BASE_URL}/orders", timeout=5)
    assert resp.status_code in (401, 403)


def test_orders_with_bogus_key_rejected() -> None:
    resp = requests.get(
        f"{BASE_URL}/orders",
        headers={"X-API-Key": "nope-not-a-real-key"},
        timeout=5,
    )
    assert resp.status_code in (401, 403)


def test_response_sets_trace_id_header(http: requests.Session) -> None:
    """The OTEL middleware we installed always writes X-Trace-Id on responses
    so callers (including these tests) can look up the trace in Tempo."""
    resp = http.get(f"{BASE_URL}/orders", params={"offset": 0, "num": 1}, timeout=5)
    # status can be 200 or 400 depending on data — we only care about the header
    assert "X-Trace-Id" in resp.headers
    trace_id = resp.headers["X-Trace-Id"]
    assert len(trace_id) == 32, f"expected 32-hex trace id, got {trace_id!r}"
    int(trace_id, 16)  # parses as hex
