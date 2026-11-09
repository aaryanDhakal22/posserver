"""Quibase's controls through the Pi (weborder stage 3): agent → main RPCs
answered with data — pause / resume the store's channels, the stock list,
items and toppings sold out (rest of day, or until turned back on, which the
daily restock skips) — and a website order cancelled on the floor after it
was accepted (its card hold is released)."""

from __future__ import annotations

import uuid

import pytest
import requests

from conftest import ADMIN_V1, STORE_V1, store_key, wait_until
from test_order_port import _channel, _menu, _ok, _order, _pizza, _place
from test_square import _connect, _ev, _fake_payment, _floor, _hold


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


def _rpc(store, type_: str, payload: dict | None = None) -> requests.Response:
    env = {"type": type_, "id": str(uuid.uuid4()), "payload": payload or {}}
    return requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(store.key), json=env, timeout=10)


def _reply(r: requests.Response) -> dict:
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["type"].endswith(".reply")
    return body["payload"]


def test_pause_and_resume(store) -> None:
    st = _reply(_rpc(store, "online.status"))
    assert st["state"] in ("open", "closed") and st["pause"] is None
    st = _reply(_rpc(store, "online.pause", {"preset": "30m", "note": "Oven down"}))
    assert st["state"] == "paused" and st["pause"]["note"] == "Oven down" and st["pause"]["until"]
    assert _reply(_rpc(store, "online.status"))["state"] == "paused"
    assert _rpc(store, "online.pause", {"preset": "forever"}).status_code == 400
    assert _rpc(store, "online.pause", {"preset": "15m", "note": "x" * 141}).status_code == 400
    st = _reply(_rpc(store, "online.resume"))
    assert st["state"] != "paused" and st["pause"] is None


def test_sold_out_from_the_pi(admin, store, mgr) -> None:
    m = _menu(mgr)
    stock = _reply(_rpc(store, "menu.stock"))
    names = {i["name"]: i for i in stock["items"]}
    assert names["Cheese Pizza"]["category"] == "Pizza" and names["Cheese Pizza"]["in_stock"] is True
    tops = next(g for g in stock["groups"] if g["name"] == "Toppings")
    assert [o["name"] for o in tops["options"]] == ["Pepperoni"]

    # Wings until turned back on; Pepperoni for the rest of the day.
    w = _reply(_rpc(store, "menu.stock.set", {"kind": "item", "id": m["wings"]["id"], "in_stock": False, "hold": True}))
    assert w["in_stock"] is False and w["hold"] is True
    p = _reply(_rpc(store, "menu.stock.set", {"kind": "option", "id": m["pep"]["id"], "in_stock": False}))
    assert p["in_stock"] is False and p["hold"] is False
    # The new day: Pepperoni is back, Wings stay out.
    _ok(admin.post(f"{ADMIN_V1}/store-status/restock", json={"location_id": store.location_id}, timeout=10))
    stock = _reply(_rpc(store, "menu.stock"))
    assert {i["name"]: i["in_stock"] for i in stock["items"]}["Wings"] is False
    assert next(g for g in stock["groups"] if g["name"] == "Toppings")["options"][0]["in_stock"] is True
    # Back on: the hold is gone with it.
    w = _reply(_rpc(store, "menu.stock.set", {"kind": "item", "id": m["wings"]["id"], "in_stock": True, "hold": True}))
    assert w["in_stock"] is True and w["hold"] is False
    # Another store's item / bad input.
    assert _rpc(store, "menu.stock.set", {"kind": "item", "id": str(uuid.uuid4()), "in_stock": False}).status_code == 400
    assert _rpc(store, "menu.stock.set", {"kind": "size", "id": m["wings"]["id"], "in_stock": False}).status_code == 400


def test_cancel_after_accept_releases_the_hold(admin, store, mgr) -> None:
    _connect(mgr)
    m = _menu(mgr)
    web = _channel(admin, store, "web")
    o = _ok(_place(web, _order([_pizza(m)], method="card", token="cnon:card-nonce-ok")), 201)
    acc = _ev("order.status", order_id=o["id"], status="accepted", kitchen_number=1101)
    ready = _ev("order.status", order_id=o["id"], status="ready")
    assert set(_floor(store, acc, ready).values()) == {"applied"}
    # The batch-close cancel (no reason) can't cancel an accepted order …
    late = _ev("order.status", order_id=o["id"], status="cancelled")
    assert _floor(store, late) == {late["event_id"]: "stale"}
    # … a cancel by hand can.
    cancel = _ev("order.status", order_id=o["id"], status="cancelled", reason="customer_asked")
    assert _floor(store, cancel) == {cancel["event_id"]: "applied"}
    assert wait_until(lambda: _hold(admin, o["id"])["status"] == "cancelled", timeout=10)
    assert _fake_payment(_hold(admin, o["id"])["payment_ref"])["status"] == "CANCELED"
    got = requests.get(f"{STORE_V1.replace('/store/v1', '/channel/v1')}/orders/{o['id']}", headers=web, timeout=5).json()
    assert got["status"] == "cancelled"
    # A finished order can't be cancelled any more.
    o2 = _ok(_place(web, _order([_pizza(m)], method="card", token="cnon:card-nonce-ok")), 201)
    done = _ev("order.status", order_id=o2["id"], status="delivered")
    _floor(store, done)
    again = _ev("order.status", order_id=o2["id"], status="cancelled", reason="other")
    assert _floor(store, again) == {again["event_id"]: "stale"}
