"""Shared fixtures for main/'s pytest suite (main-only — the cross-module
online → main → agent flow lives in the repo-root `tests/`).

Env (Doppler injects main's dev config via `task test`; the console's `test`
command also injects TARGET_ENV + MAIN_BASE_URL for the chosen env):

  TARGET_ENV      dev (default) | stg. SQS-publishing tests only run on dev.
  MAIN_BASE_URL   default http://localhost:${SERVER_PORT:-1323}
  ADMIN_API_KEY   main's admin bearer — every test provisions its own tenant
                  through /admin/v1, so this is required (tests skip without it).
  SQS_QUEUE_URL   the queue main consumes (dev: ElasticMQ dev-ingest).

Isolation: nothing here depends on the seeded dev restaurants. Each test that
needs a store gets a throwaway tenant + location (+ device key / manager login)
with a unique vendor `store_id` and a unique 8-char name prefix, and deletes it
(cascade) on teardown.
"""

from __future__ import annotations

import itertools
import json
import os
import secrets
import time
import uuid
from collections.abc import Callable
from datetime import datetime
from typing import Any

import boto3
import pytest
import requests

TARGET_ENV = os.getenv("TARGET_ENV", "dev")
IS_DEV = TARGET_ENV == "dev"

SERVER_PORT = os.getenv("SERVER_PORT", "1323")
MAIN_BASE_URL = (os.getenv("MAIN_BASE_URL") or f"http://localhost:{SERVER_PORT}").rstrip("/")
API_V1 = f"{MAIN_BASE_URL}/api/v1"
ADMIN_V1 = f"{MAIN_BASE_URL}/admin/v1"
STORE_V1 = f"{MAIN_BASE_URL}/store/v1"

ADMIN_API_KEY = os.getenv("ADMIN_API_KEY", "")

SQS_QUEUE_URL = os.getenv("SQS_QUEUE_URL", "")
SQS_DLQ_URL = os.getenv("SQS_DLQ_URL") or (SQS_QUEUE_URL + "-dlq" if SQS_QUEUE_URL else "")
AWS_REGION = os.getenv("AWS_REGION", "us-east-1")
if IS_DEV:
    # Dev publishes straight to the local ElasticMQ main consumes from.
    SQS_ENDPOINT = "http://localhost:9324"
    AWS_KEY, AWS_SECRET = "dev", "dev"
    SQS_QUEUE_URL = SQS_QUEUE_URL.replace("elasticmq:9324", "localhost:9324")
    SQS_DLQ_URL = SQS_DLQ_URL.replace("elasticmq:9324", "localhost:9324")
else:
    SQS_ENDPOINT = os.getenv("SQS_ENDPOINT") or None
    AWS_KEY, AWS_SECRET = os.getenv("AWS_ACCESS_KEY_ID"), os.getenv("AWS_SECRET_ACCESS_KEY")

# Per-run offset keeps vendor order_ids unique across consecutive runs.
RUN_OFFSET: int = (int(time.time()) % 100_000) * 10_000

# pytest-xdist worker ("gw0", "gw1", …); "" when running without -n.
XDIST_WORKER = os.getenv("PYTEST_XDIST_WORKER", "")
_WORKER_NUM = int(XDIST_WORKER[2:]) if XDIST_WORKER.startswith("gw") else 0
# Each worker draws order_ids from its own block above the data.py fixtures
# (1001-1006 + RUN_OFFSET), so ids never collide inside a run — the absence
# checks look an id up across every tenant.
_ORDER_IDS = itertools.count(RUN_OFFSET + 1100 + (_WORKER_NUM % 4) * 2000)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def wait_until(fn: Callable[[], Any], *, timeout: float = 15.0, interval: float = 0.3,
               what: str = "condition") -> Any:
    """Poll fn() until truthy or timeout; raise AssertionError with the last value."""
    deadline = time.time() + timeout
    last: Any = None
    while time.time() < deadline:
        try:
            last = fn()
            if last:
                return last
        except requests.RequestException as exc:
            last = exc
        time.sleep(interval)
    raise AssertionError(f"{what} not met within {timeout}s (last={last!r})")


def unique_order_id() -> int:
    return next(_ORDER_IDS)


def unique_store_id() -> int:
    # High range so a test tenant never collides with real (3282) or dev
    # (9001/9002) vendor ids.
    return 700_000_000 + secrets.randbelow(200_000_000)


def store_key(api_key: str) -> dict[str, str]:
    return {"X-STORE-API-KEY": api_key}


@pytest.fixture(scope="session")
def admin() -> requests.Session:
    if not ADMIN_API_KEY:
        pytest.skip("ADMIN_API_KEY not set (run via `task test` / the console)")
    try:
        requests.get(f"{MAIN_BASE_URL}/health", timeout=3).raise_for_status()
    except requests.RequestException as exc:
        pytest.skip(f"main not reachable at {MAIN_BASE_URL}: {exc}")
    s = requests.Session()
    s.headers.update({"Authorization": f"Bearer {ADMIN_API_KEY}", "Content-Type": "application/json"})
    return s


