"""Half and half (TERMINAL-PLAN.md Stage 0b): main's pricing engine, the bank
document's plain pizza and the order line's portion / mode.

  * the bank document sets each specialty's plain pizza by name
    (`half_base`, may name an item further on); the item carries it as the
    half_base_item attribute; export names it back; re-applying is a no-op;
    an unknown name fails with its path
  * the store's half settings: defaults (full price, 0 % off), saved and
    read back, shown to channels in the pricing context, bad values 400
  * /channel/v1/quote: one specialty on a half = its price, two = the dearer
    + the cheaper's extra over plain (at X % off), half toppings at half
    price rounded up, ONLY / NO on a half
  * a placed order: the Pi's SSE frame and main's read API carry each
    child's portion + mode, with readable names for older Pis
"""

from __future__ import annotations

import secrets

import pytest
import requests

from conftest import ADMIN_V1, MAIN_BASE_URL, STORE_V1
from openapi import check
from sse import SSEStream

ME = f"{STORE_V1}/me"
MENU = f"{ME}/menu"
APPLY = f"{MENU}/bank/apply"
EXPORT = f"{MENU}/bank/document"
CH_V1 = f"{MAIN_BASE_URL}/channel/v1"


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


def _ok(r: requests.Response, status: int = 200):
    assert r.status_code == status, f"{r.status_code}: {r.text}"
    return r.json() if r.content else {}


def _channel(admin, store, kind: str) -> dict:
    key = _ok(admin.post(f"{ADMIN_V1}/channels", json={"location_id": store.location_id, "kind": kind, "name": kind.title()}, timeout=5), 201)["key"]
    return {"Authorization": f"Bearer {key}"}


def _pizza(name: str, p12: int, p16: int, half_base: str | None = None) -> dict:
    it = {"name": name, "price_cents": p12, "sizes": [{"size": "12 Medium", "price_cents": p12}, {"size": "16 X-Large", "price_cents": p16}],
          "groups": ["Toppings"]}
    if half_base is not None:
        it["half_base"] = half_base
    return it


def _doc() -> dict:
    """16" XL: Cheese 12.99, Hawaiian 19.99, Supreme 21.99 (the owner's
    example); the specialties come first, so their plain pizza is a name
    further on."""
    return {
        "format": 1,
        "sizes": [{"name": "12 Medium", "short": "12\""}, {"name": "16 X-Large", "short": "16\""}],
        "groups": [{"name": "Toppings", "max_select": 0, "allow_multiple": False, "portions": True, "options": [
            {"name": "Pepperoni", "price_cents": 150, "prices": {"16 X-Large": 225}},
            {"name": "Onion", "price_cents": 125, "prices": {"16 X-Large": 175}},
            {"name": "Pineapple", "price_cents": 150, "prices": {"16 X-Large": 225}}]}],
        "menus": [{"name": "Main", "categories": [{"name": "Pizza", "items": [
            _pizza("Hawaiian", 1499, 1999, "Cheese Pizza"),
            _pizza("Supreme", 1699, 2199, "cheese  pizza"),
            _pizza("Cheese Pizza", 999, 1299)]}]}],
        "views": [], "deals": [],
    }


def _ids(mgr) -> dict:
    bank = _ok(mgr.get(f"{MENU}/bank", timeout=5))
    items = {it["name"]: it for m in bank["menus"] for c in m["categories"] for it in c["items"]}
    sizes = {z["name"]: z["id"] for z in bank["sizes"]}
    g = next(iter(bank["groups"].values())) if isinstance(bank["groups"], dict) else bank["groups"][0]
    opts = {o["name"]: o["id"] for o in g["options"]}
    return {"items": items, "sizes": sizes, "group": g["id"], "opts": opts}


def _line(m, item, size="16 X-Large", halves=(), options=(), qty=1) -> dict:
    line = {"item_id": m["items"][item]["id"], "size_id": m["sizes"][size], "qty": qty,
            "options": [{"group_id": m["group"], "option_id": m["opts"][o], "portion": por, **({"mode": mode} if mode else {})}
                        for o, por, mode in options]}
    if halves:
        line["halves"] = [{"item_id": m["items"][it]["id"], "half": h} for it, h in halves]
    return line


