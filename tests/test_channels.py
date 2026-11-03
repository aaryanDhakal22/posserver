"""Sales channels (main 3.0, PORTS-PLAN.md): admin-managed channels of a
store, each with its own /channel/v1 key; every /channel/v1 answer is checked
against the OpenAPI document (openapi.py).

  * admin: create (key shown once, defaults per kind), list, get, update
    permissions, rotate (old key dies, revoked channel comes back), revoke,
    delete; bad input
  * /channel/v1/me with the key; missing / wrong / revoked / rotated keys
  * a channel key opens nothing else (orders, admin, store, agent)
"""

from __future__ import annotations

import uuid

import requests

from conftest import ADMIN_V1, API_V1, MAIN_BASE_URL, STORE_V1
from openapi import check, spec

CH_V1 = f"{MAIN_BASE_URL}/channel/v1"


def _bearer(key: str) -> dict[str, str]:
    return {"Authorization": f"Bearer {key}"}


def _create(admin, store, kind: str = "web", name: str = "Website", **extra) -> requests.Response:
    return admin.post(f"{ADMIN_V1}/channels", json={"location_id": store.location_id, "kind": kind, "name": name, **extra}, timeout=5)


def test_spec_is_served_and_valid() -> None:
    s = spec()
    assert s["openapi"].startswith("3.1")
    ops = {op["operationId"] for item in s["paths"].values() for op in item.values() if isinstance(op, dict)}
    assert {"getMe", "getSpec"} <= ops
    r = requests.get(f"{CH_V1}/openapi.json", timeout=5)
    assert r.status_code == 200 and r.headers["Content-Type"].startswith("application/json")


def test_create_and_me(admin, store) -> None:
    r = _create(admin, store)
    assert r.status_code == 201, r.text
    ch = r.json()
    assert ch["key"].startswith("qch_") and ch["key_prefix"] == ch["key"][:12] and ch["active"] is True
    assert ch["kind"] == "web" and ch["builtin"] is False and ch["location_id"] == store.location_id
    assert ch["permissions"] == {"menu": True, "quote": True, "orders": True, "track": True, "payments": ["card"]}

    me = check(requests.get(f"{CH_V1}/me", headers=_bearer(ch["key"]), timeout=5), "getMe")
    assert me["channel"] == {"id": ch["id"], "kind": "web", "name": "Website", "permissions": ch["permissions"]}
    assert me["store"]["location_id"] == store.location_id and me["store"]["tenant_id"] == store.tenant_id
    assert me["store"]["timezone"] == "America/New_York"

    # The key is never shown again.
    listed = admin.get(f"{ADMIN_V1}/channels", params={"location_id": store.location_id}, timeout=5).json()
    assert [c["id"] for c in listed] == [ch["id"]] and "key" not in listed[0]
    assert "key" not in admin.get(f"{ADMIN_V1}/channels/{ch['id']}", timeout=5).json()


def test_defaults_per_kind(admin, store) -> None:
    want = {
        "operator": {"menu": True, "quote": True, "orders": True, "track": True, "payments": ["card", "cash"]},
        "counter": {"menu": True, "quote": True, "orders": True, "track": True, "payments": ["card", "cash"]},
        "vendor": {"menu": False, "quote": False, "orders": True, "track": True, "payments": ["card", "cash"]},
    }
    for kind, perms in want.items():
        r = _create(admin, store, kind=kind, name=kind.title())
        assert r.status_code == 201, r.text
        assert r.json()["permissions"] == perms, kind
    custom = _create(admin, store, kind="web", name="Kiosk site",
                     permissions={"menu": True, "quote": True, "orders": False, "track": False, "payments": []})
    assert custom.status_code == 201 and custom.json()["permissions"]["orders"] is False


def test_bad_input(admin, store) -> None:
    assert _create(admin, store, kind="fax").status_code == 400
    assert _create(admin, store, name="  ").status_code == 400
    assert _create(admin, store, permissions={"menu": True, "quote": True, "orders": True, "track": True,
                                                "payments": ["bitcoin"]}).status_code == 400
    r = admin.post(f"{ADMIN_V1}/channels", json={"location_id": str(uuid.uuid4()), "kind": "web", "name": "x"}, timeout=5)
    assert r.status_code == 404
    assert admin.post(f"{ADMIN_V1}/channels", json={"location_id": "nope", "kind": "web", "name": "x"}, timeout=5).status_code == 400
    assert admin.get(f"{ADMIN_V1}/channels/{uuid.uuid4()}", timeout=5).status_code == 404
    assert admin.get(f"{ADMIN_V1}/channels", params={"location_id": "nope"}, timeout=5).status_code == 400
    assert requests.get(f"{ADMIN_V1}/channels", timeout=5).status_code == 401


