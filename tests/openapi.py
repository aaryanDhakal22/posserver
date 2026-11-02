"""Contract checks for /channel/v1 (main 3.0): every answer a test gets from
the channel API is validated against the OpenAPI document main serves, so the
document can't drift from what main does (adapters generate clients from it).

    check(resp, "getMe")  # status must be documented; body must match its schema
"""

from __future__ import annotations

from functools import lru_cache
from typing import Any

import requests
from jsonschema import Draft202012Validator
from referencing import Registry, Resource
from referencing.jsonschema import DRAFT202012

from conftest import MAIN_BASE_URL

SPEC_URL = f"{MAIN_BASE_URL}/channel/v1/openapi.json"


@lru_cache(maxsize=1)
def spec() -> dict[str, Any]:
    r = requests.get(SPEC_URL, timeout=5)
    r.raise_for_status()
    return r.json()


def _operation(op_id: str) -> tuple[str, str, dict]:
    for path, item in spec()["paths"].items():
        for method, op in item.items():
            if isinstance(op, dict) and op.get("operationId") == op_id:
                return method.upper(), path, op
    raise AssertionError(f"operationId {op_id!r} is not in the channel API spec")


def _resolve(node: dict) -> dict:
    """Follow a components/responses $ref."""
    ref = node.get("$ref", "")
    if ref.startswith("#/components/responses/"):
        return spec()["components"]["responses"][ref.rsplit("/", 1)[1]]
    return node


def check(resp: requests.Response, op_id: str) -> Any:
    """Assert resp is a documented answer of op_id and its JSON body matches
    the documented schema; return the decoded body."""
    _method, path, op = _operation(op_id)
    responses = op.get("responses", {})
    doc = responses.get(str(resp.status_code)) or responses.get("default")
    assert doc is not None, f"{op_id}: status {resp.status_code} is not documented ({path}): {resp.text[:300]}"
    content = _resolve(doc).get("content", {}).get("application/json")
    if content is None:
        assert not resp.content, f"{op_id} {resp.status_code}: documented without a body, got {resp.text[:200]}"
        return None
    assert resp.headers.get("Content-Type", "").startswith("application/json"), f"{op_id}: content type {resp.headers.get('Content-Type')}"
    body = resp.json()
    registry = Registry().with_resource("urn:channel-api", Resource(contents=spec(), specification=DRAFT202012))
    schema = {"$ref": "urn:channel-api#" + _pointer(content["schema"])} if "$ref" in content["schema"] else content["schema"]
    errors = sorted(Draft202012Validator(schema, registry=registry).iter_errors(body), key=lambda e: list(e.path))
    assert not errors, f"{op_id} {resp.status_code} does not match the spec:\n" + "\n".join(
        f"  at /{'/'.join(map(str, e.path))}: {e.message}" for e in errors[:10])
    return body


def _pointer(schema: dict) -> str:
    return schema["$ref"][1:] if schema["$ref"].startswith("#") else schema["$ref"]
