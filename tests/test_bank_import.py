"""The bank document (WEBORDER-PLAN.md stage 1): apply a whole bank + views
+ deals in one transaction, export it back.

  * dry run reports every change and writes nothing (the menu version and the
    bank are untouched)
  * apply creates it all; applying the same document again changes nothing
  * export → apply is a no-op (round trip)
  * absent optional fields keep what is there (in_stock, printer, sizes,
    groups); an item moves category; a renamed price is an update
  * prune deletes what the document doesn't name (never views)
  * a bad reference fails with its path and writes nothing (all or nothing)
  * the web view compiles: prices per size, a hidden category, needs-attention
  * the vendor (Brygid) view's maps route an order line to the mapped item's
    printer
  * deals refer to items / categories / sizes by name; a duplicate code is 409
  * another store's document never touches this store
"""

from __future__ import annotations

import copy

import pytest
import requests

from conftest import STORE_V1, wait_order

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


def _printers(mgr) -> dict[str, str]:
    for name, ip in (("Pizza", "10.9.0.1"), ("Desi", "10.9.0.2")):
        _ok(mgr.post(f"{STORE_V1}/me/printers", json={"name": name, "ip": ip}, timeout=5), 201)
    return {p["name"]: p["id"] for p in _ok(mgr.get(f"{STORE_V1}/me/printers", timeout=5))}


def _doc() -> dict:
    """Lombardi's in miniature: sized pizzas with halves toppings, a curry,
    two cheesecakes merged on the web, drinks hidden on the web, Brygid maps
    and a 2-pizza bundle."""
    sizes = ["10 Small", "12 Medium", "14 Large"]
    return {
        "format": 1,
        "sizes": [{"name": "10 Small", "short": "10\""}, {"name": "12 Medium", "short": "12\""}, {"name": "14 Large", "short": "14\""}],
        "groups": [
            {"name": "Pizza Toppings", "max_select": 0, "allow_multiple": True, "portions": True, "options": [
                {"name": "Pepperoni", "price_cents": 200, "kitchen_name": "PEP", "prices": {"10 Small": 125, "12 Medium": 150, "14 Large": 200}},
                {"name": "Mushrooms", "price_cents": 200, "prices": {"10 Small": 125, "12 Medium": 150, "14 Large": 200}},
            ]},
            {"name": "Spice", "required": True, "min_select": 1, "max_select": 1, "options": [
                {"name": "Mild", "price_cents": 0, "is_default": True}, {"name": "Hot", "price_cents": 0}]},
        ],
        "menus": [{"name": "Main", "categories": [
            {"name": "Pizza", "printer": "Pizza", "items": [
                {"name": "Cheese Pizza", "price_cents": 699, "kitchen_name": "CHZ", "sku": "st:2900",
                 "sizes": [{"size": s, "price_cents": p} for s, p in zip(sizes, (699, 999, 1199))],
                 "groups": ["Pizza Toppings"]},
                {"name": "Meat Delight", "price_cents": 1099,
                 "sizes": [{"size": s, "price_cents": p} for s, p in zip(sizes, (1099, 1399, 1699))],
                 "groups": ["Pizza Toppings"]},
            ]},
            {"name": "Desi", "printer": "Desi", "items": [
                {"name": "Chicken Tikka Masala", "price_cents": 1399, "groups": ["Spice"]}]},
            {"name": "Desserts", "items": [
                {"name": "Cheesecake Strawberry", "price_cents": 499},
                {"name": "Cheesecake Oreo", "price_cents": 499}]},
            {"name": "Drinks", "items": [{"name": "Can Soda", "price_cents": 150}]},
        ]}],
        "views": [
            {"kind": "web", "name": "Web", "rules": {"markup_bps": 0, "round": "none", "hidden_categories": ["Drinks"]}, "entries": [
                {"kind": "item", "items": ["Cheese Pizza"], "display_name": "Cheese Pizza", "description": "Our classic"},
                {"kind": "merge", "items": ["Cheesecake Strawberry", "Cheesecake Oreo"], "display_name": "Cheesecake",
                 "labels": {"Cheesecake Strawberry": "Strawberry", "Cheesecake Oreo": "Oreo"}},
            ]},
            {"kind": "vendor", "name": "Brygid", "rules": {"round": "none"}, "entries": [
                {"kind": "map", "items": ["Cheese Pizza"], "size": "12 Medium", "vendor_name": "(12 Medium) Regular Tomato Cheese"},
                {"kind": "map", "items": ["Chicken Tikka Masala"], "vendor_name": "Tikka Masala (Chicken)"},
            ]},
        ],
        "deals": [
            {"kind": "bundle", "name": "2 Medium Cheese", "active": True, "price_cents": 1999, "slots": [
                {"label": "Pizza 1", "eligible": {"items": ["Cheese Pizza"], "categories": [], "sizes": ["12 Medium"]}},
                {"label": "Pizza 2", "eligible": {"items": ["Cheese Pizza"], "categories": [], "sizes": ["12 Medium"]}}]},
            {"kind": "code", "name": "Welcome", "active": True, "code": "WELCOME5", "amount_cents": 500, "min_subtotal_cents": 2000},
        ],
    }


