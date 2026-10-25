"""SQS → online caster → Postgres → HTTP read-back, for every vendor fixture in
data.py. Each fixture is re-addressed to the test's own throwaway store (its
store_id + name) so routing is deterministic; the SQS message carries only
`source=online` (scope attributes are optional for this source)."""

from datetime import datetime

import pytest
import requests

from conftest import API_V1, RUN_OFFSET, store_key, wait_order
from data import order_requests


def _approx_cents(dollars: float) -> int:
    return round(float(dollars) * 100)


def _assert_matches(label: str, sent: dict, got: dict, store) -> None:
    assert got["tenant_id"] == store.tenant_id, f"[{label}] routed to the wrong tenant"
    assert got["location_id"] == store.location_id, f"[{label}] wrong location"
    assert got["source"] == "online", f"[{label}] source"
    assert got["source_event_id"] == str(sent["order_id"]), f"[{label}] source_event_id"
    assert got["status"] == "received", f"[{label}] status"
    assert got["currency"] == "USD", f"[{label}] currency"
    assert got["money"]["total_cents"] == _approx_cents(sent["order_total"]), f"[{label}] total_cents"
    if "tip" in sent:
        assert got["money"]["tip_cents"] == _approx_cents(sent["tip"]), f"[{label}] tip_cents"

    sc = sent["customer"]
    assert got["customer"]["display_name"] == (sc["first_name"] + " " + sc["last_name"]).strip()
    assert got["customer"]["phone"] == sc["phone"], f"[{label}] customer.phone"
    assert len(got.get("items") or []) == len(sent.get("items") or []), f"[{label}] items count"

    if sent.get("service_type") == "delivery" and sent.get("delivery_address"):
        sa, ga = sent["delivery_address"], got["fulfillment"].get("address") or {}
        assert got["fulfillment"]["type"] == "delivery"
        assert ga.get("line1") == sa["street"] and ga.get("city") == sa["city"]

    # Future / deferred orders: deferred_date → fulfillment.promised_at (UTC).
    promised = (got.get("fulfillment") or {}).get("promised_at")
    if sent.get("deferred_date"):
        want = datetime.strptime(sent["deferred_date"], "%Y-%m-%dT%H:%M:%S%z")
        assert promised, f"[{label}] promised_at missing for deferred_date {sent['deferred_date']}"
        assert datetime.fromisoformat(promised.replace("Z", "+00:00")) == want, f"[{label}] promised_at {promised}"
    else:
        assert not promised, f"[{label}] promised_at set without a deferred_date"

    md = got.get("source_metadata") or {}
    assert md.get("store_id") == sent["store_id"], f"[{label}] source_metadata.store_id"
    assert md.get("store_name") == sent["store_name"], f"[{label}] source_metadata.store_name"


@pytest.mark.parametrize("name,fixture", list(order_requests.items()))
def test_vendor_fixture_round_trip(publish, store, name: str, fixture: dict) -> None:
    payload = dict(fixture)
    payload["order_id"] = fixture["order_id"] + RUN_OFFSET
    payload["store_id"] = store.store_id
    payload["store_name"] = store.name

    publish(payload)
    got = wait_order(store.key, payload["order_id"])
    _assert_matches(name, payload, got, store)

    # Also readable by canonical id under the same scope.
    r = requests.get(f"{API_V1}/orders/{got['id']}", headers=store_key(store.key), timeout=5)
    assert r.status_code == 200, r.text
    assert r.json()["source_event_id"] == str(payload["order_id"])


def test_list_returns_the_stores_orders_only(publish, make_store) -> None:
    a, b = make_store(), make_store()
    oa, ob = a.order(), b.order()
    publish(oa)
    publish(ob)
    wait_order(a.key, oa["order_id"])
    wait_order(b.key, ob["order_id"])

    rows = requests.get(f"{API_V1}/orders", headers=store_key(a.key), params={"num": 50}, timeout=5).json()
    ids = {r["source_event_id"] for r in rows}
    assert str(oa["order_id"]) in ids
    assert str(ob["order_id"]) not in ids, "store A's list leaked store B's order"


def test_pagination(store, publish) -> None:
    orders = [store.order() for _ in range(3)]
    for o in orders:
        publish(o)
    for o in orders:
        wait_order(store.key, o["order_id"])
    h = store_key(store.key)

    page = requests.get(f"{API_V1}/orders", headers=h, params={"offset": 0, "num": 2}, timeout=5)
    assert page.status_code == 200 and len(page.json()) == 2
    rest = requests.get(f"{API_V1}/orders", headers=h, params={"offset": 2, "num": 2}, timeout=5).json()
    assert len(rest) == 1
    assert {o["id"] for o in page.json()}.isdisjoint({o["id"] for o in rest})


def test_pagination_bad_params_400(store) -> None:
    h = store_key(store.key)
    assert requests.get(f"{API_V1}/orders", headers=h, params={"offset": "abc"}, timeout=5).status_code == 400
    assert requests.get(f"{API_V1}/orders", headers=h, params={"num": "xyz"}, timeout=5).status_code == 400


def test_v1_relay_envelope_is_unwrapped(store, publish) -> None:
    """The v1 online relay (still in prod) wraps the vendor JSON as a string in
    {"ID","Payload","DateCreated","CreatedAt"} with no MessageAttributes. v2
    main unwraps it, so it can read the v1 queues (shadow run + cutover)."""
    import json as _json
    o = store.order()
    v1 = {"ID": str(o["order_id"]), "Payload": _json.dumps(o),
          "DateCreated": "2026-10-16", "CreatedAt": "2026-10-16T18:00:00Z"}
    publish(_json.dumps(v1))
    got = wait_order(store.key, o["order_id"])
    assert got["tenant_id"] == store.tenant_id and got["source_event_id"] == str(o["order_id"])


def test_order_list_paging_is_bounded(store) -> None:
    h = store_key(store.key)
    for q in ("num=0", "num=500", "num=-1", "offset=-5"):
        assert requests.get(f"{API_V1}/orders?{q}", headers=h, timeout=5).status_code == 400, q
    assert requests.get(f"{API_V1}/orders?num=200", headers=h, timeout=5).status_code == 200
