"""The counter layout (TERMINAL-PLAN.md Stage 0a): a counter view's screen
layout — tabs with fixed button grids and pages, modifier screens, per-item
modifier flows, function buttons — stored on the view, versioned with the
menu, served in the view's build and carried by the bank document by names.

  * PUT / GET / DELETE …/views/:id/layout; every change bumps the menu version
  * the build carries the layout with names and prices from the bank
  * a bad layout is a 422 listing every problem; nothing is saved
  * only a counter view takes a layout (routes and the bank document)
  * the bank document round-trips it by names: export → apply changes nothing,
    an old document without a layout keeps the one there, a bad name is a
    400 with its path and writes nothing
  * another store's view is a 404
"""

from __future__ import annotations

import copy
import json

import pytest
import requests

from conftest import STORE_V1

MENU = f"{STORE_V1}/me/menu"
APPLY = f"{MENU}/bank/apply"
EXPORT = f"{MENU}/bank/document"


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    yield s
    s.close()


def _ok(r: requests.Response, status: int = 200) -> dict:
    assert r.status_code == status, f"{r.status_code}: {r.text}"
    return r.json() if r.content else {}


def _bank_doc() -> dict:
    """A counter menu in miniature: sized pizzas with toppings, wings with a
    sauce, a drinks category and a deal."""
    sizes = ["12 Medium", "16 X-Large"]
    return {
        "format": 1,
        "sizes": [{"name": "12 Medium", "short": "12\""}, {"name": "16 X-Large", "short": "16\""}],
        "groups": [
            {"name": "Pizza Toppings", "allow_multiple": True, "portions": True, "options": [
                {"name": "Pepperoni", "price_cents": 175, "prices": {"12 Medium": 175, "16 X-Large": 250}},
                {"name": "Onions", "price_cents": 175, "prices": {"12 Medium": 175, "16 X-Large": 250}}]},
            {"name": "Wing Sauce", "required": True, "min_select": 1, "max_select": 1, "options": [
                {"name": "Hot", "price_cents": 0}, {"name": "BBQ", "price_cents": 50}]},
        ],
        "menus": [{"name": "Counter", "categories": [
            {"name": "Pizza", "items": [
                {"name": "Cheese Pizza", "price_cents": 999,
                 "sizes": [{"size": s, "price_cents": p} for s, p in zip(sizes, (999, 1399))], "groups": ["Pizza Toppings"]},
                {"name": "Hawaiian Pizza", "price_cents": 1499,
                 "sizes": [{"size": s, "price_cents": p} for s, p in zip(sizes, (1499, 1999))], "groups": ["Pizza Toppings"]}]},
            {"name": "Wings", "items": [{"name": "Wings 10pc", "price_cents": 1199, "groups": ["Wing Sauce"]}]},
            {"name": "Drinks", "items": [{"name": "Can Soda", "price_cents": 150}]},
        ]}],
        "views": [],
        "deals": [{"kind": "code", "name": "Welcome", "active": True, "code": "WELCOME5", "amount_cents": 500, "min_subtotal_cents": 2000}],
    }


