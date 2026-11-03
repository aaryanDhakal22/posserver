"""Tracking (main 3.0, PORTS-PLAN.md step 6).

  * a channel's live stream (/channel/v1/events) follows its order through
    the floor the Pi reports: received → accepted (kitchen #, ready-by) →
    on_delivery (the driver's first name only) → completed
  * /channel/v1/orders/{id}/status: the same state + the steps
  * the public tracking link /track/v1/{token}: no key, no customer, no items
  * Last-Event-ID resumes; an id no longer kept → one `reset`
  * the track permission; another channel's order; another channel's stream
"""

from __future__ import annotations

import json
import queue
import secrets
import socket
import threading
import uuid
from datetime import datetime, timedelta, timezone

import pytest
import requests

from conftest import ADMIN_V1, MAIN_BASE_URL, STORE_V1, store_key
from openapi import check

CH_V1 = f"{MAIN_BASE_URL}/channel/v1"


class ChannelStream:
    """Reads a channel's SSE stream on a thread: (id, event, data) tuples."""

    def __init__(self, key: str, last_event_id: str | None = None, extra: dict | None = None) -> None:
        headers = {"Authorization": f"Bearer {key}", **(extra or {})}
        if last_event_id:
            headers["Last-Event-ID"] = last_event_id
        self.resp = requests.get(f"{CH_V1}/events", headers=headers, stream=True, timeout=(5, 60))
        assert self.resp.status_code == 200, self.resp.text
        assert self.resp.headers["Content-Type"].startswith("text/event-stream")
        self.q: queue.Queue = queue.Queue()
        threading.Thread(target=self._read, daemon=True).start()

    def _read(self) -> None:
        ev: dict = {}
        try:
            for raw in self.resp.iter_lines(decode_unicode=True):
                if raw is None:
                    continue
                if raw == "":
                    if "event" in ev or "data" in ev:
                        self.q.put((ev.get("id"), ev.get("event", "message"), ev.get("data", "")))
                    ev = {}
                    continue
                if raw.startswith(":"):
                    continue
                k, _, v = raw.partition(":")
                ev[k] = v.lstrip(" ")
        except Exception:  # noqa: BLE001 — closed
            pass

    def next(self, timeout: float = 10.0):
        try:
            return self.q.get(timeout=timeout)
        except queue.Empty as exc:
            raise AssertionError(f"no channel event within {timeout}s") from exc

    def status(self, timeout: float = 10.0) -> tuple[str, dict]:
        """The next order.status event: (id, data)."""
        while True:
            eid, event, data = self.next(timeout)
            if event == "order.status":
                return eid, json.loads(data)

    def close(self) -> None:
        # Closing alone waits for the next 15 s keepalive (see tests/sse.py):
        # shut the socket down to wake the reader at once.
        sock = getattr(getattr(self.resp.raw, "_connection", None), "sock", None)
        if sock is not None:
            try:
                sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
        self.resp.close()


def _channel(admin, store, kind="operator", **extra) -> str:
    r = admin.post(f"{ADMIN_V1}/channels", json={"location_id": store.location_id, "kind": kind, "name": kind.title(), **extra}, timeout=5)
    assert r.status_code == 201, r.text
    return r.json()["key"]


def _item(mgr) -> str:
    m = mgr.post(f"{STORE_V1}/me/menu/menus", json={"name": "Main"}, timeout=5).json()
    c = mgr.post(f"{STORE_V1}/me/menu/menus/{m['id']}/categories", json={"name": "Food"}, timeout=5).json()
    return mgr.post(f"{STORE_V1}/me/menu/categories/{c['id']}/items", json={"name": "Garlic Knots", "price_cents": 599}, timeout=5).json()["id"]


def _place(key: str, item_id: str, type_="pickup", **extra) -> dict:
    body = {"client_order_id": "trk-" + secrets.token_hex(5), "type": type_, "lines": [{"item_id": item_id, "qty": 1}],
            "payment": {"method": "cash"}, "customer": {"name": "Grace Hopper", "phone": "4105550111"}, **extra}
    r = requests.post(f"{CH_V1}/orders", json=body, headers={"Authorization": f"Bearer {key}"}, timeout=10)
    assert r.status_code == 201, r.text
    return r.json()


def _floor(device_key: str, order_id: str, status: str, **kw) -> None:
    ev = {"event_id": str(uuid.uuid4()), "type": "order.status", "order_id": order_id, "status": status,
          "at": datetime.now(timezone.utc).isoformat(), **kw}
    env = {"type": "floor.events", "id": str(uuid.uuid4()), "payload": {"events": [ev]}}
    r = requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(device_key), json=env, timeout=10)
    assert r.status_code == 200 and r.json()["payload"]["results"][0]["result"] == "applied", r.text


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


