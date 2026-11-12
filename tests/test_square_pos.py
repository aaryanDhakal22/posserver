"""Send to phone (agent contract §2.10z, main contract §2.5zf), against dev main's fake Square: the Pi's RPCs
square.pos_config / square.pos_lookup / square.pos_fake_charge with the store's key.

  * square.pos_config: the app id, the environment, the callback URL, linked + the Square location
  * square.pos_fake_charge: a transaction as if a phone charged it in the Square app (fake only)
  * square.pos_lookup: by the transaction id (the order → its card tender), by the charge's code in the note,
    by the total when it is the only one; not found / not linked are answers; a bad body is a 400
"""

from __future__ import annotations

import uuid
from datetime import datetime, timedelta, timezone

import requests

from conftest import STORE_V1, store_key
from test_square import _connect, mgr  # noqa: F401 (the fixtures come with the module)
from test_square import _fake_square_on  # noqa: F401


def _rpc(store, typ: str, payload: dict) -> tuple[int, dict]:
    env = {"type": typ, "id": str(uuid.uuid4()), "payload": payload}
    r = requests.post(f"{STORE_V1}/agent/rpc", headers=store_key(store.key), json=env, timeout=30)
    body = r.json() if r.content else {}
    return r.status_code, (body.get("payload") if r.status_code == 200 else body)


def test_not_linked_is_an_answer(store) -> None:
    code, cfg = _rpc(store, "square.pos_config", {})
    assert code == 200 and cfg["linked"] is False and cfg["environment"] == "fake" and cfg["app_id"], cfg
    code, lk = _rpc(store, "square.pos_lookup", {"transaction_id": "ord_x", "total_cents": 100})
    assert code == 200 and lk["found"] is False and lk["code"] == "square_not_linked", lk
    code, fc = _rpc(store, "square.pos_fake_charge", {"total_cents": 100, "note": "x"})
    assert code == 200 and fc["ok"] is False and fc["code"] == "square_not_linked", fc


def test_fake_charge_then_lookups(store, mgr) -> None:  # noqa: F811
    st = _connect(mgr)
    code, cfg = _rpc(store, "square.pos_config", {})
    assert code == 200 and cfg["linked"] is True and cfg["location_id"] == st["square_location_id"], cfg
    assert "callback_url" in cfg

    since = (datetime.now(timezone.utc) - timedelta(minutes=1)).isoformat()
    tag = f"QP-{uuid.uuid4().hex[:6].upper()}"
    code, fc = _rpc(store, "square.pos_fake_charge", {"total_cents": 1677, "note": f"Order #12 {tag}"})
    assert code == 200 and fc["ok"] is True and fc["transaction_id"] and fc["client_transaction_id"], fc

    # By the transaction id (= Square's order id): the card tender.
    code, lk = _rpc(store, "square.pos_lookup", {"transaction_id": fc["transaction_id"], "total_cents": 1677, "since": since})
    assert code == 200 and lk["found"] is True, lk
    p = lk["payment"]
    assert p["transaction_id"] == fc["transaction_id"] and p["id"] and p["brand"] == "VISA" and p["last4"] == "1111", p
    assert p["entry"] == "CONTACTLESS" and p["amount_cents"] == 1677 and p["tip_cents"] == 0, p

    # By the code in the note (the callback was lost).
    code, by_code = _rpc(store, "square.pos_lookup", {"code": tag, "total_cents": 1677, "since": since})
    assert code == 200 and by_code["found"] is True and by_code["payment"]["id"] == p["id"], by_code
    assert by_code["payment"]["transaction_id"] == fc["transaction_id"], by_code

    # By the total alone when only one payment has it; two = ambiguous (record it by hand).
    odd = 1000 + uuid.uuid4().int % 8000
    _rpc(store, "square.pos_fake_charge", {"total_cents": odd, "note": "no code"})
    code, by_total = _rpc(store, "square.pos_lookup", {"code": "QP-NOPE00", "total_cents": odd, "since": since})
    assert code == 200 and by_total["found"] is True and by_total["payment"]["amount_cents"] == odd, by_total
    _rpc(store, "square.pos_fake_charge", {"total_cents": odd, "note": "no code either"})
    code, two = _rpc(store, "square.pos_lookup", {"code": "QP-NOPE00", "total_cents": odd, "since": since})
    assert code == 200 and two["found"] is False and two["code"] == "ambiguous", two

    # Unknown = not found (an answer, not an error).
    code, none = _rpc(store, "square.pos_lookup", {"transaction_id": "ord_pos_nope", "total_cents": 1677})
    assert code == 200 and none["found"] is False and none["code"] == "not_found", none


def test_bad_bodies_are_400(store) -> None:
    for typ, body in (("square.pos_lookup", {"total_cents": 100}), ("square.pos_fake_charge", {"total_cents": 0})):
        code, _ = _rpc(store, typ, body)
        assert code == 400, (typ, code)