def _names_layout() -> dict:
    """The same layout by names (the bank document's form)."""
    return {
        "tab_strip": {"rows": 2, "cols": 4},
        "tabs": [
            {"name": "Pizza & Calzones", "strip_row": 0, "strip_col": 0, "color": "#ff8080", "font_color": "#000000", "font_size": 14,
             "grid": {"rows": 6, "cols": 3}, "pages": [{"name": "12\" Med"}, {"name": "16\" XL"}],
             "buttons": [
                 {"page": 0, "row": 0, "col": 0, "caption": "12\" Med Regular Cheese", "color": "#ff8080", "font_color": "#000000",
                  "font_size": 14, "action": {"type": "item", "item": "Cheese Pizza", "size": "12 Medium"}},
                 {"page": 0, "row": 0, "col": 1, "caption": "", "color": "#e7b858", "font_color": "#000000", "font_size": 0,
                  "action": {"type": "item", "item": "Hawaiian Pizza", "size": "12 Medium"}},
                 {"page": 1, "row": 0, "col": 0, "caption": "16\" XL Cheese", "color": "", "font_color": "", "font_size": 0,
                  "action": {"type": "item", "item": "Cheese Pizza", "size": "16 X-Large"}},
                 {"page": 0, "row": 1, "col": 0, "caption": "Drinks", "color": "", "font_color": "", "font_size": 0,
                  "action": {"type": "category", "category": "Drinks"}},
                 {"page": 0, "row": 1, "col": 1, "caption": "Coupon", "color": "", "font_color": "", "font_size": 0,
                  "action": {"type": "deal", "deal": "Welcome"}},
                 {"page": 0, "row": 1, "col": 2, "caption": "Open Item", "color": "", "font_color": "", "font_size": 0,
                  "action": {"type": "function", "name": "open_item"}},
             ]},
            {"name": "Wings", "strip_row": 0, "strip_col": 1, "color": "#ffff80", "font_color": "#000000", "font_size": 14,
             "grid": {"rows": 4, "cols": 4}, "pages": [{"name": "Page 1"}],
             "buttons": [{"page": 0, "row": 0, "col": 0, "caption": "", "color": "", "font_color": "", "font_size": 0,
                          "action": {"type": "item", "item": "Wings 10pc"}}]},
        ],
        "screens": [
            {"name": "12\" Pizza Toppings", "prompt": "Toppings:", "grid": {"rows": 5, "cols": 5},
             "pages": [{"name": "Add Mods"}, {"name": "1/2 Topping"}, {"name": "No Mods"}],
             "buttons": [
                 {"page": 0, "row": 0, "col": 0, "caption": "", "color": "#ff0000", "font_color": "#000000", "font_size": 12,
                  "action": {"type": "option", "group": "Pizza Toppings", "option": "Pepperoni"}},
                 {"page": 0, "row": 0, "col": 1, "caption": "", "color": "", "font_color": "", "font_size": 0,
                  "action": {"type": "option", "group": "Pizza Toppings", "option": "Onions"}},
                 {"page": 1, "row": 0, "col": 0, "caption": "WHOLE", "color": "", "font_color": "", "font_size": 0,
                  "action": {"type": "state", "state": "whole"}},
                 {"page": 1, "row": 1, "col": 0, "caption": "1ST HALF ADD", "color": "", "font_color": "", "font_size": 0,
                  "action": {"type": "state", "state": "first_add"}},
                 {"page": 1, "row": 1, "col": 1, "caption": "1ST HALF ONLY", "color": "", "font_color": "", "font_size": 0,
                  "action": {"type": "state", "state": "first_only"}},
                 {"page": 2, "row": 0, "col": 0, "caption": "NO", "color": "", "font_color": "", "font_size": 0,
                  "action": {"type": "state", "state": "no"}},
             ]},
            {"name": "Wing Sauce", "prompt": "Sauce Choice:", "grid": {"rows": 2, "cols": 3}, "pages": [{"name": "Sauces"}],
             "buttons": [
                 {"page": 0, "row": 0, "col": 0, "caption": "", "color": "", "font_color": "", "font_size": 0,
                  "action": {"type": "option", "group": "Wing Sauce", "option": "Hot"}},
                 {"page": 0, "row": 0, "col": 1, "caption": "", "color": "", "font_color": "", "font_size": 0,
                  "action": {"type": "option", "group": "Wing Sauce", "option": "BBQ"}},
             ]},
            {"name": "Beverage Options", "prompt": "Choice of Drink:", "grid": {"rows": 2, "cols": 2}, "pages": [{"name": "Can"}],
             "buttons": [
                 {"page": 0, "row": 0, "col": 0, "caption": "", "color": "", "font_color": "", "font_size": 0,
                  "action": {"type": "item_pick", "item": "Can Soda"}},
                 {"page": 0, "row": 0, "col": 1, "caption": "Any drink", "color": "", "font_color": "", "font_size": 0,
                  "action": {"type": "item_pick", "category": "Drinks"}},
             ]},
        ],
        "flows": {
            "Cheese Pizza": [
                {"screen": "12\" Pizza Toppings", "size": "12 Medium", "required_count": 0, "forced": False, "prompt": ""},
                {"screen": "12\" Pizza Toppings", "size": "12 Medium", "required_count": 0, "forced": False, "prompt": "Extra?"},
            ],
            "Wings 10pc": [
                {"screen": "Wing Sauce", "required_count": 1, "forced": True, "prompt": "Sauce Choice:"},
                {"screen": "Beverage Options", "required_count": 1, "forced": True, "prompt": ""},
            ],
        },
        "functions": {
            "nav": [{"name": "fcalc", "caption": "Calculator", "order": 1, "visible": True, "role": ""},
                    {"name": "fdrawer", "caption": "Open Drawer", "order": 2, "visible": True, "role": "cashier"}],
            "employee": [],
            "manager": [{"name": "zout", "caption": "Z Out", "order": 17, "visible": True, "role": "manager"}],
        },
        "bottom_bar": {
            "none": {"rows": 2, "cols": 9, "buttons": [
                {"row": 0, "col": 0, "name": "home", "caption": "Home", "emphasis": False},
                {"row": 0, "col": 8, "name": "cash_out", "caption": "Cash Out", "emphasis": True}]},
            "line": {"rows": 1, "cols": 9, "buttons": [
                {"row": 0, "col": 0, "name": "done", "caption": "Done", "emphasis": True},
                {"row": 0, "col": 1, "name": "remove", "caption": "Remove", "emphasis": False}]},
            "modifier": {"rows": 0, "cols": 0, "buttons": []},
        },
        "quick_notes": ["Well Done", "Cut in squares"],
    }