def _items(bank: dict) -> dict[str, dict]:
    return {it["name"]: it for m in bank["menus"] for c in m["categories"] for it in c["items"]}


def _cats(bank: dict) -> dict[str, dict]:
    return {c["name"]: c for m in bank["menus"] for c in m["categories"]}


def _changed(res: dict) -> int:
    return sum(c["created"] + c["updated"] + c["deleted"] for c in res["counts"].values())


def test_dry_run_writes_nothing_then_apply_then_noop(mgr):
    _printers(mgr)
    before = _ok(mgr.get(f"{MENU}/bank", timeout=5))
    dry = _ok(mgr.post(APPLY, params={"dry_run": 1}, json=_doc(), timeout=15))
    assert dry["dry_run"] and not dry["applied"]
    assert dry["counts"]["items"]["created"] == 6 and dry["counts"]["sizes"]["created"] == 3
    assert dry["counts"]["deals"]["created"] == 2 and dry["counts"]["views"]["created"] == 2
    after = _ok(mgr.get(f"{MENU}/bank", timeout=5))
    assert after["menus"] == [] and after["sizes"] == [] and after["version"] == before["version"]

    res = _ok(mgr.post(APPLY, json=_doc(), timeout=15))
    assert res["applied"] and res["counts"] == dry["counts"]
    bank = _ok(mgr.get(f"{MENU}/bank", timeout=5))
    items = _items(bank)
    assert set(items) == {"Cheese Pizza", "Meat Delight", "Chicken Tikka Masala", "Cheesecake Strawberry", "Cheesecake Oreo", "Can Soda"}
    chz = items["Cheese Pizza"]
    assert chz["kitchen_name"] == "CHZ" and chz["sku"] == "st:2900" and [s["price_cents"] for s in chz["sizes"]] == [699, 999, 1199]
    groups = {g["name"]: g for g in bank["groups"].values()}
    assert chz["group_ids"] == [groups["Pizza Toppings"]["id"]] and groups["Pizza Toppings"]["portions"]
    pep = next(o for o in groups["Pizza Toppings"]["options"] if o["name"] == "Pepperoni")
    assert pep["kitchen_name"] == "PEP" and sorted(p["price_cents"] for p in pep["prices"]) == [125, 150, 200]
    mild = next(o for o in groups["Spice"]["options"] if o["name"] == "Mild")
    assert mild["is_default"] and groups["Spice"]["required"]

    again = _ok(mgr.post(APPLY, json=_doc(), timeout=15))
    assert _changed(again) == 0, again["changes"]
    assert again["changes"] == []
    assert _ok(mgr.get(f"{MENU}/bank", timeout=5))["version"] == bank["version"]


def test_export_round_trip_is_a_noop(mgr):
    _printers(mgr)
    _ok(mgr.post(APPLY, json=_doc(), timeout=15))
    doc = _ok(mgr.get(EXPORT, timeout=10))
    assert doc["format"] == 1
    assert {d["name"] for d in doc["deals"]} == {"2 Medium Cheese", "Welcome"}
    pizza = next(c for c in doc["menus"][0]["categories"] if c["name"] == "Pizza")
    assert pizza["printer"] == "Pizza"
    res = _ok(mgr.post(APPLY, json=doc, timeout=15))
    assert _changed(res) == 0, res["changes"]


