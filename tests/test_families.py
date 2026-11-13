"""Families + coupons (the owner's Centurion pass, 2026-11-12; migration 00045;
contract/main.md "Families + coupons").

  * the bank document carries families, each item's / option's family and
    coupons whose parts name families + sizes; export → apply is a no-op; a
    document without `families` never prunes them
  * Store › Menu › Families: list (members + counts), create, rename, class,
    name_taken, bulk assign (kinds, another store's ids), delete refused while
    a coupon names it, merge (members + coupon parts move)
  * coupons as deals: shape checks, the store's item families only, the
    counter by default
  * the counter's pricing (counter-menu + /me/pricing/context) carries item
    families, option classes, the families list and the coupon; a coupon edit
    bumps the menu version (the Pi pulls again)
  * the counter channel's quote offers the coupon and applies it (the
    priciest item carries the price, the cheapest regular topping free)
  * the menu CSV's family column
"""

from __future__ import annotations

import copy

import pytest
import requests

from conftest import ADMIN_V1, MAIN_BASE_URL, STORE_V1, store_key
from openapi import check

ME = f"{STORE_V1}/me"
MENU = f"{ME}/menu"
FAMS = f"{MENU}/families"
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


COMBO = {"kind": "coupon", "name": "8\" Sub Combo", "description": "", "card": ["8\" hot sub", "French fries", "Can soda"],
         "price_cents": 1099, "active": True, "fulfil": "", "schedule": None,
         "parts": [{"label": "8\" hot sub", "families": ["Hot Subs"], "sizes": ["8\""], "qty": 1, "regular_mods_included": 0},
                   {"label": "Fries", "families": ["Fries"], "qty": 1, "regular_mods_included": 0},
                   {"label": "Soda", "families": ["Can Soda"], "qty": 1, "regular_mods_included": 0}]}
PIZZA_DEAL = {"kind": "coupon", "name": "14\" Pizza, 1 Topping", "description": "", "card": ["14\" pizza, 1 topping"],
              "price_cents": 1200, "active": True, "fulfil": "", "schedule": None,
              "parts": [{"label": "14\" pizza", "families": ["Pizza"], "sizes": ["14\""], "qty": 1, "regular_mods_included": 1}]}


def _doc() -> dict:
    return {
        "format": 1,
        "sizes": [{"name": "8\"", "short": "8"}, {"name": "12\"", "short": "12"}, {"name": "14\"", "short": "14"}],
        "families": [{"name": "Hot Subs", "kind": "item"}, {"name": "Fries", "kind": "item"}, {"name": "Can Soda", "kind": "item"},
                     {"name": "Pizza", "kind": "item"},
                     {"name": "Pizza Topping", "kind": "mod", "class": "regular"},
                     {"name": "Premium Pizza Topping", "kind": "mod", "class": "premium"},
                     {"name": "Pizza Choice", "kind": "mod", "class": "unpriced"}],
        "groups": [{"name": "Toppings", "max_select": 0, "allow_multiple": True, "portions": True, "options": [
            {"name": "Pepperoni", "price_cents": 200, "family": "Pizza Topping"},
            {"name": "Olives", "price_cents": 150, "family": "Pizza Topping"},
            {"name": "Steak", "price_cents": 350, "family": "Premium Pizza Topping"},
            {"name": "Well Done", "price_cents": 0, "family": "Pizza Choice"},
            {"name": "Extra Sauce", "price_cents": 50}]}],
        "menus": [{"name": "Main", "categories": [
            {"name": "Subs", "items": [
                {"name": "Cheesesteak", "price_cents": 899, "family": "Hot Subs",
                 "sizes": [{"size": "8\"", "price_cents": 899}, {"size": "12\"", "price_cents": 1299}]}]},
            {"name": "Sides", "items": [{"name": "French Fries", "price_cents": 399, "family": "Fries"}]},
            {"name": "Drinks", "items": [{"name": "Coke Can", "price_cents": 150, "family": "Can Soda"},
                                         {"name": "Water", "price_cents": 100}]},
            {"name": "Pizza", "items": [
                {"name": "Cheese Pizza", "price_cents": 1299, "family": "Pizza", "groups": ["Toppings"],
                 "sizes": [{"size": "14\"", "price_cents": 1299}]}]},
        ]}],
        "views": [],
        "deals": [copy.deepcopy(COMBO), copy.deepcopy(PIZZA_DEAL)],
    }


