"""Golden chapter: the admin surface (/admin/v1) on throwaway tenants —
tenants, locations, devices, operators, app releases, the agent commands
with no agent connected, and the read lists (filtered to this chapter's
rows; global aggregates recorded as shapes)."""

from __future__ import annotations

import secrets

import requests

from conftest import Store
from golden.recorder import Rec
from golden.util import keys, only, shape

APK = "application/vnd.android.package-archive"


def run(rec: Rec, admin: requests.Session, _request) -> None:
    a, b = Store(admin), Store(admin)
    try:
        _run(rec, admin, a, b)
    finally:
        a.delete()
        b.delete()


def _run(rec: Rec, admin: requests.Session, a: Store, b: Store) -> None:
    for s, n in ((a, "a"), (b, "b")):
        rec.alias(s.tag, f"tag-{n}")
        rec.alias(s.store_id, f"store_id-{n}")
        rec.alias(s.tenant_id, f"tenant-{n}")
        rec.alias(s.location_id, f"location-{n}")
    A, L = a.tenant_id, a.location_id

    # Tenants.
    rec.call("tenants list", "GET", "/admin/v1/tenants", session=admin, filt=only("id", A))
    tag = secrets.token_hex(4)
    rec.alias(tag, "tag-c")
    r = rec.call("tenant create", "POST", "/admin/v1/tenants", session=admin,
                 json_body={"slug": f"t-{tag}", "name": f"{tag} Test Pizza"})
    c = r.json()["id"]
    rec.call("tenant create dup slug", "POST", "/admin/v1/tenants", session=admin, json_body={"slug": f"t-{tag}", "name": "x"})
    rec.call("tenant create bad", "POST", "/admin/v1/tenants", session=admin, json_body={"slug": ""})
    rec.call("tenant config", "PATCH", f"/admin/v1/tenants/{A}/config", session=admin, json_body={"golden": 1})
    rec.call("tenant config unknown", "PATCH", "/admin/v1/tenants/00000000-0000-0000-0000-000000000000/config", session=admin, json_body={})
    user, pw = f"mgr{a.tag}", "golden-" + a.tag
    rec.alias(pw, "password-a")
    rec.call("tenant credentials", "PATCH", f"/admin/v1/tenants/{A}/credentials", session=admin,
             json_body={"username": user, "password": pw})
    rec.call("tenant credentials reserved", "PATCH", f"/admin/v1/tenants/{A}/credentials", session=admin,
             json_body={"username": "admin", "password": pw})
    rec.call("tenant credentials taken", "PATCH", f"/admin/v1/tenants/{b.tenant_id}/credentials", session=admin,
             json_body={"username": user, "password": pw})
    rec.call("tenant store-id taken", "PATCH", f"/admin/v1/tenants/{A}/store-id", session=admin, json_body={"store_id": b.store_id})
    rec.call("tenant store-id bad", "PATCH", f"/admin/v1/tenants/{A}/store-id", session=admin, json_body={"store_id": 0})
    rec.call("tenant store-id clear", "PATCH", f"/admin/v1/tenants/{c}/store-id", session=admin, json_body={"store_id": None})
    rec.call("tenant store-id set", "PATCH", f"/admin/v1/tenants/{A}/store-id", session=admin, json_body={"store_id": a.store_id})
    rec.call("tenant store-session", "POST", f"/admin/v1/tenants/{A}/store-session", session=admin)
    rec.call("tenant delete", "DELETE", f"/admin/v1/tenants/{c}", session=admin)
    rec.call("tenant delete again", "DELETE", f"/admin/v1/tenants/{c}", session=admin)

    # Locations.
    rec.call("locations list", "GET", "/admin/v1/locations", session=admin, filt=only("id", L))
    rec.call("location patch", "PATCH", f"/admin/v1/locations/{L}", session=admin,
             json_body={"name": "Golden Main", "config": {"golden": True}})
    rec.call("location pin", "PUT", f"/admin/v1/locations/{L}/pin", session=admin, json_body={"lat": 40.6650, "lng": -74.1180})
    rec.call("location pin bad", "PUT", f"/admin/v1/locations/{L}/pin", session=admin, json_body={"lat": 400, "lng": 0})
    rec.call("location map bad", "PATCH", f"/admin/v1/locations/{L}/map", session=admin, json_body={"url": "ftp://nope"})
    rec.call("location map clear", "PATCH", f"/admin/v1/locations/{L}/map", session=admin, json_body={"url": ""})

    # Devices.
    r = rec.call("device mint", "POST", "/admin/v1/devices", session=admin, json_body={"location_id": L, "name": "golden-agent"})
    dev = r.json()["device"]["id"]
    rec.call("device mint bad", "POST", "/admin/v1/devices", session=admin, json_body={"location_id": "nope"})
    rec.call("devices list", "GET", "/admin/v1/devices", session=admin, filt=only("location_id", L))
    rec.call("device revoke", "POST", f"/admin/v1/devices/{dev}/revoke", session=admin)
    rec.call("device revoke again", "POST", f"/admin/v1/devices/{dev}/revoke", session=admin)
    rec.call("device delete", "DELETE", f"/admin/v1/devices/{dev}", session=admin)
    rec.call("device delete unknown", "DELETE", f"/admin/v1/devices/{dev}", session=admin)

    # Operators.
    op_user = f"op{a.tag}"
    r = rec.call("operator create", "POST", "/admin/v1/operators", session=admin,
                 json_body={"username": op_user, "password": "golden-op-pw", "display_name": "Golden Operator", "location_ids": [L]})
    op = r.json()["id"]
    rec.call("operator create dup", "POST", "/admin/v1/operators", session=admin,
             json_body={"username": op_user, "password": "golden-op-pw", "location_ids": [L]})
    rec.call("operators list", "GET", "/admin/v1/operators", session=admin, filt=only("id", op))
    rec.call("operator patch", "PATCH", f"/admin/v1/operators/{op}", session=admin, json_body={"display_name": "Golden Op 2"})
    rec.call("operator locations", "PUT", f"/admin/v1/operators/{op}/locations", session=admin, json_body={"location_ids": [L, b.location_id]})
    rec.call("operator revoke tokens", "POST", f"/admin/v1/operators/{op}/tokens/revoke", session=admin)
    rec.call("operator delete", "DELETE", f"/admin/v1/operators/{op}", session=admin)
    rec.call("operator delete unknown", "DELETE", f"/admin/v1/operators/{op}", session=admin)

    # Tablet tokens, backups, updates, diagnostics, delivery runs, orders (empty for a new store).
    rec.call("tablet tokens", "GET", "/admin/v1/tablet-tokens", session=admin, params={"location_id": L})
    rec.call("tablet token revoke unknown", "POST", "/admin/v1/tablet-tokens/00000000-0000-0000-0000-000000000000/revoke", session=admin)
    rec.call("agent backups", "GET", "/admin/v1/agent-backups", session=admin, params={"location_id": L})
    rec.call("agent backup unknown", "GET", "/admin/v1/agent-backups/00000000-0000-0000-0000-000000000000", session=admin)
    rec.call("agent updates", "GET", "/admin/v1/agent-updates", session=admin, params={"location_id": L})
    rec.call("diag reports", "GET", "/admin/v1/diag-reports", session=admin, params={"tenant_id": A})
    rec.call("diag report unknown", "GET", "/admin/v1/diag-reports/00000000-0000-0000-0000-000000000000", session=admin)
    rec.call("diag report patch unknown", "PATCH", "/admin/v1/diag-reports/00000000-0000-0000-0000-000000000000",
             session=admin, json_body={"resolved": True})
    rec.call("delivery runs", "GET", "/admin/v1/delivery-runs", session=admin, params={"tenant_id": A, "location_id": L})
    rec.call("orders", "GET", "/admin/v1/orders", session=admin, params={"tenant_id": A})
    rec.call("orders delete tenant", "DELETE", "/admin/v1/orders", session=admin, params={"tenant_id": A})
    rec.call("order delete unknown", "DELETE", "/admin/v1/orders/00000000-0000-0000-0000-000000000000", session=admin)
    rec.call("agents health", "GET", "/admin/v1/agents/health", session=admin, filt=only("location_id", L))
    rec.call("overview", "GET", "/admin/v1/overview", session=admin, filt=lambda v: keys(v, 2))
    rec.call("geo", "GET", "/admin/v1/geo", session=admin, filt=shape)

    # Agent commands with no agent connected.
    scope = {"tenant_id": A, "location_id": L}
    rec.call("agent ping offline", "POST", "/admin/v1/agent/ping", session=admin, json_body=scope)
    rec.call("agent update offline", "POST", "/admin/v1/agent/update", session=admin, json_body={**scope, "force": True})
    rec.call("agent rollback offline", "POST", "/admin/v1/agent/rollback", session=admin, json_body={**scope, "requested_by": "console"})
    rec.call("agent update wrong scope", "POST", "/admin/v1/agent/update", session=admin,
             json_body={"tenant_id": A, "location_id": b.location_id})
    rec.call("agent updates after", "GET", "/admin/v1/agent-updates", session=admin, params={"location_id": L})

    # App releases under a throwaway app name.
    app = f"g{a.tag}"
    rec.alias(app, "app")
    apk = b"PK\x03\x04" + bytes(range(256)) * 8
    up = {"Content-Type": APK}
    rec.call("release upload", "POST", "/admin/v1/app-releases", session=admin,
             params={"app": app, "version_code": 10, "version_name": "1.0.10"}, data=apk, headers=up)
    rec.call("release upload not newer", "POST", "/admin/v1/app-releases", session=admin,
             params={"app": app, "version_code": 10, "version_name": "1.0.10"}, data=apk, headers=up)
    rec.call("release upload not apk", "POST", "/admin/v1/app-releases", session=admin,
             params={"app": app, "version_code": 11, "version_name": "1.0.11"}, data=b"nope", headers=up)
    rec.call("releases list", "GET", "/admin/v1/app-releases", session=admin, params={"app": app})
    rec.call("release delete", "DELETE", "/admin/v1/app-releases/10", session=admin, params={"app": app})
    rec.call("release delete again", "DELETE", "/admin/v1/app-releases/10", session=admin, params={"app": app})
