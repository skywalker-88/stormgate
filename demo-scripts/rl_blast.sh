#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-blast}"

echo "=== Concurrency blast against $BASE_URL/search ==="

reqs=20
concurrency=5

codes=$(seq 1 $reqs | xargs -n1 -P$concurrency -I{} \
  curl -s -o /dev/null -w "%{http_code}\n" -H "X-API-Key: $API_KEY" "$BASE_URL/search")

ok=$(echo "$codes" | grep -c "^200$")
blocked=$(echo "$codes" | grep -c "^429$")
other=$(echo "$codes" | grep -vc "200\|429")

echo "200 OK   : $ok"
echo "429 Block: $blocked"
echo "Other    : $other"
