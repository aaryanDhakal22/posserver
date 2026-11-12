"""Golden replay: record what main answers on every legacy surface, then make
a later main reproduce it.

A chapter (golden/ch_*.py) drives main through one area — HTTP routes, the
agent's SSE frames, RPC replies, SQS ingest — and every answer becomes a step.
Volatile values are normalised once at the end of the chapter, so the same
scenario against the same main gives the same transcript:

  - literal aliases first (tenant tag, store_id, vendor order ids, logins …),
  - UUIDs → <uuid#N> numbered by first appearance,
  - RFC 3339 timestamps → <time>,
  - API tokens (q…_<secret>) → q…_<token>, bcrypt hashes → <bcrypt>,
    W3C traceparents → <traceparent>,
  - per-step masks (JSON keys whose value is time- or load-dependent).

Bodies are compared as the raw bytes main sent, after normalisation (JSON key
order and spacing included); only steps that filter a shared list re-encode.

A step's compare mode: exact (default; what the Pi, hub, Quibase, online and
call-center operators read), additive (main's own web UI: old fields
unchanged, new fields allowed), superset (the route table).

  GOLDEN_RECORD=1  write golden/transcripts/<chapter>.json (the reference)
  (default)        compare against it and fail with a readable diff
"""

from __future__ import annotations

import difflib
import json
import os
import re
from pathlib import Path
from typing import Any, Callable

import requests

TRANSCRIPTS = Path(__file__).parent / "transcripts"
RECORD = os.getenv("GOLDEN_RECORD") == "1"

_UUID = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}")
_TIME = re.compile(r"\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?")
_TRACE = re.compile(r"\b00-[0-9a-f]{32}-[0-9a-f]{16}-0[0-9a-f]\b")
_BCRYPT = re.compile(r"\$2[aby]\$\d{2}\$[./A-Za-z0-9]{53}")
_TOKEN = re.compile(r"\b(q[a-z]{2,5})_[A-Za-z0-9_\-]{6,}")  # keys and their shown prefixes
# Echo's request id / trace headers and the like never go into a step.
_KEEP_HEADERS = ("content-type", "location", "cache-control", "content-disposition", "www-authenticate")


