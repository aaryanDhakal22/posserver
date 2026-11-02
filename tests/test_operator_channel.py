"""Operators on the channel API (main 3.0, PORTS-PLAN.md step 7).

  * an operator signs in at /channel/v1/operator/login (12 h, qops_) and works
    a store with X-Location-ID: /me is that store's built-in Call center
    channel + the operator; a store they may not work on → 403, no store →
    400; the legacy qopr_ token opens nothing on /channel/v1
  * the menu is the store's Call center view (a rename there shows; the
    legacy /store/v1/operator/menu keeps the bank's name)
  * an order records who took it (taken_by; source_metadata.operator on the
    Pi's frame)
  * sign-out, admin revoke, throttled bad passwords
"""

from __future__ import annotations

import secrets
from datetime import datetime, timedelta, timezone

import pytest
import requests

from conftest import ADMIN_V1, MAIN_BASE_URL, STORE_V1
from openapi import check
from sse import SSEStream

CH_V1 = f"{MAIN_BASE_URL}/channel/v1"


@pytest.fixture
def operator(admin, make_store):
    """An operator allowed one store (a), not another (b)."""
    a, b = make_store(), make_store()
    user, pw = "op" + secrets.token_hex(4), "s3cret-" + secrets.token_hex(3)
    r = admin.post(f"{ADMIN_V1}/operators", json={"username": user, "password": pw, "display_name": "Sara Smith"}, timeout=5)
    assert r.status_code == 201, r.text
    op_id = r.json()["id"]
    assert admin.put(f"{ADMIN_V1}/operators/{op_id}/locations", json={"location_ids": [a.location_id]}, timeout=5).status_code == 204
    yield {"id": op_id, "user": user, "pw": pw, "a": a, "b": b}
    admin.delete(f"{ADMIN_V1}/operators/{op_id}", timeout=5)


def _login(op) -> dict:
    r = requests.post(f"{CH_V1}/operator/login", json={"username": op["user"], "password": op["pw"]}, timeout=5)
    return check(r, "operatorLogin")


def _hdr(token: str, store=None) -> dict:
    h = {"Authorization": f"Bearer {token}"}
    if store is not None:
        h["X-Location-ID"] = store.location_id
    return h


def test_sign_in_and_work_a_store(admin, operator) -> None:
    s = _login(operator)
    assert s["token"].startswith("qops_") and s["operator"]["display_name"] == "Sara Smith"
    exp = datetime.fromisoformat(s["expires_at"].replace("Z", "+00:00"))
    assert timedelta(hours=11, minutes=59) < exp - datetime.now(timezone.utc) <= timedelta(hours=12)
    assert [x["location_id"] for x in s["locations"]] == [operator["a"].location_id] and s["locations"][0]["name"] == operator["a"].name

    me = check(requests.get(f"{CH_V1}/me", headers=_hdr(s["token"], operator["a"]), timeout=5), "getMe")
    assert me["channel"]["kind"] == "operator" and me["channel"]["name"] == "Call center"
    assert me["store"]["location_id"] == operator["a"].location_id and me["operator"]["username"] == operator["user"]
    # The same built-in channel every time; the admin sees it, built in.
    again = requests.get(f"{CH_V1}/me", headers=_hdr(s["token"], operator["a"]), timeout=5).json()
    assert again["channel"]["id"] == me["channel"]["id"]
    chans = admin.get(f"{ADMIN_V1}/channels", params={"location_id": operator["a"].location_id}, timeout=5).json()
    assert [(c["kind"], c["builtin"]) for c in chans] == [("operator", True)]

    r = requests.get(f"{CH_V1}/me", headers=_hdr(s["token"], operator["b"]), timeout=5)
    assert r.status_code == 403 and r.json()["code"] == "location_not_allowed"
    r = requests.get(f"{CH_V1}/me", headers=_hdr(s["token"]), timeout=5)
    assert r.status_code == 400 and r.json()["code"] == "location_required"

    # The legacy read-only token keeps its old surface and opens no port.
    legacy = requests.post(f"{STORE_V1}/operator/login", json={"username": operator["user"], "password": operator["pw"]}, timeout=5).json()
    assert legacy["token"].startswith("qopr_")
    assert requests.get(f"{CH_V1}/me", headers=_hdr(legacy["token"], operator["a"]), timeout=5).status_code == 401
    r = requests.get(f"{STORE_V1}/operator/menu", params={"location_id": operator["a"].location_id},
                     headers={"Authorization": f"Bearer {legacy['token']}"}, timeout=5)
    assert r.status_code == 200, r.text


