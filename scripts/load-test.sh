#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${YEXJUDGE_BASE_URL:-http://localhost:8080}"
REQUESTS="${1:-10}"
CONCURRENCY="${2:-4}"

if ! [[ "$REQUESTS" =~ ^[1-9][0-9]*$ && "$CONCURRENCY" =~ ^[1-9][0-9]*$ ]]; then
  echo "usage: $0 [requests>=1] [concurrency>=1]" >&2
  exit 2
fi

payload='{"language":"cpp","sourceCode":"#include <iostream>\nint main(){std::cout << 42 << std::endl;}","testCases":[{"id":1,"input":"","expectedOutput":"42"}],"limits":{"timeLimitMs":1000,"memoryLimitMb":128}}'
result_dir="$(mktemp -d)"
trap 'rm -rf "$result_dir"' EXIT

submit_one() {
  local index="$1"
  curl -sS -o "$result_dir/response-$index.json" \
    -w '%{http_code}' \
    -X POST "$BASE_URL/submissions" \
    -H 'Content-Type: application/json' \
    -d "$payload" > "$result_dir/status-$index"
}

start_ns="$(date +%s%N)"
active=0
for ((index = 1; index <= REQUESTS; index++)); do
  submit_one "$index" &
  ((active += 1))
  if ((active >= CONCURRENCY)); then
    wait
    active=0
  fi
done
if ((active > 0)); then
  wait
fi
end_ns="$(date +%s%N)"

accepted=0
failed=0
for ((index = 1; index <= REQUESTS; index++)); do
  status="$(cat "$result_dir/status-$index")"
  if [[ "$status" == "202" ]]; then
    ((accepted += 1))
  else
    ((failed += 1))
    printf 'request=%d status=%s response=%s\n' "$index" "$status" "$(cat "$result_dir/response-$index.json")" >&2
  fi
done

total_ms=$(( (end_ns - start_ns) / 1000000 ))
printf 'base_url=%s requests=%d concurrency=%d accepted=%d failed=%d total_ms=%d admission_avg_ms=%.2f\n' \
  "$BASE_URL" "$REQUESTS" "$CONCURRENCY" "$accepted" "$failed" "$total_ms" \
  "$(awk -v total="$total_ms" -v requests="$REQUESTS" 'BEGIN { printf "%.2f", total / requests }')"

if ((failed > 0)); then
  exit 1
fi
