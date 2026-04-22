import os
import time

import pytest
import boto3
import requests

APP_ENV = os.getenv("APP_ENV", "dev")
SQS_QUEUE_URL = os.getenv("SQS_QUEUE_URL")
SQS_ENDPOINT_URL = os.getenv("SQS_ENDPOINT")
AWS_ACCESS_KEY_ID = os.getenv("AWS_ACCESS_KEY_ID")
AWS_SECRET_ACCESS_KEY = os.getenv("AWS_SECRET_ACCESS_KEY")
AWS_REGION = os.getenv("AWS_REGION", "us-east-1")

if APP_ENV == "dev":
    AWS_ACCESS_KEY_ID = "dev"
    AWS_SECRET_ACCESS_KEY = "dev"
    SQS_ENDPOINT_URL = "http://localhost:9324"

SERVER_PORT = os.getenv("SERVER_PORT", "1323")
BASE_URL = os.getenv("APP_BASE_URL", f"http://localhost:{SERVER_PORT}/api/v1")

# API key used to authenticate against the order routes.
# In dev, defaults to a fixed value so tests are self-configuring.
API_KEY = os.getenv("API_KEY", "dev-test-api-key")
ADMIN_PASSCODE = os.getenv("ADMIN_PASSCODE", "")

# Per-run offset keeps order IDs unique across consecutive runs.
# (time_mod_200) * 1_000_000  →  max ≈ 199 M.
# Largest data.py order_id is ~61 M; combined stays < int32 max (2.1 B).
RUN_OFFSET: int = (int(time.time()) % 200) * 1_000_000


@pytest.fixture(autouse=True)
def purge_queue(sqs):
    """Start each test with an empty queue."""
    sqs.purge_queue(QueueUrl=SQS_QUEUE_URL)
    yield


@pytest.fixture(scope="session")
def sqs():

    return boto3.client(
        "sqs",
        endpoint_url=SQS_ENDPOINT_URL,
        region_name=AWS_REGION,
        aws_access_key_id=AWS_ACCESS_KEY_ID,
        aws_secret_access_key=AWS_SECRET_ACCESS_KEY,
    )


@pytest.fixture(scope="session")
def http():
    """Session-scoped HTTP client. Configures the API key on first use."""
    s = requests.Session()
    s.headers.update({"Content-Type": "application/json"})

    if ADMIN_PASSCODE:
        resp = s.post(
            f"{BASE_URL}/auth/key",
            headers={"X-Admin-Passcode": ADMIN_PASSCODE},
            json={"key": API_KEY},
        )
        assert resp.status_code == 200, (
            f"Failed to configure API key: {resp.status_code} {resp.text}"
        )

    s.headers.update({"X-API-Key": API_KEY})
    return s
