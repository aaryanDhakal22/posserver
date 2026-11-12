"""Cash drawers + the Z report from the Pi (terminal Stage 2, contract/main.md §2.5zb).
The test plays the Pi: floor events over POST /store/v1/agent/rpc; the store's manager reads them back.

  * drawer.opened / drawer.movement / drawer.closed → GET /store/v1/me/drawers[?date=] + /me/drawers/:id
    (date = the store-local date the session opened; movements with their session)
  * a replayed drawer.opened never reopens a closed session (stale); a replay of the same event is a duplicate;
    drawer.closed applies without an opened before it; a movement may come before its session
  * z.report → GET /store/v1/me/reports/z[?date=] + /me/reports/z/:id (one per batch: a rebuilt Z supersedes it;
    the old one stays readable as a version)
  * another store's ids are 404; bad events are answered invalid; a bad date is 400
"""

from __future__ import annotations

import uuid

import requests

from conftest import STORE_V1, store_key

ME = f"{STORE_V1}/me"


def _send(key: str, events: list[dict]) -> dict[str, str]:
    env = {"type": "floor.events", "id": str(uuid.uuid4()), "payload": {"events": events}}
    r = requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(key), json=env, timeout=10)
    assert r.status_code == 200, r.text
    return {x["event_id"]: x["result"] for x in r.json()["payload"]["results"]}


def _ev(typ: str, **kw) -> dict:
    return {"event_id": str(uuid.uuid4()), "type": typ, "at": "2026-11-11T20:00:00Z", **kw}


def _ok(r: requests.Response, status: int = 200) -> dict:
    assert r.status_code == status, f"{r.status_code}: {r.text}"
    return r.json()


def _drawer(**kw) -> dict:
    d = {"id": str(uuid.uuid4()), "device_id": "dev-till-1", "device_name": "Till 1", "staff_id": str(uuid.uuid4()),
         "staff_name": "Cara S.", "batch_id": 7, "opened_at": "2026-11-11T15:00:00Z", "start_cents": 15000}
    d.update(kw)
    return d


def _closed(d: dict, **kw) -> dict:
    c = {**d, "closed_at": "2026-11-11T21:00:00Z", "closed_by": d["staff_id"], "close_reason": "counted",
         "counted_cents": 23950, "denominations": [{"value_cents": 2000, "count": 11}, {"value_cents": 100, "count": 19},
                                                   {"value_cents": 50, "count": 1}],
         "expected_cents": 24000, "over_short_cents": -50, "flagged": False, "cash_sales_cents": 9500,
         "cash_refunds_cents": 500, "cash_tips_cents": 0, "pay_ins_cents": 1000, "pay_outs_cents": 1000, "no_sales": 2,
         "payments": 5}
    c.update(kw)
    return c


def _z(batch: int, date: str, **kw) -> dict:
    z = {"id": str(uuid.uuid4()), "version": 1, "batch_id": batch, "business_date": date, "timezone": "America/New_York",
         "opened_at": f"{date}T15:00:00Z", "closed_at": f"{date}T23:00:00Z", "close_kind": "manual", "forced": False,
         "counts": {"orders": 3, "counter": 2, "pickup": 1, "delivery": 0, "online": 0, "online_pickup": 0,
                    "online_delivery": 0, "voided": 0, "cancelled": 0},
         "sales": {"items_cents": 6000, "discounts_cents": 0, "fees_cents": 0, "tax_cents": 360, "tips_in_total_cents": 0,
                   "total_cents": 6360},
         "tenders": [{"tender": "cash", "count": 3, "collected_cents": 6360, "refunded_cents": 0, "tips_cents": 0,
                      "net_cents": 6360}],
         "over_short_cents": -50, "reconcile": {"tenders_cents": 6360, "refunds_cents": 0, "unpaid_cents": 0,
                                                "sales_total_cents": 6360, "tips_cents": 0, "difference_cents": 0,
                                                "drawers_difference_cents": 0}}
    z.update(kw)
    return z