# ---------------------------------------------------------------------------
# Throwaway store (tenant + location [+ device key] [+ manager login])
# ---------------------------------------------------------------------------


# Tenants this process created and has not deleted yet (the session-end sweep).
_CREATED_TENANTS: set[str] = set()


class Store:
    """A throwaway tenant + location. `name` starts with a unique 8-char prefix
    so main's store_name fallback resolves it unambiguously."""

    def __init__(self, admin: requests.Session, *, with_store_id: bool = True) -> None:
        self.admin = admin
        self.tag = secrets.token_hex(4)  # 8 chars — the fallback match key
        self.name = f"{self.tag} Test Pizza"
        self.store_id = unique_store_id() if with_store_id else None
        body: dict[str, Any] = {"slug": f"t-{self.tag}", "name": self.name}
        if self.store_id is not None:
            body["store_id"] = self.store_id
        # Creating a tenant creates its one location (one tenant = one restaurant).
        r = admin.post(f"{ADMIN_V1}/tenants", json=body, timeout=5)
        assert r.status_code == 201, r.text
        self.tenant_id: str = r.json()["id"]
        _CREATED_TENANTS.add(self.tenant_id)
        self.location_id: str = r.json()["location"]["id"]
        self._key: dict | None = None

    # -- device key (the agent's credential) --
    def mint_key(self, name: str = "test-agent") -> dict:
        r = self.admin.post(f"{ADMIN_V1}/devices", json={"location_id": self.location_id, "name": name}, timeout=5)
        assert r.status_code == 201, r.text
        return {"api_key": r.json()["api_key"], "device_id": r.json()["device"]["id"]}

    @property
    def key(self) -> str:
        if self._key is None:
            self._key = self.mint_key()
        return self._key["api_key"]

    @property
    def device_id(self) -> str:
        _ = self.key
        return self._key["device_id"]  # type: ignore[index]

    # -- browser manager session (cookie) --
    def manager_session(self) -> requests.Session:
        username, password = f"mgr{self.tag}", secrets.token_urlsafe(10)
        r = self.admin.patch(f"{ADMIN_V1}/tenants/{self.tenant_id}/credentials",
                             json={"username": username, "password": password}, timeout=5)
        assert r.status_code == 204, r.text
        s = requests.Session()
        r = s.post(f"{STORE_V1}/session", json={"username": username, "password": password}, timeout=5)
        assert r.status_code == 200, r.text
        return s

    # -- vendor payload routed to this store --
    def order(self, order_id: int | None = None, **overrides) -> dict:
        o = vendor_order(order_id or unique_order_id(), store_name=self.name, store_id=self.store_id)
        o.update(overrides)
        return o

    def delete(self) -> None:
        try:
            r = self.admin.delete(f"{ADMIN_V1}/tenants/{self.tenant_id}", timeout=10)
            if r.status_code < 500:
                _CREATED_TENANTS.discard(self.tenant_id)
        except requests.RequestException:
            pass


@pytest.fixture
def store(admin):
    s = Store(admin)
    yield s
    s.delete()


@pytest.fixture
def make_store(admin):
    """Factory for tests that need several stores (isolation) or a store
    without a store_id (name-fallback routing)."""
    made: list[Store] = []

    def _make(**kw) -> Store:
        s = Store(admin, **kw)
        made.append(s)
        return s

    yield _make
    for s in made:
        s.delete()


@pytest.fixture(scope="session", autouse=True)
def sweep_test_tenants():
    """Session-end sweep of the tenants this process created and a test that
    died before its teardown left behind. Per process on purpose: under xdist
    every worker has its own session, and a global sweep would pull stores out
    from under the workers still running."""
    yield
    if not ADMIN_API_KEY:
        return
    h = {"Authorization": f"Bearer {ADMIN_API_KEY}"}
    for tid in list(_CREATED_TENANTS):
        try:
            requests.delete(f"{ADMIN_V1}/tenants/{tid}", headers=h, timeout=10)
        except requests.RequestException:
            pass


# Leaked test tenants older than this are removed at the end of a run; younger
# ones may belong to a run still going (another shell, another agent).
STALE_TENANT_AGE_S = 3600


