"""Security review fixes, over HTTP (2026-11-12).

- H3: an order's free text can't carry printer commands: a vendor order with ESC p (drawer kick),
  DLE DC4, GS V (cut) and a forged line in its names / notes is stored, read back and pushed to the Pi
  (the SSE order frame it prints from) without one control character.
- Hardening: security headers on every answer, the app's own Content-Security-Policy, the cross-site
  guard on the manager's cookie, the readiness probe's generic body.
"""

from __future__ import annotations

import json

import requests

from conftest import API_V1, MAIN_BASE_URL, STORE_V1, unique_order_id, wait_order
from sse import SSEStream

ESC_DRAWER = "\x1bp\x00\x19"          # ESC p 0 25 …: kick the cash drawer
DLE_DRAWER = "\x10\x14\x01\x00\x01"   # DLE DC4: the real-time pulse
GS_CUT = "\x1dV\x00"                  # GS V 0: cut the paper
ESC_INIT = "\x1b@"                    # ESC @: reset the printer


def _strings(v, at=""):
    if isinstance(v, dict):
        for k, x in v.items():
            if k != "raw_payload":  # the vendor's bytes as they came, kept for the record: never printed
                yield from _strings(x, f"{at}.{k}")
    elif isinstance(v, list):
        for i, x in enumerate(v):
            yield from _strings(x, f"{at}[{i}]")
    elif isinstance(v, str):
        yield at, v


def _assert_no_control(what: str, doc) -> None:
    bad = [(at, s) for at, s in _strings(doc) if any(ord(c) < 0x20 or ord(c) == 0x7F or 0x80 <= ord(c) <= 0x9F for c in s)]
    assert not bad, f"{what}: control characters kept in {[(at, s.encode('unicode_escape').decode()) for at, s in bad]}"


def test_vendor_order_text_cannot_carry_printer_commands(publish, store) -> None:
    o = store.order(unique_order_id())
    o["customer"]["first_name"] = "Bob" + ESC_DRAWER
    o["customer"]["last_name"] = "Smith" + DLE_DRAWER
    o["notes"] = "ring bell\nPAID - CARD" + GS_CUT
    o["items"][0]["name"] = o["items"][0]["name"] + GS_CUT + ESC_INIT
    o["items"][0]["notes"] = "well done\r\nFREE - MANAGER" + ESC_DRAWER
    o["service_type"] = "delivery"
    o["delivery_address"] = {"street": "931 Taylor Ave" + ESC_INIT, "cross_streets": "side door\nNO CHARGE" + GS_CUT,
                             "suite": "4B\x07", "buz": "", "city": "Towson", "state": "MD", "zip": "21286"}

    with SSEStream(store.key) as s:
        assert s.next()["event"] == "store_config"
        publish(o)
        frame = s.wait_event("order", lambda d: str(d.get("source_event_id")) == str(o["order_id"]), timeout=30)
        assert frame, "the order frame never reached the agent's stream"
    _assert_no_control("the SSE order frame (what the Pi prints)", frame)

    got = wait_order(store.key, o["order_id"])
    _assert_no_control("the stored order", got)
    assert got["customer"]["display_name"] == "Bobp SmithV"[:0] + "Bobp Smith", got["customer"]
    assert got["notes"] == "ring bell PAID - CARDV", "a line break in a note is a space: no forged line"
    assert got["items"][0]["notes"] == "well done  FREE - MANAGERp"
    addr = got["fulfillment"]["address"]
    assert addr["line1"] == "931 Taylor Ave@" and addr["line2"] == "4B" and addr["notes"] == "side door NO CHARGEV", addr


def test_vendor_order_text_is_clipped(publish, store) -> None:
    o = store.order(unique_order_id())
    o["customer"]["first_name"], o["customer"]["last_name"] = "N" * 100, "L" * 100
    o["notes"] = "x" * 1000
    o["items"][0]["notes"] = "y" * 1000
    publish(o)
    got = wait_order(store.key, o["order_id"])
    assert len(got["customer"]["display_name"]) == 80
    assert len(got["notes"]) == 300 and len(got["items"][0]["notes"]) == 300


