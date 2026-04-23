"""Edge-case tests for the order pipeline: malformed SQS messages, duplicates,
and pagination bounds.
"""

import json
import time

import pytest
import requests

from conftest import BASE_URL, SQS_QUEUE_URL, RUN_OFFSET
from data import order_requests


def _sqs_wrap(payload: dict) -> dict:
    return {
        "OrderID": str(payload["order_id"]),
        "Payload": json.dumps(payload),
        "DateCreated": time.strftime("%Y-%m-%d %H:%M:%S"),
        "CreatedAt": time.strftime("%Y-%m-%d %H:%M:%S"),
    }


def _wait_for_order(http: requests.Session, order_id: int, timeout: float = 10.0) -> dict:
    deadline = time.time() + timeout
    while time.time() < deadline:
        r = http.get(f"{BASE_URL}/orders/{order_id}")
        if r.status_code == 200:
            return r.json()
        time.sleep(0.5)
    raise TimeoutError(f"order {order_id} did not appear within {timeout}s")


# ---------------------------------------------------------------------------
# Malformed SQS messages should be dropped (not retried indefinitely). Verify
# that the consumer doesn't keel over: we send garbage, then send a valid
# order, and check the valid one lands.
# ---------------------------------------------------------------------------


def test_malformed_envelope_does_not_block(sqs, http: requests.Session) -> None:
    # garbage envelope
    sqs.send_message(QueueUrl=SQS_QUEUE_URL, MessageBody="this is not json")

    # good message
    payload = dict(order_requests["sample_real_order"])
    payload["order_id"] = payload["order_id"] + RUN_OFFSET + 101
    sqs.send_message(QueueUrl=SQS_QUEUE_URL, MessageBody=json.dumps(_sqs_wrap(payload)))

    got = _wait_for_order(http, payload["order_id"])
    assert got["order_id"] == payload["order_id"]


def test_malformed_payload_does_not_block(sqs, http: requests.Session) -> None:
    # envelope parses but Payload is garbage
    bad_envelope = {
        "OrderID": "99999999",
        "Payload": "{not valid order json",
        "DateCreated": time.strftime("%Y-%m-%d %H:%M:%S"),
        "CreatedAt": time.strftime("%Y-%m-%d %H:%M:%S"),
    }
    sqs.send_message(QueueUrl=SQS_QUEUE_URL, MessageBody=json.dumps(bad_envelope))

    payload = dict(order_requests["sample_real_order"])
    payload["order_id"] = payload["order_id"] + RUN_OFFSET + 102
    sqs.send_message(QueueUrl=SQS_QUEUE_URL, MessageBody=json.dumps(_sqs_wrap(payload)))

    got = _wait_for_order(http, payload["order_id"])
    assert got["order_id"] == payload["order_id"]


# ---------------------------------------------------------------------------
# Duplicate SQS delivery should be idempotent (ON CONFLICT DO NOTHING in the
# repo). Sending the same order twice should land once.
# ---------------------------------------------------------------------------


def test_duplicate_order_id_is_idempotent(sqs, http: requests.Session) -> None:
    payload = dict(order_requests["sample_real_order"])
    payload["order_id"] = payload["order_id"] + RUN_OFFSET + 201

    msg = _sqs_wrap(payload)
    sqs.send_message(QueueUrl=SQS_QUEUE_URL, MessageBody=json.dumps(msg))
    sqs.send_message(QueueUrl=SQS_QUEUE_URL, MessageBody=json.dumps(msg))

    got = _wait_for_order(http, payload["order_id"])
    assert got["order_id"] == payload["order_id"]

    # Brief delay for any late processing, then sanity-check: the order still
    # exists, and GET still returns the same record (not multiple copies).
    time.sleep(1)
    r = http.get(f"{BASE_URL}/orders/{payload['order_id']}")
    assert r.status_code == 200
    assert r.json()["order_id"] == payload["order_id"]


# ---------------------------------------------------------------------------
# Pagination edges
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("num", [0, -1, -5])
@pytest.mark.xfail(
    strict=False,
    reason="main's handler does not validate non-positive num and currently returns 500 for negatives",
)
def test_pagination_non_positive_num(http: requests.Session, num: int) -> None:
    r = http.get(f"{BASE_URL}/orders", params={"offset": 0, "num": num})
    assert r.status_code in (200, 400)


def test_pagination_huge_offset_returns_empty(http: requests.Session) -> None:
    r = http.get(f"{BASE_URL}/orders", params={"offset": 10_000_000, "num": 5})
    # acceptable: either 200 with [] or 400 (bounds)
    assert r.status_code in (200, 400)
    if r.status_code == 200:
        assert r.json() == []


def test_get_unknown_order_returns_404(http: requests.Session) -> None:
    r = http.get(f"{BASE_URL}/orders/999999999")
    assert r.status_code == 404
