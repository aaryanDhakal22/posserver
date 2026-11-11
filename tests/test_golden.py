"""Golden replay of main's legacy surface (main 3.0, archive/PORTS-PLAN.md).

Each chapter in golden/ drives one area of main — every route, the agent's
SSE frames and RPC replies, SQS ingest — and compares what main answers with
the transcript recorded from main 2.2.3 (golden/transcripts/). Prod's agent,
hub, Quibase and online depend on these bytes, so a later main must reproduce
them. Record a reference with GOLDEN_RECORD=1 (only against the reference
main); see golden/recorder.py for what is normalised.
"""

from __future__ import annotations

import importlib

import pytest

from conftest import MAIN_BASE_URL
from golden.recorder import Rec

CHAPTERS = [
    pytest.param("public"),
    pytest.param("admin"),
    pytest.param("store"),
    pytest.param("menu"),
    pytest.param("agent"),
    pytest.param("orders"),
    pytest.param("geo", marks=pytest.mark.xdist_group("geo")),
]


@pytest.mark.parametrize("chapter", CHAPTERS)
def test_golden(chapter: str, admin, request) -> None:
    mod = importlib.import_module(f"golden.ch_{chapter}")
    rec = Rec(chapter, MAIN_BASE_URL)
    mod.run(rec, admin, request)
    rec.finish()


def test_golden_covers_every_legacy_route() -> None:
    """Every route main 2.2.3 served (the reference route table) is exercised
    by at least one recorded step, so no legacy route escapes the replay."""
    import glob
    import json
    import re
    from pathlib import Path

    tdir = Path(__file__).parent / "golden" / "transcripts"
    public = json.loads((tdir / "public.json").read_text())
    routes = json.loads(next(s for s in public if s["label"] == "routes")["body"])
    steps = [s for f in glob.glob(str(tdir / "*.json")) for s in json.loads(Path(f).read_text()) if s["method"]]
    missing = []
    for r in routes:
        method, path, _auth = r.split(" ", 2)
        rx = re.compile("^" + re.sub(r":[A-Za-z_]+", "[^/]+", path).replace("/*", "/.*") + "$")
        if not any(s["method"] == method and rx.match(s["path"]) for s in steps):
            missing.append(r)
    assert not missing, f"legacy routes with no golden step: {missing}"
