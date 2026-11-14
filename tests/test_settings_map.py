"""main's settings map (contract §2.5zm): GET /store/v1/settings-map is open and serves the embedded map
(the Go test checks it is the SPA's canonical file, byte for byte)."""

import requests

from conftest import MAIN_BASE_URL


def test_settings_map_is_open_and_complete():
    r = requests.get(f"{MAIN_BASE_URL}/store/v1/settings-map", timeout=10)
    assert r.status_code == 200
    assert r.headers["Content-Type"].startswith("application/json")
    assert "max-age" in r.headers.get("Cache-Control", "")
    doc = r.json()
    assert doc["version"] >= 1
    fields = {f["id"]: (p, f) for p in doc["pages"] for f in p["fields"]}
    p, f = fields["counter.counter_start"]
    assert (p["path"], f["anchor"], p["crumb"]) == ("/app/store/settings/counter", "counter_start", "Store › Settings › Counter orders")
    assert f["label"] == "Ticket numbers start at"  # one sequence for every order (agent §2.10ae, 2026-11-14)
    assert "map.delivery_radius" in fields and "counter.kitchen_start" not in fields  # kitchen_start is retired
