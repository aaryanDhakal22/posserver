"""Admin's per-store "kitchen offline" badge (GET /admin/v1/kitchen): the same
Pi liveness check that refuses channel orders with 409 kitchen_offline.

The retired-Pi rule (DURABILITY-PLAN.md, deploy/pi/RECOVERY.md): a Pi whose
agent key is still active but which never connects keeps the store "kitchen
offline" (every ASAP online order refused) until its key is revoked. Each test
has its own store; nothing here pages (no channel order is placed).
"""
from __future__ import annotations

import requests

from conftest import ADMIN_V1, wait_until


def _row(admin: requests.Session, store) -> dict | None:
    r = admin.get(f"{ADMIN_V1}/kitchen", timeout=5)
    assert r.status_code == 200, r.text
    return next((x for x in r.json() if x["location_id"] == store.location_id), None)


def _offline(admin, store) -> bool:
    row = _row(admin, store)
    return bool(row and row["kitchen_offline"])


def test_kitchen_status_needs_the_admin_key(store) -> None:
    assert requests.get(f"{ADMIN_V1}/kitchen", timeout=5).status_code == 401


def test_kitchen_offline_until_the_retired_pis_key_is_revoked(admin, store) -> None:
    # No Pi set up: listed, never checked.
    row = _row(admin, store)
    assert row == {"tenant_id": store.tenant_id, "location_id": store.location_id,
                   "kitchen_offline": False, "away_since": None}

    # A Pi key that never connects (a retired Pi whose key was never revoked).
    # Away ≥ 30 s counts as offline; right after main restarts nothing is away
    # that long yet, so this polls (on a main that's been up: at once).
    retired = store.mint_key("retired-pi")
    wait_until(lambda: _offline(admin, store), timeout=40, what="kitchen_offline badge")
    assert _row(admin, store)["away_since"]

    # A replacement Pi (its own key) connects: liveness is per store, so its
    # live stream clears the badge.
    s = store.online()
    assert s.next()["event"] == "store_config"
    wait_until(lambda: not _offline(admin, store), timeout=10, what="badge clears while the Pi streams")

    # Revoking the retired key is the rule; with the live Pi gone too, revoke
    # leaves no active key, so the store is no longer checked at all.
    r = admin.post(f"{ADMIN_V1}/devices/{retired['device_id']}/revoke", timeout=5)
    assert r.status_code in (200, 204), r.text
    r = admin.post(f"{ADMIN_V1}/devices/{store.device_id}/revoke", timeout=5)
    assert r.status_code in (200, 204), r.text
    assert not _offline(admin, store)
    assert _row(admin, store)["away_since"] is None
