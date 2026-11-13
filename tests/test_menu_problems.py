"""Store › Menu › Find problems: GET /store/v1/me/menu/problems (contract/main.md §2.5zi, spec §8.30).

The checks themselves are pinned case by case in internal/domain/menucheck (Go); this checks the route
on a real store: the shape, the counts (info never counted), that a fix makes its problem go away, and
that one store never sees another's.
"""

from __future__ import annotations

import pytest
import requests

from conftest import STORE_V1

MENU = f"{STORE_V1}/me/menu"
PROBLEMS = f"{MENU}/problems"


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
        "format": 1, "views": [], "deals": [],
        "sizes": [{"name": "10 Small", "short": "10\""}, {"name": "12 Medium", "short": "12\""}],
        "groups": [
            {"name": "Toppings", "allow_multiple": True, "options": [
                {"name": "Pepperoni", "price_cents": 150, "prices": {"10 Small": 125, "12 Medium": 175}},
                {"name": "Extra Sauce", "price_cents": 0, "prices": {"10 Small": 0, "12 Medium": 50}}]},
            {"name": "Dressings", "options": []},
        ],
        "menus": [{"name": "Main", "categories": [
            {"name": "Pizza", "items": [
                {"name": "Cheese Pizza", "groups": ["Toppings"], "sizes": [
                    {"size": "10 Small", "price_cents": 999}, {"size": "12 Medium", "price_cents": 0}]},
                {"name": "Garden Salad", "price_cents": 699, "groups": ["Dressings"]}]}]}],
    }


def _codes(body: dict) -> set[tuple[str, str]]:
    return {(p["code"], p.get("item_name") or p.get("option_name") or p.get("group_name")) for p in body["problems"]}


def test_problems_shape_counts_and_fix(store, mgr):
    _ok(mgr.post(f"{MENU}/bank/apply", json=_doc()))
    # A second "Pepperoni" through the option API (the bank document refuses one).
    tops = next(g for g in _ok(mgr.get(f"{MENU}/modifier-groups")) if g["name"] == "Toppings")
    _ok(mgr.post(f"{MENU}/modifier-groups/{tops['id']}/options", json={"name": "pepperoni", "price_delta_cents": 150}), 201)
    body = _ok(mgr.get(PROBLEMS))
    assert set(body["counts"]) == {"pos", "printer", "price", "mods"}
    assert isinstance(body["version"], int) and body["version"] > 0
    codes = _codes(body)
    # No category printer anywhere: both items print on every station.
    assert ("no_printer", "Cheese Pizza") in codes and ("no_printer", "Garden Salad") in codes
    assert ("size_zero", "Cheese Pizza") in codes
    assert ("option_size_zero", "Extra Sauce") in codes
    assert ("empty_group", "Dressings") in codes
    assert ("duplicate_option", "pepperoni") in codes
    for p in body["problems"]:
        assert p["check"] in body["counts"] and p["severity"] in ("error", "review", "info") and p["message"]
    counted = {k: sum(1 for p in body["problems"] if p["check"] == k and p["severity"] != "info") for k in body["counts"]}
    assert counted == body["counts"]
    assert body["counts"]["printer"] == 2

    # Give the category a printer: the printer problems go, the version moves.
    printer = _ok(mgr.post(f"{STORE_V1}/me/printers", json={"name": "Kitchen", "ip": "10.0.0.9"}), 201)
    menus = _ok(mgr.get(f"{MENU}/menus"))
    cat = _ok(mgr.get(f"{MENU}/menus/{menus[0]['id']}/categories"))[0]
    _ok(mgr.patch(f"{MENU}/categories/{cat['id']}", json={"name": cat["name"], "description": "", "available": True,
                                                         "default_printer_id": printer["id"]}))
    after = _ok(mgr.get(PROBLEMS))
    assert after["counts"]["printer"] == 0 and after["version"] > body["version"]
    assert not any(c == "no_printer" for c, _ in _codes(after))


def test_problems_are_per_store(store, mgr, make_store):
    _ok(mgr.post(f"{MENU}/bank/apply", json=_doc()))
    other = make_store()
    s = other.manager_session()
    try:
        body = _ok(s.get(PROBLEMS))
        assert body["problems"] == [] and body["counts"] == {"pos": 0, "printer": 0, "price": 0, "mods": 0}
    finally:
        s.close()


def test_problems_need_a_session():
    assert requests.get(PROBLEMS, timeout=10).status_code == 401