def _apply(mgr, doc, **params) -> dict:
    return _ok(mgr.post(APPLY, params=params, json=doc, timeout=30))


def _listing(mgr) -> dict:
    return _ok(mgr.get(FAMS, timeout=10))


def _by_name(rows, name, kind=None) -> dict:
    for r in rows:
        if r["name"] == name and (kind is None or r.get("kind") == kind):
            return r
    raise AssertionError(f"{name} not in {[r['name'] for r in rows]}")


def test_bank_document_families_and_coupons(mgr) -> None:
    dry = _apply(mgr, _doc(), dry_run=1)
    assert dry["counts"]["families"]["created"] == 7 and dry["counts"]["family_links"]["updated"] == 8, dry["counts"]
    assert _listing(mgr)["families"] == []  # a dry run writes nothing

    res = _apply(mgr, _doc())
    assert res["counts"]["families"]["created"] == 7 and res["counts"]["deals"]["created"] == 2
    got = _listing(mgr)
    fam = {f["name"]: f for f in got["families"]}
    assert fam["Pizza Topping"]["class"] == "regular" and fam["Premium Pizza Topping"]["class"] == "premium"
    assert fam["Hot Subs"]["class"] == "" and fam["Hot Subs"]["items"] == 1 and fam["Pizza Topping"]["options"] == 2
    assert _by_name(got["items"], "Water")["family_id"] is None
    assert _by_name(got["options"], "Extra Sauce")["family_id"] is None
    assert _by_name(got["options"], "Steak")["group"] == "Toppings"

    # The coupons store family / size ids; the document names them again.
    deals = _ok(mgr.get(f"{ME}/deals", timeout=10))
    combo = _by_name(deals, COMBO["name"])
    assert combo["channel_kinds"] == ["counter"] and combo["parts"][0]["families"] == [fam["Hot Subs"]["id"]]
    doc = _ok(mgr.get(EXPORT, timeout=10))
    assert {(f["name"], f["kind"], f.get("class", "")) for f in doc["families"]} >= {("Pizza Topping", "mod", "regular"), ("Fries", "item", "")}
    items = {i["name"]: i for m in doc["menus"] for c in m["categories"] for i in c["items"]}
    assert items["Cheesesteak"]["family"] == "Hot Subs" and "family" not in items["Water"]
    ex = _by_name(doc["deals"], COMBO["name"])
    assert ex["parts"][0]["families"] == ["Hot Subs"] and ex["parts"][0]["sizes"] == ["8\""]

    # Applying the same document / its export again changes nothing.
    for d in (_doc(), doc):
        again = _apply(mgr, d)
        assert again["changes"] == [], again["changes"]

    # A document without `families` never prunes them; one that lists them does.
    no_fams = _doc()
    del no_fams["families"]
    for g in no_fams["groups"]:
        for o in g["options"]:
            o.pop("family", None)
    for c in no_fams["menus"][0]["categories"]:
        for i in c["items"]:
            i.pop("family", None)
    _apply(mgr, no_fams, prune=1)
    assert len(_listing(mgr)["families"]) == 7
    less = _doc()
    less["families"] = [f for f in less["families"] if f["name"] != "Pizza Choice"]
    less["groups"][0]["options"][3].pop("family")
    res = _apply(mgr, less, prune=1)
    assert res["counts"]["families"]["deleted"] == 1
    assert _by_name(_listing(mgr)["options"], "Well Done")["family_id"] is None

    # Bad names fail with their path, nothing written.
    bad = _doc()
    bad["menus"][0]["categories"][0]["items"][0]["family"] = "Pizza Topping"  # a mod family on an item
    r = mgr.post(APPLY, json=bad, timeout=30)
    assert r.status_code == 400 and "items[0].family" in r.text and "unknown item family" in r.text, r.text
    bad = _doc()
    bad["deals"][0]["parts"][0]["families"] = ["Nope"]
    r = mgr.post(APPLY, json=bad, timeout=30)
    assert r.status_code == 400 and "deals[0].parts[0].families[0]" in r.text, r.text
    bad = _doc()
    bad["families"].append({"name": "hot  subs", "kind": "item"})
    assert mgr.post(APPLY, json=bad, timeout=30).status_code == 400
    bad = _doc()
    bad["families"][4]["class"] = "gold"
    assert mgr.post(APPLY, json=bad, timeout=30).status_code == 400


