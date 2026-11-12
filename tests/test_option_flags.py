"""An option's two switches in Store › Menu (MENU-PLAN, the owner 2026-11-12;
migration 00046; contract/main.md "Option switches").

  * show_on_driver_ticket: PATCH …/options/:id (absent keeps it), shown in GET
    option lists and the bank, carried by the bank document (absent keeps, the
    export writes it only when on, so a re-apply is a no-op), in every build's
    modifier_groups[].options[] (omitted when off) — the counter build the Pi
    reads and the channel menu (validated against the OpenAPI document)
  * counts_for_coupons = the option's mod family class is regular:
    PUT …/options/:id/coupons {counts} moves the option into the group's
    regular / premium family (made when missing: "Topping" / "Premium
    Topping"); nothing moves when it already is where counts says
  * POST …/options counts_for_coupons: absent = on in a topping group, else
    no family; false in a topping group = premium
  * every change bumps the menu version (the Pi pulls again); another store's
    group / option is a 404
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
    return {
        "format": 1, "sizes": [], "views": [], "deals": [],
        "groups": [
            {"name": "Combo Choice", "max_select": 1, "options": [
                {"name": "Coke", "price_cents": 0}, {"name": "Brownie", "price_cents": 0}, {"name": "Well Done", "price_cents": 0}]},
            {"name": "Toppings", "allow_multiple": True, "options": [{"name": "Pepperoni", "price_cents": 150}]},
        ],
        "menus": [{"name": "Main", "categories": [{"name": "Subs", "items": [
            {"name": "Cheesesteak Combo", "price_cents": 1099, "groups": ["Combo Choice"]},
            {"name": "Pizza", "price_cents": 999, "groups": ["Toppings"]}]}]}],
    }


def _group(mgr, name: str) -> str:
    return next(g["id"] for g in _ok(mgr.get(f"{MENU}/modifier-groups", timeout=5)) if g["name"] == name)


def _options(mgr, gid: str) -> dict[str, dict]:
    return {o["name"]: o for o in _ok(mgr.get(f"{MENU}/modifier-groups/{gid}/options", timeout=5))}


def _version(mgr) -> int:
    return _ok(mgr.get(f"{MENU}/bank", timeout=5))["version"]


def _families(mgr) -> dict:
    got = _ok(mgr.get(f"{MENU}/families", timeout=5))
    fams = {f["id"]: f for f in got["families"]}
    return {o["name"]: fams.get(o["family_id"]) for o in got["options"]}


def test_show_on_driver_ticket(mgr, store, admin):
    _ok(mgr.post(APPLY, json=_doc(), timeout=15))
    gid = _group(mgr, "Combo Choice")
    opts = _options(mgr, gid)
    assert all(o["show_on_driver_ticket"] is False for o in opts.values())
    coke = opts["Coke"]
    v0 = _version(mgr)
    out = _ok(mgr.patch(f"{MENU}/modifier-groups/{gid}/options/{coke['id']}", json={
        "name": "Coke", "price_delta_cents": 0, "in_stock": True, "show_on_driver_ticket": True}, timeout=5))
    assert out["show_on_driver_ticket"] is True and out["name"] == "Coke"
    assert _version(mgr) > v0, "the Pi pulls the counter menu again"
    # Absent keeps it (older callers).
    _ok(mgr.patch(f"{MENU}/modifier-groups/{gid}/options/{coke['id']}", json={
        "name": "Coke", "price_delta_cents": 0, "in_stock": True}, timeout=5))
    assert _options(mgr, gid)["Coke"]["show_on_driver_ticket"] is True
    bank = _ok(mgr.get(f"{MENU}/bank", timeout=5))
    assert {o["name"]: o["show_on_driver_ticket"] for o in bank["groups"][gid]["options"]} == {
        "Coke": True, "Brownie": False, "Well Done": False}

    # The counter build the Pi reads: on the flagged option only.
    body = _ok(requests.get(COUNTER_MENU, headers=store_key(store.key), timeout=10))
    built = {o["name"]: o for g in body["build"]["modifier_groups"] for o in g["options"]}
    assert built["Coke"]["show_on_driver_ticket"] is True
    assert "show_on_driver_ticket" not in built["Well Done"]
    # A channel's menu carries it too (and still matches the OpenAPI document).
    key = _ok(admin.post(f"{ADMIN_V1}/channels", json={"location_id": store.location_id, "kind": "web", "name": "Web"}, timeout=5), 201)["key"]
    menu = check(requests.get(f"{CH_V1}/menu", headers={"Authorization": f"Bearer {key}"}, timeout=10), "getMenu")
    ch = {o["name"]: o for g in menu["modifier_groups"] for o in g["options"]}
    assert ch["Coke"]["show_on_driver_ticket"] is True and "show_on_driver_ticket" not in ch["Brownie"]

    # The bank document: export writes it only when on; re-apply is a no-op; absent keeps; false clears.
    doc = _ok(mgr.get(EXPORT, timeout=10))
    dg = next(g for g in doc["groups"] if g["name"] == "Combo Choice")
    assert {o["name"]: o.get("show_on_driver_ticket") for o in dg["options"]} == {"Coke": True, "Brownie": None, "Well Done": None}
    res = _ok(mgr.post(APPLY, json=doc, timeout=15))
    assert res["changes"] == [], res["changes"]
    res = _ok(mgr.post(APPLY, json=_doc(), timeout=15))
    assert res["changes"] == [] and _options(mgr, gid)["Coke"]["show_on_driver_ticket"] is True
    d2 = _doc()
    d2["groups"][0]["options"][0]["show_on_driver_ticket"] = False
    d2["groups"][0]["options"][1]["show_on_driver_ticket"] = True
    res = _ok(mgr.post(APPLY, json=d2, timeout=15))
    assert 'option "Coke" of "Combo Choice": show_on_driver_ticket' in res["changes"], res["changes"]
    opts = _options(mgr, gid)
    assert not opts["Coke"]["show_on_driver_ticket"] and opts["Brownie"]["show_on_driver_ticket"]
    # A new option may be created with it on.
    sprite = _ok(mgr.post(f"{MENU}/modifier-groups/{gid}/options", json={"name": "Sprite", "price_delta_cents": 0,
                                                                         "show_on_driver_ticket": True}, timeout=5), 201)
    assert sprite["show_on_driver_ticket"] is True and sprite["counts_for_coupons"] is False


def test_counts_for_coupons(mgr):
    _ok(mgr.post(APPLY, json=_doc(), timeout=15))
    tops = _group(mgr, "Toppings")
    pep = _options(mgr, tops)["Pepperoni"]
    assert pep["counts_for_coupons"] is False, "no family yet"
    url = lambda oid: f"{MENU}/modifier-groups/{tops}/options/{oid}/coupons"  # noqa: E731

    # On with no regular family anywhere: a "Topping" family (regular) is made.
    v0 = _version(mgr)
    f = _ok(mgr.put(url(pep["id"]), json={"counts": True}, timeout=5))
    assert f["counts_for_coupons"] is True and f["family_name"] == "Topping" and f["option_id"] == pep["id"]
    assert _version(mgr) > v0
    assert _families(mgr)["Pepperoni"]["class"] == "regular"

    # A new option of a topping group counts by default; false makes it premium.
    onion = _ok(mgr.post(f"{MENU}/modifier-groups/{tops}/options", json={"name": "Onion", "price_delta_cents": 100}, timeout=5), 201)
    assert onion["counts_for_coupons"] is True and onion["show_on_driver_ticket"] is False
    steak = _ok(mgr.post(f"{MENU}/modifier-groups/{tops}/options", json={"name": "Steak", "price_delta_cents": 300,
                                                                          "counts_for_coupons": False}, timeout=5), 201)
    assert steak["counts_for_coupons"] is False
    fams = _families(mgr)
    assert fams["Onion"]["name"] == "Topping" and fams["Steak"]["name"] == "Premium Topping" and fams["Steak"]["class"] == "premium"

    # Steak on: the group's regular family; Pepperoni off: the group's premium family.
    assert _ok(mgr.put(url(steak["id"]), json={"counts": True}, timeout=5))["family_name"] == "Topping"
    off = _ok(mgr.put(url(pep["id"]), json={"counts": False}, timeout=5))
    assert off["counts_for_coupons"] is False and off["family_name"] == "Premium Topping"
    # Already off: nothing moves (no version bump).
    v1 = _version(mgr)
    assert _ok(mgr.put(url(pep["id"]), json={"counts": False}, timeout=5))["family_name"] == "Premium Topping"
    assert _version(mgr) == v1
    opts = _options(mgr, tops)
    assert {n: o["counts_for_coupons"] for n, o in opts.items()} == {"Pepperoni": False, "Onion": True, "Steak": True}
    bank = _ok(mgr.get(f"{MENU}/bank", timeout=5))
    assert {o["name"]: o["counts_for_coupons"] for o in bank["groups"][tops]["options"]} == {"Pepperoni": False, "Onion": True, "Steak": True}

    # Not a topping group: a new option stays without a family (absent or false).
    combo = _group(mgr, "Combo Choice")
    for body in ({"name": "Water", "price_delta_cents": 0}, {"name": "Juice", "price_delta_cents": 0, "counts_for_coupons": False}):
        o = _ok(mgr.post(f"{MENU}/modifier-groups/{combo}/options", json=body, timeout=5), 201)
        assert o["counts_for_coupons"] is False
        assert _families(mgr)[body["name"]] is None
    # …unless asked: the store's existing "Topping" family.
    tea = _ok(mgr.post(f"{MENU}/modifier-groups/{combo}/options", json={"name": "Tea", "price_delta_cents": 0,
                                                                         "counts_for_coupons": True}, timeout=5), 201)
    assert tea["counts_for_coupons"] is True and _families(mgr)["Tea"]["name"] == "Topping"


def test_counts_for_coupons_name_taken_by_another_class(mgr):
    _ok(mgr.post(APPLY, json=_doc(), timeout=15))
    _ok(mgr.post(f"{MENU}/families", json={"kind": "mod", "name": "Topping", "class": "premium"}, timeout=5), 201)
    tops = _group(mgr, "Toppings")
    pep = _options(mgr, tops)["Pepperoni"]
    f = _ok(mgr.put(f"{MENU}/modifier-groups/{tops}/options/{pep['id']}/coupons", json={"counts": True}, timeout=5))
    assert f["counts_for_coupons"] is True and f["family_name"] == "Topping (regular)"


def test_coupons_route_errors(mgr, make_store):
    _ok(mgr.post(APPLY, json=_doc(), timeout=15))
    tops, combo = _group(mgr, "Toppings"), _group(mgr, "Combo Choice")
    pep = _options(mgr, tops)["Pepperoni"]
    assert _ok(mgr.put(f"{MENU}/modifier-groups/{tops}/options/{pep['id']}/coupons", json={}, timeout=5), 400)["code"] == "bad_body"
    # The option isn't that group's.
    assert _ok(mgr.put(f"{MENU}/modifier-groups/{combo}/options/{pep['id']}/coupons", json={"counts": True}, timeout=5), 404)["code"] == "not_found"
    assert _ok(mgr.put(f"{MENU}/modifier-groups/{tops}/options/not-a-uuid/coupons", json={"counts": True}, timeout=5), 400)
    # Another store's group and option.
    other = make_store()
    om = other.manager_session()
    try:
        assert _ok(om.put(f"{MENU}/modifier-groups/{tops}/options/{pep['id']}/coupons", json={"counts": True}, timeout=5), 404)
        r = om.patch(f"{MENU}/modifier-groups/{tops}/options/{pep['id']}", json={
            "name": "Pepperoni", "price_delta_cents": 150, "in_stock": True, "show_on_driver_ticket": True}, timeout=5)
        assert r.status_code in (400, 404), r.text  # the legacy PATCH answers 400 for an id not the store's
    finally:
        om.close()
    got = _options(mgr, tops)["Pepperoni"]
    assert got["counts_for_coupons"] is False and got["show_on_driver_ticket"] is False