def test_stream_status_and_public_link(admin, store, mgr) -> None:
    item = _item(mgr)
    key = _channel(admin, store)
    stream = ChannelStream(key)
    try:
        o = _place(key, item)
        _, ev = stream.status()
        assert ev["order_id"] == o["id"] and ev["client_order_id"] == o["client_order_id"] and ev["status"] == "received"
        assert ev["driver"] is None and ev["kitchen_number"] is None

        eta = (datetime.now(timezone.utc) + timedelta(minutes=20)).replace(microsecond=0)
        _floor(store.key, o["id"], "accepted", kitchen_number=1101, prep_minutes=20, estimated_ready_at=eta.isoformat())
        _, ev = stream.status()
        assert ev["status"] == "accepted" and ev["kitchen_number"] == 1101
        assert datetime.fromisoformat(ev["estimated_ready_at"].replace("Z", "+00:00")) == eta

        _floor(store.key, o["id"], "on_delivery", driver_name="Abdul Rahman")
        _, ev = stream.status()
        assert ev["status"] == "on_delivery" and ev["driver"] == {"first_name": "Abdul"}
        _floor(store.key, o["id"], "delivered")
        _, ev = stream.status()
        assert ev["status"] == "completed"
    finally:
        stream.close()

    st = check(requests.get(f"{CH_V1}/orders/{o['id']}/status", headers={"Authorization": f"Bearer {key}"}, timeout=5), "getOrderStatus")
    assert [s["status"] for s in st["steps"]] == ["received", "accepted", "on_delivery", "completed"]
    assert st["kitchen_number"] == 1101 and st["driver"] == {"first_name": "Abdul"} and st["client_order_id"] == o["client_order_id"]

    # The customer's link: no key, nothing about the customer or the items.
    assert len(o["track_token"]) >= 30
    r = requests.get(f"{MAIN_BASE_URL}/track/v1/{o['track_token']}", timeout=5)
    pub = check(r, "getTrack")
    assert r.headers.get("Cache-Control") == "no-store"
    assert pub["status"] == "completed" and pub["driver"] == {"first_name": "Abdul"} and pub["store"]["name"] == store.name
    assert "Grace" not in r.text and "Garlic" not in r.text and "client_order_id" not in pub
    for bad in ("nope", "x" * 32, o["track_token"][:-1] + ("A" if o["track_token"][-1] != "A" else "B")):
        r = requests.get(f"{MAIN_BASE_URL}/track/v1/{bad}", timeout=5)
        assert r.status_code == 404 and check(r, "getTrack")["code"] == "not_found"


def test_resume_and_reset(admin, store, mgr) -> None:
    item = _item(mgr)
    key = _channel(admin, store)
    s1 = ChannelStream(key)
    try:
        a = _place(key, item)
        first_id, ev = s1.status()
        assert ev["order_id"] == a["id"]
    finally:
        s1.close()
    b = _place(key, item)  # while nobody listens
    # Resume after the first: the second comes.
    s2 = ChannelStream(key, last_event_id=first_id)
    try:
        _, ev = s2.status()
        assert ev["order_id"] == b["id"] and ev["status"] == "received"
    finally:
        s2.close()
    # An id that isn't kept (or isn't this channel's): reset, then live.
    s3 = ChannelStream(key, last_event_id="999999999999")
    try:
        _, event, _ = s3.next()
        assert event == "reset"
        c = _place(key, item)
        _, ev = s3.status()
        assert ev["order_id"] == c["id"]
    finally:
        s3.close()


def test_who_may_follow(admin, store, mgr) -> None:
    item = _item(mgr)
    key, other = _channel(admin, store), _channel(admin, store, kind="counter")
    o = _place(key, item)
    hdr = {"Authorization": f"Bearer {other}"}
    r = requests.get(f"{CH_V1}/orders/{o['id']}/status", headers=hdr, timeout=5)
    assert r.status_code == 404 and check(r, "getOrderStatus")["code"] == "not_found"
    # Another channel's stream never carries this channel's orders.
    s = ChannelStream(other)
    try:
        _place(key, item)
        with pytest.raises(AssertionError):
            s.status(timeout=3)  # waits on purpose: nothing may arrive
    finally:
        s.close()
    # Without the track permission: 403 on both.
    blind = _channel(admin, store, kind="web", name="Blind",
                     permissions={"menu": True, "quote": True, "orders": True, "track": False, "payments": ["card"]})
    for path in (f"/orders/{o['id']}/status", "/events"):
        r = requests.get(f"{CH_V1}{path}", headers={"Authorization": f"Bearer {blind}"}, timeout=5)
        assert r.status_code == 403 and r.json()["code"] == "forbidden", path