class Ids:
    """Name → id lookups of a store's bank (to write the layout by ids)."""

    def __init__(self, mgr):
        bank = _ok(mgr.get(f"{MENU}/bank", timeout=5))
        self.version = bank["version"]
        self.size = {s["name"]: s["id"] for s in bank["sizes"]}
        self.item = {it["name"]: it["id"] for m in bank["menus"] for c in m["categories"] for it in c["items"]}
        self.cat = {c["name"]: c["id"] for m in bank["menus"] for c in m["categories"]}
        self.group = {g["name"]: g["id"] for g in bank["groups"].values()}
        self.option = {(g["name"], o["name"]): o["id"] for g in bank["groups"].values() for o in g["options"]}
        self.deal = {d["name"]: d["id"] for d in _ok(mgr.get(f"{STORE_V1}/me/deals", timeout=5))}

    def action(self, a: dict) -> dict:
        out = {"type": a["type"]}
        if "item" in a:
            out["item_id"] = self.item[a["item"]]
        if "size" in a:
            out["size_id"] = self.size[a["size"]]
        if "category" in a:
            out["category_id"] = self.cat[a["category"]]
        if "deal" in a:
            out["deal_id"] = self.deal[a["deal"]]
        if "group" in a:
            out["group_id"] = self.group[a["group"]]
            out["option_id"] = self.option[(a["group"], a["option"])]
        for k in ("name", "state"):
            if k in a:
                out[k] = a[k]
        return out

    def layout(self, names: dict) -> dict:
        lay = copy.deepcopy(names)
        for grid in lay["tabs"] + lay["screens"]:
            for b in grid["buttons"]:
                b["action"] = self.action(b["action"])
        flows = {}
        for item, steps in lay["flows"].items():
            for s in steps:
                if "size" in s:
                    s["size_id"] = self.size[s.pop("size")]
            flows[self.item[item]] = steps
        lay["flows"] = flows
        return lay


def _setup(mgr) -> tuple[Ids, dict]:
    _ok(mgr.post(APPLY, json=_bank_doc(), timeout=15))
    views = {(v["kind"], v["name"]): v for v in _ok(mgr.get(f"{MENU}/views", timeout=5))}
    return Ids(mgr), views


def _version(mgr) -> int:
    return _ok(mgr.get(f"{MENU}/bank", timeout=5))["version"]


def test_put_get_build_delete(mgr):
    ids, views = _setup(mgr)
    counter = views[("counter", "Counter")]
    assert counter["has_layout"] is False
    url = f"{MENU}/views/{counter['id']}/layout"
    assert _ok(mgr.get(url, timeout=5), 404)["code"] == "no_layout"

    lay = ids.layout(_names_layout())
    v0 = _version(mgr)
    saved = _ok(mgr.put(url, json=lay, timeout=10))
    v1 = _version(mgr)
    assert v1 > v0, "a layout change is a new menu version"
    got = _ok(mgr.get(url, timeout=5))
    assert got == saved == lay
    assert {v["id"]: v for v in _ok(mgr.get(f"{MENU}/views", timeout=5))}[counter["id"]]["has_layout"] is True

    build = _ok(mgr.get(f"{MENU}/views/{counter['id']}/build", timeout=5))
    assert build["version"] == v1
    bl = build["layout"]
    pizza = bl["tabs"][0]["buttons"]
    assert pizza[0]["caption"] == "12\" Med Regular Cheese"
    assert pizza[0]["action"]["label"] == "Cheese Pizza" and pizza[0]["action"]["price_cents"] == 999
    assert pizza[0]["action"]["size_name"] == "12 Medium"
    assert pizza[1]["caption"] == "12 Medium Hawaiian Pizza", "no caption: the bank's name"
    assert pizza[1]["action"]["price_cents"] == 1499
    assert pizza[2]["action"]["price_cents"] == 1399
    assert pizza[3]["action"]["label"] == "Drinks" and not pizza[3]["action"].get("missing")
    assert pizza[4]["action"]["label"] == "Welcome"
    tops = bl["screens"][0]["buttons"]
    assert tops[0]["caption"] == "Pepperoni" and tops[0]["action"]["price_cents"] == 175
    sauce = bl["screens"][1]["buttons"]
    assert sauce[1]["action"]["label"] == "BBQ" and sauce[1]["action"]["price_cents"] == 50
    assert {g["name"] for g in build["modifier_groups"]} >= {"Pizza Toppings", "Wing Sauce"}
    assert len(bl["flows"][ids.item["Cheese Pizza"]]) == 2
    assert not [n for n in build["needs_attention"] if n["code"].startswith("layout_")]
    assert bl["quick_notes"] == ["Well Done", "Cut in squares"]
    # Other views' builds have no layout.
    web = _ok(mgr.get(f"{MENU}/views/{views[('web', 'Web')]['id']}/build", timeout=5))
    assert "layout" not in web

    # The same layout again: saved again (a write, so a version) and unchanged.
    _ok(mgr.put(url, json=lay, timeout=10))
    assert _ok(mgr.get(url, timeout=5)) == lay

    v2 = _version(mgr)
    _ok(mgr.delete(url, timeout=5), 204)
    assert _version(mgr) > v2
    assert _ok(mgr.get(url, timeout=5), 404)["code"] == "no_layout"
    assert "layout" not in _ok(mgr.get(f"{MENU}/views/{counter['id']}/build", timeout=5))
    v3 = _version(mgr)
    _ok(mgr.delete(url, timeout=5), 204)  # clearing nothing is fine and writes nothing
    assert _version(mgr) == v3


