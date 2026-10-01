#!/usr/bin/env bash
# Compares the request-path cost of a released agent with the working tree.
#
#   test/xbench/run.sh [base-ref] [count]     # defaults: v1.4.7, 6
#
# The base ref is extracted with git archive, and bench_test.go.in is built
# against it and against the working tree as two throwaway modules (the
# working tree is used as is, uncommitted changes included). Both talk to the
# e2e stub collector on 9991-9993, so the agent comes online and its sender
# goroutines run as in production. The runs interleave the two versions so
# machine drift hits both alike; benchstat compares them when it is installed.
# Run on an otherwise idle machine. XBENCH_WORK keeps the work directory.
set -euo pipefail

BASE="${1:-v1.4.7}"
COUNT="${2:-6}"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
XB="$ROOT/test/xbench"
WORK="${XBENCH_WORK:-$(mktemp -d)}"
mkdir -p "$WORK"

git -C "$ROOT" archive "$BASE" | (mkdir -p "$WORK/base-src" && tar -x -C "$WORK/base-src")

# mkmod <dir> <agent source>: a module running bench_test.go.in against it.
mkmod() {
    local dir="$1" src="$2" mod major
    mod="$(sed -n 's/^module //p' "$src/go.mod")"
    if [[ "$mod" == */v2 ]]; then major=2; else major=1; fi
    mkdir -p "$dir"
    sed "s#\"PINPOINT\"#\"$mod\"#" "$XB/bench_test.go.in" > "$dir/bench_test.go"
    cp "$XB/compat_v$major.go.in" "$dir/compat_test.go"
    printf 'module xbench\n\ngo 1.25\n\nrequire %s v%d.0.0\n\nreplace %s => %s\n' \
        "$mod" "$major" "$mod" "$src" > "$dir/go.mod"
    (cd "$dir" && go mod tidy && go vet .)
}
mkmod "$WORK/run-base" "$WORK/base-src"
mkmod "$WORK/run-head" "$ROOT"

(cd "$ROOT/test/e2e" && go build -o "$WORK/stubcollector" ./cmd/stubcollector)
"$WORK/stubcollector" > "$WORK/stubcollector.log" 2>&1 &
STUB=$!
trap 'kill $STUB 2>/dev/null' EXIT
sleep 1

: > "$WORK/base.txt"
: > "$WORK/head.txt"
for i in $(seq 1 "$COUNT"); do
    echo "round $i/$COUNT"
    for v in base head; do
        # XBENCH_RATE=0 runs the sampler-rejected shapes, 1 the sampled ones.
        for rate in 1 0; do
            (cd "$WORK/run-$v" && XBENCH_RATE=$rate go test -run '^$' -bench . \
                -benchtime 50000x -cpu 1,4,8 -count 1) >> "$WORK/$v.txt"
        done
    done
done

echo "results: $WORK/base.txt $WORK/head.txt"
if command -v benchstat > /dev/null; then
    benchstat "$BASE=$WORK/base.txt" "HEAD=$WORK/head.txt"
fi
