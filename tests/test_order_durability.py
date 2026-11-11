"""Order placements (DURABILITY-PLAN.md, steps 1–3): the key before any money
moves, the work detached from the request, 202 + by-key, the sweeper,
expires_at, kitchen_offline, and 3-D Secure on the same key.

Each test has its own store (own tenant); the sweeper tests leave a pending
placement as a main that died mid-work would (dev hook) and run one sweep — a
sweep only touches other mains' placements, so parallel tests don't collide.
Needs dev main with the fake Square (SQUARE_ENV=fake), fake mail and the dev hooks.
"""
from __future__ import annotations

import secrets
from datetime import datetime, timedelta, timezone
from zoneinfo import ZoneInfo

import pytest
import requests

from conftest import ADMIN_V1, MAIN_BASE_URL, STORE_V1, wait_until
from openapi import check
from test_order_port import _channel, _menu, _ok, _order, _pizza, _place
from test_square import _connect, _fake_payment, _fake_square_on, _hold  # noqa: F401 (fixture)

CH_V1 = f"{MAIN_BASE_URL}/channel/v1"
NY = ZoneInfo("America/New_York")


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


def _by_key(hdr, key: str) -> requests.Response:
    return requests.get(f"{CH_V1}/orders/by-key/{key}", headers=hdr, timeout=10)


def _quote(hdr, body) -> int:
    q = _ok(requests.post(f"{CH_V1}/quote", json={"type": body["type"], "lines": body["lines"], "payment": body["payment"]["method"]},
                          headers=hdr, timeout=10))
    return q["total_cents"]


def _sweep(admin) -> dict:
    return _ok(admin.post(f"{ADMIN_V1}/dev/placements/sweep", timeout=60))


def _orphan(admin, channel_id: str, body: dict, charge_cents: int = 0, hold: bool = False) -> str:
    r = admin.post(f"{ADMIN_V1}/dev/placements/orphan", json={"channel_id": channel_id, "order": body, "charge_cents": charge_cents,
                                                              "hold": hold}, timeout=30)
    if r.status_code == 404:
        pytest.skip("main isn't running its dev hooks")
    return _ok(r)["hold_ref"]


def _notices(admin, store, kind: str) -> list[dict]:
    r = admin.get(f"{ADMIN_V1}/dev/notices", params={"location_id": store.location_id}, timeout=5)
    if r.status_code == 404:
        pytest.skip("main isn't running its dev hooks")
    return [n for n in r.json()["notices"] if n["kind"] == kind]


def _mail(admin, to: str) -> list[dict]:
    return _ok(admin.get(f"{ADMIN_V1}/dev/mail", params={"to": to}, timeout=5))["messages"]


def _channel_id(admin, store, kind: str) -> tuple[str, dict]:
    """A new channel of kind (its id and its headers)."""
    r = _ok(admin.post(f"{ADMIN_V1}/channels", json={"location_id": store.location_id, "kind": kind, "name": kind.title()}, timeout=5), 201)
    return r["id"], {"Authorization": f"Bearer {r['key']}"}


def _email() -> str:
    return f"dur-{secrets.token_hex(4)}@example.com"


def test_replay_and_a_reused_key(admin, store, mgr) -> None:
    m = _menu(mgr)
    op = _channel(admin, store, "operator")
    body = _order([_pizza(m)], notes="Ring the bell")
    o = check(_place(op, body), "placeOrder")
    again = _place(op, body)
    assert again.status_code == 200 and check(again, "placeOrder")["id"] == o["id"]
    p = check(_by_key(op, body["client_order_id"]), "getOrderByKey")
    assert p["state"] == "placed" and p["order"]["id"] == o["id"]
    # The same key with a different order is refused, never merged.
    r = _place(op, {**body, "notes": "Knock instead"})
    assert r.status_code == 409 and check(r, "placeOrder")["code"] == "key_reused"
    # A key main never saw.
    r = _by_key(op, "never-" + secrets.token_hex(4))
    assert r.status_code == 404 and check(r, "getOrderByKey")["code"] == "unknown_key"
    # Another channel can't read this one's placement.
    other = _channel(admin, store, "operator")
    assert _by_key(other, body["client_order_id"]).status_code == 404