def test_drawer_sessions(make_store) -> None:
    store, other = make_store(), make_store()
    mgr = store.manager_session()
    d = _drawer()
    # Opened at 22:00 New York time on the 11th (03:00 UTC on the 12th): it belongs to the 11th.
    late = _drawer(device_id="dev-till-2", device_name="Till 2", opened_at="2026-11-12T03:00:00Z", start_cents=10000)
    opened, opened_late = _ev("drawer.opened", drawer=d), _ev("drawer.opened", drawer=late)
    move_in = _ev("drawer.movement", movement={"id": str(uuid.uuid4()), "drawer_id": d["id"], "kind": "pay_in",
                                              "amount_cents": 1000, "reason": "change from the bank", "staff_id": d["staff_id"],
                                              "at": "2026-11-11T16:00:00Z"})
    move_out = _ev("drawer.movement", movement={"id": str(uuid.uuid4()), "drawer_id": d["id"], "kind": "pay_out",
                                               "amount_cents": 1000, "reason": "ice", "staff_id": d["staff_id"],
                                               "approved_by": str(uuid.uuid4()), "at": "2026-11-11T17:00:00Z"})
    res = _send(store.key, [opened, opened_late, move_in, move_out])
    assert set(res.values()) == {"applied"}, res
    # The same events again: duplicates.
    assert set(_send(store.key, [opened, move_in]).values()) == {"duplicate"}

    listed = _ok(mgr.get(f"{ME}/drawers", timeout=5))["drawers"]
    assert [x["id"] for x in listed] == [late["id"], d["id"]]  # newest opened first
    assert listed[1]["closed_at"] is None and listed[1]["start_cents"] == 15000 and listed[1]["staff_name"] == "Cara S."
    on_11th = _ok(mgr.get(f"{ME}/drawers", params={"date": "2026-11-11"}, timeout=5))["drawers"]
    assert {x["id"] for x in on_11th} == {d["id"], late["id"]}
    assert _ok(mgr.get(f"{ME}/drawers", params={"date": "2026-11-12"}, timeout=5))["drawers"] == []

    closed = _ev("drawer.closed", drawer=_closed(d))
    assert _send(store.key, [closed]) == {closed["event_id"]: "applied"}
    # A drawer.opened arriving late (another event id) never reopens it.
    reopen = _ev("drawer.opened", drawer={**d, "start_cents": 1})
    assert _send(store.key, [reopen]) == {reopen["event_id"]: "stale"}

    one = _ok(mgr.get(f"{ME}/drawers/{d['id']}", timeout=5))
    dr = one["drawer"]
    assert dr["close_reason"] == "counted" and dr["counted_cents"] == 23950 and dr["expected_cents"] == 24000
    assert dr["over_short_cents"] == -50 and dr["start_cents"] == 15000 and dr["no_sales"] == 2 and dr["payments"] == 5
    assert dr["denominations"][0] == {"value_cents": 2000, "count": 11}
    assert [(m["kind"], m["amount_cents"], m["reason"]) for m in one["movements"]] == [
        ("pay_in", 1000, "change from the bank"), ("pay_out", 1000, "ice")]

    # Close batch closes a drawer nobody counted (no opened before it is fine): flagged, counted unknown.
    batch_closed = _drawer(device_id="dev-till-3")
    ev = _ev("drawer.closed", drawer=_closed(batch_closed, close_reason="batch_close", counted_cents=None, denominations=None,
                                             over_short_cents=None, flagged=True, closed_by=""))
    assert _send(store.key, [ev]) == {ev["event_id"]: "applied"}
    bc = _ok(mgr.get(f"{ME}/drawers/{batch_closed['id']}", timeout=5))["drawer"]
    assert bc["flagged"] is True and bc["counted_cents"] is None and bc["over_short_cents"] is None
    assert bc["denominations"] is None and bc["close_reason"] == "batch_close"

    # Another store's manager sees none of it; its Pi can't take the id over.
    omgr = other.manager_session()
    assert omgr.get(f"{ME}/drawers/{d['id']}", timeout=5).status_code == 404
    assert _ok(omgr.get(f"{ME}/drawers", timeout=5))["drawers"] == []
    steal = _ev("drawer.closed", drawer=_closed(d, counted_cents=1))
    assert _send(other.key, [steal]) == {steal["event_id"]: "invalid"}
    assert _ok(mgr.get(f"{ME}/drawers/{d['id']}", timeout=5))["drawer"]["counted_cents"] == 23950

    assert mgr.get(f"{ME}/drawers/not-a-uuid", timeout=5).status_code == 404
    assert _ok(mgr.get(f"{ME}/drawers", params={"date": "11/11/2026"}, timeout=5), 400)["code"] == "bad_date"


