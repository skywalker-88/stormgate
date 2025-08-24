#!/usr/bin/env bash
set -euo pipefail

HOST=${HOST:-http://localhost:8080}
ROUTE=${ROUTE:-/read}
CLIENT_HDR=${CLIENT_HDR:-}  # e.g., 'X-Forwarded-For: 1.2.3.4'

echo "== StormGate Anomaly Smoke =="
echo "Hitting ${HOST}${ROUTE} in a burst to trigger anomaly..."

# Warmup
curl -sS -o /dev/null -w "%{http_code}\n" "${HOST}${ROUTE}" || true

# Burst 100 req quickly
for i in $(seq 1 100); do
  if [ -n "$CLIENT_HDR" ]; then
    curl -sS -H "$CLIENT_HDR" -o /dev/null "${HOST}${ROUTE}" &
  else
    curl -sS -o /dev/null "${HOST}${ROUTE}" &
  fi
done
wait

sleep 1

echo "-- Checking /metrics for anomalies --"
METRICS="$(curl -sS "${HOST}/metrics")"

# If CLIENT_HDR is provided, extract the first IP to filter the metric by client
CLIENT_FILTER=""
if [ -n "$CLIENT_HDR" ]; then
  # get first IP from header value (before any comma)
  CLIENT_IP="$(printf '%s' "$CLIENT_HDR" | sed -n 's/^[Xx]-Forwarded-For:[[:space:]]*//p' | cut -d, -f1 | tr -d ' ')"
  if [ -n "$CLIENT_IP" ]; then
    CLIENT_FILTER="client=\"${CLIENT_IP}\""
    echo "Filtering anomalies for client: $CLIENT_IP"
  fi
fi

# Count series lines for this route (and client if set) safely (no pipefail issues)
COUNT=$(echo "$METRICS" | awk -v route="$ROUTE" -v clientf="$CLIENT_FILTER" '
  $1 ~ /^stormgate_anomalies_total/ {
    if ($0 ~ "route=\"" route "\"") {
      if (clientf == "" || $0 ~ clientf) c++
    }
  }
  END { print (c+0) }')

echo "Anomaly series lines for route=${ROUTE}${CLIENT_FILTER:+, client=$CLIENT_IP}: $COUNT"
if [ "$COUNT" -gt 0 ]; then
  echo "[OK] Anomaly metric present."
else
  echo "[FAIL] No anomaly metric found for $ROUTE${CLIENT_FILTER:+ and client $CLIENT_IP}"; exit 2
fi

# Sum the values for those series (no bc)
TOTAL=$(echo "$METRICS" | awk -v route="$ROUTE" -v clientf="$CLIENT_FILTER" '
  $1 ~ /^stormgate_anomalies_total/ {
    if ($0 ~ "route=\"" route "\"") {
      if (clientf == "" || $0 ~ clientf) sum += $NF
    }
  }
  END { print (sum+0) }')

echo "Total anomalies for ${ROUTE}${CLIENT_FILTER:+, client=$CLIENT_IP}: $TOTAL"
if [ "${TOTAL:-0}" -ge 1 ]; then
  echo "[OK] Detected anomalies_total >= 1"
else
  echo "[FAIL] anomalies_total is 0"; exit 3
fi

echo "[DONE] Anomaly smoke passed."