def test_absent_fields_keep_and_updates_move(mgr):
    _printers(mgr)
    _ok(mgr.post(APPLY, json=_doc(), timeout=15))
    bank = _ok(mgr.get(f"{MENU}/bank", timeout=5))
    soda = _items(bank)["Can Soda"]
    # 86 the soda by hand; give the curry a printer of its own.
    _ok(mgr.patch(f"{MENU}/items/{soda['id']}", json={"name": "Can Soda", "price_cents": 150, "in_stock": False, "tags": []}, timeout=5))

    doc = _doc()
    menu = doc["menus"][0]["categories"]
    # Cheese pizza: new price on 14", sizes/groups left out of Meat Delight (kept).
    menu[0]["items"][0]["sizes"][2]["price_cents"] = 1299
    del menu[0]["items"][1]["sizes"]
    del menu[0]["items"][1]["groups"]
    # Move the oreo cheesecake to Drinks (silly, but a move).
    oreo = menu[2]["items"].pop(1)
    menu[3]["items"].append(oreo)
    del menu[0]["printer"]  # keep the category printer
    res = _ok(mgr.post(APPLY, json=doc, timeout=15))
    ch = "\n".join(res["changes"])
    assert 'item "Cheese Pizza": sizes' in ch
    assert 'item "Cheesecake Oreo": category' in ch
    assert "Can Soda" not in ch  # in_stock absent → kept as sold out
    bank = _ok(mgr.get(f"{MENU}/bank", timeout=5))
    items, cats = _items(bank), _cats(bank)
    assert items["Can Soda"]["in_stock"] is False
    assert [s["price_cents"] for s in items["Meat Delight"]["sizes"]] == [1099, 1399, 1699]
    assert len(items["Meat Delight"]["group_ids"]) == 1
    assert items["Cheesecake Oreo"]["category_id"] == cats["Drinks"]["id"]
    assert cats["Pizza"]["default_printer_id"] is not None


def test_prune_deletes_what_the_document_does_not_name(mgr):
    _printers(mgr)
    _ok(mgr.post(APPLY, json=_doc(), timeout=15))
    doc = _doc()
    doc["menus"][0]["categories"] = [c for c in doc["menus"][0]["categories"] if c["name"] != "Drinks"]
    doc["views"][0]["rules"]["hidden_categories"] = []
    doc["deals"] = doc["deals"][:1]
    doc["groups"][0]["options"] = doc["groups"][0]["options"][:1]
    without = _ok(mgr.post(APPLY, json=doc, timeout=15))
    assert without["counts"]["items"]["deleted"] == 0  # no prune: kept
    res = _ok(mgr.post(APPLY, params={"prune": 1}, json=doc, timeout=15))
    assert res["counts"]["items"]["deleted"] == 1 and res["counts"]["categories"]["deleted"] == 1
    assert res["counts"]["deals"]["deleted"] == 1 and res["counts"]["options"]["deleted"] == 1
    assert res["counts"]["views"]["deleted"] == 0
    bank = _ok(mgr.get(f"{MENU}/bank", timeout=5))
    assert "Can Soda" not in _items(bank) and "Drinks" not in _cats(bank)


@pytest.mark.parametrize("break_it, path, msg", [
    (lambda d: d["menus"][0]["categories"][0]["items"][0]["groups"].append("Nope"), "menus[0].categories[0].items[0].groups[1]", "unknown modifier group"),
    (lambda d: d["menus"][0]["categories"][0]["items"][0]["sizes"].append({"size": "99 Huge", "price_cents": 1}), "menus[0].categories[0].items[0].sizes[3]", "unknown size"),
    (lambda d: d["menus"][0]["categories"][0].update(printer="Fryer"), "menus[0].categories[0].printer", "unknown printer"),
    (lambda d: d["views"][0]["entries"].append({"kind": "item", "items": ["Ghost"]}), "views[0].entries[2].items[0]", "unknown item"),
    (lambda d: d["deals"][0]["slots"][0]["eligible"]["sizes"].append("Huge"), "deals[0].slots[0].eligible.sizes[1]", "unknown size"),
    (lambda d: d["menus"][0]["categories"][1]["items"].append({"name": "cheese  PIZZA", "price_cents": 1}), "menus[0].categories[1].items[1]", "already at"),
    (lambda d: d.update(format=2), "format", "must be 1"),
    (lambda d: d["views"][1]["entries"].append({"kind": "item", "items": ["Cheese Pizza"]}), "views[1].entries[2]", "vendor view only maps"),
])
def test_bad_document_names_the_path_and_writes_nothing(mgr, break_it, path, msg):
    _printers(mgr)
    before = _ok(mgr.get(f"{MENU}/bank", timeout=5))
    doc = _doc()
    break_it(doc)
    r = mgr.post(APPLY, json=doc, timeout=15)
    assert r.status_code == 400, r.text
    body = r.json()
    assert body["code"] == "bad_document" and path in body["error"] and msg in body["error"], body
    after = _ok(mgr.get(f"{MENU}/bank", timeout=5))
    assert after["menus"] == [] and after["version"] == before["version"]


