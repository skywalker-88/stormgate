#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8081}"
API_KEY1="${API_KEY1:-alice}"
API_KEY2="${API_KEY2:-bob}"

echo "=== StormGate smoke test against $BASE_URL ==="

check() {
  path=$1
  expect=$2
  code=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL$path")
  if [[ "$code" == "$expect" ]]; then
    echo "[OK] $path returned $code"
  else
    echo "[FAIL] $path returned $code (expected $expect)"
  fi
}

echo
echo "---- Basic endpoints ----"
check "/" 200
check "/health" 200
check "/metrics" 200

echo
echo "---- Rate limiting checks (/read cheap) ----"
for i in {1..6}; do
  curl -s -o /dev/null -w "%{http_code} " -H "X-API-Key: $API_KEY1" "$BASE_URL/read"
done
echo

echo
echo "---- Rate limiting checks (/search expensive) ----"
for i in {1..6}; do
  curl -s -i -o >(grep -E "HTTP/1.1|Retry-After" >&2) -s -H "X-API-Key: $API_KEY1" "$BASE_URL/search" || true
done

echo
echo "---- Key isolation check (alice vs bob) ----"
echo "Alice hits /read"
for i in {1..3}; do
  curl -s -o /dev/null -w "%{http_code} " -H "X-API-Key: $API_KEY1" "$BASE_URL/read"
done
echo
echo "Bob hits /read"
for i in {1..3}; do
  curl -s -o /dev/null -w "%{http_code} " -H "X-API-Key: $API_KEY2" "$BASE_URL/read"
done
echo

echo "---- Refill test ----"
echo "Sleeping 3s…"
sleep 3
curl -i -s -H "X-API-Key: $API_KEY1" "$BASE_URL/search" | grep -E "HTTP/1.1|Retry-After"