def test_bad_drawer_events_are_invalid(store) -> None:
    d = _drawer()
    bad = [
        _ev("drawer.opened"),  # no drawer
        _ev("drawer.opened", drawer={**d, "id": "nope"}),
        _ev("drawer.opened", drawer={**d, "start_cents": -1}),
        _ev("drawer.closed", drawer=d),  # no closed_at / reason
        _ev("drawer.closed", drawer=_closed(d, close_reason="lost")),
        _ev("drawer.movement", movement={"id": str(uuid.uuid4()), "drawer_id": d["id"], "kind": "refund",
                                         "amount_cents": 100, "at": "2026-11-11T16:00:00Z"}),
        _ev("drawer.movement", movement={"id": str(uuid.uuid4()), "drawer_id": d["id"], "kind": "pay_in",
                                         "amount_cents": 0, "at": "2026-11-11T16:00:00Z"}),
        _ev("z.report", report={"id": str(uuid.uuid4()), "business_date": "2026-11-11"}),  # no batch_id
        _ev("z.report", report={"id": str(uuid.uuid4()), "batch_id": 3, "business_date": "2026-13-01"}),
        _ev("z.report", report={"batch_id": 3, "business_date": "2026-11-11"}),  # no id
    ]
    res = _send(store.key, bad)
    assert set(res.values()) == {"invalid"}, res
    # A movement before its session is kept.
    early = _ev("drawer.movement", movement={"id": str(uuid.uuid4()), "drawer_id": str(uuid.uuid4()), "kind": "pay_in",
                                            "amount_cents": 500, "at": "2026-11-11T16:00:00Z"})
    assert _send(store.key, [early]) == {early["event_id"]: "applied"}


def test_z_reports(make_store) -> None:
    store, other = make_store(), make_store()
    mgr = store.manager_session()
    z1, z2, z3 = _z(7, "2026-11-10"), _z(8, "2026-11-11"), _z(9, "2026-11-11", closed_at="2026-11-12T08:00:00Z", close_kind="auto")
    evs = [_ev("z.report", report=z) for z in (z1, z2, z3)]
    assert set(_send(store.key, evs).values()) == {"applied"}
    assert set(_send(store.key, evs[:1]).values()) == {"duplicate"}

    newest = _ok(mgr.get(f"{ME}/reports/z", timeout=5))["reports"]
    assert [r["batch_id"] for r in newest] == [9, 8, 7]
    s = newest[0]
    assert s["id"] == z3["id"] and s["business_date"] == "2026-11-11" and s["close_kind"] == "auto"
    assert s["total_cents"] == 6360 and s["orders"] == 3 and s["over_short_cents"] == -50 and s["difference_cents"] == 0
    day = _ok(mgr.get(f"{ME}/reports/z", params={"date": "2026-11-11"}, timeout=5))["reports"]
    assert [r["batch_id"] for r in day] == [9, 8]
    assert _ok(mgr.get(f"{ME}/reports/z", params={"date": "2026-11-09"}, timeout=5))["reports"] == []
    assert _ok(mgr.get(f"{ME}/reports/z", params={"date": "yesterday"}, timeout=5), 400)["code"] == "bad_date"

    full = _ok(mgr.get(f"{ME}/reports/z/{z2['id']}", timeout=5))
    assert full["report"] == z2 and full["received_at"]

    # A rebuilt Z for the same batch supersedes it (new id, version 2); the
    # old one stays readable from the history (hardening M-9: never
    # overwritten in place).
    z2b = _z(8, "2026-11-11", sales={**z2["sales"], "total_cents": 7000})
    ev = _ev("z.report", report=z2b)
    assert _send(store.key, [ev]) == {ev["event_id"]: "applied"}
    old = _ok(mgr.get(f"{ME}/reports/z/{z2['id']}", timeout=5))
    assert old["report"] == z2 and old["version"] == 1 and old["superseded"]["superseded_by"] == z2b["id"]
    cur = _ok(mgr.get(f"{ME}/reports/z/{z2b['id']}", timeout=5))
    assert cur["report"]["sales"]["total_cents"] == 7000 and cur["version"] == 2
    assert [v["id"] for v in cur["versions"]] == [z2["id"]]
    assert len(_ok(mgr.get(f"{ME}/reports/z", params={"date": "2026-11-11"}, timeout=5))["reports"]) == 2

    # Another store: never sees them, can't take a report id over.
    omgr = other.manager_session()
    assert omgr.get(f"{ME}/reports/z/{z1['id']}", timeout=5).status_code == 404
    assert _ok(omgr.get(f"{ME}/reports/z", timeout=5))["reports"] == []
    steal = _ev("z.report", report={**z1, "batch_id": 1})
    assert _send(other.key, [steal]) == {steal["event_id"]: "invalid"}
    assert mgr.get(f"{ME}/reports/z/not-a-uuid", timeout=5).status_code == 404


def test_reports_need_a_session(store) -> None:
    for path in ("/reports/z", f"/reports/z/{uuid.uuid4()}", "/drawers", f"/drawers/{uuid.uuid4()}"):
        assert requests.get(f"{ME}{path}", timeout=5).status_code == 401, path
        assert requests.get(f"{ME}{path}", headers=store_key(store.key), timeout=5).status_code == 401, path