def test_families_editor(mgr, make_store) -> None:
    _apply(mgr, _doc())
    got = _listing(mgr)
    fam = {f["name"]: f for f in got["families"]}

    # Create, name_taken (case + spaces), class, rename.
    bottle = _ok(mgr.post(FAMS, json={"kind": "item", "name": "  Bottle   Soda "}, timeout=5), 201)
    assert bottle["name"] == "Bottle Soda" and bottle["class"] == ""
    r = mgr.post(FAMS, json={"kind": "item", "name": "bottle soda"}, timeout=5)
    assert r.status_code == 409 and r.json()["code"] == "name_taken"
    assert mgr.post(FAMS, json={"kind": "mod", "name": "Bottle Soda"}, timeout=5).status_code == 201  # another kind
    assert mgr.post(FAMS, json={"kind": "item", "name": "X", "class": "premium"}, timeout=5).status_code == 400
    assert mgr.post(FAMS, json={"kind": "dish", "name": "X"}, timeout=5).status_code == 400
    assert mgr.post(FAMS, json={"kind": "item", "name": "x" * 61}, timeout=5).status_code == 400
    sauce = _ok(mgr.post(FAMS, json={"kind": "mod", "name": "Sauces"}, timeout=5), 201)
    assert sauce["class"] == "regular"
    sauce = _ok(mgr.patch(f"{FAMS}/{sauce['id']}", json={"class": "premium", "name": "Sauce Add-on"}, timeout=5))
    assert (sauce["name"], sauce["class"]) == ("Sauce Add-on", "premium")
    assert mgr.patch(f"{FAMS}/{sauce['id']}", json={"class": "free"}, timeout=5).status_code == 400

    # Bulk assign: kinds must match, every id the store's.
    water = _by_name(got["items"], "Water")["id"]
    extra = _by_name(got["options"], "Extra Sauce")["id"]
    assert _ok(mgr.post(f"{FAMS}/assign", json={"family_id": bottle["id"], "items": [water]}, timeout=5)) == {"items": 1, "options": 0}
    assert _ok(mgr.post(f"{FAMS}/assign", json={"family_id": bottle["id"], "items": [water]}, timeout=5))["items"] == 0
    r = mgr.post(f"{FAMS}/assign", json={"family_id": bottle["id"], "options": [extra]}, timeout=5)
    assert r.status_code == 400 and r.json()["code"] == "wrong_kind"
    assert _ok(mgr.post(f"{FAMS}/assign", json={"family_id": sauce["id"], "options": [extra]}, timeout=5))["options"] == 1
    other = make_store()
    with other.manager_session() as om:
        foreign = _ok(om.post(FAMS, json={"kind": "item", "name": "Theirs"}, timeout=5), 201)
        assert om.post(f"{FAMS}/assign", json={"family_id": foreign["id"], "items": [water]}, timeout=5).status_code == 400
        assert om.patch(f"{FAMS}/{bottle['id']}", json={"name": "Mine"}, timeout=5).status_code == 404
    r = mgr.post(f"{FAMS}/assign", json={"family_id": foreign["id"], "items": [water]}, timeout=5)
    assert r.status_code == 404
    assert _ok(mgr.post(f"{FAMS}/assign", json={"family_id": None, "items": [water]}, timeout=5))["items"] == 1

    # A family a coupon names can't be deleted; merging moves members + coupon parts.
    r = mgr.delete(f"{FAMS}/{fam['Can Soda']['id']}", timeout=5)
    assert r.status_code == 409 and r.json()["code"] == "family_in_coupon" and r.json()["coupons"] == [COMBO["name"]]
    _ok(mgr.post(f"{FAMS}/assign", json={"family_id": bottle["id"], "items": [water]}, timeout=5))
    assert mgr.post(f"{FAMS}/{bottle['id']}/merge", json={"into": sauce["id"]}, timeout=5).status_code == 400  # another kind
    m = _ok(mgr.post(f"{FAMS}/{fam['Can Soda']['id']}/merge", json={"into": bottle["id"]}, timeout=5))
    assert (m["moved_items"], m["moved_options"], m["coupons_changed"]) == (1, 0, 1)
    combo = _by_name(_ok(mgr.get(f"{ME}/deals", timeout=5)), COMBO["name"])
    assert combo["parts"][2]["families"] == [bottle["id"]]
    after = _listing(mgr)
    assert _by_name(after["families"], "Bottle Soda", "item")["items"] == 2
    assert all(f["name"] != "Can Soda" for f in after["families"])
    # A family no coupon names deletes; its members go back to no family.
    _ok(mgr.delete(f"{FAMS}/{sauce['id']}", timeout=5), 204)
    assert _by_name(_listing(mgr)["options"], "Extra Sauce")["family_id"] is None
    assert mgr.delete(f"{FAMS}/{sauce['id']}", timeout=5).status_code == 404