def test_slow_card_answers_202_then_by_key(admin, store, mgr) -> None:
    _connect(mgr)
    m = _menu(mgr)
    web = _channel(admin, store, "web")
    body = _order([_pizza(m)], method="card", token="cnon:card-nonce-slow-3")
    r = requests.post(f"{CH_V1}/orders", params={"wait_ms": 500}, json=body, headers=web, timeout=10)
    assert r.status_code == 202, r.text
    assert check(r, "placeOrder") == {"state": "pending", "client_order_id": body["client_order_id"]}
    assert check(_by_key(web, body["client_order_id"]), "getOrderByKey")["state"] == "pending"
    # The same order again while main is still at work: it waits for the same run.
    assert wait_until(lambda: _by_key(web, body["client_order_id"]).json()["state"] == "placed", timeout=20)
    p = check(_by_key(web, body["client_order_id"]), "getOrderByKey")
    assert p["order"]["payment"]["status"] == "paid"
    assert _hold(admin, p["order"]["id"])["status"] == "held"


def test_caller_hangs_up_and_the_order_still_completes(admin, store, mgr) -> None:
    _connect(mgr)
    m = _menu(mgr)
    web = _channel(admin, store, "web")
    body = _order([_pizza(m)], method="card", token="cnon:card-nonce-slow-3")
    with pytest.raises(requests.exceptions.Timeout):
        requests.post(f"{CH_V1}/orders", json=body, headers=web, timeout=1)  # gives up mid-card-call
    assert wait_until(lambda: _by_key(web, body["client_order_id"]).json()["state"] == "placed", timeout=20)
    o = _by_key(web, body["client_order_id"]).json()["order"]
    h = _hold(admin, o["id"])
    assert h["status"] == "held" and _fake_payment(h["payment_ref"])["status"] == "APPROVED"
    # The caller's retry (its outbox) gets the same order, nothing charged twice.
    again = _place(web, body)
    assert again.status_code == 200 and again.json()["id"] == o["id"]


def test_sweeper_finishes_a_dead_mains_placements(admin, store, mgr) -> None:
    _connect(mgr)
    m = _menu(mgr)
    ch_id, web = _channel_id(admin, store, "web")
    # Died after the card call: the sweeper re-issues it (Square returns the same
    # payment) and saves the order.
    held = _order([_pizza(m)], method="card", token="cnon:card-nonce-ok")
    total = _quote(web, held)
    ref = _orphan(admin, ch_id, held, charge_cents=total, hold=True)
    assert ref and check(_by_key(web, held["client_order_id"]), "getOrderByKey")["state"] == "pending"
    # Died before the card call: the sweeper runs it again from the start.
    early = _order([_pizza(m)], method="card", token="cnon:card-nonce-ok")
    _orphan(admin, ch_id, early)
    _sweep(admin)
    p = check(_by_key(web, held["client_order_id"]), "getOrderByKey")
    assert p["state"] == "placed"
    assert _hold(admin, p["order"]["id"])["payment_ref"] == ref  # the same hold, not a second one
    assert check(_by_key(web, early["client_order_id"]), "getOrderByKey")["state"] == "placed"


def test_sweeper_voids_when_the_total_moved(admin, store, mgr) -> None:
    _connect(mgr)
    m = _menu(mgr)
    ch_id, web = _channel_id(admin, store, "web")
    email = _email()
    body = _order([_pizza(m)], method="card", token="cnon:card-nonce-ok",
                  customer={"name": "Ada Lovelace", "phone": "4105550101", "email": email})
    total = _quote(web, body)
    ref = _orphan(admin, ch_id, body, charge_cents=total + 100, hold=True)  # the menu moved since the card call
    _sweep(admin)
    p = check(_by_key(web, body["client_order_id"]), "getOrderByKey")
    assert p["state"] == "voided" and p["error"]["code"] == "voided" and "order" not in p
    assert _fake_payment(ref)["status"] == "CANCELED"
    assert wait_until(lambda: any("was not placed" in x["Subject"] for x in _mail(admin, email)), timeout=15)
    assert _notices(admin, store, "order_not_placed")
    # The customer's retry gets the same answer.
    r = _place(web, body)
    assert r.status_code == 409 and check(r, "placeOrder")["code"] == "voided"


