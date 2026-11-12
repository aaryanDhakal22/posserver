"""Store status (main 3.0, archive/PORTS-PLAN.md step 3).

  * /channel/v1/store: open / paused / closed, order types + ETAs, today,
    scheduling, sold out — OpenAPI-checked; the manager's /me/status is the same
  * pause: presets, note, unpause, indefinite (no next-open time), bad input
  * ordering settings: types off, ETAs, ASAP-only; refused values
  * dated hours exceptions: closed today → closed + next opening; replace by
    date; another store can't remove one
  * /channel/v1/store/times: slots every 15 min after the ETA, window, errors
  * sold out + the restock (dev trigger): no-until items and options come
    back, a later out_until stays, a passed one comes back
  * /channel/v1/store/delivery-check (geo fake): inside / outside / not found /
    several places / delivery off / store without a pin / rate limit
"""

from __future__ import annotations

from datetime import datetime, timedelta
from zoneinfo import ZoneInfo

import pytest
import requests

from conftest import ADMIN_V1, MAIN_BASE_URL, STORE_V1, wait_until
from openapi import check
from test_geo import _pin_store, _street, fake_geo  # noqa: F401 (fixture)
from test_geo_choose import _ambiguous

CH_V1 = f"{MAIN_BASE_URL}/channel/v1"
NY = ZoneInfo("America/New_York")  # every test store's zone


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


def _channel(admin, store, kind: str = "web") -> dict:
    r = admin.post(f"{ADMIN_V1}/channels", json={"location_id": store.location_id, "kind": kind, "name": kind.title()}, timeout=5)
    assert r.status_code == 201, r.text
    return {"Authorization": f"Bearer {r.json()['key']}"}


def _status(hdr) -> dict:
    return check(requests.get(f"{CH_V1}/store", headers=hdr, timeout=5), "getStore")


def _ts(s: str) -> datetime:
    return datetime.fromisoformat(s.replace("Z", "+00:00"))


def _today() -> str:
    return datetime.now(NY).date().isoformat()


def test_status_and_pause(admin, store, mgr) -> None:
    hdr = _channel(admin, store)
    st = _status(hdr)
    assert st["state"] == "open" and st["accepting"] is True and st["hours_set"] is False and st["pause"] is None
    assert st["order_types"]["pickup"] == {"enabled": True, "eta_minutes": 20}
    assert st["order_types"]["delivery"]["eta_minutes"] == 45 and st["order_types"]["delivery"]["radius_m"] > 0
    assert st["scheduling"] == {"days_ahead": 7, "slot_minutes": 15} and st["sold_out"] == []
    assert st["store"]["timezone"] == "America/New_York" and st["today"]["date"] == _today()
    assert mgr.get(f"{STORE_V1}/me/status", timeout=5).json()["state"] == "open"

    before = datetime.now().astimezone()
    r = mgr.post(f"{STORE_V1}/me/pause", json={"preset": "30m", "note": "Kitchen backed up"}, timeout=5)
    assert r.status_code == 200, r.text
    st = _status(hdr)
    until = _ts(st["pause"]["until"])
    assert st["state"] == "paused" and st["accepting"] is False and st["pause"]["note"] == "Kitchen backed up"
    assert timedelta(minutes=29) < until - before < timedelta(minutes=31) and _ts(st["next_open_at"]) == until

    assert mgr.post(f"{STORE_V1}/me/pause", json={"preset": "2h"}, timeout=5).status_code == 400
    assert mgr.post(f"{STORE_V1}/me/pause", json={"preset": "15m", "note": "x" * 141}, timeout=5).status_code == 400
    assert mgr.delete(f"{STORE_V1}/me/pause", timeout=5).status_code == 200
    assert _status(hdr)["state"] == "open"

    assert mgr.post(f"{STORE_V1}/me/pause", json={"preset": "indefinite"}, timeout=5).status_code == 200
    st = _status(hdr)
    assert st["state"] == "paused" and st["pause"]["until"] is None and st["next_open_at"] is None
    # Paused until further notice: no times to schedule for either.
    t = check(requests.get(f"{CH_V1}/store/times", params={"type": "pickup", "date": _today()}, headers=hdr, timeout=5), "getStoreTimes")
    assert t["times"] == []
    # "today" ends at local midnight for a store without hours.
    mgr.post(f"{STORE_V1}/me/pause", json={"preset": "today"}, timeout=5)
    until = _ts(_status(hdr)["pause"]["until"]).astimezone(NY)
    assert (until.hour, until.minute) == (0, 0) and until.date() == datetime.now(NY).date() + timedelta(days=1)