def test_unknown_field_and_duplicate_code(mgr):
    _printers(mgr)
    doc = _doc()
    doc["menus"][0]["colour"] = "red"
    r = mgr.post(APPLY, json=doc, timeout=15)
    assert r.status_code == 400 and "colour" in r.text
    doc = _doc()
    doc["deals"].append(copy.deepcopy(doc["deals"][1]) | {"name": "Welcome again"})
    r = mgr.post(APPLY, json=doc, timeout=15)
    assert r.status_code == 409 and r.json()["code"] == "code_taken"
    assert _ok(mgr.get(f"{MENU}/bank", timeout=5))["menus"] == []


def test_web_view_builds_and_needs_attention(mgr):
    _printers(mgr)
    doc = _doc()
    doc["views"][0]["rules"]["require_photo"] = True
    res = _ok(mgr.post(APPLY, json=doc, timeout=15))
    na = res["needs_attention"]["web:Web"]
    assert any(n["code"] == "no_photo" for n in na)
    assert "vendor:Brygid" in res["needs_attention"]
    views = {(v["kind"], v["name"]): v for v in _ok(mgr.get(f"{MENU}/views", timeout=5))}
    build = _ok(mgr.get(f"{MENU}/views/{views[('web', 'Web')]['id']}/build", timeout=5))
    names = {it["name"] for c in build["categories"] for it in c["items"]}
    assert "Cheesecake" in names and "Can Soda" not in names  # merged; Drinks hidden
    chz = next(it for c in build["categories"] for it in c["items"] if it["name"] == "Cheese Pizza")
    assert [s["price_cents"] for s in chz["sizes"]] == [699, 999, 1199]


def _line(name: str) -> dict:
    return {"name": name, "size_name": "Serving", "size_id": 1, "quantity": 1, "price": 5.0, "plu": "",
            "who": "", "group_id": "", "notes": "", "modifiers": []}


def test_brygid_maps_route_by_the_document(mgr, store, publish):
    printers = _printers(mgr)
    _ok(mgr.post(APPLY, json=_doc(), timeout=15))
    o = store.order(items=[_line("(12 Medium) Regular Tomato  Cheese"), _line("Tikka Masala (Chicken)")])
    publish(o)
    by = {li["name"]: li for li in wait_order(store.key, o["order_id"])["items"]}
    chz, tikka = by["(12 Medium) Regular Tomato  Cheese"], by["Tikka Masala (Chicken)"]
    assert chz["routing"] == "matched" and chz["printer_id"] == printers["Pizza"], chz
    assert tikka["routing"] == "matched" and tikka["printer_id"] == printers["Desi"], tikka


def test_other_store_untouched(mgr, make_store):
    _printers(mgr)
    other = make_store()
    o_mgr = other.manager_session()
    _ok(o_mgr.post(f"{STORE_V1}/me/printers", json={"name": "Pizza", "ip": "10.9.1.1"}, timeout=5), 201)
    _ok(o_mgr.post(f"{STORE_V1}/me/printers", json={"name": "Desi", "ip": "10.9.1.2"}, timeout=5), 201)
    _ok(o_mgr.post(APPLY, json=_doc(), timeout=15))
    assert _ok(mgr.get(f"{MENU}/bank", timeout=5))["menus"] == []
    _ok(mgr.post(APPLY, json=_doc(), timeout=15))
    res = _ok(mgr.post(APPLY, params={"prune": 1}, json={"format": 1, "sizes": [], "groups": [], "menus": [], "views": [], "deals": []}, timeout=15))
    assert res["counts"]["items"]["deleted"] == 6
    assert len(_items(_ok(o_mgr.get(f"{MENU}/bank", timeout=5)))) == 6
    o_mgr.close()