def _quote(hdr, lines) -> dict:
    return check(requests.post(f"{CH_V1}/quote", json={"type": "pickup", "payment": "cash", "lines": lines}, headers=hdr, timeout=5), "quote")


def _settings(mgr, **half) -> dict:
    cur = _ok(mgr.get(f"{ME}/pricing", timeout=5))
    return _ok(mgr.put(f"{ME}/pricing", json={"store": {**cur["store"], **half}, "kinds": cur["kinds"]}, timeout=5))


def test_bank_document_plain_pizza(mgr) -> None:
    res = _ok(mgr.post(APPLY, json=_doc(), timeout=15))
    assert res["applied"] and res["counts"]["items"]["created"] == 3
    m = _ids(mgr)
    chz = m["items"]["Cheese Pizza"]["id"]
    assert m["items"]["Hawaiian"]["attributes"]["half_base_item"] == chz
    assert m["items"]["Supreme"]["attributes"]["half_base_item"] == chz
    assert "half_base_item" not in (m["items"]["Cheese Pizza"]["attributes"] or {})

    doc = _ok(mgr.get(EXPORT, timeout=5))
    names = {it["name"]: it.get("half_base") for c in doc["menus"][0]["categories"] for it in c["items"]}
    assert names == {"Hawaiian": "Cheese Pizza", "Supreme": "Cheese Pizza", "Cheese Pizza": None}
    again = _ok(mgr.post(APPLY, json=doc, timeout=15))
    assert sum(c["created"] + c["updated"] + c["deleted"] for c in again["counts"].values()) == 0, again["changes"]

    # "" clears it; absent keeps it.
    d = _doc()
    d["menus"][0]["categories"][0]["items"][0]["half_base"] = ""
    del d["menus"][0]["categories"][0]["items"][1]["half_base"]
    res = _ok(mgr.post(APPLY, json=d, timeout=15))
    assert 'item "Hawaiian": half_base' in res["changes"] and res["counts"]["items"]["updated"] == 1
    m = _ids(mgr)
    assert "half_base_item" not in m["items"]["Hawaiian"]["attributes"] and m["items"]["Supreme"]["attributes"]["half_base_item"] == chz

    for bad, msg in (("Calzone", "unknown item"), ("Supreme", "own plain pizza")):
        d = _doc()
        d["menus"][0]["categories"][0]["items"][1]["half_base"] = bad
        r = mgr.post(APPLY, json=d, timeout=15)
        assert r.status_code == 400 and "menus[0].categories[0].items[1].half_base" in r.text and msg in r.text, r.text


def test_half_settings(admin, store, mgr) -> None:
    got = _ok(mgr.get(f"{ME}/pricing", timeout=5))
    assert got["store"]["half_topping_price"] == "full" and got["store"]["half_specialty_off_pct"] == 0
    got = _settings(mgr, half_topping_price="half", half_specialty_off_pct=25)
    assert got["store"]["half_topping_price"] == "half" and got["store"]["half_specialty_off_pct"] == 25
    ctx = check(requests.get(f"{CH_V1}/pricing", headers=_channel(admin, store, "counter"), timeout=5), "getPricing")
    assert ctx["settings"]["half_topping_price"] == "half" and ctx["settings"]["half_specialty_off_pct"] == 25
    cur = _ok(mgr.get(f"{ME}/pricing", timeout=5))
    for bad in ({"half_topping_price": "quarter"}, {"half_specialty_off_pct": 101}, {"half_specialty_off_pct": -1}):
        assert mgr.put(f"{ME}/pricing", json={"store": {**cur["store"], **bad}, "kinds": cur["kinds"]}, timeout=5).status_code == 400, bad


