"""Edge cases on the ingest + read paths: poison messages must not block the
consumer, duplicate deliveries are idempotent, bad ids/params are clean 4xx."""

import pytest
import requests

from conftest import API_V1, store_key, wait_order


def test_garbage_message_does_not_block(publish, store) -> None:
    publish("this is not json")
    good = store.order()
    publish(good)
    assert wait_order(store.key, good["order_id"])["source_event_id"] == str(good["order_id"])


def test_missing_order_id_does_not_block(publish, store) -> None:
    bad = store.order()
    bad.pop("order_id")
    publish(bad)
    good = store.order()
    publish(good)
    assert wait_order(store.key, good["order_id"])["source_event_id"] == str(good["order_id"])


def test_duplicate_delivery_is_idempotent(publish, store) -> None:
    o = store.order()
    publish(o)
    publish(o)
    # The consumer works a receive batch in order: once an order published
    # after the duplicate has landed, the duplicate was processed (ack-skipped).
    sentinel = store.order()
    publish(sentinel)
    wait_order(store.key, o["order_id"])
    wait_order(store.key, sentinel["order_id"])
    rows = requests.get(f"{API_V1}/orders", headers=store_key(store.key), params={"num": 50}, timeout=5).json()
    assert [r["source_event_id"] for r in rows].count(str(o["order_id"])) == 1


def test_unknown_uuid_404(store) -> None:
    r = requests.get(f"{API_V1}/orders/00000000-0000-0000-0000-000000000000", headers=store_key(store.key), timeout=5)
    assert r.status_code == 404


def test_invalid_uuid_400(store) -> None:
    r = requests.get(f"{API_V1}/orders/not-a-uuid", headers=store_key(store.key), timeout=5)
    assert r.status_code == 400


def test_unknown_source_event_404(store) -> None:
    r = requests.get(f"{API_V1}/orders/by-source/online/999999999999", headers=store_key(store.key), timeout=5)
    assert r.status_code == 404


def test_huge_offset_is_empty(store) -> None:
    r = requests.get(f"{API_V1}/orders", headers=store_key(store.key), params={"offset": 10_000_000, "num": 5}, timeout=5)
    assert r.status_code == 200 and r.json() == []


@pytest.mark.parametrize("num", [0, -1, -5])
def test_non_positive_num(store, num: int) -> None:
    r = requests.get(f"{API_V1}/orders", headers=store_key(store.key), params={"offset": 0, "num": num}, timeout=5)
    assert r.status_code == 400
