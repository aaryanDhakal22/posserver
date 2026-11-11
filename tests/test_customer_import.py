"""Customers for the counter (TERMINAL-PLAN.md stage 0c), on a throwaway store
with synthetic customers only (555-01xx-style numbers, "Test" names):

  * the admin bulk import (NDJSON / JSON, dry run, counts + skip reasons,
    addresses de-duplicated by line 1 + zip, a re-import is a no-op, ≤ 5,000)
  * the website's own name and the import: never overwritten; first + last
    follow the website's name
  * the Pi's change feed (agent key only): pages, cursor, deletions, store
    isolation
"""

from __future__ import annotations

import json
import secrets

import requests

from conftest import ADMIN_V1, STORE_V1, store_key
from test_accounts import CH_V1, _dev_fakes, _sign_in  # noqa: F401 (_dev_fakes: autouse)
from test_order_port import _channel, _ok


def _phone() -> str:
    return "410" + str(secrets.randbelow(9000000) + 1000000)


def _import(admin, store, recs, *, dry_run: bool = False, ndjson: bool = True, status: int = 200):
    url = f"{ADMIN_V1}/locations/{store.location_id}/customers/import"
    params = {"dry_run": "1"} if dry_run else None
    if ndjson:
        body = "\n".join(json.dumps(r) for r in recs) + "\n"
        r = admin.post(url, data=body.encode(), params=params, headers={"Content-Type": "application/x-ndjson"}, timeout=60)
    else:
        r = admin.post(url, json={"customers": recs}, params=params, timeout=60)
    return _ok(r, status)


def _feed_all(key: str, since: str = "", limit: int = 500) -> tuple[dict, dict, set, str]:
    """Pages the feed to its end → (customers by phone, last page, deleted ids, cursor)."""
    by_phone: dict[str, dict] = {}
    deleted: set[str] = set()
    while True:
        page = _ok(requests.get(f"{STORE_V1}/agent/customers", params={"since": since, "limit": limit},
                                headers=store_key(key), timeout=10))
        for c in page["customers"]:
            by_phone[c["phone"]] = c
        deleted |= set(page["deleted"])
        since = page["cursor"]
        if not page["has_more"]:
            return by_phone, page, deleted, since


def test_import_counts_dry_run_and_dedupe(admin, store) -> None:
    p1, p2, p3 = _phone(), _phone(), _phone()
    recs = [
        {"ref": "st:1", "phone": f"({p1[:3]}) {p1[3:6]}-{p1[6:]}", "first_name": "Ada", "last_name": "Test", "notes": "ring twice",
         "addresses": [{"label": "Home", "line1": "1 Test Street", "line2": "Apt 2", "city": "Towson", "state": "md", "zip": "21286"},
                       {"line1": "1 test st.", "zip": "21286-0001", "notes": "side door"},   # same place
                       {"line1": "", "city": "Towson"},                                       # no line 1
                       {"line1": "9 Test Rd"}]},                                              # no city or zip
        {"ref": "st:2", "phone": "555-0101"},                                                 # 7 digits
        {"ref": "st:3", "phone": p2},                                                         # nothing to keep
        {"ref": "st:4", "phone": "1" + p3, "name": "Bo Q Test"},
    ]
    dry = _import(admin, store, recs, dry_run=True)
    assert dry["dry_run"] is True and dry["received"] == 4 and dry["created"] == 2 and dry["skipped"] == 2
    assert dry["skip_reasons"] == {"bad_phone": 1, "empty": 1}
    assert [(r["index"], r["ref"], r["reason"]) for r in dry["skipped_rows"]] == [(1, "st:2", "bad_phone"), (2, "st:3", "empty")]
    assert dry["addresses"]["added"] == 1 and dry["addresses"]["matched"] == 1
    assert dry["addresses"]["skip_reasons"] == {"no_line1": 1, "no_city_or_zip": 1}
    feed, _, _, _ = _feed_all(store.key)
    assert feed == {}, "a dry run saved customers"

    res = _import(admin, store, recs)
    assert {k: res[k] for k in ("dry_run", "created", "updated", "unchanged", "skipped")} == \
        {"dry_run": False, "created": 2, "updated": 0, "unchanged": 0, "skipped": 2}
    feed, _, _, _ = _feed_all(store.key)
    ada = feed[p1]
    assert (ada["first_name"], ada["last_name"], ada["name"], ada["notes"], ada["source"]) == \
        ("Ada", "Test", "Ada Test", "ring twice", "import")
    assert len(ada["addresses"]) == 1
    a = ada["addresses"][0]
    # The second line of the same place filled the blank notes; nothing was overwritten.
    assert (a["line1"], a["line2"], a["state"], a["zip"], a["notes"], a["default"]) == \
        ("1 Test Street", "Apt 2", "MD", "21286", "side door", True)
    bo = feed[p3]
    assert (bo["first_name"], bo["last_name"], bo["name"], bo["addresses"]) == ("Bo Q", "Test", "Bo Q Test", [])

    # The same batch again (as JSON this time) changes nothing.
    again = _import(admin, store, recs, ndjson=False)
    assert (again["created"], again["updated"], again["unchanged"]) == (0, 0, 2)

    # A later import updates what the import wrote and adds a new address.
    upd = _import(admin, store, [{"phone": p1, "first_name": "Ada", "last_name": "Tester",
                                  "addresses": [{"line1": "2 Test Ave", "city": "Towson", "zip": "21204"}]}])
    assert upd["updated"] == 1 and upd["addresses"]["added"] == 1
    feed, _, _, _ = _feed_all(store.key)
    assert feed[p1]["name"] == "Ada Tester" and feed[p1]["notes"] == "ring twice" and len(feed[p1]["addresses"]) == 2