def test_build_flags_what_went_away(mgr):
    ids, views = _setup(mgr)
    counter = views[("counter", "Counter")]
    _ok(mgr.put(f"{MENU}/views/{counter['id']}/layout", json=ids.layout(_names_layout()), timeout=10))
    _ok(mgr.delete(f"{STORE_V1}/me/deals/{ids.deal['Welcome']}", timeout=5), 204)
    _ok(mgr.delete(f"{MENU}/items/{ids.item['Can Soda']}", timeout=5), 204)
    build = _ok(mgr.get(f"{MENU}/views/{counter['id']}/build", timeout=5))
    codes = {n["code"] for n in build["needs_attention"]}
    assert {"layout_item_missing", "layout_deal_missing"} <= codes
    soda = build["layout"]["screens"][2]["buttons"][0]
    assert soda["action"]["missing"] is True


def test_broken_buttons_and_entries_are_named_and_removable(mgr):
    """The owner deleted items that counter buttons and a website change still name (prod, 2026-11-13):
    every needs-attention line says what it is by what is left (a button's caption, its tab or screen and
    cell, the item id; an entry's id, name and what it changed); Remove takes one button off the layout
    while others stay broken (a whole-layout PUT refuses until all are fixed), the deleted item's flow
    goes with its last button, and Find problems lists them under `broken`."""
    ids, views = _setup(mgr)
    counter, web = views[("counter", "Counter")], views[("web", "Web")]
    url = f"{MENU}/views/{counter['id']}/layout"
    _ok(mgr.put(url, json=ids.layout(_names_layout()), timeout=10))
    entry = _ok(mgr.post(f"{MENU}/views/{web['id']}/entries", json={
        "kind": "item", "anchor": {"item_ids": [ids.item["Can Soda"]]}, "display_name": "Fountain Soda",
        "price_override_cents": 199}, timeout=5), 201)
    pizza, soda = ids.item["Cheese Pizza"], ids.item["Can Soda"]
    _ok(mgr.delete(f"{MENU}/items/{pizza}", timeout=5), 204)
    _ok(mgr.delete(f"{MENU}/items/{soda}", timeout=5), 204)

    def lines() -> dict:
        build = _ok(mgr.get(f"{MENU}/views/{counter['id']}/build", timeout=5))
        return {(n["where_kind"], n["where"], n["cell"]["page"], n["cell"]["row"], n["cell"]["col"]): n
                for n in build["needs_attention"] if n["code"] == "layout_item_missing"}

    got = lines()
    # One line per button; Cheese Pizza's flow adds none (its buttons name it already).
    assert set(got) == {("tab", "Pizza & Calzones", 0, 0, 0), ("tab", "Pizza & Calzones", 1, 0, 0),
                        ("screen", "Beverage Options", 0, 0, 0)}, got
    med = got[("tab", "Pizza & Calzones", 0, 0, 0)]
    assert med["caption"] == "12\" Med Regular Cheese" and med["item_id"] == pizza and med["cell"]["page_name"] == "12\" Med"
    assert "name" not in med  # the bank's name went with the item
    assert got[("tab", "Pizza & Calzones", 1, 0, 0)]["caption"] == "16\" XL Cheese"
    pick = got[("screen", "Beverage Options", 0, 0, 0)]
    assert pick["item_id"] == soda and "caption" not in pick

    gone = [n for n in _ok(mgr.get(f"{MENU}/views/{web['id']}/build", timeout=5))["needs_attention"] if n["code"] == "anchor_gone"]
    assert len(gone) == 1, gone
    assert gone[0]["entry_id"] == entry["id"] and gone[0]["item_id"] == soda and gone[0]["name"] == "Fountain Soda"
    assert gone[0]["entry_kind"] == "item" and gone[0]["change"] == "$1.99" and gone[0]["price_cents"] == 199

    probs = _ok(mgr.get(f"{MENU}/problems", timeout=10))
    broken = [p for p in probs["problems"] if p["check"] == "broken"]
    assert probs["counts"]["broken"] == 4 and len(broken) == 4, broken
    named = {p["message"] for p in broken}
    assert "Button “12\" Med Regular Cheese” on tab “Pizza & Calzones” (page “12\" Med”, row 1, column 1): its item was deleted (item " + pizza[:8] + ")." in named
    assert "Change “Fountain Soda” ($1.99): its item was deleted (item " + soda[:8] + ")." in named
    assert {p["view_name"] for p in broken} == {"Counter", "Web"} and all(p["attention"] for p in broken)

    # A whole-layout PUT is refused while any button is broken; one button comes off on its own.
    assert _ok(mgr.put(url, json=_ok(mgr.get(url, timeout=5)), timeout=10), 422)["code"] == "invalid_layout"
    v = _version(mgr)
    ref = lambda k: {"where_kind": k[0], "where": k[1], "page": k[2], "row": k[3], "col": k[4]}  # noqa: E731
    assert _ok(mgr.post(f"{url}/remove", json={"buttons": [ref(("tab", "Pizza & Calzones", 0, 0, 0))]}, timeout=10)) == {"buttons": 1, "flows": 0}
    assert _version(mgr) > v
    assert set(lines()) == {("tab", "Pizza & Calzones", 1, 0, 0), ("screen", "Beverage Options", 0, 0, 0)}
    assert pizza in _ok(mgr.get(url, timeout=5))["flows"]  # a button still sells it
    # Naming nothing that is there writes nothing; an empty body is a 400.
    v = _version(mgr)
    assert _ok(mgr.post(f"{url}/remove", json={"buttons": [ref(("tab", "Nowhere", 0, 0, 0))]}, timeout=10)) == {"buttons": 0, "flows": 0}
    assert _version(mgr) == v
    assert _ok(mgr.post(f"{url}/remove", json={}, timeout=5), 400)["code"] == "bad_request"
    # The rest: Cheese Pizza's flow goes with its last button, and the layout is clean again.
    assert _ok(mgr.post(f"{url}/remove", json={"buttons": [ref(k) for k in lines()]}, timeout=10)) == {"buttons": 2, "flows": 1}
    assert lines() == {}
    lay = _ok(mgr.get(url, timeout=5))
    assert pizza not in lay["flows"]
    _ok(mgr.put(url, json=lay, timeout=10))

    # The website change comes off its view.
    _ok(mgr.delete(f"{MENU}/entries/{entry['id']}", timeout=5), 204)
    assert not [n for n in _ok(mgr.get(f"{MENU}/views/{web['id']}/build", timeout=5))["needs_attention"] if n["code"] == "anchor_gone"]
    assert _ok(mgr.get(f"{MENU}/problems", timeout=10))["counts"]["broken"] == 0


