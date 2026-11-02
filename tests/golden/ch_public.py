"""Golden chapter: unauthenticated routes, the route table and every auth
failure shape (each credential kind, missing and wrong)."""

from __future__ import annotations

import secrets

import requests

from golden.recorder import Rec
from golden.util import routes_list

BAD = {"X-STORE-API-KEY": "qpos_definitely-not-a-real-key-000000"}


def run(rec: Rec, admin: requests.Session, _request) -> None:
    # Failed logins are throttled per username (5 / 10 min): a fresh name per run.
    nobody = "nobody-" + secrets.token_hex(4)
    rec.alias(nobody, "nobody")
    rec.call("health", "GET", "/health")
    rec.call("version", "GET", "/api/v1/version", mask=("version", "commit"))
    rec.call("ready", "GET", "/api/v1/ready", mask=("latency_ms",))
    rec.call("routes", "GET", "/__routes", filt=routes_list, compare="superset")

    # The SPA and its redirects: status + headers only (the bundle changes
    # with every UI build).
    for p in ("/app", "/app/store/menu", "/admin", "/admin/", "/store", "/store/"):
        r = rec.call(f"spa {p}", "GET", p, redirects=False)
        rec.steps[-1]["body"] = "" if r.status_code == 200 else rec.steps[-1]["body"]

    # Device key: missing, unknown.
    for label, h in (("none", {}), ("bad", BAD)):
        rec.call(f"orders key {label}", "GET", "/api/v1/orders", headers=h)
        rec.call(f"order key {label}", "GET", "/api/v1/orders/00000000-0000-0000-0000-000000000000", headers=h)
        rec.call(f"sse key {label}", "GET", "/api/v1/events/orders", headers=h)
        rec.call(f"store-config key {label}", "GET", "/store/v1/store-config", headers=h)
        rec.call(f"agent rpc key {label}", "POST", "/store/v1/agent/rpc", headers=h, json_body={"type": "ping", "id": "1"})
        rec.call(f"agent restore key {label}", "GET", "/store/v1/agent/restore", headers=h)

    # Admin key: missing, wrong, both header forms.
    rec.call("admin none", "GET", "/admin/v1/overview")
    rec.call("admin wrong bearer", "GET", "/admin/v1/overview", headers={"Authorization": "Bearer wrong"})
    rec.call("admin wrong x-admin-key", "GET", "/admin/v1/overview", headers={"X-Admin-Key": "wrong"})

    # Store session: no cookie, garbage cookie, bad login bodies.
    rec.call("session none", "GET", "/store/v1/session")
    rec.call("me none", "GET", "/store/v1/me/location")
    rec.call("me garbage cookie", "GET", "/store/v1/me/location", headers={"Cookie": "quicc_store_session=garbage"})
    rec.call("login no password", "POST", "/store/v1/session", json_body={"username": nobody})
    rec.call("login unknown", "POST", "/store/v1/session", json_body={"username": nobody, "password": "x"})
    rec.call("login not json", "POST", "/store/v1/session", data="nope", headers={"Content-Type": "application/json"})

    # Operator token: none, garbage, wrong prefix.
    rec.call("operator me none", "GET", "/store/v1/operator/me")
    rec.call("operator me bad", "GET", "/store/v1/operator/me", headers={"Authorization": "Bearer qopr_" + "0" * 64})
    rec.call("operator menu bad", "GET", "/store/v1/operator/menu", headers={"Authorization": "Bearer nope"})
    rec.call("operator login no password", "POST", "/store/v1/operator/login", json_body={"username": nobody})
    rec.call("operator login unknown", "POST", "/store/v1/operator/login", json_body={"username": nobody, "password": "x"})

    # Report key (tablet diagnostics) and the Pi link.
    rec.call("diag report none", "POST", "/store/v1/tablet/diag-reports", json_body={})
    rec.call("diag report bad", "POST", "/store/v1/tablet/diag-reports", json_body={}, headers={"X-Quibase-Report-Key": "qrep_" + "0" * 40})
    rec.call("agent link no password", "POST", "/store/v1/agent/link", json_body={"username": nobody})
    rec.call("agent link unknown", "POST", "/store/v1/agent/link", json_body={"username": nobody, "password": "x", "name": "pi"})

    rec.call("unknown route", "GET", "/api/v1/nope")

