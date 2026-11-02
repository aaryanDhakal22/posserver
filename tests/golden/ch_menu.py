"""Golden chapter: the menu editor (/store/v1/me/menu/*, CSV import/export,
unmatched names) and the call-center operator surface (login, me, menu)."""

from __future__ import annotations

import requests

from conftest import Store
from golden.recorder import Rec

CSV = """menu,category,item,price,printer,sizes
Main,Pizza,Cheese Pizza,10.00,Pizza,Large
Main,Pizza,Pepperoni Pizza,$12.00,Pizza,
Main,Pizza,Garlic Knots,4.00,Sub,
Main,Subs,Philly Cheese Steak,9.50,,
"""
NIL = "00000000-0000-0000-0000-0000000000ff"


def run(rec: Rec, admin: requests.Session, _request) -> None:
    a, b = Store(admin), Store(admin)
    try:
        _run(rec, admin, a, b)
    finally:
        a.delete()
        b.delete()


def _run(rec: Rec, admin: requests.Session, a: Store, b: Store) -> None:
    for s, n in ((a, "a"), (b, "b")):
        rec.alias(s.tag, f"tag-{n}")
        rec.alias(s.store_id, f"store_id-{n}")
    mgr = a.manager_session()
    M = "/store/v1/me/menu"
    for name, ip in (("Pizza", "10.0.0.1"), ("Sub", "10.0.0.2")):
        mgr.post(f"{rec.base}/store/v1/me/printers", json={"name": name, "ip": ip}, timeout=5)
    printers = {p["name"]: p["id"] for p in mgr.get(f"{rec.base}/store/v1/me/printers", timeout=5).json()}

    # Menus → categories → items.
    rec.call("menus empty", "GET", f"{M}/menus", session=mgr)
    m1 = rec.call("menu add", "POST", f"{M}/menus", session=mgr, json_body={"name": "Dinner"}).json()
    m2 = rec.call("menu add 2", "POST", f"{M}/menus", session=mgr, json_body={"name": "Lunch"}).json()
    rec.call("menu add empty name", "POST", f"{M}/menus", session=mgr, json_body={"name": ""})
    rec.call("menu edit", "PATCH", f"{M}/menus/{m2['id']}", session=mgr, json_body={"name": "Lunch Specials", "available": False})
    rec.call("menus reorder", "POST", f"{M}/menus/reorder", session=mgr, json_body={"ids": [m2["id"], m1["id"]]})
    rec.call("menus reorder bad", "POST", f"{M}/menus/reorder", session=mgr, json_body={"ids": ["nope"]})
    rec.call("menus", "GET", f"{M}/menus", session=mgr)
    c1 = rec.call("category add", "POST", f"{M}/menus/{m1['id']}/categories", session=mgr, json_body={"name": "Mains"}).json()
    c2 = rec.call("category add 2", "POST", f"{M}/menus/{m1['id']}/categories", session=mgr, json_body={"name": "Sides"}).json()
    rec.call("category add unknown menu", "POST", f"{M}/menus/{NIL}/categories", session=mgr, json_body={"name": "x"})
    rec.call("category edit", "PATCH", f"{M}/categories/{c2['id']}", session=mgr,
             json_body={"name": "Sides & Extras", "default_printer_id": printers["Sub"]})
    rec.call("categories reorder", "POST", f"{M}/categories/reorder", session=mgr, json_body={"ids": [c2["id"], c1["id"]]})
    rec.call("categories", "GET", f"{M}/menus/{m1['id']}/categories", session=mgr)
    i1 = rec.call("item add", "POST", f"{M}/categories/{c1['id']}/items", session=mgr, json_body={"name": "Burger", "price_cents": 500}).json()
    i2 = rec.call("item add 2", "POST", f"{M}/categories/{c2['id']}/items", session=mgr, json_body={"name": "Fries", "price_cents": 300}).json()
    rec.call("item add bad price", "POST", f"{M}/categories/{c1['id']}/items", session=mgr, json_body={"name": "x", "price_cents": -1})
    item_body = {"name": "Deluxe Burger", "description": "with cheese", "price_cents": 1299, "tax_rate_bps": 600,
                 "in_stock": True, "out_until": None, "tags": ["Popular"], "attributes": {}}
    rec.call("item edit", "PATCH", f"{M}/items/{i1['id']}", session=mgr, json_body=item_body)
    rec.call("item edit out of scope", "PATCH", f"{M}/items/{NIL}", session=mgr, json_body=item_body)
    rec.call("item printer", "PATCH", f"{M}/items/{i1['id']}/printer", session=mgr, json_body={"printer_id": printers["Pizza"]})
    rec.call("item printer clear", "PATCH", f"{M}/items/{i2['id']}/printer", session=mgr, json_body={"printer_id": None})
    rec.call("item printer foreign", "PATCH", f"{M}/items/{i2['id']}/printer", session=mgr, json_body={"printer_id": NIL})
    rec.call("items reorder", "POST", f"{M}/items/reorder", session=mgr, json_body={"ids": [i1["id"]]})
    rec.call("items", "GET", f"{M}/categories/{c1['id']}/items", session=mgr)

    # Modifier groups + options, linked M:N.
    g = rec.call("group add", "POST", f"{M}/modifier-groups", session=mgr,
                 json_body={"name": "Add-ons", "min_select": 0, "max_select": 2}).json()
    rec.call("group add bad", "POST", f"{M}/modifier-groups", session=mgr, json_body={"name": "x", "min_select": 3, "max_select": 1})
    rec.call("group edit", "PATCH", f"{M}/modifier-groups/{g['id']}", session=mgr,
             json_body={"name": "Toppings", "required": False, "min_select": 0, "max_select": 3, "allow_multiple": True})
    o1 = rec.call("option add", "POST", f"{M}/modifier-groups/{g['id']}/options", session=mgr,
                  json_body={"name": "Bacon", "price_delta_cents": 150}).json()
    o2 = rec.call("option add 2", "POST", f"{M}/modifier-groups/{g['id']}/options", session=mgr,
                  json_body={"name": "Onions", "price_delta_cents": 0}).json()
    rec.call("option edit", "PATCH", f"{M}/modifier-groups/{g['id']}/options/{o2['id']}", session=mgr,
             json_body={"name": "Grilled Onions", "price_delta_cents": 50, "in_stock": False})
    rec.call("options reorder", "POST", f"{M}/modifier-groups/{g['id']}/options/reorder", session=mgr,
             json_body={"ids": [o2["id"], o1["id"]]})
    rec.call("options", "GET", f"{M}/modifier-groups/{g['id']}/options", session=mgr)
    rec.call("groups", "GET", f"{M}/modifier-groups", session=mgr)
    rec.call("item link group", "POST", f"{M}/items/{i1['id']}/modifier-groups", session=mgr, json_body={"group_id": g["id"]})
    rec.call("item link group again", "POST", f"{M}/items/{i1['id']}/modifier-groups", session=mgr, json_body={"group_id": g["id"]})
    rec.call("item groups", "GET", f"{M}/items/{i1['id']}/modifier-groups", session=mgr)

    # The whole menu as the call center reads it.
    user, pw = f"op{a.tag}", "golden-op-pw"
    op = admin.post(f"{rec.base}/admin/v1/operators", json={"username": user, "password": pw, "display_name": "Golden Op"},
                    timeout=5).json()
    admin.put(f"{rec.base}/admin/v1/operators/{op['id']}/locations", json={"location_ids": [a.location_id]}, timeout=5)
    ops = requests.Session()
    r = rec.call("operator login wrong", "POST", "/store/v1/operator/login", session=ops, json_body={"username": user, "password": "wrong"})
    r = rec.call("operator login", "POST", "/store/v1/operator/login", session=ops, json_body={"username": user, "password": pw})
    ops.headers["Authorization"] = f"Bearer {r.json().get('token', '')}"
    rec.call("operator me", "GET", "/store/v1/operator/me", session=ops)
    rec.call("operator menu", "GET", "/store/v1/operator/menu", session=ops, params={"location_id": a.location_id})
    rec.call("operator menu other store", "GET", "/store/v1/operator/menu", session=ops, params={"location_id": b.location_id})
    rec.call("operator menu no location", "GET", "/store/v1/operator/menu", session=ops)
    admin.post(f"{rec.base}/admin/v1/operators/{op['id']}/tokens/revoke", timeout=5)
    rec.call("operator me revoked", "GET", "/store/v1/operator/me", session=ops)
    admin.delete(f"{rec.base}/admin/v1/operators/{op['id']}", timeout=5)

    # Unlink, delete.
    rec.call("item unlink group", "DELETE", f"{M}/items/{i1['id']}/modifier-groups/{g['id']}", session=mgr)
    rec.call("option delete", "DELETE", f"{M}/modifier-groups/{g['id']}/options/{o1['id']}", session=mgr)
    rec.call("group delete", "DELETE", f"{M}/modifier-groups/{g['id']}", session=mgr)
    rec.call("item delete", "DELETE", f"{M}/items/{i2['id']}", session=mgr)
    rec.call("category delete", "DELETE", f"{M}/categories/{c2['id']}", session=mgr)
    rec.call("menu delete", "DELETE", f"{M}/menus/{m2['id']}", session=mgr)
    rec.call("menu delete again", "DELETE", f"{M}/menus/{m2['id']}", session=mgr)

    # CSV: dry run, import, re-import (no-op), export, replace.
    h = {"Content-Type": "text/csv"}
    rec.call("csv dry run", "POST", f"{M}/import", session=mgr, params={"dry_run": 1, "replace": 0}, data=CSV.encode(), headers=h)
    rec.call("csv import", "POST", f"{M}/import", session=mgr, params={"dry_run": 0, "replace": 0}, data=CSV.encode(), headers=h)
    rec.call("csv import again", "POST", f"{M}/import", session=mgr, params={"dry_run": 0, "replace": 0}, data=CSV.encode(), headers=h)
    rec.call("csv bad header", "POST", f"{M}/import", session=mgr, data=b"category,item\nA,B\n", headers=h)
    exp = rec.call("csv export", "GET", f"{M}/export.csv", session=mgr)
    rec.call("csv replace", "POST", f"{M}/import", session=mgr, params={"dry_run": 0, "replace": 1}, data=exp.content, headers=h)
    rec.call("menus after csv", "GET", f"{M}/menus", session=mgr)
    rec.call("unmatched", "GET", f"{M}/unmatched", session=mgr, params={"days": 7})
