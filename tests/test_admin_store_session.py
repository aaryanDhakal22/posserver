"""Admin "open as store": POST /admin/v1/tenants/:id/store-session sets a 2h
impersonated store-session cookie scoped to that one tenant — plus the
store-username rules that keep the "admin" label unambiguous (reserved name,
unique usernames → 409)."""

from __future__ import annotations

import secrets
import uuid

import requests

from conftest import ADMIN_API_KEY, ADMIN_V1, STORE_V1


def _bearer(key: str = ADMIN_API_KEY) -> dict[str, str]:
    return {"Authorization": f"Bearer {key}"}


def open_store(tenant_id: str) -> tuple[requests.Response, requests.Session]:
    """POST the open-as-store route from a fresh browser-like session (bearer on
    this one request only, so the session keeps just the cookie it received).
    Never uses the shared `admin` session — its jar would pick up the cookie."""
    browser = requests.Session()
    r = browser.post(f"{ADMIN_V1}/tenants/{tenant_id}/store-session", headers=_bearer(), timeout=5)
    return r, browser


def test_requires_admin_key(store) -> None:
    url = f"{ADMIN_V1}/tenants/{store.tenant_id}/store-session"
    assert requests.post(url, timeout=5).status_code == 401
    r = requests.post(url, headers=_bearer("not-the-admin-key"), timeout=5)
    assert r.status_code == 401, r.text


def test_unknown_tenant_is_404(admin) -> None:
    r, _ = open_store(str(uuid.uuid4()))
    assert r.status_code == 404, r.text
    assert r.json() == {"error": "tenant not found"}


def test_bad_tenant_id_is_400(admin) -> None:
    r, _ = open_store("not-a-uuid")
    assert r.status_code == 400, r.text


def test_open_store_session(store) -> None:
    r, browser = open_store(store.tenant_id)
    assert r.status_code == 200, r.text
    assert r.json() == {
        "tenant_id": store.tenant_id,
        "location_id": store.location_id,
        "tenant_name": store.name,
        "expires_in_seconds": 7200,
    }
    assert any(c.name == "quicc_store_session" for c in browser.cookies), "no session cookie set"

    who = browser.get(f"{STORE_V1}/session", timeout=5)
    assert who.status_code == 200, who.text
    body = who.json()
    assert body["tenant"]["id"] == store.tenant_id
    assert body["location"]["id"] == store.location_id
    assert body["impersonated"] is True
    assert body["impersonated_by"] == "admin"

    loc = browser.get(f"{STORE_V1}/me/location", timeout=5)
    assert loc.status_code == 200 and loc.json()["id"] == store.location_id, loc.text
    printers = browser.get(f"{STORE_V1}/me/printers", timeout=5)
    assert printers.status_code == 200, printers.text


def test_open_store_works_without_manager_login(store) -> None:
    """The tenant needn't have a username/password — that's the point."""
    tenants = requests.get(f"{ADMIN_V1}/tenants", headers=_bearer(), timeout=5).json()
    assert not next(t for t in tenants if t["id"] == store.tenant_id)["has_login"]
    r, browser = open_store(store.tenant_id)
    assert r.status_code == 200, r.text
    assert browser.get(f"{STORE_V1}/me/location", timeout=5).status_code == 200


def test_impersonated_cookie_is_scoped_to_its_tenant(make_store) -> None:
    a, b = make_store(), make_store()
    b_mgr = b.manager_session()
    r = b_mgr.post(f"{STORE_V1}/me/printers", json={"name": "B only", "ip": "10.0.0.9"}, timeout=5)
    assert r.status_code == 201, r.text
    b_printer = r.json()["id"]

    r, as_a = open_store(a.tenant_id)
    assert r.status_code == 200, r.text
    assert as_a.get(f"{STORE_V1}/session", timeout=5).json()["tenant"]["id"] == a.tenant_id
    assert as_a.get(f"{STORE_V1}/me/location", timeout=5).json()["id"] == a.location_id
    assert not any(p["id"] == b_printer for p in as_a.get(f"{STORE_V1}/me/printers", timeout=5).json())
    r = as_a.patch(f"{STORE_V1}/me/printers/{b_printer}", json={"name": "hijack", "ip": "1.1.1.1"}, timeout=5)
    assert r.status_code == 404, r.text
    # B's printer is untouched.
    mine = {p["id"]: p for p in b_mgr.get(f"{STORE_V1}/me/printers", timeout=5).json()}
    assert mine[b_printer]["name"] == "B only"


def test_manager_session_is_not_impersonated(store) -> None:
    who = store.manager_session().get(f"{STORE_V1}/session", timeout=5)
    assert who.status_code == 200, who.text
    assert who.json()["impersonated"] is False
    assert "impersonated_by" not in who.json()


# ---------------------------------------------------------------------------
# Store usernames: "admin" reserved, duplicates → 409
# ---------------------------------------------------------------------------


def _create_tenant(admin: requests.Session, made: list[str], **extra) -> requests.Response:
    """Create a tenant named like conftest's Store (so the session sweep would
    catch a leak) and remember it for cleanup."""
    tag = secrets.token_hex(4)
    r = admin.post(f"{ADMIN_V1}/tenants", json={"slug": f"t-{tag}", "name": f"{tag} Test Pizza", **extra}, timeout=5)
    if r.status_code == 201:
        made.append(r.json()["id"])
    return r


def _cleanup(admin: requests.Session, made: list[str]) -> None:
    for tid in made:
        try:
            admin.delete(f"{ADMIN_V1}/tenants/{tid}", timeout=10)
        except requests.RequestException:
            pass


def test_create_with_reserved_username_is_400(admin) -> None:
    made: list[str] = []
    try:
        for name in ("admin", "Admin", " ADMIN "):
            r = _create_tenant(admin, made, username=name, password="pw-123456")
            assert r.status_code == 400, (name, r.text)
            assert r.json() == {"error": 'username "admin" is reserved'}
    finally:
        _cleanup(admin, made)


def test_set_reserved_username_is_400(admin, store) -> None:
    r = admin.patch(f"{ADMIN_V1}/tenants/{store.tenant_id}/credentials",
                    json={"username": "Admin", "password": "pw-123456"}, timeout=5)
    assert r.status_code == 400, r.text
    assert r.json() == {"error": 'username "admin" is reserved'}


def test_duplicate_username_on_create_is_409(admin) -> None:
    made: list[str] = []
    username = f"dup{secrets.token_hex(4)}"
    try:
        r = _create_tenant(admin, made, username=username, password="pw-123456")
        assert r.status_code == 201, r.text
        r = _create_tenant(admin, made, username=username, password="pw-654321")
        assert r.status_code == 409, r.text
        assert r.json() == {"error": "username already taken"}
    finally:
        _cleanup(admin, made)


def test_duplicate_username_on_credentials_is_409(admin, make_store) -> None:
    a, b = make_store(), make_store()
    a.manager_session()  # gives A the login mgr<a.tag>
    r = admin.patch(f"{ADMIN_V1}/tenants/{b.tenant_id}/credentials",
                    json={"username": f"mgr{a.tag}", "password": "pw-123456"}, timeout=5)
    assert r.status_code == 409, r.text
    assert r.json() == {"error": "username already taken"}
