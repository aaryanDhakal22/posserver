"""SSE stream tests for main/.

Validates the /api/v1/events/orders endpoint: connection, event format, and
_traceparent injection (so the agent can resume a distributed trace).
"""

import json
import time
import threading
from typing import Optional

import pytest
import requests

from conftest import BASE_URL, API_KEY, RUN_OFFSET, SQS_QUEUE_URL
from data import order_requests


def _sqs_wrap(payload: dict) -> dict:
    return {
        "OrderID": str(payload["order_id"]),
        "Payload": json.dumps(payload),
        "DateCreated": time.strftime("%Y-%m-%d %H:%M:%S"),
        "CreatedAt": time.strftime("%Y-%m-%d %H:%M:%S"),
    }


class _SSECollector:
    """Opens a long-lived GET on /api/v1/events/orders and collects parsed events."""

    def __init__(self, base_url: str, api_key: str) -> None:
        self.url = f"{base_url}/events/orders"
        self.api_key = api_key
        self.events: list[tuple[str, str]] = []
        self._stop = threading.Event()
        self._thread: Optional[threading.Thread] = None
        self._ready = threading.Event()

    def start(self) -> None:
        def run() -> None:
            with requests.get(
                self.url,
                headers={"X-API-Key": self.api_key, "Accept": "text/event-stream"},
                stream=True,
                timeout=30,
            ) as resp:
                assert resp.status_code == 200
                self._ready.set()
                event_type: Optional[str] = None
                data_line: Optional[str] = None
                for raw in resp.iter_lines(decode_unicode=True):
                    if self._stop.is_set():
                        return
                    if raw is None:
                        continue
                    if raw == "":
                        if event_type and data_line is not None:
                            self.events.append((event_type, data_line))
                        event_type, data_line = None, None
                        continue
                    if raw.startswith("event:"):
                        event_type = raw.split(":", 1)[1].strip()
                    elif raw.startswith("data:"):
                        data_line = raw.split(":", 1)[1].strip()
                    # ':' keep-alives / comments are ignored

        self._thread = threading.Thread(target=run, daemon=True)
        self._thread.start()
        assert self._ready.wait(timeout=5), "SSE connection never established"

    def stop(self) -> None:
        self._stop.set()

    def wait_for(self, predicate, timeout: float = 10.0) -> Optional[tuple[str, str]]:
        deadline = time.time() + timeout
        while time.time() < deadline:
            for ev in self.events:
                if predicate(ev):
                    return ev
            time.sleep(0.1)
        return None


def test_sse_connection_establishes(http: requests.Session) -> None:
    """Just opening the connection should return 200 and the connected comment."""
    with requests.get(
        f"{BASE_URL}/events/orders",
        headers={"X-API-Key": API_KEY, "Accept": "text/event-stream"},
        stream=True,
        timeout=5,
    ) as resp:
        assert resp.status_code == 200
        assert resp.headers.get("Content-Type", "").startswith("text/event-stream")
        first = next(resp.iter_lines(decode_unicode=True))
        assert first == ": connected"


def test_sse_delivers_order_event(sqs, http: requests.Session) -> None:
    """Publish a synthetic order, then verify it surfaces as an SSE event."""
    payload = dict(order_requests["sample_real_order"])
    payload["order_id"] = payload["order_id"] + RUN_OFFSET + 42  # unique

    collector = _SSECollector(BASE_URL, API_KEY)
    collector.start()
    try:
        sqs.send_message(QueueUrl=SQS_QUEUE_URL, MessageBody=json.dumps(_sqs_wrap(payload)))

        matched = collector.wait_for(
            lambda ev: ev[0] == "order" and str(payload["order_id"]) in ev[1],
            timeout=15,
        )
        assert matched, f"no matching order event received. saw={collector.events[:5]}"

        _, data = matched
        parsed = json.loads(data)
        assert parsed["order_id"] == payload["order_id"]
        # Trace context must be injected by the broker
        assert "_traceparent" in parsed
        tp = parsed["_traceparent"]
        # W3C traceparent form: 00-<32 hex>-<16 hex>-<02 hex>
        parts = tp.split("-")
        assert len(parts) == 4, f"malformed traceparent: {tp}"
        assert parts[0] == "00"
        assert len(parts[1]) == 32
        assert len(parts[2]) == 16
    finally:
        collector.stop()


@pytest.mark.parametrize("missing_header", ["X-API-Key"])
def test_sse_rejects_unauthenticated(missing_header: str) -> None:
    resp = requests.get(f"{BASE_URL}/events/orders", timeout=5)
    assert resp.status_code in (401, 403)