def test_bad_layout_is_422_with_every_problem(mgr):
    ids, views = _setup(mgr)
    counter = views[("counter", "Counter")]
    url = f"{MENU}/views/{counter['id']}/layout"
    lay = ids.layout(_names_layout())
    pizza = lay["tabs"][0]["buttons"]
    pizza[0]["action"].pop("size_id")                       # a sized item needs a size
    pizza[1]["col"] = 0                                      # overlaps button 0
    pizza[2]["page"] = 5                                     # no such page
    pizza[3]["color"] = "pink"                               # not #rrggbb
    lay["tabs"][1]["buttons"][0]["action"]["item_id"] = "00000000-0000-0000-0000-000000000001"  # not in the bank
    lay["screens"][1]["buttons"][0]["action"]["option_id"] = ids.option[("Pizza Toppings", "Pepperoni")]  # wrong group
    lay["flows"][ids.item["Wings 10pc"]][0]["screen"] = "Ghost"  # no such screen
    lay["tabs"][1]["strip_col"] = 0                          # strip cell taken
    v0 = _version(mgr)
    body = _ok(mgr.put(url, json=lay, timeout=10), 422)
    assert body["code"] == "invalid_layout"
    paths = {p["path"] for p in body["problems"]}
    wings = ids.item["Wings 10pc"]
    assert paths >= {
        "tabs[0].buttons[0].action.size_id", "tabs[0].buttons[1]", "tabs[0].buttons[2].page", "tabs[0].buttons[3].color",
        "tabs[1].buttons[0].action.item_id", "screens[1].buttons[0].action.option_id", f"flows.{wings}[0].screen", "tabs[1]",
    }, body["problems"]
    assert all(p["message"] for p in body["problems"])
    assert _version(mgr) == v0
    assert _ok(mgr.get(url, timeout=5), 404)["code"] == "no_layout"
    # Unknown fields and broken JSON are a 400 (not a list of problems).
    bad = ids.layout(_names_layout())
    bad["tabs"][0]["buttons"][0]["colour"] = "#ffffff"
    assert _ok(mgr.put(url, json=bad, timeout=10), 400)["code"] == "bad_request"
    assert _ok(mgr.put(url, data="{", headers={"Content-Type": "application/json"}, timeout=10), 400)["code"] == "bad_request"


def test_only_counter_views_take_a_layout(mgr):
    ids, views = _setup(mgr)
    lay = ids.layout(_names_layout())
    for kind, name in (("web", "Web"), ("operator", "Call center"), ("vendor", "Brygid")):
        url = f"{MENU}/views/{views[(kind, name)]['id']}/layout"
        assert _ok(mgr.put(url, json=lay, timeout=10), 409)["code"] == "not_counter"
        assert _ok(mgr.get(url, timeout=5), 409)["code"] == "not_counter"
        assert _ok(mgr.delete(url, timeout=5), 409)["code"] == "not_counter"
    doc = _bank_doc()
    doc["views"] = [{"kind": "web", "name": "Web", "rules": {"round": "none"}, "entries": [], "layout": _names_layout()}]
    r = _ok(mgr.post(APPLY, json=doc, timeout=15), 400)
    assert r["code"] == "bad_document" and "views[0].layout" in r["error"]
    assert _ok(mgr.get(f"{MENU}/views/not-a-uuid/layout", timeout=5), 400)


