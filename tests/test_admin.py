"""Admin surface (/admin/v1): provisioning, device keys, tenant/location edits,
and the vendor store_id lifecycle."""

import secrets

import requests

from conftest import ADMIN_V1, Store, unique_store_id


def test_overview_shape(admin) -> None:
    r = admin.get(f"{ADMIN_V1}/overview", timeout=5)
    assert r.status_code == 200, r.text
    assert {"tenants", "locations", "devices"} <= set(r.json())


def test_mint_device_returns_cleartext_once(admin, store) -> None:
    r = admin.post(f"{ADMIN_V1}/devices", json={"location_id": store.location_id, "name": "mint-test"}, timeout=5)
    assert r.status_code == 201, r.text
    minted = r.json()
    assert minted["device"]["kind"] == "agent"
    assert minted["device"]["tenant_id"] == store.tenant_id
    assert minted["api_key"].startswith("qpos_") and len(minted["api_key"]) > 32
    assert minted["device"]["key_prefix"] == minted["api_key"][:12]

    listed = next(d for d in admin.get(f"{ADMIN_V1}/devices", timeout=5).json() if d["id"] == minted["device"]["id"])
    assert "api_key" not in listed and "key_hash" not in listed


def test_revoke_is_idempotent(admin, store) -> None:
    for _ in range(2):
        r = admin.post(f"{ADMIN_V1}/devices/{store.device_id}/revoke", timeout=5)
        assert r.status_code == 204, r.text


def test_update_tenant_config(admin, store) -> None:
    cfg = {"dark_mode": True, "locale": "en-US"}
    r = admin.patch(f"{ADMIN_V1}/tenants/{store.tenant_id}/config", json=cfg, timeout=5)
    assert r.status_code == 200, r.text
    assert r.json()["config"] == cfg


def test_update_location(admin, store) -> None:
    updated = {
        "name": "Renamed", "timezone": "America/Los_Angeles", "daily_reset_hour": 3,
        "phone": "415-555-0001", "address_line1": "1 Market St", "address_line2": "San Francisco, CA",
        "config": {"tax_rate_bps": 925, "receipt_footer": "THANK YOU"},
    }
    r = admin.patch(f"{ADMIN_V1}/locations/{store.location_id}", json=updated, timeout=5)
    assert r.status_code == 200, r.text
    for k, v in updated.items():
        assert r.json()[k] == v, k


# --- vendor store_id -------------------------------------------------------


def _tenant(admin, tenant_id: str) -> dict:
    return next(t for t in admin.get(f"{ADMIN_V1}/tenants", timeout=5).json() if t["id"] == tenant_id)


def test_create_with_store_id_is_exposed(admin, store) -> None:
    assert _tenant(admin, store.tenant_id)["store_id"] == store.store_id


def test_set_and_clear_store_id(admin, make_store) -> None:
    s = make_store(with_store_id=False)
    assert _tenant(admin, s.tenant_id)["store_id"] is None
    new_id = unique_store_id()
    r = admin.patch(f"{ADMIN_V1}/tenants/{s.tenant_id}/store-id", json={"store_id": new_id}, timeout=5)
    assert r.status_code == 200, r.text
    assert r.json()["store_id"] == new_id
    r = admin.patch(f"{ADMIN_V1}/tenants/{s.tenant_id}/store-id", json={"store_id": None}, timeout=5)
    assert r.status_code == 200, r.text
    assert r.json()["store_id"] is None


def test_duplicate_store_id_conflicts(admin, make_store) -> None:
    a, b = make_store(), make_store()
    r = admin.patch(f"{ADMIN_V1}/tenants/{b.tenant_id}/store-id", json={"store_id": a.store_id}, timeout=5)
    assert r.status_code == 409, r.text
    # …and on create.
    r = admin.post(f"{ADMIN_V1}/tenants", json={"slug": f"t-dupe{secrets.token_hex(3)}",
                                                "name": "dupe Test Pizza", "store_id": a.store_id}, timeout=5)
    assert r.status_code == 409, r.text


def test_non_positive_store_id_rejected(admin, store) -> None:
    for bad in (0, -7):
        r = admin.patch(f"{ADMIN_V1}/tenants/{store.tenant_id}/store-id", json={"store_id": bad}, timeout=5)
        assert r.status_code == 400, r.text


def test_store_id_unknown_tenant_404(admin) -> None:
    r = admin.patch(f"{ADMIN_V1}/tenants/00000000-0000-0000-0000-0000000000aa/store-id", json={"store_id": 5}, timeout=5)
    assert r.status_code == 404, r.text


def test_store_id_survives_unrelated_edits(admin, store) -> None:
    admin.patch(f"{ADMIN_V1}/tenants/{store.tenant_id}/config", json={"x": 1}, timeout=5)
    assert _tenant(admin, store.tenant_id)["store_id"] == store.store_id


def test_deleting_tenant_frees_its_store_id(admin) -> None:
    s = Store(admin)
    sid = s.store_id
    s.delete()
    slug = f"t-reuse{secrets.token_hex(3)}"
    r = admin.post(f"{ADMIN_V1}/tenants", json={"slug": slug, "name": "reuse Test Pizza", "store_id": sid}, timeout=5)
    try:
        assert r.status_code == 201, r.text
    finally:
        if r.status_code == 201:
            requests.delete(f"{ADMIN_V1}/tenants/{r.json()['id']}", headers=admin.headers, timeout=5)


# --- one tenant = one restaurant = one location ------------------------------


def test_tenant_create_returns_its_location(admin, store) -> None:
    # The Store fixture reads the location from the create response.
    assert store.location_id
    locs = [l for l in admin.get(f"{ADMIN_V1}/locations", timeout=5).json() if l["tenant_id"] == store.tenant_id]
    assert [l["id"] for l in locs] == [store.location_id], "exactly one location per tenant"
    assert locs[0]["slug"] == "main" and locs[0]["name"] == store.name
    assert locs[0]["timezone"] == "America/New_York"


def test_location_create_and_delete_routes_are_gone(admin, store) -> None:
    r = admin.post(f"{ADMIN_V1}/locations", json={"tenant_id": store.tenant_id, "slug": "second", "name": "x"}, timeout=5)
    assert r.status_code in (404, 405), r.text
    r = admin.delete(f"{ADMIN_V1}/locations/{store.location_id}", timeout=5)
    assert r.status_code in (404, 405), r.text


def test_location_is_still_editable(admin, store) -> None:
    r = admin.patch(f"{ADMIN_V1}/locations/{store.location_id}", json={
        "name": "Edited", "timezone": "America/Chicago", "daily_reset_hour": 4,
        "phone": "", "address_line1": "", "address_line2": "", "config": {},
    }, timeout=5)
    assert r.status_code == 200 and r.json()["timezone"] == "America/Chicago", r.text
