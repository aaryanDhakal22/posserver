"""Tiny SSE client for tests: opens main's device-authed stream on a thread and
queues parsed frames ({"event", "data"}), so a test can play the agent."""

from __future__ import annotations

import json
import threading
import time
from queue import Empty, Queue

import requests

from conftest import API_V1, store_key


class SSEStream:
    def __init__(self, api_key: str, *, timeout: float = 60.0) -> None:
        self.resp = requests.get(
            f"{API_V1}/events/orders",
            headers={**store_key(api_key), "Accept": "text/event-stream"},
            stream=True,
            timeout=timeout,
        )
        if self.resp.status_code != 200:
            raise AssertionError(f"SSE connect failed: {self.resp.status_code} {self.resp.text}")
        self.frames: Queue = Queue()
        self._stop = threading.Event()
        self._done = threading.Event()
        threading.Thread(target=self._read, daemon=True).start()

    def _read(self) -> None:
        event, data = "", []
        try:
            for raw in self.resp.iter_lines(decode_unicode=True):
                if self._stop.is_set():
                    return
                if raw is None:
                    continue
                if raw == "":
                    if event or data:
                        self.frames.put({"event": event, "data": "\n".join(data)})
                    event, data = "", []
                elif raw.startswith(":"):
                    continue
                elif raw.startswith("event:"):
                    event = raw[6:].strip()
                elif raw.startswith("data:"):
                    data.append(raw[5:].strip())
        except (requests.RequestException, AttributeError, ValueError):
            return  # connection closed by close()
        finally:
            self._done.set()

    def next(self, *, timeout: float = 10.0) -> dict:
        try:
            return self.frames.get(timeout=timeout)
        except Empty as exc:
            raise AssertionError(f"no SSE frame within {timeout}s") from exc

    def wait_event(self, event: str, predicate=lambda d: True, *, timeout: float = 20.0) -> dict | None:
        """Return the decoded data of the first `event` frame matching predicate."""
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                f = self.frames.get(timeout=0.5)
            except Empty:
                continue
            if f["event"] != event:
                continue
            d = json.loads(f["data"])
            if predicate(d):
                return d
        return None

    def alive(self) -> bool:
        """False once the server closed the stream."""
        return not self._done.is_set()

    def drain(self) -> None:
        """Drop every frame received so far."""
        while True:
            try:
                self.frames.get_nowait()
            except Empty:
                return

    def close(self) -> None:
        self._stop.set()
        self.resp.close()

    def __enter__(self) -> "SSEStream":
        return self

    def __exit__(self, *_exc) -> None:
        self.close()
