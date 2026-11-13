"""Quick add (owner 2026-11-13, migration 00008; contract/main.md §2.5zb): the
terminal's Quicc Add line (a typed name + price) prints where the store's
"Quick add" menu item routes, set in Store › Menu like any item.

  * made once per store (its own "Quick add" menu + category, no printer) on
    the Pi's first counter-menu pull or Store › Menu's GET …/menu/quick-add
  * the counter menu names it (`quick_add_item_id`) and routes it like any item
    (printer / category default / no kitchen ticket); it is in no build
  * never deleted (409 quick_add_kept, nor its category / menu); never in the
    bank document; a pruning apply keeps it; online lines never match it
"""

from __future__ import annotations

import requests

from conftest import STORE_V1, store_key, wait_order

MENU = f"{STORE_V1}/me/menu"
APPLY = f"{MENU}/bank/apply"
EXPORT = f"{MENU}/bank/document"
COUNTER_MENU = f"{STORE_V1}/agent/counter-menu"


def _ok(r: requests.Response, status: int = 200):
    assert r.status_code == status, f"{r.status_code}: {r.text}"
    return r.json() if r.content else {}


def _doc() -> dict:
    return {
        "format": 1, "sizes": [], "groups": [],
        "menus": [{"name": "Counter", "categories": [
            {"name": "Pizza", "items": [{"name": "Cheese Pizza", "price_cents": 1000}]}]}],
        "views": [{"kind": "counter", "name": "Counter", "rules": {"markup_bps": 0, "round": "none",
                                                                  "require_photo": False, "hidden_categories": []},
                   "entries": []}],
        "deals": [],
    }


def _counter(store) -> dict:
    return _ok(requests.get(COUNTER_MENU, headers=store_key(store.key), timeout=10))


def test_quick_add_item_routes_like_any_item(store) -> None:
    mgr = store.manager_session()
    wings = _ok(mgr.post(f"{STORE_V1}/me/printers", json={"name": "Wings", "ip": "10.0.0.3"}, timeout=5), 201)["id"]
    _ok(mgr.post(APPLY, json=_doc(), timeout=20))

    # The Pi's first pull makes it: named in the body, in no build, no route yet (every station).
    body = _counter(store)
    qa_id = body["quick_add_item_id"]
    assert qa_id
    built = {i["id"] for c in body["build"]["categories"] for i in c["items"]}
    assert qa_id not in built and "Quick add" not in {c["name"] for c in body["build"]["categories"]}
    assert qa_id not in body["routes"]
    # Store › Menu reads the same one (made once).
    qa = _ok(mgr.get(f"{MENU}/quick-add", timeout=5))
    assert qa["item_id"] == qa_id and qa["category_id"] and qa["menu_id"]
    assert _ok(mgr.get(f"{MENU}/quick-add", timeout=5)) == qa
    menus = _ok(mgr.get(f"{MENU}/menus", timeout=5))
    assert [m["name"] for m in menus] == ["Counter", "Quick add"], "the Quick add menu comes last"
    items = _ok(mgr.get(f"{MENU}/categories/{qa['category_id']}/items", timeout=5))
    assert [(i["id"], i.get("quick_add")) for i in items] == [(qa_id, True)]

    # Its own printer → the counter menu routes it there; the category's default when it has none.
    v0 = body["menu_version"]
    _ok(mgr.patch(f"{MENU}/items/{qa_id}/printer", json={"printer_id": wings}, timeout=5), 204)
    body = _counter(store)
    assert body["menu_version"] > v0 and body["routes"][qa_id]["printer_id"] == wings
    _ok(mgr.patch(f"{MENU}/items/{qa_id}/printer", json={"printer_id": None}, timeout=5), 204)
    _ok(mgr.patch(f"{MENU}/categories/{qa['category_id']}", json={"name": "Quick add", "description": "",
                                                                 "available": True, "default_printer_id": wings}, timeout=5))
    assert _counter(store)["routes"][qa_id]["printer_id"] == wings
    # No kitchen ticket → receipt only.
    _ok(mgr.patch(f"{MENU}/items/{qa_id}/printer", json={"printer_id": None, "no_kitchen_ticket": True}, timeout=5), 204)
    assert _counter(store)["routes"][qa_id] == {"printer_id": "", "no_kitchen": True}

    # Never deleted, nor what holds it.
    for path in (f"items/{qa_id}", f"categories/{qa['category_id']}", f"menus/{qa['menu_id']}"):
        r = mgr.delete(f"{MENU}/{path}", timeout=5)
        assert r.status_code == 409 and r.json()["code"] == "quick_add_kept", (path, r.status_code, r.text)

    # Not in the bank document; a pruning apply of it keeps the item (and its settings).
    doc = _ok(mgr.get(EXPORT, timeout=10))
    assert "Quick add" not in {m["name"] for m in doc["menus"]}, doc["menus"]
    res = _ok(mgr.post(APPLY, params={"prune": 1}, json=doc, timeout=20))
    assert not [c for c in res["changes"] if "Quick add" in c], res["changes"]
    body = _counter(store)
    assert body["quick_add_item_id"] == qa_id and body["routes"][qa_id] == {"printer_id": "", "no_kitchen": True}
    mgr.close()


def test_online_line_never_matches_quick_add(store, publish) -> None:
    mgr = store.manager_session()
    _ok(mgr.post(APPLY, json=_doc(), timeout=20))
    _counter(store)  # makes the Quick add item

    o = store.order(items=[{"name": "Quick add", "size_name": "", "size_id": 1, "quantity": 1, "price": 2.0, "plu": "",
                            "who": "", "group_id": "", "notes": "", "modifiers": []}])
    publish(o)
    line = wait_order(store.key, o["order_id"])["items"][0]
    assert line["routing"] == "unmatched" and not line.get("menu_item_id"), line
    mgr.close()
