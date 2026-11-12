"""The Website page (Store › Website): the store edits its website's look as
a draft, previews it with a private link, and publishes it.

  * draft → changes → publish (version + 1) → the storefront's /channel/v1/site
    serves it; discard keeps the published look
  * the document is checked (unknown fields, presets, colours, limits)
  * a preview token answers /channel/v1/site with the draft; a stale one is 403
  * logo / cover uploads: sniffed types (an SVG only as a logo), size cap,
    served to the store and its storefront only, cached for good
  * an admin opened as the store can publish too (published_by admin)
"""

from __future__ import annotations

import secrets

import pytest
import requests

from conftest import ADMIN_V1, MAIN_BASE_URL, STORE_V1
from openapi import check
from test_admin_store_session import open_store

CH_V1 = f"{MAIN_BASE_URL}/channel/v1"
PNG = b"\x89PNG\r\n\x1a\n" + b"\x00" * 64
SVG = b'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10"/></svg>'


@pytest.fixture
def platform_key(admin):
    r = admin.post(f"{ADMIN_V1}/platform-keys", json={"name": f"test website {secrets.token_hex(3)}"}, timeout=5)
    assert r.status_code == 201, r.text
    body = r.json()
    yield body["key"]
    admin.post(f"{ADMIN_V1}/platform-keys/{body['platform_key']['id']}/revoke", timeout=5)


def _hosted(admin, store) -> str:
    host = f"t-{store.tag}.quiccpos.com"
    r = admin.post(f"{ADMIN_V1}/locations/{store.location_id}/site-domains", json={"host": host}, timeout=5)
    assert r.status_code == 201, r.text
    return host


def _site(key: str, host: str, **params) -> requests.Response:
    return requests.get(f"{CH_V1}/site", headers={"Authorization": f"Bearer {key}", "X-Site-Host": host}, params=params, timeout=5)


def test_draft_preview_publish(admin, store, platform_key) -> None:
    host = _hosted(admin, store)
    mgr = store.manager_session()

    w = mgr.get(f"{STORE_V1}/me/site/website", timeout=5).json()
    assert w["has_draft"] is False and w["draft"] == {} and w["published"] == {} and w["changes"] == []
    assert w["preview_host"] == host and w["preview_url"].startswith(f"http://{host}:5173/p/qpv_")
    token = w["preview_url"].split("/p/")[1]
    assert "/" not in token
    assert token.startswith("qpv_")
    # The token is made once and kept.
    assert mgr.get(f"{STORE_V1}/me/site/website", timeout=5).json()["preview_url"] == w["preview_url"]

    doc = {"preset": "classic-paper", "brand": "#2f9e5b", "texts": {"headline": " Fresh pies "},
           "blocks": [{"id": "hero", "on": True}, {"id": "about", "on": True}],
           "announcement": {"on": True, "text": "Closed Thanksgiving", "from": "2026-11-26", "to": "2026-11-26"}}
    r = mgr.put(f"{STORE_V1}/me/site/website/draft", json=doc, timeout=5)
    assert r.status_code == 200, r.text
    w = r.json()
    assert w["has_draft"] is True and w["draft"]["brand"] == "#2F9E5B" and w["draft"]["texts"]["headline"] == "Fresh pies"
    assert w["changes"] == ["announcement", "blocks", "brand", "preset", "texts"]

    # The storefront sees the published look; the preview link sees the draft.
    before = check(_site(platform_key, host), "getSite")
    assert before["settings"] == {} and "preview" not in before
    seen = check(_site(platform_key, host, preview=token), "getSite")
    assert seen["preview"] is True and seen["settings"]["preset"] == "classic-paper"
    r = _site(platform_key, host, preview="qpv_stale")
    assert r.status_code == 403 and r.json()["code"] == "bad_preview"

    r = mgr.post(f"{STORE_V1}/me/site/website/publish", timeout=5)
    assert r.status_code == 200, r.text
    w = r.json()
    assert w["has_draft"] is False and w["published"]["preset"] == "classic-paper" and w["published_by"] == "manager"
    assert w["published_at"] and w["version"] > before["version"]
    after = check(_site(platform_key, host), "getSite")
    assert after["version"] == w["version"] and after["settings"]["brand"] == "#2F9E5B"
    # Nothing left to publish.
    r = mgr.post(f"{STORE_V1}/me/site/website/publish", timeout=5)
    assert r.status_code == 409 and r.json()["code"] == "nothing_to_publish"

    # Discard: the draft goes, the published look stays.
    assert mgr.put(f"{STORE_V1}/me/site/website/draft", json={**doc, "preset": "cafe-calm"}, timeout=5).status_code == 200
    w = mgr.delete(f"{STORE_V1}/me/site/website/draft", timeout=5).json()
    assert w["has_draft"] is False and w["draft"]["preset"] == "classic-paper"
    # With no draft open, the preview shows the published look.
    assert check(_site(platform_key, host, preview=token), "getSite")["settings"]["preset"] == "classic-paper"


