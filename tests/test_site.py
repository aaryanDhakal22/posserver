"""Store websites (weborder stage 2): the storefront is one server for every
store. It signs in with a platform key (qpk_…) and names the host it is
serving (X-Site-Host); main maps a verified host to its store and answers as
that store's built-in web channel.

  * admin: platform keys (key once, revoke stops it at once), hosts per store
    (platform subdomains verified at once, unique across stores), verify
  * store manager: add the store's own domain (unverified, never resolves
    until verified), primary, remove; platform subdomains are admin-only
  * /channel/v1 with the platform key: host → the store's web channel (menu,
    store, site), unknown / unverified host, missing host, revoked key
  * GET /channel/v1/site: hosts, version, ETag → 304
  * a host never reaches another store
"""

from __future__ import annotations

import secrets

import pytest
import requests

from conftest import ADMIN_V1, API_V1, MAIN_BASE_URL, STORE_V1
from openapi import check

CH_V1 = f"{MAIN_BASE_URL}/channel/v1"


@pytest.fixture
def platform_key(admin):
    r = admin.post(f"{ADMIN_V1}/platform-keys", json={"name": f"test storefront {secrets.token_hex(3)}"}, timeout=5)
    assert r.status_code == 201, r.text
    body = r.json()
    yield body
    admin.post(f"{ADMIN_V1}/platform-keys/{body['platform_key']['id']}/revoke", timeout=5)


def _site(key: str, host: str) -> dict[str, str]:
    return {"Authorization": f"Bearer {key}", "X-Site-Host": host}


def _platform_host(store) -> str:
    return f"t-{store.tag}.quiccpos.com"


def _add_host(admin, store, host: str, **extra) -> requests.Response:
    return admin.post(f"{ADMIN_V1}/locations/{store.location_id}/site-domains", json={"host": host, **extra}, timeout=5)


def test_platform_keys(admin, platform_key) -> None:
    key, pk = platform_key["key"], platform_key["platform_key"]
    assert key.startswith("qpk_") and pk["key_prefix"] == key[:12] and pk["revoked_at"] is None
    listed = admin.get(f"{ADMIN_V1}/platform-keys", timeout=5).json()["keys"]
    mine = [k for k in listed if k["id"] == pk["id"]]
    assert len(mine) == 1 and "key" not in mine[0] and "key_hash" not in mine[0]
    assert admin.post(f"{ADMIN_V1}/platform-keys", json={"name": " "}, timeout=5).status_code == 400
    assert admin.post(f"{ADMIN_V1}/platform-keys/{secrets.token_hex(16)[:8]}/revoke", timeout=5).status_code == 400
    # Platform keys are admin-only.
    assert requests.get(f"{ADMIN_V1}/platform-keys", timeout=5).status_code == 401


def test_storefront_opens_the_store_by_host(admin, store, platform_key) -> None:
    host = _platform_host(store)
    r = _add_host(admin, store, host.upper() + ".")  # normalised
    assert r.status_code == 201, r.text
    d = r.json()
    assert d["host"] == host and d["verified"] is True and d["primary"] is True  # first host = primary

    # The store's built-in web channel exists as soon as it has a host.
    chans = admin.get(f"{ADMIN_V1}/channels", params={"location_id": store.location_id}, timeout=5).json()
    web = [c for c in chans if c["kind"] == "web" and c["builtin"]]
    assert len(web) == 1 and web[0]["name"] == "Website"

    key = platform_key["key"]
    me = check(requests.get(f"{CH_V1}/me", headers=_site(key, host), timeout=5), "getMe")
    assert me["channel"]["id"] == web[0]["id"] and me["channel"]["kind"] == "web"
    assert me["store"]["location_id"] == store.location_id
    # A port and a scheme on the header are ignored.
    assert requests.get(f"{CH_V1}/me", headers=_site(key, f"https://{host}:443"), timeout=5).status_code == 200
    check(requests.get(f"{CH_V1}/store", headers=_site(key, host), timeout=5), "getStore")

    # The web channel's permissions are the admin's to change (card + cash for Lombardi's).
    r = admin.patch(f"{ADMIN_V1}/channels/{web[0]['id']}", json={"permissions": {
        "menu": True, "quote": True, "orders": True, "track": True, "payments": ["card", "cash"]}}, timeout=5)
    assert r.status_code == 200, r.text
    me = requests.get(f"{CH_V1}/me", headers=_site(key, host), timeout=5).json()
    assert me["channel"]["permissions"]["payments"] == ["card", "cash"]


def test_storefront_refusals(admin, store, platform_key) -> None:
    key = platform_key["key"]
    host = _platform_host(store)
    assert _add_host(admin, store, host).status_code == 201

    r = requests.get(f"{CH_V1}/me", headers={"Authorization": f"Bearer {key}"}, timeout=5)
    assert r.status_code == 400 and r.json()["code"] == "host_required"
    r = requests.get(f"{CH_V1}/me", headers=_site(key, f"nobody-{store.tag}.quiccpos.com"), timeout=5)
    assert r.status_code == 404 and r.json()["code"] == "unknown_site"
    r = requests.get(f"{CH_V1}/me", headers=_site("qpk_" + "0" * 64, host), timeout=5)
    assert r.status_code == 401 and r.json()["code"] == "unauthorized"

    # A revoked platform key stops at once.
    other = admin.post(f"{ADMIN_V1}/platform-keys", json={"name": "test revoke"}, timeout=5).json()
    assert requests.get(f"{CH_V1}/me", headers=_site(other["key"], host), timeout=5).status_code == 200
    r = admin.post(f"{ADMIN_V1}/platform-keys/{other['platform_key']['id']}/revoke", timeout=5)
    assert r.status_code == 200 and r.json()["revoked_at"]
    assert requests.get(f"{CH_V1}/me", headers=_site(other["key"], host), timeout=5).status_code == 401

    # The platform key opens nothing but /channel/v1.
    for url in (f"{STORE_V1}/me/menu/bank", f"{ADMIN_V1}/tenants", f"{API_V1}/orders"):
        assert requests.get(url, headers=_site(key, host), timeout=5).status_code in (401, 403), url


