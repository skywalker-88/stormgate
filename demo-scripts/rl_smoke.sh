#!/usr/bin/env bash
set -euo pipefail

# Targets
BASE_URL="${BASE_URL:-http://localhost:8080}"   # StormGate by default
NGINX_URL="${NGINX_URL:-http://localhost:8081}" # NGINX (sanity split)
API_KEY1="${API_KEY1:-alice}"
API_KEY2="${API_KEY2:-bob}"

echo "=== StormGate smoke test against $BASE_URL ==="

check() {
  local path="$1" expect="$2"
  local code
  code=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL$path")
  if [[ "$code" == "$expect" ]]; then
    echo "[OK] $path returned $code"
  else
    echo "[FAIL] $path returned $code (expected $expect)"
    exit 1
  fi
}

# ---------- Basic endpoints ----------
echo
echo "---- Basic endpoints ----"
check "/" 200
check "/health" 200
check "/metrics" 200

# ---------- Cheap route behaves (no 429 under small burst) ----------
echo
echo "---- Rate limiting checks (/read cheap) ----"
for i in {1..6}; do
  curl -s -o /dev/null -w "%{http_code} " -H "X-API-Key: $API_KEY1" "$BASE_URL/read"
done
echo

# ---------- Force a 429 on /search and capture full response ----------
echo
echo "---- Rate limiting checks (/search expensive) ----"
out=""
for i in {1..50}; do
  out="$(curl -s -i -H "X-API-Key: $API_KEY1" "$BASE_URL/search")" || true
  echo "$out" | head -n1
  echo "$out" | grep -qE '^HTTP/.* 429' && break
  sleep 0.05
done

# ---------- Assert fingerprint headers + JSON body ----------
echo
echo "---- Assert StormGate 429 fingerprint on /search ----"
echo "$out" | grep -qE '^HTTP/.* 429' || { echo "[FAIL] expected 429 on /search"; exit 1; }

# Header names are canonicalized by Go's HTTP server → match case-insensitively.
echo "$out" | grep -qi '^X-Stormgate:[[:space:]]*protector'   || { echo "[FAIL] missing X-Stormgate: protector"; exit 1; }
echo "$out" | grep -qi '^X-Ratelimit-Limit:'                  || { echo "[FAIL] missing X-Ratelimit-Limit"; exit 1; }
echo "$out" | grep -qi '^X-Ratelimit-Remaining:'              || { echo "[FAIL] missing X-Ratelimit-Remaining"; exit 1; }
echo "$out" | grep -qi '^X-Ratelimit-Reset:'                  || { echo "[FAIL] missing X-Ratelimit-Reset"; exit 1; }
echo "$out" | grep -qi '^Retry-After:'                        || { echo "[FAIL] missing Retry-After"; exit 1; }

# JSON body check (works with or without jq). Strip CRs; drop headers up to the first blank line.
body="$(printf '%s' "$out" | tr -d '\r' | sed '1,/^$/d')"
if command -v jq >/dev/null 2>&1; then
  printf '%s' "$body" | jq -e '.error=="rate_limited"' >/dev/null \
    || { echo "[FAIL] invalid/missing JSON body"; exit 1; }
else
  echo "$body" | grep -q '{"error":"rate_limited"}' \
    || { echo "[FAIL] missing JSON body"; exit 1; }
fi
echo "[OK] 429 fingerprint present"

# ---------- Key isolation check ----------
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

# ---------- Refill test ----------
echo "---- Refill test ----"
echo "Sleeping 3s…"
sleep 3
curl -i -s -H "X-API-Key: $API_KEY1" "$BASE_URL/search" | grep -E "HTTP/1.1|Retry-After" || true

# ---------- Metrics check ----------
echo
echo "---- Metrics check (stormgate_limited_total) ----"
curl -s "$BASE_URL/metrics" | grep -E 'stormgate_limited_total\{route="/search"\}[[:space:]]+[1-9][0-9]*' \
  && echo "[OK] stormgate_limited_total incremented" \
  || { echo "[FAIL] limited metric not incremented"; exit 1; }

echo
echo "✅ Smoke complete"
