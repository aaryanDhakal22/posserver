"""
Shared pytest configuration.

Run the suite:
    cd tests
    uv sync
    uv run pytest -v

Set BASE_URL env var to point at a non-default server address.
"""

import os
import time

import pytest
import requests

BASE_URL: str = os.getenv("BASE_URL", "http://localhost:1323/api/v1")

# Per-run offset keeps order IDs unique across consecutive runs.
# (time_mod_200) * 1_000_000  →  max ≈ 199 M.
# Largest data.py order_id is ~61 M; combined stays < int32 max (2.1 B).
RUN_OFFSET: int = (int(time.time()) % 200) * 1_000_000


@pytest.fixture(scope="session")
def http() -> requests.Session:
    session = requests.Session()
    session.headers.update({"Content-Type": "application/json"})
    return session
