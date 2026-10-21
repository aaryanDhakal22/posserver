"""main's device-authed SSE stream: store_config handshake first, scoped order
frames with trace context, no cross-tenant leakage."""

import json

import requests

from conftest import API_V1, STORE_V1
from sse import SSEStream


def test_sse_requires_key(admin) -> None:
    assert requests.get(f"{API_V1}/events/orders", timeout=5).status_code == 401


def test_first_frame_is_store_config(store) -> None:
    with SSEStream(store.key) as s:
        frame = s.next()
        assert frame["event"] == "store_config", frame
        cfg = json.loads(frame["data"])
        assert cfg["tenant"]["id"] == store.tenant_id
        assert cfg["location"]["id"] == store.location_id
        assert isinstance(cfg["printers"], list)


def test_store_config_reflects_printer_added_by_manager(store) -> None:
    mgr = store.manager_session()
    r = mgr.post(f"{STORE_V1}/me/printers", json={"name": "Pizza", "ip": "10.0.0.42"}, timeout=5)
    assert r.status_code == 201, r.text
    with SSEStream(store.key) as s:
        printers = json.loads(s.next()["data"])["printers"]
        assert any(p["name"] == "Pizza" and p["ip"] == "10.0.0.42" for p in printers), printers


def test_order_frame_for_own_scope_with_traceparent(publish, store) -> None:
    with SSEStream(store.key) as s:
        assert s.next()["event"] == "store_config"
        o = store.order()
        publish(o)
        got = s.wait_event("order", lambda d: d.get("source_event_id") == str(o["order_id"]))
        assert got, "order never arrived on the store's SSE stream"
        assert got["tenant_id"] == store.tenant_id
        assert got["money"]["total_cents"] == 1234
        parts = got.get("_traceparent", "").split("-")
        assert len(parts) == 4 and parts[0] == "00" and len(parts[1]) == 32 and len(parts[2]) == 16, got.get("_traceparent")


def test_no_cross_tenant_leak(publish, make_store) -> None:
    a, b = make_store(), make_store()
    with SSEStream(b.key) as s:
        assert s.next()["event"] == "store_config"
        o = a.order()
        publish(o)
        leaked = s.wait_event("order", lambda d: d.get("source_event_id") == str(o["order_id"]), timeout=10)
        assert leaked is None, f"store B's stream received store A's order: {leaked}"


def test_printer_edit_pushes_store_config_live(store) -> None:
    """Editing printers in /app/store pushes a fresh store_config frame to the
    connected agent mid-stream — no reconnect needed."""
    mgr = store.manager_session()
    with SSEStream(store.key) as s:
        assert s.next()["event"] == "store_config"
        r = mgr.post(f"{STORE_V1}/me/printers", json={"name": "Wings", "ip": "10.0.0.50"}, timeout=5)
        assert r.status_code == 201, r.text
        pid = r.json()["id"]
        pushed = s.wait_event("store_config", lambda d: any(p["name"] == "Wings" for p in d["printers"]), timeout=10)
        assert pushed, "no live store_config after adding a printer"

        mgr.patch(f"{STORE_V1}/me/printers/{pid}", json={"name": "Wings", "ip": "10.0.0.51"}, timeout=5)
        pushed = s.wait_event("store_config", lambda d: any(p["ip"] == "10.0.0.51" for p in d["printers"]), timeout=10)
        assert pushed, "no live store_config after editing a printer"

        mgr.delete(f"{STORE_V1}/me/printers/{pid}", timeout=5)
        pushed = s.wait_event("store_config", lambda d: not any(p["id"] == pid for p in d["printers"]), timeout=10)
        assert pushed, "no live store_config after deleting a printer"
