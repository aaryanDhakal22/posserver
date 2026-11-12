"""Customers made or changed at the counter (TERMINAL-PLAN 1d, contract §2.5zc).
The test plays the Pi: the floor event customer.upsert over POST
/store/v1/agent/rpc, then reads the store's change feed. Synthetic data only.

  * a new phone → a customer under the Pi's ids (source counter)
  * the Pi's copy behind (a new id for a known phone) → merged into main's
    customer; the import's name is overwritten, the website's never
  * names / notes only when listed in `set`; addresses: known id = edit, the
    same place = blanks filled, else added
  * bad events are answered invalid; another store's Pi never touches them
  * main pushes the command customers.changed to the store's Pi
"""

from __future__ import annotations

import uuid
from datetime import datetime, timezone

import requests

from conftest import STORE_V1, store_key
from sse import SSEStream
from test_accounts import _dev_fakes, _sign_in  # noqa: F401 (_dev_fakes: autouse)
from test_customer_import import _feed_all, _import, _phone
from test_order_port import _channel


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _send(key: str, events: list[dict]) -> dict[str, str]:
    env = {"type": "floor.events", "id": str(uuid.uuid4()), "payload": {"events": events}}
    r = requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(key), json=env, timeout=10)
    assert r.status_code == 200, r.text
    return {x["event_id"]: x["result"] for x in r.json()["payload"]["results"]}


def _upsert(customer: dict, *, set: list[str] = ("name", "notes")) -> dict:
    return {"event_id": str(uuid.uuid4()), "type": "customer.upsert", "at": _now(), "customer": customer, "set": list(set)}


def test_counter_customer_created_under_the_pis_ids(store, make_store) -> None:
    phone, cid, aid = _phone(), str(uuid.uuid4()), str(uuid.uuid4())
    ev = _upsert({"id": cid, "phone": f"({phone[:3]}) {phone[3:6]}-{phone[6:]}", "first_name": "Ada", "last_name": "Test",
                  "notes": "side door", "addresses": [{"id": aid, "line1": "1 Test Street", "line2": "Apt 2", "city": "Towson",
                                                       "zip": "21286"}]})
    assert _send(store.key, [ev]) == {ev["event_id"]: "applied"}
    assert _send(store.key, [ev]) == {ev["event_id"]: "duplicate"}
    feed, _, _, _ = _feed_all(store.key)
    c = feed[phone]
    assert (c["id"], c["first_name"], c["last_name"], c["name"], c["notes"], c["source"]) == \
        (cid, "Ada", "Test", "Ada Test", "side door", "counter")
    assert [(a["id"], a["line1"], a["line2"], a["default"]) for a in c["addresses"]] == [(aid, "1 Test Street", "Apt 2", True)]

    # An edit: the address by its id, the name; notes not in `set` stay.
    ed = _upsert({"id": cid, "phone": phone, "first_name": "Adah", "last_name": "Test", "notes": "",
                  "addresses": [{"id": aid, "line1": "1 Test Street", "line2": "Apt 3", "city": "Towson", "zip": "21286"},
                                {"id": str(uuid.uuid4()), "line1": "1 test st.", "city": "Towson", "zip": "21286"},  # same place
                                {"id": str(uuid.uuid4()), "line1": "7 Work Rd", "city": "Towson"}]}, set=["name"])
    assert _send(store.key, [ed]) == {ed["event_id"]: "applied"}
    feed, _, _, _ = _feed_all(store.key)
    c = feed[phone]
    assert c["name"] == "Adah Test" and c["notes"] == "side door"
    assert sorted((a["line1"], a["line2"]) for a in c["addresses"]) == [("1 Test Street", "Apt 3"), ("7 Work Rd", "")]

    # Another store's Pi: its own customer, never this one.
    other = make_store()
    ev2 = _upsert({"id": cid, "phone": phone, "first_name": "Zed", "last_name": "Other"})
    assert _send(other.key, [ev2]) == {ev2["event_id"]: "applied"}
    feed, _, _, _ = _feed_all(store.key)
    assert feed[phone]["name"] == "Adah Test"
    ofeed, _, _, _ = _feed_all(other.key)
    assert ofeed[phone]["name"] == "Zed Other" and ofeed[phone]["id"] != cid


def test_counter_merges_by_phone_and_never_overwrites_the_website(admin, store) -> None:
    # An imported customer; the Pi made its own copy offline (another id).
    p1 = _phone()
    _import(admin, store, [{"phone": p1, "first_name": "Old", "last_name": "Pos", "notes": "from the old POS",
                            "addresses": [{"line1": "2 Test Avenue", "zip": "21286"}]}])
    main_id = _feed_all(store.key)[0][p1]["id"]
    ev = _upsert({"id": str(uuid.uuid4()), "phone": p1, "first_name": "New", "last_name": "Name", "notes": "counter note",
                  "addresses": [{"id": str(uuid.uuid4()), "line1": "2 test ave", "city": "Towson", "zip": "21286"}]})
    assert _send(store.key, [ev]) == {ev["event_id"]: "applied"}
    c = _feed_all(store.key)[0][p1]
    assert (c["id"], c["name"], c["notes"], c["source"]) == (main_id, "New Name", "counter note", "import")
    assert [(a["line1"], a["city"]) for a in c["addresses"]] == [("2 Test Avenue", "Towson")]

    # The website's own name stays.
    web = _channel(admin, store, "web")
    p2 = _phone()
    _sign_in(admin, web, p2, name="Web Own Test")
    ev = _upsert({"id": str(uuid.uuid4()), "phone": p2, "first_name": "Counter", "last_name": "Typed", "notes": "likes extra sauce"})
    assert _send(store.key, [ev]) == {ev["event_id"]: "applied"}
    c = _feed_all(store.key)[0][p2]
    assert c["name"] == "Web Own Test" and c["notes"] == "likes extra sauce" and c["source"] == "web"


def test_bad_counter_customers_are_invalid(store) -> None:
    good = {"id": str(uuid.uuid4()), "phone": _phone(), "first_name": "Bad", "last_name": "Test"}
    bad = [
        _upsert({**good, "id": "nope"}),
        _upsert({**good, "phone": "555-0101"}),
        _upsert({**good, "addresses": [{"line1": "", "city": "Towson"}]}),
        _upsert(good, set=["email"]),
        {"event_id": str(uuid.uuid4()), "type": "customer.upsert", "at": _now()},
    ]
    assert set(_send(store.key, bad).values()) == {"invalid"}
    # Its phone can't be changed under a known id.
    ok = _upsert(good)
    moved = _upsert({**good, "phone": _phone()})
    assert _send(store.key, [ok, moved]) == {ok["event_id"]: "applied", moved["event_id"]: "invalid"}


def test_customers_changed_is_pushed_to_the_pi(admin, store) -> None:
    with SSEStream(store.key) as s:
        assert s.next()["event"] == "store_config"
        # The watcher's first look at a store only records it (every 3 s):
        # change the customers until a push comes.
        cmd = None
        for _ in range(4):
            _import(admin, store, [{"phone": _phone(), "first_name": "Push", "last_name": "Test"}])
            cmd = s.wait_event("command", lambda d: d.get("type") == "customers.changed", timeout=5)
            if cmd:
                break
        assert cmd and cmd["payload"]["at"], "customers.changed was not pushed"
