#!/usr/bin/env bash
set -uo pipefail

# Measures statement coverage of the agent and the plugins across the three
# test layers - the unit tests of the agent and of every plugin module, the
# mock-collector suite in test/it, and the live stack of test/e2e run against
# its bundled stub collector - and merges them into one report.
#
# Every layer writes Go's binary coverage data (GOCOVERDIR, -test.gocoverdir)
# into a directory of its own, so the layers are reported apart and together
# without a merge tool: go tool covdata reads several directories at once. A
# plugin's tests count toward the agent as well as toward the plugin, since
# the database plugins are what drive most of sql_driver.go.
#
# Usage: scripts/coverage.sh [OUT_DIR]
#
# OUT_DIR (default: $TMPDIR/pinpoint-go-coverage) is emptied first. It ends up
# holding unit/, it/ and e2e/ (binary data), all.out (the merged text
# profile) and agent.html (the agent package, line by line). The run goes on
# past a failing suite, so one break still reports the rest, and exits 1 if
# any suite failed. e2e needs the ports run_e2e.sh defaults to.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${1:-${TMPDIR:-/tmp}/pinpoint-go-coverage}"
AGENT=github.com/pinpoint-apm/pinpoint-go-agent/v2
HTTP=github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2
GRPC=github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/v2

rm -rf "$OUT"
mkdir -p "$OUT"/{unit,it,e2e}
OUT="$(cd "$OUT" && pwd)"
failed=()

echo "== unit: agent"
(cd "$ROOT_DIR" && go test -count=1 -cover -covermode=atomic ./ \
    -args -test.gocoverdir="$OUT/unit") || failed+=("unit: agent")

for dir in "$ROOT_DIR"/plugin/*/; do
    name=$(basename "$dir")
    echo "== unit: plugin/$name"
    (cd "$dir" && go test -count=1 -cover -covermode=atomic -coverpkg="$AGENT,$(go list .)" ./ \
        -args -test.gocoverdir="$OUT/unit") || failed+=("unit: plugin/$name")
done

echo "== it"
(cd "$ROOT_DIR/test/it" && go test -count=1 -timeout 15m -cover -covermode=atomic \
    -coverpkg="$AGENT,$HTTP" ./... -args -test.gocoverdir="$OUT/it") || failed+=("it")

# The e2e binaries are built by run_e2e.sh, which GOFLAGS reaches. Their own
# packages are in -coverpkg although nothing reports them: a binary whose main
# package is not instrumented links no coverage runtime and writes no data.
echo "== e2e (bundled stub collector)"
(cd "$ROOT_DIR/test/e2e" &&
    GOFLAGS="-cover -covermode=atomic -coverpkg=$AGENT,$HTTP,$GRPC,$AGENT/test/e2e/..." \
    GOCOVERDIR="$OUT/e2e" ./run_e2e.sh --local-collector) || failed+=("e2e")

echo ""
for layer in unit it e2e; do
    echo "== $layer"
    go tool covdata percent -i="$OUT/$layer" -pkg="$AGENT,$HTTP,$GRPC"
done
echo "== combined"
go tool covdata percent -i="$OUT/unit,$OUT/it,$OUT/e2e" | grep -v "$AGENT/test/" | sort

go tool covdata textfmt -i="$OUT/unit,$OUT/it,$OUT/e2e" -o "$OUT/all.out"
# go tool cover resolves files through the root module, which cannot see the
# plugin modules, so the line-by-line report keeps to the agent package.
{
    head -1 "$OUT/all.out"
    grep "^$AGENT/[^/]*\.go:" "$OUT/all.out"
} >"$OUT/agent.out"
(cd "$ROOT_DIR" && go tool cover -html="$OUT/agent.out" -o "$OUT/agent.html")
echo ""
echo "Report: $OUT/agent.html"

if [ ${#failed[@]} -ne 0 ]; then
    printf 'FAILED: %s\n' "${failed[@]}"
    exit 1
fi