def test_security_headers_everywhere() -> None:
    for path in ("/health", "/api/v1/ready", "/nope-not-a-route", "/store/v1/session", "/app", "/app/store/payments"):
        r = requests.get(MAIN_BASE_URL + path, timeout=5)
        h = r.headers
        assert h.get("X-Content-Type-Options") == "nosniff", (path, dict(h))
        assert h.get("X-Frame-Options") == "DENY", path
        assert "frame-ancestors 'none'" in h.get("Content-Security-Policy", ""), path
        assert h.get("Referrer-Policy"), path
        assert "Strict-Transport-Security" not in h, f"{path}: HSTS on plain http"
    api = requests.get(MAIN_BASE_URL + "/health", timeout=5).headers["Content-Security-Policy"]
    assert "default-src 'none'" in api
    app = requests.get(MAIN_BASE_URL + "/app", timeout=5)
    csp = app.headers["Content-Security-Policy"]
    script = next(d for d in csp.split(";") if d.strip().startswith("script-src"))
    assert "'self'" in script and "unsafe" not in script and "*" not in script, script
    if app.status_code == 200:  # a UI build is embedded: each inline script is allowed by its hash
        assert app.text.count("<script>") == script.count("'sha256-") > 0, script
    # Over TLS (the proxy says so) HSTS is sent.
    tls = requests.get(MAIN_BASE_URL + "/health", headers={"X-Forwarded-Proto": "https"}, timeout=5)
    assert tls.headers.get("Strict-Transport-Security", "").startswith("max-age="), dict(tls.headers)


def test_cross_site_write_on_the_managers_cookie_is_refused(store) -> None:
    mgr = store.manager_session()
    printer = {"name": "Kitchen", "ip": "10.0.0.2"}
    evil = {"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}
    r = mgr.post(f"{STORE_V1}/me/printers", json=printer, headers=evil, timeout=5)
    assert r.status_code == 403 and r.json()["code"] == "cross_site", r.text
    r = mgr.post(f"{STORE_V1}/me/printers", json=printer, headers={"Origin": "https://evil.example"}, timeout=5)
    assert r.status_code == 403, "an older browser sends only Origin"
    r = mgr.delete(f"{STORE_V1}/session", headers={"Sec-Fetch-Site": "same-site", "Origin": "https://shop.example"}, timeout=5)
    assert r.status_code == 403, "a sibling site can't log the manager out"
    assert mgr.get(f"{STORE_V1}/me/printers", timeout=5).json() == [], "nothing was written"
    # Reads are not its business; the app itself and non-browser callers write as before.
    assert mgr.get(f"{STORE_V1}/me/location", headers=evil, timeout=5).status_code == 200
    r = mgr.post(f"{STORE_V1}/me/printers", json=printer, headers={"Sec-Fetch-Site": "same-origin"}, timeout=5)
    assert r.status_code == 201, r.text
    r = mgr.post(f"{STORE_V1}/me/printers", json={"name": "Bar", "ip": "10.0.0.3"}, timeout=5)
    assert r.status_code == 201, r.text
    # A cross-site login (login CSRF) is refused too.
    r = requests.post(f"{STORE_V1}/session", json={"username": "x", "password": "y"}, headers=evil, timeout=5)
    assert r.status_code == 403, r.text
    # A device key is not a cookie: a cross-site page can't supply it, so the guard stays out of its way.
    r = requests.get(f"{API_V1}/orders", headers={"X-STORE-API-KEY": store.key, **evil}, timeout=5)
    assert r.status_code == 200, r.text


def test_ready_says_what_not_why() -> None:
    r = requests.get(f"{API_V1}/ready", timeout=5)
    body = r.json()
    assert set(body) == {"ready", "db", "sqs"} and r.status_code in (200, 503)
    for check in (body["db"], body["sqs"]):
        assert check.get("error", "unavailable") == "unavailable", json.dumps(body)