def test_menu_is_the_call_center_view_and_orders_say_who(admin, operator) -> None:
    a = operator["a"]
    mgr = a.manager_session()
    try:
        m = mgr.post(f"{STORE_V1}/me/menu/menus", json={"name": "Main"}, timeout=5).json()
        c = mgr.post(f"{STORE_V1}/me/menu/menus/{m['id']}/categories", json={"name": "Food"}, timeout=5).json()
        item = mgr.post(f"{STORE_V1}/me/menu/categories/{c['id']}/items", json={"name": "Cheese Pizza", "price_cents": 999}, timeout=5).json()
        op_view = next(v for v in mgr.get(f"{STORE_V1}/me/menu/views", timeout=5).json() if v["kind"] == "operator")
        r = mgr.post(f"{STORE_V1}/me/menu/views/{op_view['id']}/entries",
                     json={"kind": "item", "anchor": {"item_ids": [item["id"]]}, "display_name": "CHZ PIE (call center)"}, timeout=5)
        assert r.status_code == 201, r.text
    finally:
        mgr.close()
    s = _login(operator)
    h = _hdr(s["token"], a)
    menu = check(requests.get(f"{CH_V1}/menu", headers=h, timeout=5), "getMenu")
    assert menu["view"]["kind"] == "operator" and [i["name"] for c in menu["categories"] for i in c["items"]] == ["CHZ PIE (call center)"]

    body = {"client_order_id": "op-" + secrets.token_hex(5), "type": "pickup", "lines": [{"item_id": item["id"], "qty": 1}],
            "payment": {"method": "cash"}, "customer": {"name": "Caller One", "phone": "4105550123"}}
    with SSEStream(a.key) as stream:
        assert stream.next()["event"] == "store_config"
        o = check(requests.post(f"{CH_V1}/orders", json=body, headers=h, timeout=10), "placeOrder")
        frame = stream.wait_event("order", lambda d: d.get("id") == o["id"])
    assert o["taken_by"] == {"id": operator["id"], "display_name": "Sara Smith"}
    assert frame["source"] == "operator" and frame["source_metadata"]["operator"]["display_name"] == "Sara Smith"
    assert frame["items"][0]["name"] == "Cheese Pizza", "the kitchen reads the bank's name"
    got = check(requests.get(f"{CH_V1}/orders/{o['id']}", headers=h, timeout=5), "getOrder")
    assert got["taken_by"]["display_name"] == "Sara Smith"


def test_sign_out_revoke_and_throttle(admin, operator) -> None:
    s = _login(operator)
    h = _hdr(s["token"], operator["a"])
    assert requests.get(f"{CH_V1}/me", headers=h, timeout=5).status_code == 200
    r = requests.post(f"{CH_V1}/operator/logout", headers={"Authorization": f"Bearer {s['token']}"}, timeout=5)
    assert r.status_code == 204
    assert requests.get(f"{CH_V1}/me", headers=h, timeout=5).status_code == 401
    assert requests.post(f"{CH_V1}/operator/logout", headers={"Authorization": f"Bearer {s['token']}"}, timeout=5).status_code == 204
    assert requests.post(f"{CH_V1}/operator/logout", headers={"Authorization": "Bearer qch_nope"}, timeout=5).status_code == 401

    s2 = _login(operator)
    assert admin.post(f"{ADMIN_V1}/operators/{operator['id']}/tokens/revoke", timeout=5).status_code in (200, 204)
    assert requests.get(f"{CH_V1}/me", headers=_hdr(s2["token"], operator["a"]), timeout=5).status_code == 401

    codes = [requests.post(f"{CH_V1}/operator/login", json={"username": operator["user"], "password": "wrong"}, timeout=5)
             for _ in range(6)]
    assert check(codes[0], "operatorLogin")["code"] == "invalid_credentials"
    assert codes[-1].status_code == 429 and check(codes[-1], "operatorLogin")["code"] == "too_many_attempts"