def test_bank_document_round_trip(mgr):
    doc = _bank_doc()
    doc["views"] = [{"kind": "counter", "name": "Counter", "rules": {"round": "none"}, "entries": [], "layout": _names_layout()}]
    dry = _ok(mgr.post(APPLY, params={"dry_run": 1}, json=doc, timeout=15))
    assert dry["counts"]["layouts"]["created"] == 1
    views = {(v["kind"], v["name"]): v for v in _ok(mgr.get(f"{MENU}/views", timeout=5))}
    assert not views.get(("counter", "Counter"), {}).get("has_layout"), "a dry run writes nothing"

    res = _ok(mgr.post(APPLY, json=doc, timeout=15))
    assert res["counts"]["layouts"]["created"] == 1
    ids = Ids(mgr)
    counter = {(v["kind"], v["name"]): v for v in _ok(mgr.get(f"{MENU}/views", timeout=5))}[("counter", "Counter")]
    got = _ok(mgr.get(f"{MENU}/views/{counter['id']}/layout", timeout=5))
    assert got == ids.layout(_names_layout()), "names resolve to the bank's ids"

    exported = _ok(mgr.get(EXPORT, timeout=10))
    ev = next(v for v in exported["views"] if v["kind"] == "counter")
    assert ev["layout"] == _names_layout()
    assert all("layout" not in v for v in exported["views"] if v["kind"] != "counter")

    v0 = _version(mgr)
    again = _ok(mgr.post(APPLY, json=exported, timeout=15))
    assert sum(c["created"] + c["updated"] + c["deleted"] for c in again["counts"].values()) == 0, again["changes"]
    assert again["counts"]["layouts"]["unchanged"] == 1
    assert _version(mgr) == v0
    assert _ok(mgr.get(EXPORT, timeout=10)) == exported

    # An old document (no layout on the view) keeps the layout there.
    old = copy.deepcopy(exported)
    for v in old["views"]:
        v.pop("layout", None)
    _ok(mgr.post(APPLY, json=old, timeout=15))
    assert _ok(mgr.get(f"{MENU}/views/{counter['id']}/layout", timeout=5)) == got

    # A changed layout is an update.
    doc2 = copy.deepcopy(exported)
    lay = next(v for v in doc2["views"] if v["kind"] == "counter")["layout"]
    lay["tabs"][0]["buttons"][0]["caption"] = "Cheese 12"
    res = _ok(mgr.post(APPLY, json=doc2, timeout=15))
    assert res["counts"]["layouts"]["updated"] == 1
    assert _ok(mgr.get(f"{MENU}/views/{counter['id']}/layout", timeout=5))["tabs"][0]["buttons"][0]["caption"] == "Cheese 12"


@pytest.mark.parametrize("edit,path,msg", [
    (lambda l: l["screens"][0]["buttons"][0]["action"].update(option="Bacon"),
     "views[0].layout.screens[0].buttons[0].action.option", "no option"),
    (lambda l: l["tabs"][0]["buttons"][0]["action"].update(item="Calzone"),
     "views[0].layout.tabs[0].buttons[0].action.item", "unknown item"),
    (lambda l: l["tabs"][0]["buttons"][4]["action"].update(deal="Free Soda"),
     "views[0].layout.tabs[0].buttons[4].action.deal", "unknown deal"),
    (lambda l: l["flows"]["Cheese Pizza"][0].update(size="20 Party"),
     "views[0].layout.flows.Cheese Pizza[0].size", "unknown size"),
    (lambda l: l["tabs"][0]["buttons"][0]["action"].pop("size"),
     "views[0].layout.tabs[0].buttons[0].action.size_id", "size_id is required"),
])
def test_bank_document_bad_layout_writes_nothing(mgr, edit, path, msg):
    doc = _bank_doc()
    doc["views"] = [{"kind": "counter", "name": "Counter", "rules": {"round": "none"}, "entries": [], "layout": _names_layout()}]
    edit(doc["views"][0]["layout"])
    v0 = _version(mgr)
    r = _ok(mgr.post(APPLY, json=doc, timeout=15), 400)
    assert r["code"] == "bad_document" and r["error"].startswith(path) and msg in r["error"], r
    assert _version(mgr) == v0
    assert _ok(mgr.get(f"{MENU}/bank", timeout=5))["menus"] == [], "all or nothing"


def test_another_stores_view_is_404(make_store):
    a, b = make_store(), make_store()
    ma, mb = a.manager_session(), b.manager_session()
    try:
        ids, views = _setup(ma)
        counter = views[("counter", "Counter")]
        url = f"{MENU}/views/{counter['id']}/layout"
        _ok(ma.put(url, json=ids.layout(_names_layout()), timeout=10))
        assert _ok(mb.get(url, timeout=5), 404)["code"] == "not_found"
        assert _ok(mb.put(url, json=ids.layout(_names_layout()), timeout=10), 404)["code"] == "not_found"
        assert _ok(mb.delete(url, timeout=5), 404)["code"] == "not_found"
        assert _ok(ma.get(url, timeout=5))["tabs"][0]["name"] == "Pizza & Calzones"
    finally:
        ma.close()
        mb.close()


