"""
End-to-end order tests.

Each order in data.py is run through a full round-trip:
    POST /orders  →  GET /orders/{id}  →  assert fields match

Additional tests cover GET /orders/latest and pagination.
"""

import time

import pytest
import requests

from conftest import BASE_URL, RUN_OFFSET
from data import order_requests


# ---------------------------------------------------------------------------
# Utilities
# ---------------------------------------------------------------------------

def _unique(order: dict) -> dict:
    """Shallow-copy an order dict and apply the session run-offset to order_id."""
    copy = dict(order)
    copy["order_id"] = order["order_id"] + RUN_OFFSET
    return copy


def _approx(a, b, rel: float = 1e-3) -> bool:
    """Float equality tolerant of float32 storage precision."""
    denom = max(abs(float(a)), abs(float(b)), 1e-9)
    return abs(float(a) - float(b)) / denom < rel


def _count(obj: dict, key: str) -> int:
    v = obj.get(key)
    return len(v) if v else 0


def _assert_matches(label: str, sent: dict, got: dict) -> None:
    """Assert that the retrieved order contains the expected data."""
    assert got["order_id"]     == sent["order_id"],     f"[{label}] order_id"
    assert got["store_id"]     == sent["store_id"],     f"[{label}] store_id"
    assert got["store_name"]   == sent["store_name"],   f"[{label}] store_name"
    assert got["service_type"] == sent["service_type"], f"[{label}] service_type"
    assert got["is_tax_exempt"] == sent["is_tax_exempt"], f"[{label}] is_tax_exempt"
    assert _approx(got["order_total"],   sent["order_total"]),          f"[{label}] order_total"
    assert _approx(got["balance_owing"], sent.get("balance_owing", 0)), f"[{label}] balance_owing"

    gc, sc = got["customer"], sent["customer"]
    assert gc["first_name"] == sc["first_name"], f"[{label}] customer.first_name"
    assert gc["last_name"]  == sc["last_name"],  f"[{label}] customer.last_name"
    assert gc["phone"]      == sc["phone"],      f"[{label}] customer.phone"

    assert _count(got, "items")        == _count(sent, "items"),        f"[{label}] items count"
    assert _count(got, "taxes")        == _count(sent, "taxes"),        f"[{label}] taxes count"
    assert _count(got, "payments")     == _count(sent, "payments"),     f"[{label}] payments count"
    assert _count(got, "coupons")      == _count(sent, "coupons"),      f"[{label}] coupons count"
    assert _count(got, "misc_charges") == _count(sent, "misc_charges"), f"[{label}] misc_charges count"

    if sent.get("delivery_address"):
        da = got.get("delivery_address")
        assert da, f"[{label}] expected delivery_address"
        assert da["street"] == sent["delivery_address"]["street"], f"[{label}] delivery_address.street"
        assert da["city"]   == sent["delivery_address"]["city"],   f"[{label}] delivery_address.city"

    if sent.get("delivery_provider"):
        dp = got.get("delivery_provider")
        assert dp, f"[{label}] expected delivery_provider"
        assert dp["provider_name"] == sent["delivery_provider"]["provider_name"], f"[{label}] provider_name"
        assert dp["delivery_id"]   == sent["delivery_provider"]["delivery_id"],   f"[{label}] delivery_id"

    if sent.get("items"):
        assert sorted(i["name"] for i in got["items"]) == \
               sorted(i["name"] for i in sent["items"]), f"[{label}] item names"

        assert sorted(len(i.get("modifiers") or []) for i in got["items"]) == \
               sorted(len(i.get("modifiers") or []) for i in sent["items"]), f"[{label}] modifier counts"

    if sent.get("coupons"):
        assert sorted(c["name"] for c in got["coupons"]) == \
               sorted(c["name"] for c in sent["coupons"]), f"[{label}] coupon names"


# ---------------------------------------------------------------------------
# Parametrized round-trip: one test per order in data.py
# ---------------------------------------------------------------------------

@pytest.mark.parametrize("name,order_data", list(order_requests.items()))
def test_create_and_retrieve_by_id(http: requests.Session, name: str, order_data: dict) -> None:
    payload = _unique(order_data)

    post = http.post(f"{BASE_URL}/orders", json=payload)
    assert post.status_code == 201, f"[{name}] POST {post.status_code}: {post.text}"

    get = http.get(f"{BASE_URL}/orders/{payload['order_id']}")
    assert get.status_code == 200, f"[{name}] GET {get.status_code}: {get.text}"

    _assert_matches(name, payload, get.json())


# ---------------------------------------------------------------------------
# GET /orders/latest
# ---------------------------------------------------------------------------

def test_get_latest(http: requests.Session) -> None:
    # Unix timestamp is always increasing and fits in int32 until 2038.
    # It's larger than any data.py order_id + RUN_OFFSET (~260 M max),
    # so it will always become the latest order in the DB.
    sentinel_id = int(time.time())
    payload = {**list(order_requests.values())[0], "order_id": sentinel_id}

    post = http.post(f"{BASE_URL}/orders", json=payload)
    assert post.status_code == 201, f"POST failed: {post.text}"

    latest = http.get(f"{BASE_URL}/orders/latest")
    assert latest.status_code == 200, f"GET /orders/latest failed: {latest.text}"
    assert latest.json()["order_id"] == sentinel_id


# ---------------------------------------------------------------------------
# GET /orders pagination
# ---------------------------------------------------------------------------

def test_pagination_num_limit(http: requests.Session) -> None:
    r = http.get(f"{BASE_URL}/orders", params={"offset": 0, "num": 2})
    assert r.status_code == 200
    assert isinstance(r.json(), list)
    assert len(r.json()) <= 2


def test_pagination_offset_shifts_window(http: requests.Session) -> None:
    page0 = http.get(f"{BASE_URL}/orders", params={"offset": 0, "num": 3}).json()
    page1 = http.get(f"{BASE_URL}/orders", params={"offset": 1, "num": 3}).json()
    if len(page0) > 1 and len(page1) > 0:
        assert page0[0]["order_id"] != page1[0]["order_id"]


def test_pagination_bad_params(http: requests.Session) -> None:
    assert http.get(f"{BASE_URL}/orders", params={"offset": "abc"}).status_code == 400
    assert http.get(f"{BASE_URL}/orders", params={"num": "xyz"}).status_code == 400
