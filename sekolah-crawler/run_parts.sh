#!/usr/bin/env bash
# Runs the "detail" phase for a range of split parts, one at a time.
#
# Usage: ./run_parts.sh START-END [concurrency] [rate]
#   ./run_parts.sh 2-8            # parts 2..8, default concurrency=5 rate=5
#   ./run_parts.sh 2-8 8 8        # parts 2..8, concurrency=8 rate=8
#
# Each part gets its own output/checkpoint/error-log/log file, so parts
# never interfere with each other. "detail" is idempotent and resumes from
# its own checkpoint, so if this script (or the machine) gets interrupted
# mid-part — e.g. killed for memory pressure — just re-run the exact same
# command: already-finished parts report "nothing to do" and skip in under
# a second, and the interrupted part picks up exactly where it left off.
set -uo pipefail

usage() {
  echo "Usage: $0 START-END [concurrency] [rate]" >&2
  echo "  e.g. $0 2-8       # parts 2 through 8, concurrency=5 rate=5" >&2
  echo "  e.g. $0 2-8 8 8   # parts 2 through 8, concurrency=8 rate=8" >&2
  exit 1
}

[ $# -ge 1 ] || usage

RANGE="$1"
CONCURRENCY="${2:-5}"
RATE="${3:-5}"

if [[ ! "$RANGE" =~ ^([0-9]+)-([0-9]+)$ ]]; then
  echo "error: range must look like START-END, e.g. 2-8" >&2
  exit 1
fi
START="${BASH_REMATCH[1]}"
END="${BASH_REMATCH[2]}"
if [ "$START" -gt "$END" ]; then
  echo "error: START ($START) must be <= END ($END)" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

BIN="./sekolah-crawler"
PARTS_DIR="parts"

if [ ! -x "$BIN" ]; then
  echo "building $BIN..."
  go build -o sekolah-crawler . || { echo "build failed" >&2; exit 1; }
fi

SAMPLE=$(ls "$PARTS_DIR"/part_*.csv 2>/dev/null | head -1)
if [ -z "$SAMPLE" ]; then
  echo "error: no $PARTS_DIR/part_*.csv files found — run 'split' first" >&2
  exit 1
fi
WIDTH=$(basename "$SAMPLE" .csv | sed 's/part_//' | awk '{print length}')

echo "running parts $START-$END (width=$WIDTH digits, concurrency=$CONCURRENCY, rate=$RATE)"
echo

RESULTS=()

for ((n = START; n <= END; n++)); do
  NAME=$(printf "part_%0${WIDTH}d" "$n")
  IDS_FILE="$PARTS_DIR/${NAME}.csv"

  if [ ! -f "$IDS_FILE" ]; then
    echo "=== $NAME: skipped ($IDS_FILE not found) ==="
    RESULTS+=("$NAME: SKIPPED (missing input)")
    continue
  fi

  OUT="detail_${NAME}.csv"
  CHECKPOINT="done_${NAME}.txt"
  ERRLOG="err_${NAME}.tsv"
  LOG="detail_${NAME}.log"

  echo "=== $NAME: starting (log: $LOG) ==="
  "$BIN" detail -ids "$IDS_FILE" -out "$OUT" -checkpoint "$CHECKPOINT" \
    -error-log "$ERRLOG" -concurrency "$CONCURRENCY" -rate "$RATE" \
    >>"$LOG" 2>&1
  code=$?

  if [ $code -eq 0 ]; then
    echo "=== $NAME: done (see $OUT) ==="
    RESULTS+=("$NAME: OK")
  else
    echo "=== $NAME: exited with code $code (see $LOG) — re-run this script to finish it ==="
    RESULTS+=("$NAME: INTERRUPTED (exit $code)")
  fi
  echo
done

echo "=== summary ($START-$END) ==="
printf '%s\n' "${RESULTS[@]}"

if printf '%s\n' "${RESULTS[@]}" | grep -q INTERRUPTED; then
  echo
  echo "one or more parts did not finish — re-run: $0 $RANGE $CONCURRENCY $RATE"
  exit 1
fi
exit 0