class Rec:
    def __init__(self, chapter: str, base: str) -> None:
        self.chapter = chapter
        self.base = base.rstrip("/")
        self.steps: list[dict[str, Any]] = []
        self._aliases: dict[str, str] = {}

    # -- volatile literals ---------------------------------------------------
    def alias(self, value: Any, name: str) -> None:
        """Replace every occurrence of value (as text) with <name>."""
        if value is None or value == "":
            return
        self._aliases[str(value)] = f"<{name}>"

    # -- steps -------------------------------------------------------------
    def call(self, label: str, method: str, path: str, *, session: requests.Session | None = None,
             headers: dict | None = None, json_body: Any = None, data: Any = None, params: dict | None = None,
             filt: Callable[[Any], Any] | None = None, mask: tuple[str, ...] = (), compare: str = "",
             stream: bool = False, redirects: bool = True, timeout: float = 10, unordered: bool = False) -> requests.Response:
        """One HTTP request → one step. filt re-shapes a JSON body (keep only
        this chapter's rows of a shared list); mask blanks volatile keys;
        unordered: a JSON list whose order main doesn't fix (rows that tie on
        the sort key) compares as a multiset."""
        s = session or requests
        r = s.request(method, self.base + path, headers=headers, json=json_body, data=data, params=params,
                      timeout=timeout, stream=stream, allow_redirects=redirects)
        return self.record(label, method, path, r, params=params, filt=filt, mask=mask, compare=compare, stream=stream,
                           unordered=unordered)

    def record(self, label: str, method: str, path: str, r: requests.Response, *, params: dict | None = None,
               filt: Callable[[Any], Any] | None = None, mask: tuple[str, ...] = (), compare: str = "",
               stream: bool = False, unordered: bool = False) -> requests.Response:
        """A response made elsewhere (a call that had to run on a thread
        while the test played the agent) → one step."""
        body = "" if stream else r.text
        if filt is not None and body:
            body = _dump(filt(json.loads(body)))
        self.steps.append({
            "label": label, "method": method, "path": path, "params": params or {},
            "status": r.status_code,
            "headers": {k.lower(): v for k, v in r.headers.items() if k.lower() in _KEEP_HEADERS},
            "cookies": _cookie_shapes(r),
            "body": body, "mask": list(mask), "compare": compare or compare_mode(path),
            **({"unordered": True} if unordered else {}),
        })
        return r

    def value(self, label: str, value: Any, *, mask: tuple[str, ...] = (), compare: str = "exact") -> None:
        """A step that isn't an HTTP answer: an SSE frame, an RPC command, a
        derived value."""
        body = value if isinstance(value, str) else _dump(value)
        self.steps.append({"label": label, "method": "", "path": "", "params": {}, "status": 0, "headers": {},
                           "cookies": [], "body": body, "mask": list(mask), "compare": compare})

    # -- normalise + record / compare ---------------------------------------
    def normalised(self) -> list[dict[str, Any]]:
        aliases = sorted(self._aliases.items(), key=lambda kv: -len(kv[0]))
        uuids: dict[str, str] = {}

        def norm(text: str) -> str:
            for raw, name in aliases:
                text = text.replace(raw, name)
            text = _UUID.sub(lambda m: uuids.setdefault(m.group(0), f"<uuid#{len(uuids) + 1}>"), text)
            text = _TIME.sub("<time>", text)
            text = _BCRYPT.sub("<bcrypt>", text)
            text = _TRACE.sub("<traceparent>", text)
            return _TOKEN.sub(lambda m: f"{m.group(1)}_<token>", text)

        out = []
        for st in self.steps:
            body = st["body"]
            if st["mask"] and body:
                try:
                    body = _dump(_mask(json.loads(body), set(st["mask"])))
                except ValueError:
                    pass
            out.append({**st, "path": norm(st["path"]), "params": json.loads(norm(_dump(st["params"]))),
                        "headers": {k: norm(v) for k, v in st["headers"].items()},
                        "cookies": [norm(c) for c in st["cookies"]], "body": norm(body)})
        return out

    def finish(self) -> None:
        steps = self.normalised()
        path = TRANSCRIPTS / f"{self.chapter}.json"
        if RECORD:
            TRANSCRIPTS.mkdir(parents=True, exist_ok=True)
            path.write_text(json.dumps(steps, indent=1, ensure_ascii=False) + "\n")
            return
        if not path.exists():
            raise AssertionError(f"no golden transcript {path.name}: record it with GOLDEN_RECORD=1 against the reference main")
        want = json.loads(path.read_text())
        problems = compare(want, steps)
        if problems:
            raise AssertionError(f"golden {self.chapter}: {len(problems)} difference(s)\n\n" + "\n\n".join(problems[:15]))


def compare_mode(path: str) -> str:
    """additive for what only main's web UI reads (/admin/v1, /store/v1 but
    the operator, agent, store-config and tablet routes); exact otherwise."""
    if path.startswith(("/store/v1/operator/", "/store/v1/agent", "/store/v1/store-config", "/store/v1/tablet/")):
        return "exact"
    return "additive" if path.startswith(("/admin/v1/", "/store/v1/")) else "exact"


def compare(want: list[dict], got: list[dict]) -> list[str]:
    problems: list[str] = []
    gl = {(i, s["label"]): s for i, s in enumerate(got)}
    if [s["label"] for s in want] != [s["label"] for s in got]:
        diff = difflib.unified_diff([s["label"] for s in want], [s["label"] for s in got], "reference", "now", lineterm="", n=1)
        problems.append("steps differ:\n" + "\n".join(diff))
        return problems
    for i, w in enumerate(want):
        g = gl[(i, w["label"])]
        where = f"[{i}] {w['label']} ({w['method']} {w['path']})"
        if g.get("unordered") and w["body"] != g["body"]:
            try:
                same = sorted(map(_dump, _loads(w["body"]))) == sorted(map(_dump, _loads(g["body"])))
            except (ValueError, TypeError):
                same = False
            if same:
                w = {**w, "body": g["body"]}  # the same rows, in another order
        for k in ("method", "path", "params", "status", "headers", "cookies"):
            if w[k] != g[k]:
                problems.append(f"{where}: {k} {json.dumps(w[k])} → {json.dumps(g[k])}")
        if w.get("compare") == "superset":
            missing = set(json.loads(w["body"])) - set(json.loads(g["body"]))
            if missing:
                problems.append(f"{where}: missing {sorted(missing)}")
        elif w.get("compare") == "additive" and w["body"] != g["body"]:
            # Read only by main's own web UI (it ships with main): every field
            # the reference had must be there unchanged; new fields are fine.
            try:
                lost = _lost(_loads(w["body"]), _loads(g["body"]), "")
            except ValueError:
                lost = _lost_csv(w, g)
            if lost:
                problems.append(f"{where}: fields changed or gone\n  " + "\n  ".join(lost[:20]))
        elif w["body"] != g["body"]:
            diff = difflib.unified_diff(_pretty(w["body"]), _pretty(g["body"]), "reference", "now", lineterm="", n=2)
            problems.append(f"{where}: body\n" + "\n".join(list(diff)[:40]))
    return problems


