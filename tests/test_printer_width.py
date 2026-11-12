"""A printer's print width (printers.print_columns, 2026-11-12): how many font-A
characters fit on its line — 48 (80 mm, 576 dots, the default), 42 (a 72 mm
print area, 512 dots: Lombardi's Online printer) or 32 (58 mm, 384 dots). The
Pi lays every slip out for the printer it lands on.

  * a new printer is 48; POST may give it; PATCH sets it (alone or with name /
    ip), GET carries it; anything else is a 400
  * the agent's store_config (SSE frame + GET /store/v1/store-config) carries
    it only when it isn't 48: an absent print_columns is 48, so the frame stays
    byte-identical for older agents (golden)
  * the Pi's width test saves it over the duplex channel: RPC printer.width
    {printer_id, print_columns} → {ok, id, name, print_columns}; only a printer
    of the Pi's own location (not_found), bad widths / ids are bad_request
"""

import json
import uuid

import requests

from conftest import STORE_V1, store_key
from sse import SSEStream


def _rpc(api_key: str, msg_type: str, payload: dict) -> requests.Response:
    env = {"type": msg_type, "id": str(uuid.uuid4()), "payload": payload}
    return requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(api_key), json=env, timeout=5)


def _add(mgr, name: str, ip: str, **extra) -> dict:
    r = mgr.post(f"{STORE_V1}/me/printers", json={"name": name, "ip": ip, **extra}, timeout=5)
    assert r.status_code == 201, r.text
    return r.json()


def _mine(mgr) -> dict[str, dict]:
    r = mgr.get(f"{STORE_V1}/me/printers", timeout=5)
    assert r.status_code == 200, r.text
    return {p["id"]: p for p in r.json()}


def test_print_columns_default_patch_and_store_config(store) -> None:
    mgr = store.manager_session()
    online = _add(mgr, "Online", "10.0.0.106")
    assert online["print_columns"] == 48
    narrow = _add(mgr, "Bar", "10.0.0.107", print_columns=32)
    assert narrow["print_columns"] == 32

    with SSEStream(store.key) as s:
        first = s.next()
        assert first["event"] == "store_config"
        widths = {p["name"]: p.get("print_columns") for p in json.loads(first["data"])["printers"]}
        assert widths == {"Online": None, "Bar": 32}  # the default 48 is left out

        # Only the width: name and ip stay as they are.
        r = mgr.patch(f"{STORE_V1}/me/printers/{online['id']}", json={"print_columns": 42}, timeout=5)
        assert r.status_code == 200, r.text
        assert (r.json()["print_columns"], r.json()["name"], r.json()["ip"]) == (42, "Online", "10.0.0.106")
        pushed = s.wait_event("store_config",
                              lambda d: any(p["id"] == online["id"] and p["print_columns"] == 42 for p in d["printers"]),
                              timeout=10)
        assert pushed, "the new width was not pushed to the agent"

    assert _mine(mgr)[online["id"]]["print_columns"] == 42
    r = requests.get(f"{STORE_V1}/store-config", headers=store_key(store.key), timeout=5)
    assert r.status_code == 200, r.text
    assert {p["name"]: p.get("print_columns") for p in r.json()["printers"]} == {"Online": 42, "Bar": 32}

    # With a rename in the same PATCH; a rename alone keeps the width.
    r = mgr.patch(f"{STORE_V1}/me/printers/{online['id']}", json={"name": "Online 2", "ip": "10.0.0.106", "print_columns": 48},
                  timeout=5)
    assert r.status_code == 200 and r.json()["print_columns"] == 48, r.text
    r = mgr.patch(f"{STORE_V1}/me/printers/{online['id']}", json={"name": "Online", "ip": "10.0.0.106"}, timeout=5)
    assert r.status_code == 200 and r.json()["print_columns"] == 48, r.text

    for bad in (40, 0, 64, "42"):
        r = mgr.patch(f"{STORE_V1}/me/printers/{online['id']}", json={"print_columns": bad}, timeout=5)
        assert r.status_code == 400, (bad, r.text)
    r = mgr.post(f"{STORE_V1}/me/printers", json={"name": "Grill", "ip": "10.0.0.108", "print_columns": 40}, timeout=5)
    assert r.status_code == 400, r.text
    assert set(p["name"] for p in _mine(mgr).values()) == {"Online", "Bar"}
    assert _mine(mgr)[online["id"]]["print_columns"] == 48
    r = requests.get(f"{STORE_V1}/store-config", headers=store_key(store.key), timeout=5)
    assert "print_columns" not in {p["name"]: p for p in r.json()["printers"]}["Online"]


def test_printer_width_rpc_from_the_pi(store) -> None:
    mgr = store.manager_session()
    p = _add(mgr, "Online", "10.0.0.106")

    with SSEStream(store.key) as s:
        assert s.next()["event"] == "store_config"
        r = _rpc(store.key, "printer.width", {"printer_id": p["id"], "print_columns": 42})
        assert r.status_code == 200, r.text
        assert r.json()["type"] == "printer.width.reply"
        assert r.json()["payload"] == {"ok": True, "id": p["id"], "name": "Online", "print_columns": 42}
        assert s.wait_event("store_config",
                            lambda d: any(x["id"] == p["id"] and x["print_columns"] == 42 for x in d["printers"]),
                            timeout=10), "the width from the Pi was not pushed back to it"
    assert _mine(mgr)[p["id"]]["print_columns"] == 42

    for payload in ({"printer_id": p["id"], "print_columns": 40},
                    {"printer_id": p["id"]},
                    {"printer_id": "not-a-uuid", "print_columns": 42}):
        r = _rpc(store.key, "printer.width", payload)
        assert r.status_code == 200, r.text
        assert r.json()["payload"] == {"ok": False, "code": "bad_request"}, payload
    r = _rpc(store.key, "printer.width", {"printer_id": str(uuid.uuid4()), "print_columns": 32})
    assert r.json()["payload"] == {"ok": False, "code": "not_found"}
    assert _mine(mgr)[p["id"]]["print_columns"] == 42


def test_printer_width_rpc_is_scoped(make_store) -> None:
    a, b = make_store(), make_store()
    p = _add(a.manager_session(), "Online", "10.0.0.106")
    r = _rpc(b.key, "printer.width", {"printer_id": p["id"], "print_columns": 32})
    assert r.status_code == 200, r.text
    assert r.json()["payload"] == {"ok": False, "code": "not_found"}
    assert _mine(a.manager_session())[p["id"]]["print_columns"] == 48