@pytest.mark.parametrize("doc,needle", [
    ({"colour": "red"}, "unknown field"),
    ({"preset": "neon"}, "preset"),
    ({"brand": "red"}, "brand"),
    ({"languages": ["fr", "en"]}, "languages"),
    ({"texts": {"headline": "x" * 81}}, "headline"),
    ({"blocks": [{"id": "video", "on": True}]}, "home block"),
    ({"picks": [str(i) for i in range(9)]}, "8"),
    ({"announcement": {"on": True}}, "announcement"),
    ({"logo": "00000000-0000-0000-0000-000000000000"}, "logo"),
])
def test_bad_documents(store, doc, needle) -> None:
    mgr = store.manager_session()
    r = mgr.put(f"{STORE_V1}/me/site/website/draft", json=doc, timeout=5)
    assert r.status_code == 400 and r.json()["code"] == "bad_settings", r.text
    assert needle in r.json()["error"]
    assert mgr.get(f"{STORE_V1}/me/site/website", timeout=5).json()["has_draft"] is False


def test_images(admin, make_store, platform_key) -> None:
    a, b = make_store(), make_store()
    host = _hosted(admin, a)
    mgr = a.manager_session()
    up = lambda kind, body, ct="image/png": mgr.post(f"{STORE_V1}/me/site/images", params={"kind": kind}, data=body,
                                                     headers={"Content-Type": ct}, timeout=5)
    r = up("logo", SVG, "image/svg+xml")
    assert r.status_code == 201, r.text
    logo = r.json()
    assert logo["kind"] == "logo" and logo["content_type"] == "image/svg+xml" and logo["size"] == len(SVG)
    cover = up("cover", PNG).json()
    assert cover["content_type"] == "image/png"
    # The real type wins over the declared one; an SVG is only a logo; no GIFs; a cap.
    assert up("cover", SVG, "image/png").json()["code"] == "bad_image"
    assert up("logo", b"GIF89a....", "image/gif").status_code == 400
    assert up("banner", PNG).status_code == 400
    assert up("cover", PNG + b"\x00" * (2 << 20)).status_code == 400

    # The draft may use the store's own images, each as its kind.
    r = mgr.put(f"{STORE_V1}/me/site/website/draft", json={"logo": logo["id"], "cover": cover["id"]}, timeout=5)
    assert r.status_code == 200, r.text
    assert mgr.put(f"{STORE_V1}/me/site/website/draft", json={"logo": cover["id"]}, timeout=5).status_code == 400
    other = b.manager_session()
    assert other.put(f"{STORE_V1}/me/site/website/draft", json={"logo": logo["id"]}, timeout=5).status_code == 400

    # Served to the store and its storefront, never to another store; cached for good; SVG sandboxed.
    r = mgr.get(f"{STORE_V1}/me/site/images/{logo['id']}", timeout=5)
    assert r.status_code == 200 and r.content == SVG
    assert other.get(f"{STORE_V1}/me/site/images/{logo['id']}", timeout=5).status_code == 404
    r = requests.get(f"{CH_V1}/site/images/{logo['id']}", headers={"Authorization": f"Bearer {platform_key}", "X-Site-Host": host}, timeout=5)
    assert r.status_code == 200 and r.content == SVG and r.headers["Content-Type"] == "image/svg+xml"
    assert "immutable" in r.headers["Cache-Control"] and "sandbox" in r.headers["Content-Security-Policy"]
    assert r.headers["X-Content-Type-Options"] == "nosniff"
    assert requests.get(f"{CH_V1}/site/images/{logo['id']}", timeout=5).status_code == 401
    r = requests.get(f"{CH_V1}/site/images/not-an-id", headers={"Authorization": f"Bearer {platform_key}", "X-Site-Host": host}, timeout=5)
    assert r.status_code == 404


def test_admin_opened_as_store_publishes(store) -> None:
    r, browser = open_store(store.tenant_id)
    assert r.status_code == 200, r.text
    assert browser.put(f"{STORE_V1}/me/site/website/draft", json={"preset": "taqueria-bold"}, timeout=5).status_code == 200
    w = browser.post(f"{STORE_V1}/me/site/website/publish", timeout=5).json()
    assert w["published_by"] == "admin" and w["published"] == {"preset": "taqueria-bold", "texts": {}, "announcement": {"on": False}}