def test_hosts_admin_and_manager(admin, make_store, platform_key) -> None:
    a, b = make_store(), make_store()
    key = platform_key["key"]
    host_a = _platform_host(a)
    assert _add_host(admin, a, host_a).status_code == 201
    # A host belongs to one store.
    r = _add_host(admin, b, host_a)
    assert r.status_code == 409 and r.json()["code"] == "host_taken"
    assert _add_host(admin, b, "not a host").status_code == 400

    mgr = a.manager_session()
    # Platform subdomains are admin-only.
    r = mgr.post(f"{STORE_V1}/me/site/domains", json={"host": f"x-{a.tag}.quiccpos.com"}, timeout=5)
    assert r.status_code == 403 and r.json()["code"] == "admin_only"
    # The store's own domain starts unverified and never resolves until verified.
    own = f"order-{a.tag}.example.com"
    r = mgr.post(f"{STORE_V1}/me/site/domains", json={"host": own}, timeout=5)
    assert r.status_code == 201, r.text
    assert r.json()["verified"] is False and r.json()["primary"] is False
    assert requests.get(f"{CH_V1}/me", headers=_site(key, own), timeout=5).status_code == 404
    r = admin.patch(f"{ADMIN_V1}/site-domains/{own}", json={"verified": True}, timeout=5)
    assert r.status_code == 200 and r.json()["verified"] is True
    me = requests.get(f"{CH_V1}/me", headers=_site(key, own), timeout=5).json()
    assert me["store"]["location_id"] == a.location_id

    # Primary moves; the list puts it first.
    r = mgr.post(f"{STORE_V1}/me/site/domains/{own}/primary", timeout=5)
    assert r.status_code == 200, r.text
    ds = r.json()["domains"]
    assert [d["host"] for d in ds] == [own, host_a] and [d["primary"] for d in ds] == [True, False]
    assert r.json()["base_domain"] == "quiccpos.com"

    # Another store's manager can't touch a's hosts.
    mgr_b = b.manager_session()
    assert mgr_b.delete(f"{STORE_V1}/me/site/domains/{own}", timeout=5).status_code == 404
    assert mgr_b.post(f"{STORE_V1}/me/site/domains/{own}/primary", timeout=5).status_code == 404
    assert mgr_b.get(f"{STORE_V1}/me/site/domains", timeout=5).json()["domains"] == []

    # Removing a host stops it at once.
    assert mgr.delete(f"{STORE_V1}/me/site/domains/{own}", timeout=5).status_code == 204
    assert requests.get(f"{CH_V1}/me", headers=_site(key, own), timeout=5).status_code == 404
    assert admin.delete(f"{ADMIN_V1}/locations/{a.location_id}/site-domains/{host_a}", timeout=5).status_code == 204
    assert requests.get(f"{CH_V1}/me", headers=_site(key, host_a), timeout=5).status_code == 404


def test_channel_site(admin, store, platform_key) -> None:
    key = platform_key["key"]
    host = _platform_host(store)
    assert _add_host(admin, store, host).status_code == 201
    mgr = store.manager_session()
    own = f"order-{store.tag}.example.com"
    assert mgr.post(f"{STORE_V1}/me/site/domains", json={"host": own}, timeout=5).status_code == 201

    r = requests.get(f"{CH_V1}/site", headers=_site(key, host), timeout=5)
    site = check(r, "getSite")
    # Only verified hosts are listed; nothing published yet.
    assert site == {"version": 0, "primary_host": host, "hosts": [host], "settings": {}, "email": ""}
    etag = r.headers["ETag"]
    r = requests.get(f"{CH_V1}/site", headers={**_site(key, host), "If-None-Match": etag}, timeout=5)
    assert r.status_code == 304
    check(r, "getSite")

    # A newly verified host changes the answer (and its ETag).
    admin.patch(f"{ADMIN_V1}/site-domains/{own}", json={"verified": True}, timeout=5)
    r = requests.get(f"{CH_V1}/site", headers={**_site(key, host), "If-None-Match": etag}, timeout=5)
    assert r.status_code == 200 and sorted(r.json()["hosts"]) == sorted([host, own])


def test_host_never_reaches_another_store(admin, make_store, platform_key) -> None:
    a, b = make_store(), make_store()
    key = platform_key["key"]
    ha, hb = _platform_host(a), _platform_host(b)
    assert _add_host(admin, a, ha).status_code == 201
    assert _add_host(admin, b, hb).status_code == 201
    me_a = requests.get(f"{CH_V1}/me", headers=_site(key, ha), timeout=5).json()
    me_b = requests.get(f"{CH_V1}/me", headers=_site(key, hb), timeout=5).json()
    assert me_a["store"]["location_id"] == a.location_id and me_b["store"]["location_id"] == b.location_id
    assert me_a["channel"]["id"] != me_b["channel"]["id"]
    # A deleted store's host is gone with it.
    a.delete()
    assert requests.get(f"{CH_V1}/me", headers=_site(key, ha), timeout=5).status_code == 404