def test_ordering_settings(admin, store, mgr) -> None:
    hdr = _channel(admin, store, "counter")
    body = {"pickup_enabled": True, "delivery_enabled": False, "pickup_eta_minutes": 15, "delivery_eta_minutes": 50, "schedule_days": 3}
    r = mgr.put(f"{STORE_V1}/me/ordering", json=body, timeout=5)
    assert r.status_code == 200 and r.json()["pickup_eta_minutes"] == 15, r.text
    st = _status(hdr)
    assert st["order_types"]["delivery"]["enabled"] is False and st["scheduling"]["days_ahead"] == 3 and st["accepting"] is True
    r = requests.get(f"{CH_V1}/store/times", params={"type": "delivery", "date": _today()}, headers=hdr, timeout=5)
    assert check(r, "getStoreTimes")["code"] == "type_off" and r.status_code == 409

    for bad in ({**body, "pickup_eta_minutes": -1}, {**body, "delivery_eta_minutes": 241}, {**body, "schedule_days": 31}):
        assert mgr.put(f"{STORE_V1}/me/ordering", json=bad, timeout=5).status_code == 400, bad
    mgr.put(f"{STORE_V1}/me/ordering", json={**body, "schedule_days": 0}, timeout=5)
    r = requests.get(f"{CH_V1}/store/times", params={"type": "pickup", "date": _today()}, headers=hdr, timeout=5)
    assert r.status_code == 409 and r.json()["code"] == "asap_only"
    mgr.put(f"{STORE_V1}/me/ordering", json={**body, "pickup_enabled": False}, timeout=5)
    st = _status(hdr)
    assert st["state"] == "open" and st["accepting"] is False and st["next_open_at"] is None
    got = mgr.get(f"{STORE_V1}/me/ordering", timeout=5).json()
    assert got["settings"]["pickup_enabled"] is False and got["exceptions"] == []


def test_hours_exceptions(admin, store, mgr, make_store) -> None:
    hdr = _channel(admin, store)
    today = _today()
    r = mgr.post(f"{STORE_V1}/me/hours-exceptions", json={"date": today, "closed": True, "note": "Closed for a private event"}, timeout=5)
    assert r.status_code == 200, r.text
    exc = r.json()
    st = _status(hdr)
    tomorrow = datetime.now(NY).date() + timedelta(days=1)
    assert st["state"] == "closed" and st["accepting"] is False and st["today"]["closed"] is True
    assert st["today"]["note"] == "Closed for a private event"
    assert _ts(st["next_open_at"]).astimezone(NY) == datetime(tomorrow.year, tomorrow.month, tomorrow.day, tzinfo=NY)

    # The same date again replaces it: open 00:00-23:59 today.
    r = mgr.post(f"{STORE_V1}/me/hours-exceptions",
                 json={"date": today, "closed": False, "hours": [{"open": "00:00", "close": "23:59"}], "note": "Short day"}, timeout=5)
    assert r.status_code == 200 and r.json()["id"] == exc["id"], r.text
    st = _status(hdr)
    assert st["state"] == "open" and st["today"]["hours"] == [{"open": "00:00", "close": "23:59"}]
    assert len(mgr.get(f"{STORE_V1}/me/ordering", timeout=5).json()["exceptions"]) == 1

    for bad in ({"date": "2026-02-30", "closed": True}, {"date": today, "closed": False},
                {"date": today, "closed": False, "hours": [{"open": "25:00", "close": "26:00"}]}):
        assert mgr.post(f"{STORE_V1}/me/hours-exceptions", json=bad, timeout=5).status_code == 400, bad
    other = make_store().manager_session()
    try:
        assert other.delete(f"{STORE_V1}/me/hours-exceptions/{exc['id']}", timeout=5).status_code == 404
    finally:
        other.close()
    assert mgr.delete(f"{STORE_V1}/me/hours-exceptions/{exc['id']}", timeout=5).status_code == 204
    assert _status(hdr)["today"]["hours"] == []


def test_times(admin, store) -> None:
    hdr = _channel(admin, store, "operator")
    tomorrow = (datetime.now(NY).date() + timedelta(days=1)).isoformat()
    r = requests.get(f"{CH_V1}/store/times", params={"type": "pickup", "date": tomorrow}, headers=hdr, timeout=5)
    t = check(r, "getStoreTimes")
    first, last = (_ts(x).astimezone(NY) for x in (t["times"][0], t["times"][-1]))
    # No hours = open all day: 00:00 + 20 min → 00:30 (15-min slots), up to midnight.
    assert (first.hour, first.minute) == (0, 30) and (last.hour, last.minute) == (0, 0) and len(t["times"]) == 95
    d = check(requests.get(f"{CH_V1}/store/times", params={"type": "delivery", "date": tomorrow}, headers=hdr, timeout=5), "getStoreTimes")
    assert _ts(d["times"][0]).astimezone(NY).minute == 45
    for params in ({"type": "dinein", "date": tomorrow}, {"type": "pickup", "date": "2020-01-01"},
                   {"type": "pickup", "date": (datetime.now(NY).date() + timedelta(days=9)).isoformat()}, {"type": "pickup"}):
        r = requests.get(f"{CH_V1}/store/times", params=params, headers=hdr, timeout=5)
        assert r.status_code == 400 and check(r, "getStoreTimes")["code"] == "bad_request", params


