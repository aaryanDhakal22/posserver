"""Included toppings (the owner, 2026-11-13; contract/main.md "Included toppings").

  * the bank document: an item's included_toppings + a size's own count, an
    option's never_included; export writes them, re-apply is a no-op, 0 /
    false clear
  * the pricing context (the Pi's counter-menu `pricing`, /me/pricing/context)
    carries them; a channel quote frees the cheapest N charged toppings, never
    a never_included one (Shrimp), and the order line it makes
  * Store › Menu: PATCH …/options/:id never_included (absent keeps), the item's
    attributes, the CSV's included_toppings column
"""

from __future__ import annotations

import pytest
import requests

from conftest import ADMIN_V1, MAIN_BASE_URL, STORE_V1, store_key
from openapi import check

ME = f"{STORE_V1}/me"
MENU = f"{ME}/menu"
APPLY = f"{MENU}/bank/apply"
EXPORT = f"{MENU}/bank/document"
COUNTER_MENU = f"{STORE_V1}/agent/counter-menu"
CH_V1 = f"{MAIN_BASE_URL}/channel/v1"


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


def _ok(r: requests.Response, status: int = 200) -> dict:
    assert r.status_code == status, f"{r.status_code}: {r.text}"
    return r.json() if r.content else {}


def _doc() -> dict:
    p = {"12\"": 175, "14\"": 225}
    return {
        "format": 1, "views": [], "deals": [],
        "sizes": [{"name": "12\"", "short": "12"}, {"name": "14\"", "short": "14"}],
        "groups": [{"name": "Pizza Toppings", "allow_multiple": True, "portions": True, "options": [
            {"name": n, "price_cents": 175, "prices": dict(p)} for n in ("Pepperoni", "Sausage", "Mushrooms", "Onions", "Olives")] + [
            {"name": "Grilled Chicken", "price_cents": 250, "prices": {"12\"": 250, "14\"": 300}},
            {"name": "Shrimp", "price_cents": 250, "prices": {"12\"": 250, "14\"": 300}, "never_included": True}]}],
        "menus": [{"name": "Main", "categories": [{"name": "Pizza", "items": [
            {"name": "Build Your Own Pizza", "price_cents": 1599, "groups": ["Pizza Toppings"], "included_toppings": 5,
             "sizes": [{"size": "12\"", "price_cents": 1599}, {"size": "14\"", "price_cents": 1799}]},
            {"name": "Chicken Alfredo Pizza", "price_cents": 1499, "groups": ["Pizza Toppings"], "included_toppings": 2,
             "sizes": [{"size": "12\"", "price_cents": 1499}, {"size": "14\"", "price_cents": 1699, "included_toppings": 3}]},
            {"name": "Cheese Pizza", "price_cents": 1199, "groups": ["Pizza Toppings"],
             "sizes": [{"size": "12\"", "price_cents": 1199}, {"size": "14\"", "price_cents": 1399}]}]}]}],
    }


def test_bank_document_round_trip(mgr) -> None:
    dry = _ok(mgr.post(APPLY, params={"dry_run": 1}, json=_doc(), timeout=30))
    assert dry["dry_run"] is True
    _ok(mgr.post(APPLY, json=_doc(), timeout=30))
    doc = _ok(mgr.get(EXPORT, timeout=10))
    items = {i["name"]: i for m in doc["menus"] for c in m["categories"] for i in c["items"]}
    assert items["Build Your Own Pizza"]["included_toppings"] == 5
    assert "included_toppings" not in items["Cheese Pizza"]
    alf = items["Chicken Alfredo Pizza"]
    assert alf["included_toppings"] == 2 and [s.get("included_toppings") for s in alf["sizes"]] == [None, 3]
    opts = {o["name"]: o for g in doc["groups"] for o in g["options"]}
    assert opts["Shrimp"]["never_included"] is True and "never_included" not in opts["Pepperoni"]
    again = _ok(mgr.post(APPLY, json=doc, timeout=30))
    assert again["changes"] == [], again["changes"]
    # 0 / false clear; a bad count is the document's error.
    d = _doc()
    d["menus"][0]["categories"][0]["items"][0]["included_toppings"] = 0
    d["groups"][0]["options"][-1]["never_included"] = False
    res = _ok(mgr.post(APPLY, json=d, timeout=30))
    assert any("Build Your Own Pizza" in c and "included_toppings" in c for c in res["changes"]), res["changes"]
    assert any("Shrimp" in c and "never_included" in c for c in res["changes"]), res["changes"]
    d["menus"][0]["categories"][0]["items"][0]["included_toppings"] = 51
    r = mgr.post(APPLY, json=d, timeout=30)
    assert r.status_code == 400, r.text