def test_expires_at(admin, store, mgr) -> None:
    _connect(mgr)
    m = _menu(mgr)
    ch_id, web = _channel_id(admin, store, "web")
    past = (datetime.now(timezone.utc) - timedelta(seconds=5)).isoformat()
    email = _email()
    late = _order([_pizza(m)], method="card", token="cnon:card-nonce-ok", expires_at=past,
                  customer={"name": "Ada Lovelace", "phone": "4105550101", "email": email})
    r = _place(web, late)
    assert r.status_code == 409 and check(r, "placeOrder")["code"] == "expired"
    assert check(_by_key(web, late["client_order_id"]), "getOrderByKey")["state"] == "expired"
    assert _place(web, late).json()["code"] == "expired"  # recorded: a retry gets the same
    assert wait_until(lambda: any("was not placed" in x["Subject"] for x in _mail(admin, email)), timeout=15)
    # A dead main's placement whose deadline passed after its card call: voided, hold released.
    body = _order([_pizza(m)], method="card", token="cnon:card-nonce-ok", expires_at=past)
    ref = _orphan(admin, ch_id, body, charge_cents=_quote(web, body), hold=True)
    _sweep(admin)
    assert check(_by_key(web, body["client_order_id"]), "getOrderByKey")["state"] == "voided"
    assert _fake_payment(ref)["status"] == "CANCELED"
    # In time: placed as usual.
    ok = _order([_pizza(m)], method="card", token="cnon:card-nonce-ok", expires_at=(datetime.now(timezone.utc) + timedelta(minutes=5)).isoformat())
    assert _place(web, ok).status_code == 201


def test_kitchen_offline(admin, store, mgr) -> None:
    m = _menu(mgr)
    op = _channel(admin, store, "operator")
    _ = store.key  # the store has a Pi now, and it has never connected
    # Away ≥ 30 s counts as offline; right after main restarts nothing is away
    # that long yet, so this waits (on an idle box: at once).
    got: list = []
    assert wait_until(lambda: got.append(_place(op, _order([_pizza(m)]))) or got[-1].status_code == 409, timeout=40)
    r = got[-1]
    assert check(r, "placeOrder")["code"] == "kitchen_offline"
    r2 = _place(op, _order([_pizza(m)]))
    assert r2.json()["code"] == "kitchen_offline"
    assert len(_notices(admin, store, "kitchen_offline")) == 1  # one page per store per 10 min
    # A scheduled order far enough ahead is still taken.
    tomorrow = (datetime.now(NY).date() + timedelta(days=1)).isoformat()
    times = _ok(requests.get(f"{CH_V1}/store/times", params={"type": "pickup", "date": tomorrow}, headers=op, timeout=5))["times"]
    later = _place(op, _order([_pizza(m)], promised_at=times[10]))
    assert later.status_code == 201, later.text
    # The Pi connects: ASAP orders are taken again.
    s = store.online()
    assert s.next()["event"] == "store_config"
    assert _place(op, _order([_pizza(m)])).status_code == 201
    # A store with no Pi set up isn't checked (dev and test stores).


def test_verification_then_the_same_key_again(admin, store, mgr) -> None:
    _connect(mgr)
    m = _menu(mgr)
    web = _channel(admin, store, "web")
    body = _order([_pizza(m)], method="card", token="cnon:card-nonce-verification")
    r = _place(web, body)
    assert r.status_code == 402 and check(r, "placeOrder")["code"] == "verification_required"
    p = check(_by_key(web, body["client_order_id"]), "getOrderByKey")
    assert p["state"] == "pending" and p["error"]["code"] == "verification_required"
    # The buyer verified: the same order, a new token + the verification.
    body["payment"] = {"method": "card", "token": "cnon:card-nonce-ok", "verification_token": "verf:ok"}
    o = check(_place(web, body), "placeOrder")
    assert check(_by_key(web, body["client_order_id"]), "getOrderByKey")["order"]["id"] == o["id"]