def test_quote_halves(admin, store, mgr) -> None:
    _ok(mgr.post(APPLY, json=_doc(), timeout=15))
    m = _ids(mgr)
    counter = _channel(admin, store, "counter")
    ctx = check(requests.get(f"{CH_V1}/pricing", headers=counter, timeout=5), "getPricing")
    assert ctx["items"][m["items"]["Hawaiian"]["id"]]["half_base"] == m["items"]["Cheese Pizza"]["id"]

    q = _quote(counter, [
        _line(m, "Cheese Pizza", halves=[("Hawaiian", 2)], options=[("Pepperoni", "first", None)]),
        _line(m, "Cheese Pizza", halves=[("Hawaiian", 1), ("Supreme", 2)]),
        _line(m, "Cheese Pizza", halves=[("Supreme", 1), ("Hawaiian", 2)]),
        _line(m, "Hawaiian", options=[("Pepperoni", "left", "only"), ("Pineapple", "second", "no")]),
    ])
    assert q["errors"] == []
    assert [ln["unit_cents"] for ln in q["lines"]] == [1999 + 225, 2899, 2899, 1999 + 225]
    assert q["lines"][0]["mods"] == ["16 X-Large", "Hawaiian (2nd half)", "Pepperoni (1st half)"]
    assert q["lines"][3]["mods"] == ["16 X-Large", "ONLY Pepperoni (1st half)", "NO Pineapple (2nd half)"]

    _settings(mgr, half_topping_price="half", half_specialty_off_pct=50)
    q = _quote(counter, [_line(m, "Cheese Pizza", halves=[("Hawaiian", 1), ("Supreme", 2)], options=[("Onion", "second", None)])])
    assert q["lines"][0]["unit_cents"] == 2199 + 350 + 88  # 1.75 / 2 rounds up

    q = _quote(counter, [_line(m, "Cheese Pizza", size="12 Medium", halves=[("Hawaiian", 1), ("Supreme", 1)])])
    assert [e["code"] for e in q["errors"]] == ["option_rules"]


def test_order_line_portion_and_mode(admin, store, mgr) -> None:
    _ok(mgr.post(APPLY, json=_doc(), timeout=15))
    m = _ids(mgr)
    op = _channel(admin, store, "operator")
    body = {"client_order_id": "t-" + secrets.token_hex(6), "type": "pickup",
            "lines": [_line(m, "Cheese Pizza", halves=[("Hawaiian", 2)],
                            options=[("Pepperoni", "first", "only"), ("Onion", "whole", None), ("Pineapple", "second", "no")]),
                      _line(m, "Cheese Pizza", size="12 Medium", options=[("Onion", "whole", None)])],
            "payment": {"method": "cash", "token": ""},
            "customer": {"name": "Ada Lovelace", "phone": "(410) 555-0101", "email": "ada@example.com"}}
    with SSEStream(store.key) as s:
        assert s.next()["event"] == "store_config"
        o = check(requests.post(f"{CH_V1}/orders", json=body, headers=op, timeout=10), "placeOrder")
        frame = s.wait_event("order", lambda d: d.get("id") == o["id"])
    assert frame, "the order never reached the Pi's stream"
    pz, plain = frame["items"]
    assert (pz["name"], pz["size"], pz["unit_price_cents"]) == ("Cheese Pizza", "16 X-Large", 1999)
    want = [("Hawaiian (2nd half)", "second", "specialty", 0, 1), ("ONLY Pepperoni (1st half)", "first", "only", 225, 1),
            ("Onion", None, "add", 175, 1), ("NO Pineapple (2nd half)", "second", "no", 0, 1)]
    assert [(c["name"], c.get("portion"), c.get("mode"), c["unit_price_cents"], c["quantity"]) for c in pz["children"]] == want
    assert pz["line_total_cents"] == 1999 + 225 + 175
    # A line without halves: no portion; mode says add.
    assert [(c["name"], c.get("portion"), c.get("mode")) for c in plain["children"]] == [("Onion", None, "add")]

    # main's own read (the database round trip) carries the same.
    r = requests.get(f"{MAIN_BASE_URL}/api/v1/orders/{o['id']}", headers={"X-STORE-API-KEY": store.key}, timeout=5)
    assert r.status_code == 200, r.text
    got = r.json()["items"][0]["children"]
    assert [(c["name"], c.get("portion"), c.get("mode")) for c in got] == [(n, p, md) for n, p, md, _, _ in want]