def test_bank_document_upcharge_over(mgr) -> None:
    """A part's upcharge_over (its base families) is named in the document and stored as family ids."""
    doc = _doc()
    combo = _by_name(doc["deals"], COMBO["name"])
    combo["parts"][0] = {**combo["parts"][0], "families": ["Hot Subs", "Pizza"], "upcharge_over": ["Hot Subs"]}
    _apply(mgr, doc)
    fam = {f["name"]: f for f in _listing(mgr)["families"]}
    stored = _by_name(_ok(mgr.get(f"{ME}/deals", timeout=10)), COMBO["name"])
    assert stored["parts"][0]["upcharge_over"] == [fam["Hot Subs"]["id"]]
    ex = _ok(mgr.get(EXPORT, timeout=10))
    assert _by_name(ex["deals"], COMBO["name"])["parts"][0]["upcharge_over"] == ["Hot Subs"]
    assert _apply(mgr, ex)["changes"] == []
    combo["parts"][0]["upcharge_over"] = ["No Such Family"]
    r = mgr.post(APPLY, json=doc, timeout=30)
    assert r.status_code in (400, 422) and "upcharge_over" in r.text, r.text


def test_coupon_deals_api(mgr, make_store) -> None:
    _apply(mgr, _doc())
    fam = {f["name"]: f for f in _listing(mgr)["families"]}
    base = {"kind": "coupon", "name": "Fries + Can", "active": False, "fulfil": "", "schedule": None, "price_cents": 450,
            "card": ["Fries", "A can"], "parts": [{"label": "Fries", "families": [fam["Fries"]["id"]], "qty": 1, "regular_mods_included": 0},
                                                  {"label": "Can", "families": [fam["Can Soda"]["id"]], "qty": 1, "regular_mods_included": 0}]}
    d = _ok(mgr.post(f"{ME}/deals", json=base, timeout=5), 201)
    assert d["channel_kinds"] == ["counter"] and d["card"] == ["Fries", "A can"]
    for patch in ({"parts": []}, {"price_cents": -1}, {"card": ["x" * 61]}, {"card": ["a"] * 7},
                  {"parts": [{**base["parts"][0], "qty": 0}]}, {"parts": [{**base["parts"][0], "qty": 11}]},
                  {"parts": [{**base["parts"][0], "families": []}]},
                  {"parts": [{**base["parts"][0], "families": [fam["Pizza Topping"]["id"]]}]},  # a mod family
                  {"parts": [{**base["parts"][0], "sizes": ["00000000-0000-0000-0000-000000000001"]}]},
                  {"parts": [{**base["parts"][0], "upcharge_over": [fam["Can Soda"]["id"]]}]}):  # not one of the part's families
        r = mgr.post(f"{ME}/deals", json={**base, "name": "Bad", **patch}, timeout=5)
        assert r.status_code == 400, (patch, r.text)
    other = make_store()
    with other.manager_session() as om:
        r = om.post(f"{ME}/deals", json={**base, "name": "Theirs"}, timeout=5)
        assert r.status_code == 400  # another store's families
    # upcharge_over: some of the part's families (all of them = none).
    part = {**base["parts"][0], "families": [fam["Fries"]["id"], fam["Can Soda"]["id"]], "upcharge_over": [fam["Fries"]["id"]]}
    up = _ok(mgr.post(f"{ME}/deals", json={**base, "name": "Upcharge", "parts": [part]}, timeout=5), 201)
    assert up["parts"][0]["upcharge_over"] == [fam["Fries"]["id"]]
    every = _ok(mgr.post(f"{ME}/deals", json={**base, "name": "Every", "parts": [{**part, "upcharge_over": part["families"]}]}, timeout=5), 201)
    assert "upcharge_over" not in every["parts"][0]
    for x in (d, up, every):
        _ok(mgr.delete(f"{ME}/deals/{x['id']}", timeout=5), 204)


