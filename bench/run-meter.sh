#!/usr/bin/env bash
# Meter bench. Seeds 100k pending events, runs worker, times drain.
# Usage: run-meter.sh <name> <start-cmd...>
set -uo pipefail

NAME=$1
shift

ROOT=$(cd "$(dirname "$0")/.." && pwd)
RESULTS=$ROOT/results
mkdir -p "$RESULTS"

echo "=== $NAME ==="
docker exec -i bench-pg psql -U bench -d bench < "$ROOT/bench/seed-meter.sql" >/dev/null
SEEDED=$(docker exec bench-pg psql -U bench -d bench -tAc "SELECT count(*) FROM pending_events WHERE claimed_at IS NULL" | tr -d '[:space:]')
echo "seeded=$SEEDED"

T0=$(date +%s%N)
"$@" >"$RESULTS/$NAME.log" 2>&1 &
PID=$!

# Poll DB until drained.
T1=0
for i in $(seq 1 300); do
  REMAINING=$(docker exec bench-pg psql -U bench -d bench -tAc "SELECT count(*) FROM pending_events WHERE claimed_at IS NULL" 2>/dev/null | tr -d '[:space:]')
  REMAINING=${REMAINING:-999999}
  if [[ "$REMAINING" =~ ^[0-9]+$ ]] && [[ "$REMAINING" -eq 0 ]]; then
    T1=$(date +%s%N)
    break
  fi
  sleep 0.2
done

if [[ "$T1" -eq 0 ]]; then
  echo "ERROR: did not drain within timeout"
  kill -TERM $PID 2>/dev/null || true
  exit 1
fi

ELAPSED_MS=$(( (T1 - T0) / 1000000 ))
THROUGHPUT=$(( SEEDED * 1000 / ELAPSED_MS ))

LISTEN_PID=$(pgrep -P $PID 2>/dev/null | head -1)
[[ -z "$LISTEN_PID" ]] && LISTEN_PID=$PID
RSS_KB=$(ps -o rss= -p $LISTEN_PID 2>/dev/null | tr -d ' ')
RSS_KB=${RSS_KB:-0}
RSS_MB=$(( RSS_KB / 1024 ))

kill -TERM $PID 2>/dev/null || true
sleep 0.3
kill -KILL $PID 2>/dev/null || true
pkill -P $PID 2>/dev/null || true
wait 2>/dev/null || true

{
  echo "seeded=$SEEDED"
  echo "elapsed_ms=$ELAPSED_MS"
  echo "throughput_per_sec=$THROUGHPUT"
  echo "rss_mb=$RSS_MB"
} | tee "$RESULTS/$NAME.summary"
