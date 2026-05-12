#!/usr/bin/env bash
# Stream bench. Holds N SSE connections for D seconds, samples RSS.
# Usage: run-stream.sh <name> <port> <conns> <duration-secs> <start-cmd...>
set -euo pipefail

NAME=$1
PORT=$2
CONNS=$3
DURATION=$4
shift 4

ROOT=$(cd "$(dirname "$0")/.." && pwd)
RESULTS=$ROOT/results
mkdir -p "$RESULTS"

echo "=== $NAME (conns=$CONNS, dur=${DURATION}s) ==="

"$@" >"$RESULTS/$NAME.log" 2>&1 &
PID=$!
trap "kill -TERM $PID 2>/dev/null || true; sleep 0.2; kill -KILL $PID 2>/dev/null || true; pkill -P $PID 2>/dev/null || true; wait 2>/dev/null || true" EXIT

# Wait until /count responds.
for i in $(seq 1 200); do
  if curl -sf "http://localhost:$PORT/count" >/dev/null 2>&1; then break; fi
  sleep 0.05
done

LISTEN_PID=$(ss -tlnpH "sport = :$PORT" 2>/dev/null | grep -oP 'pid=\K[0-9]+' | head -1)
[[ -z "$LISTEN_PID" ]] && LISTEN_PID=$PID
echo "listen_pid=$LISTEN_PID"

# Sample RSS during load.
: > "$RESULTS/$NAME.rss"
TOTAL=$(( DURATION + 10 ))
(
  for i in $(seq 1 $TOTAL); do
    RSS=$(ps -o rss= -p $LISTEN_PID 2>/dev/null | tr -d ' ' || echo 0)
    echo "$i ${RSS:-0}" >> "$RESULTS/$NAME.rss"
    sleep 1
  done
) &
RSS_PID=$!

/tmp/bench-sse-client -url="http://localhost:$PORT/stream" -n="$CONNS" -duration="${DURATION}s" \
  2>&1 | tee "$RESULTS/$NAME.client.txt" >/dev/null || true
grep -E "(active|FINAL)" "$RESULTS/$NAME.client.txt" | tail -3 || true

kill $RSS_PID 2>/dev/null || true
kill -TERM $PID 2>/dev/null || true
sleep 0.3
kill -KILL $PID 2>/dev/null || true
wait 2>/dev/null || true

PEAK_KB=$(awk '{print $2}' "$RESULTS/$NAME.rss" | sort -n | tail -1)
PEAK_MB=$(( PEAK_KB / 1024 ))
FINAL_ACTIVE=$(grep FINAL "$RESULTS/$NAME.client.txt" | grep -oP 'active=\K[0-9]+' | tail -1)
FINAL_EVENTS=$(grep FINAL "$RESULTS/$NAME.client.txt" | grep -oP 'events=\K[0-9]+' | tail -1)
FINAL_FAILED=$(grep FINAL "$RESULTS/$NAME.client.txt" | grep -oP 'failed=\K[0-9]+' | tail -1)

{
  echo "conns_requested=$CONNS"
  echo "final_active=$FINAL_ACTIVE"
  echo "final_events=$FINAL_EVENTS"
  echo "final_failed=$FINAL_FAILED"
  echo "peak_rss_mb=$PEAK_MB"
  echo "peak_rss_kb=$PEAK_KB"
} | tee "$RESULTS/$NAME.summary"
