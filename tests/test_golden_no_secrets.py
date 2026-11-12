"""No secret in a golden transcript (2026-11-12).

A recorded link answer once carried the live OTLP ingest token into the public repo
(`golden/transcripts/agent.json`, `secrets.otlp_headers`). The recorder masks secret-bearing fields
now (`golden.recorder.mask_secrets`); this fails the gate if a transcript ever holds one again,
whoever wrote it and however. Needs no main: it reads the files.
"""

from __future__ import annotations

import json
import re
from pathlib import Path

import pytest

from golden.recorder import SECRET, TRANSCRIPTS, mask_secrets

# Things that look like a credential wherever they stand in the file.
PATTERNS = {
    "a bearer token": re.compile(r"Bearer(?:\s|%20|\+)+[A-Za-z0-9._~+/=-]{12,}"),
    "an Authorization header value": re.compile(r"Authorization\s*[=:]\s*(?!<)[A-Za-z0-9._~+/=%-]{12,}", re.IGNORECASE),
    "a JWT": re.compile(r"eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\."),
    "an AWS access key": re.compile(r"\b(?:AKIA|ASIA)[0-9A-Z]{16}\b"),
    "a Doppler token": re.compile(r"\bdp\.(?:st|pt|ct|sa)\.[A-Za-z0-9._-]{20,}"),
    "a Tailscale key": re.compile(r"\btskey-[a-z]+-[A-Za-z0-9-]{10,}"),
    "a Square token": re.compile(r"\b(?:EAAA[A-Za-z0-9_-]{40,}|sq0[a-z]{3}-[A-Za-z0-9_-]{20,})"),
    "a Twilio SID / key": re.compile(r"\b(?:AC|SK)[0-9a-f]{32}\b"),
    "a GitHub token": re.compile(r"\bgh[pousr]_[A-Za-z0-9]{30,}"),
    "a private key": re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----"),
    "an unmasked QuiccPOS key": re.compile(r"\bq[a-z]{2,5}_(?!<token>)[A-Za-z0-9_-]{16,}"),
}

TRANSCRIPT_FILES = sorted(TRANSCRIPTS.glob("*.json"))


def test_there_are_transcripts() -> None:
    assert TRANSCRIPT_FILES, f"no golden transcripts under {TRANSCRIPTS}"


@pytest.mark.parametrize("path", TRANSCRIPT_FILES, ids=lambda p: p.name)
def test_transcript_holds_no_secret(path: Path) -> None:
    text = path.read_text()
    found = []
    for n, line in enumerate(text.splitlines(), 1):
        for what, pat in PATTERNS.items():
            if pat.search(line):
                found.append(f"{path.name}:{n}: {what}")  # never the value
    assert not found, "secret-looking strings in a golden transcript (mask them, never commit them):\n" + "\n".join(found)

    # Every secret-bearing field is empty or a placeholder: masking the stored steps changes nothing.
    for i, step in enumerate(json.loads(text)):
        for part in ("body", "path"):
            assert mask_secrets(step[part]) == step[part], (
                f"{path.name} step {i} ({step['label']}): a secret-bearing field in its {part} is not masked; "
                "record with the current recorder (GOLDEN_RECORD=1) or replace the value with <secret>")
        for k, v in step.get("headers", {}).items():
            assert k.lower() not in ("authorization", "cookie", "set-cookie", "x-store-api-key", "x-admin-key"), (
                f"{path.name} step {i}: header {k} must never be recorded")
            assert not PATTERNS["a bearer token"].search(str(v)), f"{path.name} step {i}: header {k} holds a bearer token"


def test_mask_secrets() -> None:
    link = ('{"api_key":"qpos_<token>","manager":{"password_hash":"<bcrypt>"},"secrets":{"pushover_app_token":"",'
            '"pushover_user_key":"u123456789012345678901234567890","otlp_endpoint":"https://otlp.example.com",'
            '"otlp_headers":"Authorization=Bearer abcdefghijklmnopqrstuvwxyz0123456789","tailscale_authkey":"tskey-auth-abc123-def456",'
            '"pushover_admin_devices":"OwnerPhone"},"name":"Lombardi\'s","masked":"\\u003cmasked\\u003e"}')
    got = mask_secrets(link)
    assert "abcdefghij" not in got and "tskey-auth" not in got and "u1234567890" not in got and "otlp.example.com" not in got
    for key in ("pushover_user_key", "otlp_endpoint", "otlp_headers", "tailscale_authkey"):
        assert f'"{key}":"{SECRET}"' in got, key
    # Placeholders, empty values and fields that are no secret stay as they are.
    for kept in ('"api_key":"qpos_<token>"', '"password_hash":"<bcrypt>"', '"pushover_app_token":""',
                 '"pushover_admin_devices":"OwnerPhone"', '"name":"Lombardi\'s"', '"masked":"\\u003cmasked\\u003e"'):
        assert kept in got, kept
    assert mask_secrets(got) == got, "masking twice changes nothing"
    # JSON carried inside a JSON string (an SSE frame's data, an RPC payload).
    nested = '{"data":"{\\"secrets\\":{\\"otlp_headers\\":\\"Authorization=Bearer zzzzzzzzzzzzzzzzzzzz\\",\\"n\\":1}}"}'
    out = mask_secrets(nested)
    assert "zzzzzzzz" not in out and f'\\"otlp_headers\\":\\"{SECRET}\\"' in out and '\\"n\\":1' in out
    # Each pattern catches what it is for.
    samples = {
        "a bearer token": "Authorization=Bearer abcdefghijklmnopqrstuvwxyz",
        "a JWT": "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.sig",
        "an AWS access key": "AKIAIOSFODNN7EXAMPLE",
        "a Tailscale key": "tskey-auth-kAbCdEf12345-xyz",
        "an unmasked QuiccPOS key": "qpos_abcdefghijklmnopqrstuvwx",
    }
    for what, sample in samples.items():
        assert PATTERNS[what].search(sample), what
    assert not PATTERNS["an unmasked QuiccPOS key"].search("qpos_<token>")
