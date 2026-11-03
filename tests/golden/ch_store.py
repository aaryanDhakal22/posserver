"""Golden chapter: the store manager's session and /store/v1/me/* (location,
pin, alerts, printers, devices, the read-only orders / floor / runs) —
everything the SPA's store pages call, except the menu (ch_menu)."""

from __future__ import annotations

import requests

from conftest import Store
from golden.recorder import Rec

LOC_KEYS = ("name", "timezone", "daily_reset_hour", "phone", "address_line1", "address_line2", "config")


def run(rec: Rec, admin: requests.Session, _request) -> None:
    a = Store(admin)
    try:
        _run(rec, admin, a)
    finally:
        a.delete()


def _run(rec: Rec, admin: requests.Session, a: Store) -> None:
    rec.alias(a.tag, "tag")
    rec.alias(a.store_id, "store_id")
    user, pw = f"mgr{a.tag}", "golden-" + a.tag
    rec.alias(pw, "password")
    admin.patch(f"{rec.base}/admin/v1/tenants/{a.tenant_id}/credentials", json={"username": user, "password": pw}, timeout=5)

    mgr = requests.Session()
    rec.call("login wrong password", "POST", "/store/v1/session", session=mgr, json_body={"username": user, "password": "wrong"})
    rec.call("login", "POST", "/store/v1/session", session=mgr, json_body={"username": user, "password": pw})
    rec.call("whoami", "GET", "/store/v1/session", session=mgr)

    # Location (the PATCH is a full replace of the editable fields).
    loc = rec.call("location", "GET", "/store/v1/me/location", session=mgr).json()
    body = {k: loc[k] for k in LOC_KEYS}
    body.update(name="Golden Pizza", phone="4105550199", daily_reset_hour=4,
                config={**(loc.get("config") or {}), "opening_hours": {"mon": [{"open": "11:00", "close": "22:00"}],
                                                              "fri": [{"open": "11:00", "close": "14:00"}, {"open": "16:00", "close": "02:00"}]}})
    rec.call("location save", "PATCH", "/store/v1/me/location", session=mgr, json_body=body)
    rec.call("location save bad tz", "PATCH", "/store/v1/me/location", session=mgr, json_body={**body, "timezone": "Mars/Base"})
    rec.call("location save bad hours", "PATCH", "/store/v1/me/location", session=mgr,
             json_body={**body, "config": {"opening_hours": {"mon": [{"open": "25:00", "close": "22:00"}]}}})
    rec.call("location save bad device", "PATCH", "/store/v1/me/location", session=mgr,
             json_body={**body, "config": {**body["config"], "pushover_owner_devices": "bad device!"}})
    rec.call("location after", "GET", "/store/v1/me/location", session=mgr)
    rec.call("pin", "PUT", "/store/v1/me/location/pin", session=mgr, json_body={"lat": 40.6650, "lng": -74.1180})
    rec.call("pin bad", "PUT", "/store/v1/me/location/pin", session=mgr, json_body={"lat": 200, "lng": 0})
    rec.call("pin missing", "PUT", "/store/v1/me/location/pin", session=mgr, json_body={"lat": 40.1})
    rec.call("alerts test no devices", "POST", "/store/v1/me/alerts/test", session=mgr)

    # Printers (the first one becomes the receipt printer).
    p1 = rec.call("printer add", "POST", "/store/v1/me/printers", session=mgr, json_body={"name": "Pizza", "ip": "10.0.0.42"}).json()
    rec.call("printer add 2", "POST", "/store/v1/me/printers", session=mgr, json_body={"name": "Wings", "ip": "10.0.0.7"})
    rec.call("printer add dup name", "POST", "/store/v1/me/printers", session=mgr, json_body={"name": "Pizza", "ip": "10.0.0.9"})
    rec.call("printer add bad ip", "POST", "/store/v1/me/printers", session=mgr, json_body={"name": "X", "ip": "nope"})
    pid = p1.get("id") or p1.get("printer", {}).get("id")
    rec.call("printer edit", "PATCH", f"/store/v1/me/printers/{pid}", session=mgr, json_body={"name": "Pizza 2", "ip": "10.0.0.43"})
    rec.call("printers", "GET", "/store/v1/me/printers", session=mgr)
    rec.call("printers live (no agent)", "GET", "/store/v1/me/printers/live", session=mgr)
    rec.call("printer ip changes", "GET", "/store/v1/me/printers/ip-changes", session=mgr, params={"limit": 20})
    rec.call("printer delete", "DELETE", f"/store/v1/me/printers/{pid}", session=mgr)
    rec.call("printer delete again", "DELETE", f"/store/v1/me/printers/{pid}", session=mgr)
    rec.call("printers after", "GET", "/store/v1/me/printers", session=mgr)

    # Devices a manager may mint (never an agent).
    d = rec.call("device mobile", "POST", "/store/v1/me/devices", session=mgr, json_body={"name": "phone", "kind": "mobile"}).json()
    rec.call("device agent refused", "POST", "/store/v1/me/devices", session=mgr, json_body={"name": "sneaky", "kind": "agent"})
    rec.call("devices", "GET", "/store/v1/me/devices", session=mgr)
    rec.call("mobile key reads orders", "GET", "/api/v1/orders", headers={"X-STORE-API-KEY": d["api_key"]})
    rec.call("mobile key no sse", "GET", "/api/v1/events/orders", headers={"X-STORE-API-KEY": d["api_key"]})
    rec.call("mobile key no store-config", "GET", "/store/v1/store-config", headers={"X-STORE-API-KEY": d["api_key"]})
    rec.call("device revoke", "POST", f"/store/v1/me/devices/{d['device']['id']}/revoke", session=mgr)
    rec.call("revoked key", "GET", "/api/v1/orders", headers={"X-STORE-API-KEY": d["api_key"]})

    # Read-only views of a store with nothing yet.
    rec.call("orders", "GET", "/store/v1/me/orders", session=mgr)
    rec.call("floor", "GET", "/store/v1/me/floor", session=mgr)
    rec.call("delivery runs", "GET", "/store/v1/me/delivery-runs", session=mgr)

    # Admin open-as-store: the impersonated cookie.
    imp = requests.Session()
    r = admin.post(f"{rec.base}/admin/v1/tenants/{a.tenant_id}/store-session", timeout=5)
    imp.cookies.update(r.cookies)
    rec.call("impersonated whoami", "GET", "/store/v1/session", session=imp)

    rec.call("logout", "DELETE", "/store/v1/session", session=mgr)
    rec.call("after logout", "GET", "/store/v1/session", session=mgr)
