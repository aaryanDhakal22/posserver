"""Menu CSV import/export (`/store/v1/me/menu/import`, `/export.csv`).

Self-provisioning: a throwaway tenant with a manager session and two printers.
"""

import pytest

from conftest import STORE_V1

MENU = """menu,category,item,price,printer,sizes
Main,Pizza,Cheese Pizza,10.00,Pizza,Large
Main,Pizza,Pepperoni Pizza,$12.00,Pizza,
Main,Pizza,Garlic Knots,4.00,Sub,
Main,Subs,Philly Cheese Steak,9.50,,
"""


@pytest.fixture
def mgr(store):
    s = store.manager_session()
    for name, ip in (("Pizza", "10.0.0.1"), ("Sub", "10.0.0.2")):
        r = s.post(f"{STORE_V1}/me/printers", json={"name": name, "ip": ip}, timeout=5)
        assert r.status_code == 201, r.text
    yield s
    s.close()


def _import(mgr, body: str, *, dry: bool = False, replace: bool = False):
    return mgr.post(f"{STORE_V1}/me/menu/import",
                    params={"dry_run": int(dry), "replace": int(replace)},
                    data=body.encode(), headers={"Content-Type": "text/csv"}, timeout=10)


def test_dry_run_writes_nothing(mgr) -> None:
    r = _import(mgr, MENU, dry=True)
    assert r.status_code == 200, r.text
    res = r.json()
    assert (res["dry_run"], res["menus"], res["categories"], res["items"], res["item_printers"]) == (True, 1, 2, 4, 1)
    assert mgr.get(f"{STORE_V1}/me/menu/menus", timeout=5).json() == []


def test_import_export_round_trip(mgr) -> None:
    r = _import(mgr, MENU)
    assert r.status_code == 200, r.text
    assert r.json()["items"] == 4

    # Idempotent: the same file again changes nothing.
    again = _import(mgr, MENU).json()
    assert again["changes"] == [], again

    exp = mgr.get(f"{STORE_V1}/me/menu/export.csv", timeout=5)
    assert exp.status_code == 200
    assert exp.headers["Content-Type"].startswith("text/csv")
    lines = exp.text.splitlines()
    assert lines[0] == "menu,category,item,price,printer,description"
    assert "Main,Pizza,Garlic Knots,4.00,Sub," in lines
    assert "Main,Subs,Philly Cheese Steak,9.50,," in lines

    # Export → import is a no-op too.
    assert _import(mgr, exp.text).json()["changes"] == []

    # The import routes orders: Pizza category default, Garlic Knots on Sub.
    menus = mgr.get(f"{STORE_V1}/me/menu/menus", timeout=5).json()
    cats = mgr.get(f"{STORE_V1}/me/menu/menus/{menus[0]['id']}/categories", timeout=5).json()
    printers = {p["id"]: p["name"] for p in mgr.get(f"{STORE_V1}/me/printers", timeout=5).json()}
    pizza = next(c for c in cats if c["name"] == "Pizza")
    assert printers[pizza["default_printer_id"]] == "Pizza"
    items = {i["name"]: i for i in mgr.get(f"{STORE_V1}/me/menu/categories/{pizza['id']}/items", timeout=5).json()}
    assert items["Cheese Pizza"]["printer_id"] is None
    assert printers[items["Garlic Knots"]["printer_id"]] == "Sub"
    assert items["Pepperoni Pizza"]["price_cents"] == 1200


def test_unknown_printer_is_rejected(mgr) -> None:
    r = _import(mgr, "category,item,printer\nGrill,Burger,Grill\n")
    assert r.status_code == 400
    assert "Grill" in r.json()["error"]
    assert mgr.get(f"{STORE_V1}/me/menu/menus", timeout=5).json() == []


def test_bad_rows_are_rejected(mgr) -> None:
    r = _import(mgr, "category,item,price\nPizza,Cheese,abc\n")
    assert r.status_code == 400
    assert "line 2" in r.json()["error"]
    assert _import(mgr, "item,price\nCheese,1\n").status_code == 400


def test_replace_rebuilds_the_menu(mgr) -> None:
    assert _import(mgr, MENU).status_code == 200
    res = _import(mgr, "category,item,price\nSides,Fries,3.00\n", replace=True).json()
    assert res["menus_deleted"] == 1 and res["items"] == 1
    menus = mgr.get(f"{STORE_V1}/me/menu/menus", timeout=5).json()
    assert [m["name"] for m in menus] == ["Main"]
    cats = mgr.get(f"{STORE_V1}/me/menu/menus/{menus[0]['id']}/categories", timeout=5).json()
    assert [c["name"] for c in cats] == ["Sides"]


def test_requires_a_session() -> None:
    import requests
    assert requests.post(f"{STORE_V1}/me/menu/import", data=b"category,item\nA,B\n", timeout=5).status_code == 401
    assert requests.get(f"{STORE_V1}/me/menu/export.csv", timeout=5).status_code == 401