# --- size chooser screens (MENU-PLAN M2) ----------------------------------------------------------


def _chooser_doc() -> dict:
    """_bank_doc + a counter layout whose Hawaiian button has no size: its flow
    starts with a size chooser screen ("Pizza Size")."""
    doc = _bank_doc()
    lay = _names_layout()
    lay["tabs"][0]["buttons"][1]["action"] = {"type": "item", "item": "Hawaiian Pizza"}
    lay["screens"].append({"name": "Pizza Size", "prompt": "Which size?", "grid": {"rows": 1, "cols": 2}, "pages": [{"name": "Size"}],
                           "buttons": [
                               {"page": 0, "row": 0, "col": 0, "caption": "", "color": "", "font_color": "", "font_size": 0,
                                "action": {"type": "size", "size": "12 Medium"}},
                               {"page": 0, "row": 0, "col": 1, "caption": "XL", "color": "", "font_color": "", "font_size": 0,
                                "action": {"type": "size", "size": "16 X-Large"}}]})
    lay["flows"]["Hawaiian Pizza"] = [
        {"screen": "Pizza Size", "required_count": 1, "forced": True, "prompt": "Size?"},
        {"screen": "12\" Pizza Toppings", "required_count": 0, "forced": False, "prompt": ""},
    ]
    doc["views"] = [{"kind": "counter", "name": "Counter", "rules": {"round": "none"}, "entries": [], "layout": lay}]
    return doc


def test_size_chooser_round_trip(mgr):
    _ok(mgr.post(APPLY, json=_chooser_doc(), timeout=15))
    ids = Ids(mgr)
    counter = {(v["kind"], v["name"]): v for v in _ok(mgr.get(f"{MENU}/views", timeout=5))}[("counter", "Counter")]
    stored = _ok(mgr.get(f"{MENU}/views/{counter['id']}/layout", timeout=5))
    size_btns = stored["screens"][3]["buttons"]
    assert size_btns[0]["action"] == {"type": "size", "size_id": ids.size["12 Medium"]}
    assert stored["tabs"][0]["buttons"][1]["action"] == {"type": "item", "item_id": ids.item["Hawaiian Pizza"]}

    build = _ok(mgr.get(f"{MENU}/views/{counter['id']}/build", timeout=5))
    bl = build["layout"]
    haw = bl["tabs"][0]["buttons"][1]
    assert haw["caption"] == "Hawaiian Pizza" and haw["action"]["label"] == "Hawaiian Pizza"
    assert "price_cents" not in haw["action"] and "size_name" not in haw["action"] and not haw["action"].get("missing")
    chooser = bl["screens"][3]["buttons"]
    assert chooser[0]["caption"] == "12 Medium"
    assert chooser[0]["action"]["type"] == "size" and chooser[0]["action"]["size_id"] == ids.size["12 Medium"]
    assert chooser[0]["action"]["label"] == "12 Medium" and chooser[0]["action"]["size_name"] == "12 Medium"
    assert "price_cents" not in chooser[0]["action"], "the price comes from the line's item"
    assert chooser[1]["caption"] == "XL" and chooser[1]["action"]["size_name"] == "16 X-Large"
    assert bl["flows"][ids.item["Hawaiian Pizza"]][0]["screen"] == "Pizza Size"
    # The terminal prices the chosen size from the item's own sizes in the build.
    haw_item = next(it for c in build["categories"] for it in c["items"] if it["id"] == ids.item["Hawaiian Pizza"])
    assert {s["size_id"]: s["price_cents"] for s in haw_item["sizes"]} == {ids.size["12 Medium"]: 1499, ids.size["16 X-Large"]: 1999}
    assert not [n for n in build["needs_attention"] if n["code"].startswith("layout_")]

    exported = _ok(mgr.get(EXPORT, timeout=10))
    ev = next(v for v in exported["views"] if v["kind"] == "counter")["layout"]
    assert ev["screens"][3]["buttons"][0]["action"] == {"type": "size", "size": "12 Medium"}
    assert ev["tabs"][0]["buttons"][1]["action"] == {"type": "item", "item": "Hawaiian Pizza"}
    again = _ok(mgr.post(APPLY, json=exported, timeout=15))
    assert sum(c["created"] + c["updated"] + c["deleted"] for c in again["counts"].values()) == 0, again["changes"]

    # A size that goes away: the chooser button is missing and needs attention.
    _ok(mgr.delete(f"{MENU}/sizes/{ids.size['16 X-Large']}", timeout=5), 204)
    build = _ok(mgr.get(f"{MENU}/views/{counter['id']}/build", timeout=5))
    assert build["layout"]["screens"][3]["buttons"][1]["action"]["missing"] is True
    assert "layout_size_missing" in {n["code"] for n in build["needs_attention"]}