def pytest_sessionfinish(session, exitstatus) -> None:  # noqa: ARG001
    """Controller only (or a plain run): remove stale leftovers of earlier,
    crashed runs. Session fixtures never run on the xdist controller, hence a
    hook rather than the fixture above."""
    if hasattr(session.config, "workerinput") or not ADMIN_API_KEY:
        return
    h = {"Authorization": f"Bearer {ADMIN_API_KEY}"}
    cutoff = time.time() - STALE_TENANT_AGE_S
    try:
        for t in requests.get(f"{ADMIN_V1}/tenants", headers=h, timeout=5).json():
            if not (t.get("slug", "").startswith("t-") and t.get("name", "").endswith(" Test Pizza")):
                continue
            try:
                created = datetime.fromisoformat(t["created_at"]).timestamp()
            except (KeyError, TypeError, ValueError):
                continue
            if created < cutoff:
                requests.delete(f"{ADMIN_V1}/tenants/{t['id']}", headers=h, timeout=10)
    except (requests.RequestException, ValueError):
        pass


# ---------------------------------------------------------------------------
# SQS
# ---------------------------------------------------------------------------


@pytest.fixture(scope="session")
def sqs():
    if not IS_DEV:
        pytest.skip("SQS-publishing tests run on dev only (never into a shared queue)")
    if not SQS_QUEUE_URL:
        pytest.skip("SQS_QUEUE_URL not set")
    return boto3.client("sqs", endpoint_url=SQS_ENDPOINT, region_name=AWS_REGION,
                        aws_access_key_id=AWS_KEY, aws_secret_access_key=AWS_SECRET)


def online_attrs(*, tenant_id: str | None = None, location_id: str | None = None,
                 source: str = "online") -> dict:
    """MessageAttributes as online sends them. Scope attrs are optional (main
    ignores them for source=online)."""
    attrs = {"source": {"DataType": "String", "StringValue": source}}
    if tenant_id:
        attrs["tenant_id"] = {"DataType": "String", "StringValue": tenant_id}
    if location_id:
        attrs["location_id"] = {"DataType": "String", "StringValue": location_id}
    return attrs


@pytest.fixture
def publish(sqs):
    """publish(payload_dict_or_raw_str, **online_attrs kwargs) → MessageId."""
    def _publish(payload, **attrs) -> str:
        body = payload if isinstance(payload, str) else json.dumps(payload)
        r = sqs.send_message(QueueUrl=SQS_QUEUE_URL, MessageBody=body, MessageAttributes=online_attrs(**attrs))
        return r["MessageId"]
    return _publish


@pytest.fixture
def purged_queue(sqs):
    """Opt-in: start from an empty dev queue. Dev-only by construction (the
    sqs fixture skips elsewhere). ElasticMQ rate-limits purges; on cooldown we
    just carry on — tests don't depend on an empty queue for correctness."""
    try:
        sqs.purge_queue(QueueUrl=SQS_QUEUE_URL)
    except Exception:  # noqa: BLE001
        pass
    yield


def wait_order(api_key: str, source_event_id: int | str, *, timeout: float = 30.0) -> dict:
    """Poll main's scoped by-source lookup until the order lands."""
    def _get():
        r = requests.get(f"{API_V1}/orders/by-source/online/{source_event_id}", headers=store_key(api_key), timeout=3)
        return r.json() if r.status_code == 200 else None
    return wait_until(_get, timeout=timeout, what=f"order {source_event_id} in main")


def admin_find_order(admin: requests.Session, source_event_id: int | str) -> dict | None:
    r = admin.get(f"{ADMIN_V1}/orders", params={"source_event_id": str(source_event_id)}, timeout=5)
    r.raise_for_status()
    rows = r.json()
    return rows[0] if rows else None


# ---------------------------------------------------------------------------
# Vendor payload
# ---------------------------------------------------------------------------


def vendor_order(order_id: int, *, store_name: str, store_id: int | None,
                 customer: tuple[str, str] = ("Pytest", "Main")) -> dict:
    """A minimal valid vendor order (shape the online caster expects)."""
    o = {
        "tVer": "1.0.0",
        "order_id": order_id,
        "vendor_store_id": "",
        "store_name": store_name,
        "service_type": "takeout",
        "submitted_date": "2026-05-12T12:00:00-0400",
        "print_date": "2026-05-12T12:00:00-0400",
        "tip": 0.0,
        "taxes": [],
        "is_tax_exempt": False,
        "order_total": 12.34,
        "balance_owing": 0.0,
        "notes": "",
        "customer": {"first_name": customer[0], "last_name": customer[1], "company": "",
                     "phone": "4105550100", "ext": "", "email": "pytest@example.com"},
        "items": [{"name": "Test Pizza", "size_name": "12 Medium", "size_id": 1, "quantity": 1,
                   "price": 12.34, "plu": "", "who": "", "group_id": "", "notes": "", "modifiers": []}],
        "coupons": [],
        "payments": [],
        "misc_charges": [],
    }
    if store_id is not None:
        o["store_id"] = store_id
    return o


def new_uuid() -> str:
    return str(uuid.uuid4())
