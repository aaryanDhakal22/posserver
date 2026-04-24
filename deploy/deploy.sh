#!/usr/bin/env bash
# deploy.sh — Blue-green deploy for quicc-prod
#
# Usage:  ./deploy.sh <image-tag>
# Example: ./deploy.sh prod-a1b2c3d
#
# Secrets are injected by Doppler (token read from .doppler-prod-token).
# Slot-control vars (tags, priorities) live in .env.prod and are managed
# by this script via sed.

set -euo pipefail

IMAGE_TAG="${1:?Usage: ./deploy.sh <image-tag>}"
ENV_FILE=".env.prod"
STATE_FILE=".state"
HEALTH_RETRIES=30
HEALTH_INTERVAL=2

DOPPLER_TOKEN=$(cat .doppler-prod-token)
export DOPPLER_TOKEN
COMPOSE="doppler run -- docker compose -f docker-compose.prod.yml --env-file ${ENV_FILE}"

# ── 1. Determine slots ─────────────────────────────────────
ACTIVE=$(cat "$STATE_FILE" 2>/dev/null || echo "blue")
if [[ "$ACTIVE" == "blue" ]]; then
  INACTIVE="green"
else
  INACTIVE="blue"
fi

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "▶  Active   : ${ACTIVE}"
echo "▶  Deploying: ${INACTIVE}  →  ${IMAGE_TAG}"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# ── 2. Write new image tag into .env.prod ──────────────────
INACTIVE_UPPER="${INACTIVE^^}"
sed -i "s|^${INACTIVE_UPPER}_TAG=.*|${INACTIVE_UPPER}_TAG=${IMAGE_TAG}|" "$ENV_FILE"
echo "✓  Updated ${INACTIVE_UPPER}_TAG=${IMAGE_TAG} in ${ENV_FILE}"

# ── 3. Ensure postgres is running, pull + start inactive slot
$COMPOSE up -d postgres
echo "✓  Postgres running"

$COMPOSE pull ${INACTIVE}-api
echo "✓  Pulled ${IMAGE_TAG}"

$COMPOSE --profile "$INACTIVE" up -d --no-deps ${INACTIVE}-api
echo "✓  Started quicc-${INACTIVE}-api"

# ── 4. Health check ────────────────────────────────────────
echo "▶  Waiting for quicc-${INACTIVE}-api /health ..."
PASSED=0
for i in $(seq 1 $HEALTH_RETRIES); do
  if docker run --rm --network proxy alpine:3.20 \
       wget -q -O /dev/null "http://quicc-${INACTIVE}-api:1323/health" 2>/dev/null; then
    echo "✓  Health check passed (attempt ${i})"
    PASSED=1
    break
  fi
  echo "   attempt ${i}/${HEALTH_RETRIES} — not ready yet"
  sleep $HEALTH_INTERVAL
done

if [[ $PASSED -eq 0 ]]; then
  echo "✗  Health check failed after ${HEALTH_RETRIES} attempts — rolling back"
  $COMPOSE stop ${INACTIVE}-api
  exit 1
fi

# ── 5. Swap Traefik priorities ─────────────────────────────
if [[ "$INACTIVE" == "green" ]]; then
  sed -i 's/^BLUE_PRIORITY=.*/BLUE_PRIORITY=1/'    "$ENV_FILE"
  sed -i 's/^GREEN_PRIORITY=.*/GREEN_PRIORITY=100/' "$ENV_FILE"
else
  sed -i 's/^GREEN_PRIORITY=.*/GREEN_PRIORITY=1/'  "$ENV_FILE"
  sed -i 's/^BLUE_PRIORITY=.*/BLUE_PRIORITY=100/'   "$ENV_FILE"
fi
echo "✓  Swapped Traefik priorities in ${ENV_FILE}"

# Recreate API containers to pick up new priority labels (postgres untouched).
$COMPOSE --profile blue  up -d --no-deps blue-api  2>/dev/null || true
$COMPOSE --profile green up -d --no-deps green-api 2>/dev/null || true

echo "▶  Waiting 3s for Traefik to pick up new labels ..."
sleep 3

# ── 6. Stop old active slot ────────────────────────────────
$COMPOSE stop ${ACTIVE}-api
echo "✓  Stopped quicc-${ACTIVE}-api"

# ── 7. Save state ──────────────────────────────────────────
echo "$INACTIVE" > "$STATE_FILE"

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "✅  Deploy complete"
echo "    Active slot : ${INACTIVE}"
echo "    Image tag   : ${IMAGE_TAG}"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