def test_unknown_missing_and_wrong_keys() -> None:
    check(requests.get(f"{CH_V1}/me", timeout=5), "getMe")
    for bad in ("nope", "qch_" + "0" * 64, "qopr_" + "0" * 64):
        r = check(requests.get(f"{CH_V1}/me", headers=_bearer(bad), timeout=5), "getMe")
        assert r["code"] == "unauthorized"


def test_update_rotate_revoke_delete(admin, store) -> None:
    ch = _create(admin, store, kind="counter", name="Front counter").json()
    cid, k1 = ch["id"], ch["key"]

    r = admin.patch(f"{ADMIN_V1}/channels/{cid}", json={"name": "Counter 1"}, timeout=5)
    assert r.status_code == 200 and r.json()["name"] == "Counter 1" and r.json()["permissions"]["payments"] == ["card", "cash"]
    perms = {"menu": True, "quote": True, "orders": True, "track": True, "payments": ["cash"]}
    r = admin.patch(f"{ADMIN_V1}/channels/{cid}", json={"permissions": perms}, timeout=5)
    assert r.status_code == 200 and r.json()["permissions"] == perms
    assert admin.patch(f"{ADMIN_V1}/channels/{cid}", json={"name": ""}, timeout=5).status_code == 400
    assert check(requests.get(f"{CH_V1}/me", headers=_bearer(k1), timeout=5), "getMe")["channel"]["permissions"] == perms

    rot = admin.post(f"{ADMIN_V1}/channels/{cid}/rotate", timeout=5)
    assert rot.status_code == 200
    k2 = rot.json()["key"]
    assert k2 != k1 and rot.json()["key_prefix"] == k2[:12]
    assert requests.get(f"{CH_V1}/me", headers=_bearer(k1), timeout=5).status_code == 401, "the old key dies at once"
    assert requests.get(f"{CH_V1}/me", headers=_bearer(k2), timeout=5).status_code == 200

    r = admin.post(f"{ADMIN_V1}/channels/{cid}/revoke", timeout=5)
    assert r.status_code == 200 and r.json()["active"] is False and r.json()["revoked_at"]
    assert admin.post(f"{ADMIN_V1}/channels/{cid}/revoke", timeout=5).status_code == 200, "idempotent"
    assert requests.get(f"{CH_V1}/me", headers=_bearer(k2), timeout=5).status_code == 401

    k3 = admin.post(f"{ADMIN_V1}/channels/{cid}/rotate", timeout=5).json()["key"]
    assert requests.get(f"{CH_V1}/me", headers=_bearer(k3), timeout=5).status_code == 200, "a new key reactivates it"

    assert admin.delete(f"{ADMIN_V1}/channels/{cid}", timeout=5).status_code == 204
    assert admin.delete(f"{ADMIN_V1}/channels/{cid}", timeout=5).status_code == 404
    assert requests.get(f"{CH_V1}/me", headers=_bearer(k3), timeout=5).status_code == 401


def test_channel_key_opens_nothing_else(admin, store) -> None:
    key = _create(admin, store).json()["key"]
    h = _bearer(key)
    assert requests.get(f"{API_V1}/orders", headers={"X-STORE-API-KEY": key}, timeout=5).status_code == 401
    assert requests.get(f"{ADMIN_V1}/overview", headers=h, timeout=5).status_code == 401
    assert requests.get(f"{STORE_V1}/me/location", headers=h, timeout=5).status_code == 401
    assert requests.get(f"{STORE_V1}/operator/me", headers=h, timeout=5).status_code == 401
    assert requests.get(f"{STORE_V1}/store-config", headers={"X-STORE-API-KEY": key}, timeout=5).status_code == 401


def test_channels_go_with_their_store(admin, make_store) -> None:
    a = make_store()
    ch = _create(admin, a).json()
    a.delete()
    assert admin.get(f"{ADMIN_V1}/channels/{ch['id']}", timeout=5).status_code == 404
    assert requests.get(f"{CH_V1}/me", headers=_bearer(ch["key"]), timeout=5).status_code == 401