def _lost_csv(w: dict, g: dict) -> list[str]:
    """A CSV body (main's own Store › Menu export) in additive mode: the same
    rows, each keeping the reference's columns unchanged; new columns may only
    be added at the end (the 2026-11-12 no_kitchen_ticket column)."""
    if "text/csv" not in str(w.get("headers", {}).get("content-type", "")):
        return ["(not JSON) body changed"]
    old, new = w["body"].splitlines(), g["body"].splitlines()
    if len(old) != len(new):
        return [f"(csv) {len(old)} rows → {len(new)}"]
    lost = []
    for i, (a, b) in enumerate(zip(old, new)):
        if not (b == a or b.startswith(a + ",")):
            lost.append(f"(csv) row {i}: {a!r} → {b!r}")
    return lost


_BARE = re.compile(r'(?<=[:\[,])(<[^<>"]+>)(?=[,}\]])')


def _loads(body: str) -> Any:
    """JSON with bare placeholders (<store_id> stood for a number) quoted."""
    return json.loads(_BARE.sub(r'"\1"', body))


def _lost(want: Any, got: Any, at: str) -> list[str]:
    """What of want is missing or different in got (extra keys in got are fine)."""
    if isinstance(want, dict):
        if not isinstance(got, dict):
            return [f"{at or '/'}: was an object"]
        out: list[str] = []
        for k, v in want.items():
            if k not in got:
                out.append(f"{at}/{k}: gone")
            else:
                out.extend(_lost(v, got[k], f"{at}/{k}"))
        return out
    if isinstance(want, list):
        if not isinstance(got, list) or len(want) != len(got):
            return [f"{at or '/'}: list of {len(want)} became {len(got) if isinstance(got, list) else type(got).__name__}"]
        out = []
        for i, (a, b) in enumerate(zip(want, got)):
            out.extend(_lost(a, b, f"{at}/{i}"))
        return out
    return [] if want == got else [f"{at or '/'}: {json.dumps(want)} → {json.dumps(got)}"]


def _dump(v: Any) -> str:
    # Go's encoding/json: compact, HTML-escaped <, >, &.
    s = json.dumps(v, separators=(",", ":"), ensure_ascii=False)
    return s.replace("<", "\\u003c").replace(">", "\\u003e").replace("&", "\\u0026")


def _pretty(body: str) -> list[str]:
    try:
        return json.dumps(json.loads(body), indent=1, ensure_ascii=False).splitlines()
    except ValueError:
        # Normalised bodies may not parse (<store_id> stands in for a number):
        # one field per line is still a readable diff.
        return re.sub(r',(?=")', ",\n", body).splitlines()


def _mask(v: Any, keys: set[str]) -> Any:
    if isinstance(v, dict):
        return {k: ("<masked>" if k in keys else _mask(x, keys)) for k, x in v.items()}
    if isinstance(v, list):
        return [_mask(x, keys) for x in v]
    return v


def _cookie_shapes(r: requests.Response) -> list[str]:
    """Set-Cookie without the value: name + attributes (Path, HttpOnly, …)."""
    out = []
    for raw in r.raw.headers.getlist("Set-Cookie") if hasattr(r.raw, "headers") and r.raw is not None else []:
        parts = [p.strip() for p in raw.split(";")]
        name = parts[0].split("=", 1)[0]
        empty = parts[0].endswith("=")
        attrs = sorted(p for p in parts[1:] if not p.lower().startswith("expires="))
        out.append(f"{name}={'' if empty else '<value>'}; " + "; ".join(attrs))
    return out