@pytest.mark.parametrize("edit,path,msg", [
    # a size button belongs on a screen
    (lambda l: l["tabs"][0]["buttons"][5].update(action={"type": "size", "size": "12 Medium"}),
     "views[0].layout.tabs[0].buttons[5].action.type", "must be one of"),
    (lambda l: l["screens"][3]["buttons"][0]["action"].update(size="20 Party"),
     "views[0].layout.screens[3].buttons[0].action.size", "unknown size"),
    (lambda l: l["screens"][3]["buttons"][0]["action"].pop("size"),
     "views[0].layout.screens[3].buttons[0].action.size_id", "size_id is required"),
    (lambda l: l["screens"][3]["buttons"][0]["action"].update(item="Can Soda"),
     "views[0].layout.screens[3].buttons[0].action.item_id", "doesn't belong"),
    # the chooser isn't the flow's first step: the sizeless button needs its size again
    (lambda l: l["flows"]["Hawaiian Pizza"].reverse(),
     "views[0].layout.tabs[0].buttons[1].action.size_id", "size_id is required"),
])
def test_size_chooser_bad(mgr, edit, path, msg):
    doc = _chooser_doc()
    edit(doc["views"][0]["layout"])
    r = _ok(mgr.post(APPLY, json=doc, timeout=15), 400)
    assert r["code"] == "bad_document" and r["error"].startswith(path) and msg in r["error"], r
    assert _ok(mgr.get(f"{MENU}/bank", timeout=5))["menus"] == [], "all or nothing"


def test_size_chooser_by_ids_route(mgr):
    """PUT …/layout takes the size action by id (422 lists a bad one)."""
    ids, views = _setup(mgr)
    counter = views[("counter", "Counter")]
    url = f"{MENU}/views/{counter['id']}/layout"
    names = _chooser_doc()["views"][0]["layout"]
    lay = ids.layout({**names, "screens": [s for s in names["screens"] if s["name"] != "Pizza Size"]})
    lay["screens"].append({"name": "Pizza Size", "prompt": "", "grid": {"rows": 1, "cols": 1}, "pages": [{"name": "Size"}],
                           "buttons": [{"page": 0, "row": 0, "col": 0, "caption": "", "color": "", "font_color": "", "font_size": 0,
                                        "action": {"type": "size", "size_id": ids.size["16 X-Large"]}}]})
    assert _ok(mgr.put(url, json=lay, timeout=10)) == lay
    lay["screens"][-1]["buttons"][0]["action"]["size_id"] = "00000000-0000-0000-0000-000000000009"
    body = _ok(mgr.put(url, json=lay, timeout=10), 422)
    assert any(p["path"] == "screens[3].buttons[0].action.size_id" and "store's sizes" in p["message"] for p in body["problems"]), body


def test_renames_keep_the_layout(mgr):
    """MENU-PLAN M1: an item and a size renamed through `was` keep their ids, so
    the counter layout (and its flows) still resolve; the document names the new
    names only."""
    _ok(mgr.post(APPLY, json=_chooser_doc(), timeout=15))
    before = Ids(mgr)
    exported = _ok(mgr.get(EXPORT, timeout=10))
    raw = json.dumps(exported).replace('"Cheese Pizza"', '"Cheese"').replace('"12 Medium"', '"12 Med"')
    doc = json.loads(raw)
    next(s for s in doc["sizes"] if s["name"] == "12 Med")["was"] = ["12 Medium"]
    pizza = next(c for c in doc["menus"][0]["categories"] if c["name"] == "Pizza")
    next(it for it in pizza["items"] if it["name"] == "Cheese")["was"] = ["cheese pizza"]
    dry = _ok(mgr.post(APPLY, params={"dry_run": 1, "prune": 1}, json=doc, timeout=15))
    assert 'size "12 Medium" renamed to "12 Med"' in dry["changes"] and 'item "Cheese Pizza" renamed to "Cheese"' in dry["changes"], dry["changes"]
    assert all(c["deleted"] == 0 for k, c in dry["counts"].items() if k != "entries"), dry["counts"]
    _ok(mgr.post(APPLY, params={"prune": 1}, json=doc, timeout=15))
    after = Ids(mgr)
    assert after.item["Cheese"] == before.item["Cheese Pizza"] and after.size["12 Med"] == before.size["12 Medium"]
    counter = {(v["kind"], v["name"]): v for v in _ok(mgr.get(f"{MENU}/views", timeout=5))}[("counter", "Counter")]
    build = _ok(mgr.get(f"{MENU}/views/{counter['id']}/build", timeout=5))
    assert not [n for n in build["needs_attention"] if n["code"].startswith("layout_")], build["needs_attention"]
    b0 = build["layout"]["tabs"][0]["buttons"][0]["action"]
    assert b0["label"] == "Cheese" and b0["size_name"] == "12 Med" and b0["item_id"] == before.item["Cheese Pizza"]
    assert build["layout"]["screens"][3]["buttons"][0]["caption"] == "12 Med"
    assert before.item["Cheese Pizza"] in build["layout"]["flows"]
    again = _ok(mgr.post(APPLY, params={"prune": 1}, json=doc, timeout=15))
    assert again["changes"] == [], again["changes"]