def test_sold_out_and_restock(admin, store, mgr) -> None:
    hdr = _channel(admin, store)
    m = mgr.post(f"{STORE_V1}/me/menu/menus", json={"name": "Main"}, timeout=5).json()
    c = mgr.post(f"{STORE_V1}/me/menu/menus/{m['id']}/categories", json={"name": "Food"}, timeout=5).json()
    items = {n: mgr.post(f"{STORE_V1}/me/menu/categories/{c['id']}/items", json={"name": n, "price_cents": 500}, timeout=5).json()
             for n in ("Wings", "Soup", "Salad")}
    later = (datetime.now().astimezone() + timedelta(hours=2)).isoformat()
    soon = (datetime.now().astimezone() + timedelta(seconds=1)).isoformat()

    def out(name: str, until: str | None) -> None:
        r = mgr.patch(f"{STORE_V1}/me/menu/items/{items[name]['id']}",
                      json={**items[name], "in_stock": False, "out_until": until}, timeout=5)
        assert r.status_code == 200, r.text

    out("Wings", None)
    out("Soup", later)
    out("Salad", soon)
    g = mgr.post(f"{STORE_V1}/me/menu/modifier-groups", json={"name": "Sauce"}, timeout=5).json()
    o = mgr.post(f"{STORE_V1}/me/menu/modifier-groups/{g['id']}/options", json={"name": "Ranch"}, timeout=5).json()
    mgr.patch(f"{STORE_V1}/me/menu/modifier-groups/{g['id']}/options/{o['id']}", json={"name": "Ranch", "in_stock": False}, timeout=5)

    sold = {x["name"]: x for x in _status(hdr)["sold_out"]}
    assert set(sold) >= {"Wings", "Soup"} and sold["Wings"]["until"] is None and sold["Soup"]["until"]

    def restocked() -> bool:
        r = admin.post(f"{ADMIN_V1}/store-status/restock", json={"location_id": store.location_id}, timeout=5)
        assert r.status_code == 200, r.text
        return [x["name"] for x in _status(hdr)["sold_out"]] == ["Soup"]

    assert wait_until(restocked, timeout=10), _status(hdr)["sold_out"]
    opts = mgr.get(f"{STORE_V1}/me/menu/modifier-groups/{g['id']}/options", timeout=5).json()
    assert opts[0]["in_stock"] is True


@pytest.mark.xdist_group("geo")  # swaps main's global geo providers
def test_delivery_check(admin, store, mgr, make_store, fake_geo) -> None:
    fake = fake_geo
    _pin_store(admin, store)
    hdr = _channel(admin, store)
    near, far, lost, many = _street(), _street(), _street(), _street()
    fake.add(near, 40.6690, -74.1143)
    fake.add(far, 40.9000, -74.1143)  # ~26 km north
    _ambiguous(fake, many)

    def chk(street: str, h=hdr) -> requests.Response:
        return requests.post(f"{CH_V1}/store/delivery-check",
                             json={"line1": street, "city": "Bayonne", "state": "NJ", "zip": "07002"}, headers=h, timeout=10)

    r = check(chk(near), "checkDelivery")
    assert r["deliverable"] is True and r["result"] == "inside" and 0 < r["distance_m"] < r["radius_m"]
    r = check(chk(far), "checkDelivery")
    assert r["deliverable"] is False and r["result"] == "outside" and r["distance_m"] > 20000
    assert check(chk(lost), "checkDelivery")["result"] == "not_found"
    r = check(chk(many), "checkDelivery")
    assert r["result"] == "check" and len(r["candidates"]) >= 2 and r["candidates"][0]["label"]
    r = requests.post(f"{CH_V1}/store/delivery-check", json={"line1": "", "zip": "07002"}, headers=hdr, timeout=5)
    assert r.status_code == 400 and check(r, "checkDelivery")["code"] == "bad_request"

    body = {"pickup_enabled": True, "delivery_enabled": False, "pickup_eta_minutes": 20, "delivery_eta_minutes": 45, "schedule_days": 7}
    mgr.put(f"{STORE_V1}/me/ordering", json=body, timeout=5)
    assert check(chk(near), "checkDelivery")["result"] == "delivery_off"

    # A store without a pin can't answer; 30 checks a minute per channel.
    bare = make_store()
    bare_hdr = _channel(admin, bare)
    codes = [chk(near, bare_hdr) for _ in range(31)]
    assert check(codes[0], "checkDelivery")["result"] == "no_store_location"
    assert codes[-1].status_code == 429 and codes[-1].headers.get("Retry-After")
    check(codes[-1], "checkDelivery")
