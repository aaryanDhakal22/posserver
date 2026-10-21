"""Tablet-app (Quibase) releases: console uploads (admin), agents download.

Each test uses its own app name so it never disturbs real `quibase` releases.
"""

import hashlib
import secrets

import requests

from conftest import ADMIN_V1, STORE_V1, store_key


def _apk(n: int = 2048) -> bytes:
    return b"PK\x03\x04" + secrets.token_bytes(n)


def _upload(admin, app, code, body, **headers):
    return admin.post(f"{ADMIN_V1}/app-releases", params={"app": app, "version_code": code, "version_name": f"1.0.{code}"},
                      data=body, headers={"Content-Type": "application/vnd.android.package-archive", **headers}, timeout=30)


def test_upload_latest_download(admin, store) -> None:
    app = f"t{secrets.token_hex(4)}"
    agent = store_key(store.key)
    assert requests.get(f"{STORE_V1}/agent/app/latest", params={"app": app}, headers=agent, timeout=5).status_code == 204

    v1, v2 = _apk(), _apk(4096)
    r = _upload(admin, app, 10, v1, **{"X-Sha256": hashlib.sha256(v1).hexdigest()})
    assert r.status_code == 201, r.text
    assert r.json()["sha256"] == hashlib.sha256(v1).hexdigest() and r.json()["size"] == len(v1)
    assert _upload(admin, app, 12, v2).status_code == 201

    latest = requests.get(f"{STORE_V1}/agent/app/latest", params={"app": app}, headers=agent, timeout=5)
    assert latest.status_code == 200
    assert latest.json()["version_code"] == 12 and latest.json()["version_name"] == "1.0.12"

    apk = requests.get(f"{STORE_V1}/agent/app/releases/12/apk", params={"app": app}, headers=agent, timeout=30)
    assert apk.status_code == 200 and apk.content == v2
    assert requests.get(f"{STORE_V1}/agent/app/releases/99/apk", params={"app": app}, headers=agent, timeout=5).status_code == 404

    listed = admin.get(f"{ADMIN_V1}/app-releases", params={"app": app}, timeout=5).json()
    assert [x["version_code"] for x in listed] == [12, 10]
    assert admin.delete(f"{ADMIN_V1}/app-releases/12", params={"app": app}, timeout=5).status_code == 204
    assert admin.delete(f"{ADMIN_V1}/app-releases/12", params={"app": app}, timeout=5).status_code == 404
    assert requests.get(f"{STORE_V1}/agent/app/latest", params={"app": app}, headers=agent, timeout=5).json()["version_code"] == 10


def test_upload_rejects_bad_input(admin) -> None:
    app = f"t{secrets.token_hex(4)}"
    assert _upload(admin, app, 5, b"not a zip at all").status_code == 400
    assert _upload(admin, app, 5, _apk(), **{"X-Sha256": "00" * 32}).status_code == 400
    assert _upload(admin, app, 5, _apk()).status_code == 201
    r = _upload(admin, app, 5, _apk())
    assert r.status_code == 409 and r.json()["code"] == "not_newer"
    assert _upload(admin, app, 0, _apk()).status_code == 400
    assert requests.post(f"{ADMIN_V1}/app-releases", params={"app": app, "version_code": 9, "version_name": "x"},
                         data=_apk(), timeout=5).status_code == 401


def test_only_agent_keys_download(store) -> None:
    mgr = store.manager_session()
    r = mgr.post(f"{STORE_V1}/me/devices", json={"name": "phone", "kind": "mobile"}, timeout=5)
    mobile = store_key(r.json()["api_key"])
    assert requests.get(f"{STORE_V1}/agent/app/latest", headers=mobile, timeout=5).status_code == 403
    assert requests.get(f"{STORE_V1}/agent/app/latest", timeout=5).status_code == 401
