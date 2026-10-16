"""Online-order routing: main resolves the tenant from the vendor body's
store_id (tenants.store_id) first, then falls back to the fuzzy store_name
match; anything unroutable is never persisted and redrives to the DLQ. The SQS
scope attributes are optional for source=online."""

import json
import time

import pytest

from conftest import (
    SQS_DLQ_URL,
    admin_find_order,
    unique_store_id,
    wait_order,
    wait_until,
)


def test_store_id_wins_over_a_wrong_name(publish, store) -> None:
    o = store.order(store_name="Completely Unrelated Name")
    publish(o)
    got = wait_order(store.key, o["order_id"])
    assert got["tenant_id"] == store.tenant_id


def test_no_store_id_falls_back_to_name(publish, make_store) -> None:
    s = make_store(with_store_id=False)
    o = s.order()
    assert "store_id" not in o
    publish(o)
    assert wait_order(s.key, o["order_id"])["tenant_id"] == s.tenant_id


def test_unmapped_store_id_falls_back_to_name(publish, store) -> None:
    o = store.order(store_id=unique_store_id())  # an id no tenant holds
    publish(o)
    assert wait_order(store.key, o["order_id"])["tenant_id"] == store.tenant_id


def test_store_id_beats_another_tenants_name(publish, make_store) -> None:
    """If store_id and store_name point at different tenants, store_id wins."""
    a, b = make_store(), make_store()
    o = a.order(store_name=b.name)
    publish(o)
    assert wait_order(a.key, o["order_id"])["tenant_id"] == a.tenant_id


def test_unroutable_order_is_not_persisted(admin, publish) -> None:
    from conftest import unique_order_id, vendor_order
    oid = unique_order_id()
    publish(vendor_order(oid, store_name="zz-nowhere-diner", store_id=unique_store_id()))
    time.sleep(6)
    assert admin_find_order(admin, oid) is None


def test_scope_attributes_are_optional_for_online(publish, store) -> None:
    # publish() sends only source=online by default; also accept a message whose
    # scope attrs point at a DIFFERENT tenant — online routing ignores them.
    o = store.order()
    publish(o, tenant_id="00000000-0000-0000-0000-0000000000aa", location_id="00000000-0000-0000-0000-0000000000bb")
    assert wait_order(store.key, o["order_id"])["tenant_id"] == store.tenant_id


def test_non_online_source_without_scope_is_dropped(admin, publish, store) -> None:
    o = store.order()
    publish(o, source="mobile")  # mobile requires scope attrs (and has no caster yet)
    time.sleep(5)
    assert admin_find_order(admin, o["order_id"]) is None


@pytest.mark.slow
def test_unroutable_order_lands_in_dlq(sqs, publish) -> None:
    """End-to-end redrive: dev ElasticMQ moves dev-ingest → dev-ingest-dlq after
    3 receives (30s visibility each), so this takes ~90s."""
    from conftest import unique_order_id, vendor_order
    oid = unique_order_id()
    publish(vendor_order(oid, store_name="zz-dlq-diner", store_id=unique_store_id()))

    def in_dlq():
        # The dev DLQ accumulates old messages: walk all of it (each read hides
        # what it saw for a while so the next read moves on), not just the
        # first 10.
        for _ in range(50):
            r = sqs.receive_message(QueueUrl=SQS_DLQ_URL, MaxNumberOfMessages=10, WaitTimeSeconds=1,
                                    VisibilityTimeout=20)
            msgs = r.get("Messages", [])
            if not msgs:
                return False
            for m in msgs:
                try:
                    if json.loads(m["Body"]).get("order_id") == oid:
                        sqs.delete_message(QueueUrl=SQS_DLQ_URL, ReceiptHandle=m["ReceiptHandle"])
                        return True
                except (ValueError, AttributeError):
                    continue
        return False

    wait_until(in_dlq, timeout=150, interval=5, what="unroutable order in the DLQ")
