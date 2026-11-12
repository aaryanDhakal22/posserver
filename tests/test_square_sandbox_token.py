"""Paste a Square sandbox token (staging / dev only, 2026-11-12; contract §2.5za): Store › Payments takes a
sandbox access token + location instead of the OAuth round trip, stored like an OAuth connection. Dev main runs
the fake Square, which takes it too (production refuses it: 403, `squarepay/sandbox_token_test.go`)."""

from __future__ import annotations

import uuid
from urllib.parse import parse_qs, urlparse

import pytest
import requests

from conftest import STORE_V1, store_key
from test_square import FAKE


@pytest.fixture(autouse=True)
def _fake_square_on():
    if requests.get(f"{FAKE}/_fake/payments", timeout=5).status_code != 200:
        pytest.skip("main isn't running the fake Square (SQUARE_ENV=fake)")


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


def _fake_token() -> tuple[str, list[dict]]:
    """A token the fake Square issued (as the Developer Console's sandbox token) + its locations."""
    r = requests.get(f"{FAKE}/oauth2/authorize", params={"client_id": "sq0idp-fake", "redirect_uri": "http://paste.invalid/cb",
                                                          "state": "s", "scope": "PAYMENTS_WRITE"}, allow_redirects=False, timeout=10)
    code = parse_qs(urlparse(r.headers["Location"]).query)["code"][0]
    tok = requests.post(f"{FAKE}/oauth2/token", json={"client_id": "sq0idp-fake", "client_secret": "sq0csp-fake",
                                                      "grant_type": "authorization_code", "code": code}, timeout=10).json()
    locs = requests.get(f"{FAKE}/v2/locations", headers={"Authorization": f"Bearer {tok['access_token']}"}, timeout=10).json()
    return tok["access_token"], locs["locations"]


def test_paste_a_sandbox_token(store, mgr) -> None:
    token, locs = _fake_token()
    active = next(x for x in locs if x["status"] == "ACTIVE")["id"]
    inactive = next(x for x in locs if x["status"] != "ACTIVE")["id"]
    url = f"{STORE_V1}/me/square/sandbox-token"
    assert mgr.post(url, json={"access_token": token}, timeout=10).status_code == 400
    r = mgr.post(url, json={"access_token": "EAAA-nope", "location_id": active}, timeout=10)
    assert r.status_code == 422 and r.json()["code"] == "token_refused", r.text
    r = mgr.post(url, json={"access_token": token, "location_id": inactive}, timeout=10)
    assert r.status_code == 422 and r.json()["code"] == "bad_location", r.text
    assert requests.post(url, json={"access_token": token, "location_id": active}, timeout=10).status_code == 401  # a manager's

    r = mgr.post(url, json={"access_token": token, "location_id": active}, timeout=15)
    assert r.status_code == 200, r.text
    st = r.json()
    assert st["connected"] and st["pasted"] and st["square_location_id"] == active and st["in_person"] is True, st
    assert token not in r.text

    # The terminal's reader session (the Pi's RPC) hands out the pasted token.
    env = {"type": "square.session", "id": str(uuid.uuid4()), "payload": {}}
    s = requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(store.key), json=env, timeout=15).json()["payload"]
    assert s["ok"] and s["access_token"] == token and s["location_id"] == active, s

    # Disconnect clears it (not revoked at Square: it's the owner's token).
    assert mgr.delete(f"{STORE_V1}/me/square", timeout=10).status_code == 204
    after = mgr.get(f"{STORE_V1}/me/square", timeout=10).json()
    assert not after["connected"] and not after["pasted"], after
    assert requests.get(f"{FAKE}/v2/locations", headers={"Authorization": f"Bearer {token}"}, timeout=10).status_code == 200