def test_counter_pricing_and_quote(admin, store, mgr) -> None:
    _apply(mgr, _doc())
    got = _listing(mgr)
    fam = {f["name"]: f for f in got["families"]}
    body = _ok(requests.get(COUNTER_MENU, headers=store_key(store.key), timeout=10))
    pricing = body["pricing"]
    items = {i["name"]: i for c in body["build"]["categories"] for i in c["items"]}
    opts = {o["name"]: o for g in body["build"]["modifier_groups"] for o in g["options"]}
    assert pricing["items"][items["Cheesesteak"]["id"]]["family"] == fam["Hot Subs"]["id"]
    assert "family" not in pricing["items"][items["Water"]["id"]]
    assert pricing["options"][opts["Pepperoni"]["id"]] == {"family": fam["Pizza Topping"]["id"], "class": "regular"}
    assert pricing["options"][opts["Steak"]["id"]]["class"] == "premium"
    assert opts["Extra Sauce"]["id"] not in pricing["options"]
    assert {d["name"] for d in pricing["deals"] if d["kind"] == "coupon"} == {COMBO["name"], PIZZA_DEAL["name"]}
    assert {(f["name"], f["kind"], f["class"]) for f in body["families"]} >= {("Pizza Topping", "mod", "regular"), ("Pizza", "item", "")}
    ctx = _ok(mgr.get(f"{ME}/pricing/context", params={"kind": "counter"}, timeout=10))
    assert ctx["options"][opts["Olives"]["id"]]["class"] == "regular"

    # A coupon edit bumps the menu version: the Pi's ETag no longer matches.
    etag = requests.get(COUNTER_MENU, headers=store_key(store.key), timeout=10).headers["ETag"]
    combo = _by_name(_ok(mgr.get(f"{ME}/deals", timeout=5)), COMBO["name"])
    _ok(mgr.put(f"{ME}/deals/{combo['id']}", json={**combo, "price_cents": 1049}, timeout=5))
    r = requests.get(COUNTER_MENU, headers={**store_key(store.key), "If-None-Match": etag}, timeout=10)
    assert r.status_code == 200 and r.headers["ETag"] != etag

    # The counter channel: the cart fits the combo → offered; sent back → applied.
    key = _ok(admin.post(f"{ADMIN_V1}/channels", json={"location_id": store.location_id, "kind": "counter", "name": "Counter"}, timeout=5), 201)["key"]
    hdr = {"Authorization": f"Bearer {key}"}
    sizes = {z["name"]: z["id"] for z in body["build"]["sizes"]}
    tops = next(g for g in body["build"]["modifier_groups"] if g["name"] == "Toppings")["id"]
    lines = [{"item_id": items["Cheesesteak"]["id"], "size_id": sizes["8\""], "qty": 1},
             {"item_id": items["French Fries"]["id"], "qty": 1},
             {"item_id": items["Coke Can"]["id"], "qty": 2},
             {"item_id": items["Cheese Pizza"]["id"], "size_id": sizes["14\""], "qty": 1, "options": [
                 {"group_id": tops, "option_id": opts["Pepperoni"]["id"]}, {"group_id": tops, "option_id": opts["Olives"]["id"]},
                 {"group_id": tops, "option_id": opts["Steak"]["id"]}]}]
    q = check(requests.post(f"{CH_V1}/quote", json={"type": "pickup", "lines": lines}, headers=hdr, timeout=10), "quote")
    offers = {o["name"]: o for o in q["coupon_offers"]}
    off = {n: o["regular_cents"] - o["total_cents"] for n, o in offers.items()}
    assert offers[COMBO["name"]]["lines"] == [0, 1, 2] and off[COMBO["name"]] == 899 + 399 + 150 - 1049
    assert all("saved_cents" not in o for o in q["coupon_offers"])
    # Pizza 1299 + pep 200 + olives 150 + steak 350 = 1999; in the coupon 1200 + 200 + 350 (olives, the cheaper regular one, free).
    assert off[PIZZA_DEAL["name"]] == 1999 - 1750
    assert [o["name"] for o in q["coupon_offers"]] == [PIZZA_DEAL["name"], COMBO["name"]] or \
        off[q["coupon_offers"][0]["name"]] >= off[q["coupon_offers"][1]["name"]]
    cart = {"type": "pickup", "lines": lines, "coupons": [{"id": o["id"], "lines": o["lines"]} for o in q["coupon_offers"]]}
    q = check(requests.post(f"{CH_V1}/quote", json=cart, headers=hdr, timeout=10), "quote")
    assert q["errors"] == [] and "coupon_offers" not in q
    applied = {c["name"]: c for c in q["coupons"]}
    pz = applied[PIZZA_DEAL["name"]]["units"][0]
    assert pz["carrier"] and pz["item_cents"] == 1200 and pz["free_mods"] == ["Olives"] and pz["mods_cents"] == 550
    # A coupon re-prices its lines (the owner, 2026-11-12): the lines as sold, the subtotal their sum, no discount.
    assert [x["total_cents"] for x in q["lines"]] == [1049, 0, 150, 1750]
    assert q["subtotal_cents"] == 1049 + 150 + 1750 and q["discounts"] == [] and q["saved_cents"] == 0
    assert all("coupon_cents" not in x for x in q["lines"]) and all("saved_cents" not in c for c in q["coupons"])

    # The web doesn't get the counter's coupons: asked for there, it has ended.
    web = _ok(admin.post(f"{ADMIN_V1}/channels", json={"location_id": store.location_id, "kind": "web", "name": "Web"}, timeout=5), 201)["key"]
    q = check(requests.post(f"{CH_V1}/quote", json=cart, headers={"Authorization": f"Bearer {web}"}, timeout=10), "quote")
    assert [e["code"] for e in q["errors"]] == ["coupon_ended"] and "coupons" not in q


