"""Small helpers shared by the golden chapters."""

from __future__ import annotations

from typing import Any


def shape(v: Any) -> Any:
    """The structure of a value with every leaf replaced by its type: for
    aggregates over shared data (counts, other stores' rows) whose values
    depend on what else is in the database."""
    if isinstance(v, dict):
        return {k: shape(x) for k, x in v.items()}
    if isinstance(v, list):
        return [shape(v[0])] if v else []
    if v is None:
        return "null"
    return type(v).__name__


def keys(v: Any, depth: int = 3) -> Any:
    """Only the key structure (sorted), to `depth` levels; list elements
    merged. For aggregates over every store, whose rows differ in content."""
    if depth == 0:
        return "…"
    if isinstance(v, dict):
        return {k: keys(v[k], depth - 1) for k in sorted(v)}
    if isinstance(v, list):
        merged: dict = {}
        for x in v:
            if isinstance(x, dict):
                merged.update(x)
        return [keys(merged, depth - 1)] if merged else []
    return "·"


def only(key: str, value: Any):
    """filt: keep the rows of a shared list whose `key` equals value."""
    def f(rows: Any) -> Any:
        return [r for r in rows if isinstance(r, dict) and r.get(key) == value]
    return f


def routes_list(rows: Any) -> list[str]:
    """/__routes as sorted "METHOD path auth" strings (compared as a superset:
    a later main may add routes, never drop or change one)."""
    if isinstance(rows, dict):
        rows = rows.get("routes", [])
    return sorted(f"{r['method']} {r['path']} {r.get('auth', '')}" for r in rows)
