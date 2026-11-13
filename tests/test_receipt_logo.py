"""The receipt logo (main 4.0.1): the store's PUBLISHED Website logo goes to
the Pi so it prints at the top of the receipts.

  * store_config carries receipt_logo {id, etag, content_type} for a PNG /
    JPEG / WebP logo; left out (= null) with no logo or an SVG one
  * publishing pushes the fresh store_config live
  * GET /store/v1/agent/receipt-logo (agent key): the bytes + Content-Type +
    ETag, 304 on If-None-Match, 404 no_logo when none (or SVG), per store
"""

from __future__ import annotations

import json

import requests

from conftest import STORE_V1, store_key
from sse import SSEStream

LOGO = f"{STORE_V1}/agent/receipt-logo"
PNG = b"\x89PNG\r\n\x1a\n" + b"\x00\x01receipt-logo" * 8
SVG = b'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10"/></svg>'


def _upload(mgr: requests.Session, body: bytes, ct: str) -> dict:
    r = mgr.post(f"{STORE_V1}/me/site/images", params={"kind": "logo"}, data=body, headers={"Content-Type": ct}, timeout=5)
    assert r.status_code == 201, r.text
    return r.json()


def _publish_logo(mgr: requests.Session, logo_id: str) -> None:
    r = mgr.put(f"{STORE_V1}/me/site/website/draft", json={"logo": logo_id}, timeout=5)
    assert r.status_code == 200, r.text
    r = mgr.post(f"{STORE_V1}/me/site/website/publish", timeout=5)
    assert r.status_code == 200, r.text


def _no_logo(key: str) -> None:
    r = requests.get(LOGO, headers=store_key(key), timeout=5)
    assert r.status_code == 404 and r.json()["code"] == "no_logo", r.text


def test_receipt_logo(make_store) -> None:
    a, b = make_store(), make_store()
    mgr = a.manager_session()
    assert requests.get(LOGO, timeout=5).status_code == 401

    # No logo yet: nothing in store_config, 404 on the route.
    _no_logo(a.key)
    with SSEStream(a.key) as s:
        frame = s.next()
        assert frame["event"] == "store_config"
        assert json.loads(frame["data"]).get("receipt_logo") is None

    # An SVG logo can't be printed: still none.
    _publish_logo(mgr, _upload(mgr, SVG, "image/svg+xml")["id"])
    _no_logo(a.key)

    # Publishing a PNG logo pushes store_config live with it.
    png = _upload(mgr, PNG, "image/png")
    with SSEStream(a.key) as s:
        first = json.loads(s.next()["data"])
        assert first.get("receipt_logo") is None
        _publish_logo(mgr, png["id"])
        cfg = s.wait_event("store_config", lambda d: d.get("receipt_logo") is not None)
    assert cfg, "publishing did not push a store_config with the receipt logo"
    logo = cfg["receipt_logo"]
    assert logo["id"] == png["id"] and logo["content_type"] == "image/png" and logo["etag"].startswith('"')
    assert set(logo) == {"id", "etag", "content_type"}

    # A fresh connect's handshake carries it too.
    with SSEStream(a.key) as s:
        assert json.loads(s.next()["data"])["receipt_logo"] == logo

    r = requests.get(LOGO, headers=store_key(a.key), timeout=5)
    assert r.status_code == 200, r.text
    assert r.content == PNG and r.headers["Content-Type"] == "image/png" and r.headers["ETag"] == logo["etag"]
    r = requests.get(LOGO, headers={**store_key(a.key), "If-None-Match": logo["etag"]}, timeout=5)
    assert r.status_code == 304 and r.content == b""
    r = requests.get(LOGO, headers={**store_key(a.key), "If-None-Match": '"lstale"'}, timeout=5)
    assert r.status_code == 200 and r.content == PNG

    # Another store sees only its own (none).
    _no_logo(b.key)