def test_csv_family_column(mgr) -> None:
    _apply(mgr, _doc())
    csv = "category,item,price,family\nDrinks,Water,1.00,Bottled Water\nDrinks,Coke Can,1.50,\n"
    res = _ok(mgr.post(f"{MENU}/import", params={"dry_run": 1}, data=csv, headers={"Content-Type": "text/csv"}, timeout=10))
    assert res["families_created"] == 1 and res["item_families_changed"] == 1
    res = _ok(mgr.post(f"{MENU}/import", data=csv, headers={"Content-Type": "text/csv"}, timeout=10))
    assert res["families_created"] == 1
    got = _listing(mgr)
    water = _by_name(got["families"], "Bottled Water", "item")
    assert _by_name(got["items"], "Water")["family_id"] == water["id"]
    assert _by_name(got["items"], "Coke Can")["family_id"] == _by_name(got["families"], "Can Soda")["id"]  # blank keeps
    out = mgr.get(f"{MENU}/export.csv", timeout=10).text
    assert out.splitlines()[0].endswith(",family,included_toppings") and ",Water,1.00,,,no,Bottled Water," in out, out


def test_coupons_on_the_web_and_call_center(admin, store, mgr) -> None:
    """The owner (2026-11-12): coupons work everywhere. A coupon switched on for web + operator is
    offered and applied in those channels' quotes the same way as the counter (it re-prices its
    lines), and an order sent with `coupons` is priced with them; a coupon that doesn't fit as sent
    refuses the order (cart_invalid)."""
    import secrets

    doc = _doc()
    for d in doc["deals"]:
        d["channel_kinds"] = ["counter", "web", "operator"]
    _apply(mgr, doc)
    store.online()  # main refuses channel orders while the store's Pi has no live stream
    body = _ok(requests.get(COUNTER_MENU, headers=store_key(store.key), timeout=10))
    items = {i["name"]: i for c in body["build"]["categories"] for i in c["items"]}
    sizes = {z["name"]: z["id"] for z in body["build"]["sizes"]}
    lines = [{"item_id": items["Cheesesteak"]["id"], "size_id": sizes["8\""], "qty": 1},
             {"item_id": items["French Fries"]["id"], "qty": 1},
             {"item_id": items["Coke Can"]["id"], "qty": 1}]
    for kind in ("web", "operator"):
        key = _ok(admin.post(f"{ADMIN_V1}/channels", json={"location_id": store.location_id, "kind": kind, "name": kind}, timeout=5), 201)["key"]
        hdr = {"Authorization": f"Bearer {key}"}
        q = check(requests.post(f"{CH_V1}/quote", json={"type": "pickup", "lines": lines}, headers=hdr, timeout=10), "quote")
        [offer] = [o for o in q["coupon_offers"] if o["name"] == COMBO["name"]]
        coupons = [{"id": offer["id"], "lines": offer["lines"]}]
        q = check(requests.post(f"{CH_V1}/quote", json={"type": "pickup", "lines": lines, "coupons": coupons}, headers=hdr, timeout=10), "quote")
        assert q["errors"] == [] and [x["total_cents"] for x in q["lines"]] == [1099, 0, 0]
        assert q["subtotal_cents"] == 1099 and q["discounts"] == []
        if kind != "operator":  # a new web channel takes cards only; the order path is the same for both
            continue
        order = {"client_order_id": "cp-" + secrets.token_hex(6), "type": "pickup", "lines": lines, "coupons": coupons,
                 "payment": {"method": "cash"}, "customer": {"name": "Ada Lovelace", "phone": "(410) 555-0101"},
                 "expected_total_cents": q["total_cents"]}
        r = requests.post(f"{CH_V1}/orders", json=order, headers=hdr, timeout=15)
        o = check(r, "placeOrder")
        assert r.status_code == 201, r.text
        assert o["money"]["subtotal_cents"] == 1099 and o["money"]["total_cents"] == q["total_cents"]
        # A coupon that doesn't fit as sent (a line it can't take): the order is refused, nothing placed.
        bad = dict(order, client_order_id="cp-" + secrets.token_hex(6), coupons=[{"id": offer["id"], "lines": [0, 0, 1]}],
                   expected_total_cents=None)
        r = requests.post(f"{CH_V1}/orders", json=bad, headers=hdr, timeout=15)
        assert r.status_code == 422 and r.json()["code"] == "cart_invalid", r.text
