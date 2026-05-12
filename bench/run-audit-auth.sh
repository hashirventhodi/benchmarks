#!/usr/bin/env bash
# Auth+RLS audit bench. Same shape as run-audit.sh but points at audit-auth.js.
set -euo pipefail

NAME=$1
PORT=$2
shift 2

ROOT=$(cd "$(dirname "$0")/.." && pwd)
RESULTS=$ROOT/results
mkdir -p "$RESULTS"

echo "=== $NAME ==="
docker exec bench-pg psql -U bench -d bench -c "TRUNCATE audit_entries RESTART IDENTITY;" >/dev/null

T0=$(date +%s%N)
"$@" >"$RESULTS/$NAME.log" 2>&1 &
PID=$!
trap "kill -TERM $PID 2>/dev/null || true; sleep 0.2; kill -KILL $PID 2>/dev/null || true; pkill -P $PID 2>/dev/null || true; wait 2>/dev/null || true" EXIT

for i in $(seq 1 200); do
  if curl -sf "http://localhost:$PORT/healthz" >/dev/null 2>&1; then
    T1=$(date +%s%N)
    COLD_MS=$(( (T1 - T0) / 1000000 ))
    echo "cold_start_ms=$COLD_MS"
    break
  fi
  sleep 0.05
done

LISTEN_PID=$(ss -tlnpH "sport = :$PORT" 2>/dev/null | grep -oP 'pid=\K[0-9]+' | head -1)
[[ -z "$LISTEN_PID" ]] && LISTEN_PID=$PID

URL="http://localhost:$PORT/audit" k6 run --quiet --vus 20 --duration 5s "$ROOT/bench/audit-auth.js" >/dev/null 2>&1 || true

: > "$RESULTS/$NAME.rss"
(
  for i in $(seq 1 35); do
    RSS=$(ps -o rss= -p $LISTEN_PID 2>/dev/null | tr -d ' ' || echo 0)
    echo "$i ${RSS:-0}" >> "$RESULTS/$NAME.rss"
    sleep 1
  done
) &
RSS_PID=$!

URL="http://localhost:$PORT/audit" VUS=100 DURATION=30s \
  k6 run --summary-export="$RESULTS/$NAME.json" "$ROOT/bench/audit-auth.js" \
  2>&1 | tee "$RESULTS/$NAME.k6.txt" | grep -E "(http_req_duration|http_reqs|iterations|http_req_failed)"

kill $RSS_PID 2>/dev/null || true
kill -TERM $PID 2>/dev/null || true
pkill -P $PID 2>/dev/null || true
sleep 0.3
kill -KILL $PID 2>/dev/null || true
wait 2>/dev/null || true

PEAK_KB=$(awk '{print $2}' "$RESULTS/$NAME.rss" | sort -n | tail -1)
PEAK_MB=$(( PEAK_KB / 1024 ))
{
  echo "peak_rss_mb=$PEAK_MB"
  echo "peak_rss_kb=$PEAK_KB"
  echo "cold_start_ms=$COLD_MS"
} | tee "$RESULTS/$NAME.summary"