def test_import_refusals(admin, store) -> None:
    url = f"{ADMIN_V1}/locations/{store.location_id}/customers/import"
    r = admin.post(url, data=b'{"phone": "4105550101"}\n{oops\n', headers={"Content-Type": "application/x-ndjson"}, timeout=10)
    assert r.status_code == 400 and "line 2" in r.json()["error"]
    many = [{"phone": _phone(), "first_name": "Test"} for _ in range(5001)]
    r = admin.post(url, json=many, timeout=30)
    assert r.status_code == 413 and r.json()["code"] == "too_many_records"
    r = admin.post(f"{ADMIN_V1}/locations/00000000-0000-0000-0000-000000000000/customers/import", json=[], timeout=5)
    assert r.status_code == 404
    r = requests.post(url, json=[], timeout=5)  # no admin key
    assert r.status_code == 401


def test_website_name_wins_and_names_stay_in_step(admin, store) -> None:
    web = _channel(admin, store, "web")
    phone = _phone()
    _, me = _sign_in(admin, web, phone, name="Ada Own Test")
    feed, _, _, _ = _feed_all(store.key)
    c = feed[phone]
    assert (c["first_name"], c["last_name"], c["source"]) == ("Ada Own", "Test", "web")

    res = _import(admin, store, [{"phone": phone, "first_name": "Old", "last_name": "Name", "notes": "from the old POS",
                                  "addresses": [{"line1": "3 Test Ln", "city": "Towson", "zip": "21286"}]}])
    assert res["updated"] == 1 and res["kept"] == {"name": 1}
    feed, _, _, _ = _feed_all(store.key)
    c = feed[phone]
    assert c["name"] == "Ada Own Test" and c["notes"] == "from the old POS" and c["source"] == "web"
    assert [a["line1"] for a in c["addresses"]] == ["3 Test Ln"]
    # The website sees the imported address; its account JSON is unchanged otherwise.
    acct = _ok(requests.get(f"{CH_V1}/account", headers=me, timeout=5))
    assert acct["name"] == "Ada Own Test" and "first_name" not in acct and "notes" not in acct
    assert [a["line1"] for a in acct["addresses"]] == ["3 Test Ln"]

    # The website renames: first + last follow.
    _ok(requests.patch(f"{CH_V1}/account", json={"name": "Ada Lovelace-Test"}, headers=me, timeout=5))
    feed, _, _, _ = _feed_all(store.key)
    assert (feed[phone]["first_name"], feed[phone]["last_name"]) == ("Ada", "Lovelace-Test")

    # An imported customer who signs in keeps the imported name (sign-in only fills an empty one).
    p2 = _phone()
    _import(admin, store, [{"phone": p2, "first_name": "Imp", "last_name": "Test"}])
    _sign_in(admin, web, p2, name="Other Name")
    feed, _, _, _ = _feed_all(store.key)
    assert feed[p2]["name"] == "Imp Test" and feed[p2]["source"] == "import"


def test_feed_pages_deletions_and_isolation(admin, store, make_store) -> None:
    phones = [_phone() for _ in range(5)]
    _import(admin, store, [{"phone": p, "first_name": "Feed", "last_name": f"Test{i}",
                            "addresses": [{"line1": f"{i} Test Ct", "zip": "21286"}]} for i, p in enumerate(phones)])

    # Paged two at a time: every customer once, has_more until the end.
    pages, since, seen = 0, "", []
    while True:
        page = _ok(requests.get(f"{STORE_V1}/agent/customers", params={"since": since, "limit": 2},
                                headers=store_key(store.key), timeout=10))
        pages += 1
        seen += [c["phone"] for c in page["customers"]]
        assert set(page) == {"customers", "deleted", "cursor", "has_more", "reset"} and page["reset"] is False
        since = page["cursor"]
        if not page["has_more"]:
            break
    assert pages >= 3 and sorted(seen) == sorted(phones)
    c = page["customers"][-1] if page["customers"] else None
    if c:
        assert set(c) == {"id", "phone", "first_name", "last_name", "name", "notes", "source", "updated_at", "addresses"}

    # The last cursor stays 2 minutes behind: a poll right away sends the same (recent) rows again.
    again = _ok(requests.get(f"{STORE_V1}/agent/customers", params={"since": since}, headers=store_key(store.key), timeout=10))
    assert sorted(c["phone"] for c in again["customers"]) == sorted(phones)

    # A customer deleted on the website comes as a deletion.
    web = _channel(admin, store, "web")
    gone = _phone()
    s, me = _sign_in(admin, web, gone, name="Gone Test")
    assert requests.delete(f"{CH_V1}/account", headers=me, timeout=10).status_code == 204
    feed, _, deleted, _ = _feed_all(store.key)
    assert s["customer"]["id"] in deleted and gone not in feed

    # Another store sees none of it; bad cursors and missing keys are refused.
    other = make_store()
    feed, _, deleted, _ = _feed_all(other.key)
    assert feed == {} and deleted == set()
    r = requests.get(f"{STORE_V1}/agent/customers", params={"since": "nope"}, headers=store_key(store.key), timeout=5)
    assert r.status_code == 400 and r.json()["code"] == "bad_cursor"
    assert requests.get(f"{STORE_V1}/agent/customers", timeout=5).status_code == 401