def test_quote_frees_the_cheapest_and_charges_shrimp(admin, store, mgr) -> None:
    _ok(mgr.post(APPLY, json=_doc(), timeout=30))
    body = _ok(requests.get(COUNTER_MENU, headers=store_key(store.key), timeout=10))
    items = {i["name"]: i for c in body["build"]["categories"] for i in c["items"]}
    opts = {o["name"]: o for g in body["build"]["modifier_groups"] for o in g["options"]}
    sizes = {z["name"]: z["id"] for z in body["build"]["sizes"]}
    gid = body["build"]["modifier_groups"][0]["id"]
    pricing = body["pricing"]
    byo, alf = items["Build Your Own Pizza"], items["Chicken Alfredo Pizza"]
    assert pricing["items"][byo["id"]]["included_toppings"] == 5
    assert pricing["items"][alf["id"]]["included_toppings_sizes"] == {sizes["14\""]: 3}
    assert pricing["options"][opts["Shrimp"]["id"]]["never_included"] is True
    ctx = _ok(mgr.get(f"{ME}/pricing/context", params={"kind": "web"}, timeout=10))
    assert ctx["items"][byo["id"]]["included_toppings"] == 5

    key = _ok(admin.post(f"{ADMIN_V1}/channels", json={"location_id": store.location_id, "kind": "web", "name": "Web"}, timeout=5), 201)["key"]
    hdr = {"Authorization": f"Bearer {key}"}

    def line(item, size, names):
        return {"item_id": item["id"], "size_id": sizes[size], "qty": 1,
                "options": [{"group_id": gid, "option_id": opts[n]["id"]} for n in names]}

    five = ["Pepperoni", "Sausage", "Mushrooms", "Onions", "Olives"]
    lines = [line(byo, "14\"", five),                                      # SoftTouch: $17.99
             line(byo, "14\"", ["Shrimp"] + five[:4]),                     # shrimp charged: 17.99 + 3.00
             line(byo, "14\"", ["Grilled Chicken"] + five),                 # the cheapest 5 free: + 3.00
             line(alf, "12\"", ["Pepperoni", "Sausage", "Mushrooms"]),      # 2 included at 12": + 1.75
             line(alf, "14\"", ["Pepperoni", "Sausage", "Mushrooms"]),      # 3 at 14": + 0
             line(items["Cheese Pizza"], "14\"", ["Pepperoni"])]            # none included: + 2.25
    q = check(requests.post(f"{CH_V1}/quote", json={"type": "pickup", "lines": lines}, headers=hdr, timeout=10), "quote")
    assert q["errors"] == [], q["errors"]
    assert [x["unit_cents"] for x in q["lines"]] == [1799, 2099, 2099, 1674, 1699, 1624]


def test_store_menu_switches_and_csv(mgr) -> None:
    _ok(mgr.post(APPLY, json=_doc(), timeout=30))
    gid = next(g["id"] for g in _ok(mgr.get(f"{MENU}/modifier-groups", timeout=5)) if g["name"] == "Pizza Toppings")
    opts = {o["name"]: o for o in _ok(mgr.get(f"{MENU}/modifier-groups/{gid}/options", timeout=5))}
    assert opts["Shrimp"]["never_included"] is True and opts["Pepperoni"]["never_included"] is False
    gc = opts["Grilled Chicken"]
    base = {"name": gc["name"], "price_delta_cents": gc["price_delta_cents"], "in_stock": True}
    out = _ok(mgr.patch(f"{MENU}/modifier-groups/{gid}/options/{gc['id']}", json={**base, "never_included": True}, timeout=5))
    assert out["never_included"] is True
    _ok(mgr.patch(f"{MENU}/modifier-groups/{gid}/options/{gc['id']}", json=base, timeout=5))  # absent keeps
    assert {o["name"]: o for o in _ok(mgr.get(f"{MENU}/modifier-groups/{gid}/options", timeout=5))}["Grilled Chicken"]["never_included"] is True

    # The CSV: export writes the count; a re-import is a no-op; a new count sets the attribute.
    csv = mgr.get(f"{MENU}/export.csv", timeout=5).text
    lines = csv.splitlines()
    assert lines[0].endswith(",included_toppings")
    assert any(ln.startswith("Main,Pizza,Build Your Own Pizza,") and ln.endswith(",5") for ln in lines), lines
    res = _ok(mgr.post(f"{MENU}/import", data=csv, headers={"Content-Type": "text/csv"}, timeout=10))
    assert res["changes"] == [], res["changes"]
    res = _ok(mgr.post(f"{MENU}/import", data="category,item,included_toppings\nPizza,Cheese Pizza,1\n",
                       headers={"Content-Type": "text/csv"}, timeout=10))
    assert res["items_updated"] == 1, res
    doc = _ok(mgr.get(EXPORT, timeout=10))
    items = {i["name"]: i for m in doc["menus"] for c in m["categories"] for i in c["items"]}
    assert items["Cheese Pizza"]["included_toppings"] == 1
