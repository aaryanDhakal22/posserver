"""Square OAuth relayed by the storefront (DURABILITY-PLAN.md step 6), against
dev main's in-process fake Square.

main sits on a private network, so Square's redirect lands on the public
storefront, which relays {code, state} to POST /square/oauth/forward with its
platform key. main checks the key, then the signed state, then exchanges the
code exactly as its own callback does (tokens sealed with SQUARE_TOKEN_KEY:
the Go test `squarepay/oauth_test.go` opens the stored rows), and answers
{result, redirect} for the manager's browser.

  * a relayed callback connects the store; the redirect is Store › Settings › Payments
  * no platform key, a wrong / revoked one, a channel key: 401, nothing used
    (the same code + state still connect afterwards)
  * a forged, damaged or re-signed-for-another-store state: 400 bad_state
  * the owner said no: 200 denied; a replayed code: 502 square_error
"""

from __future__ import annotations

import secrets
from urllib.parse import parse_qs, urlparse

import pytest
import requests

from conftest import ADMIN_V1, MAIN_BASE_URL, STORE_V1
from test_order_port import _channel, _ok

FAKE = f"{MAIN_BASE_URL}/__fake/square"
FORWARD = f"{MAIN_BASE_URL}/square/oauth/forward"
CH_V1 = f"{MAIN_BASE_URL}/channel/v1"


@pytest.fixture(autouse=True)
def _fake_square_on():
    if requests.get(f"{FAKE}/_fake/payments", timeout=5).status_code != 200:
        pytest.skip("main isn't running the fake Square (SQUARE_ENV=fake)")


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


@pytest.fixture
def pkey(admin):
    r = admin.post(f"{ADMIN_V1}/platform-keys", json={"name": f"oauth relay {secrets.token_hex(3)}"}, timeout=5)
    assert r.status_code == 201, r.text
    body = r.json()
    yield body
    admin.post(f"{ADMIN_V1}/platform-keys/{body['platform_key']['id']}/revoke", timeout=5)


def _square_redirect(mgr, deny: bool = False) -> dict[str, str]:
    """The owner on Square's page: what Square sends to the redirect URL
    (not followed, as if it were the storefront's)."""
    url = _ok(mgr.post(f"{STORE_V1}/me/square/connect", timeout=5))["url"] + ("&deny=1" if deny else "")
    r = requests.get(url, allow_redirects=False, timeout=10)
    assert r.status_code == 302, r.text
    q = parse_qs(urlparse(r.headers["Location"]).query)
    return {k: v[0] for k, v in q.items() if k in ("code", "state", "error")}


def _forward(key: str | None, body: dict) -> requests.Response:
    headers = {"Authorization": f"Bearer {key}"} if key is not None else {}
    return requests.post(FORWARD, json=body, headers=headers, timeout=15)


def test_relayed_callback_connects(admin, store, mgr, pkey) -> None:
    sq = _square_redirect(mgr)
    assert sq["code"] and sq["state"]
    r = _forward(pkey["key"], sq)
    assert r.status_code == 200, r.text
    out = r.json()
    assert out["result"] == "connected" and out["redirect"].endswith("/app/store/payments?square=connected")
    assert sq["code"] not in r.text
    st = _ok(mgr.get(f"{STORE_V1}/me/square", timeout=10))
    assert st["connected"] and st["square_location_id"]
    # The stored (sealed) access token opens: the storefront gets the SDK setup.
    web = _channel(admin, store, "web")
    pay = _ok(requests.get(f"{CH_V1}/payments", headers=web, timeout=5))
    assert pay["card"]["location_id"] == st["square_location_id"]
    # Square's codes are single use: a replay connects nothing new.
    again = _forward(pkey["key"], sq)
    assert again.status_code == 502 and again.json()["code"] == "square_error" and again.json()["result"] == "error"


def test_refused_without_the_platform_key(admin, store, mgr, pkey) -> None:
    sq = _square_redirect(mgr)
    chan_key = _channel(admin, store, "web")["Authorization"].removeprefix("Bearer ")
    other = admin.post(f"{ADMIN_V1}/platform-keys", json={"name": "revoked relay"}, timeout=5).json()
    admin.post(f"{ADMIN_V1}/platform-keys/{other['platform_key']['id']}/revoke", timeout=5)
    for what, key in {"no key": None, "empty": "", "wrong": "qpk_" + "0" * 64, "revoked": other["key"],
                      "channel key": chan_key, "admin key": admin.headers["Authorization"].removeprefix("Bearer ")}.items():
        r = _forward(key, sq)
        assert r.status_code == 401, f"{what}: {r.status_code} {r.text}"
        assert r.json()["code"] == "unauthorized" and "redirect" not in r.json(), what
    assert _ok(mgr.get(f"{STORE_V1}/me/square", timeout=10))["connected"] is False
    # Nothing was exchanged: the same code + state still connect with the right key.
    r = _forward(pkey["key"], sq)
    assert r.status_code == 200 and r.json()["result"] == "connected", r.text


def test_bad_states_are_refused(store, mgr, pkey) -> None:
    sq = _square_redirect(mgr)
    loc, exp, mac = sq["state"].split(".")
    other_store = "00000000-0000-4000-8000-000000000000"
    for what, state in {
        "garbage": "nope",
        "another store, same signature": f"{other_store}.{exp}.{mac}",
        "a later expiry": f"{loc}.{int(exp) + 3600}.{mac}",
        "an expired time": f"{loc}.1000000000.{mac}",
        "a forged signature": f"{loc}.{exp}.{'A' * len(mac)}",
    }.items():
        r = _forward(pkey["key"], {"code": sq["code"], "state": state})
        assert r.status_code == 400, f"{what}: {r.status_code} {r.text}"
        body = r.json()
        assert body["code"] == "bad_state" and body["result"] == "expired", what
        assert body["redirect"].endswith("/app/store/payments?square=expired"), what
    # Malformed bodies.
    assert _forward(pkey["key"], {"code": sq["code"]}).status_code == 400
    assert _forward(pkey["key"], {"code": "x" * 2000, "state": sq["state"]}).status_code == 400
    assert _ok(mgr.get(f"{STORE_V1}/me/square", timeout=10))["connected"] is False
    # The honest state still works.
    assert _forward(pkey["key"], sq).json()["result"] == "connected"


def test_owner_said_no(mgr, pkey) -> None:
    sq = _square_redirect(mgr, deny=True)
    assert sq.get("error") == "access_denied" and "code" not in sq
    r = _forward(pkey["key"], sq)
    assert r.status_code == 200, r.text
    assert r.json()["result"] == "denied" and r.json()["redirect"].endswith("square=denied")
    assert _ok(mgr.get(f"{STORE_V1}/me/square", timeout=10))["connected"] is False
