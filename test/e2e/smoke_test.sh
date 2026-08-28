#!/usr/bin/env bash
set -euo pipefail

HOST="${HOST:-127.0.0.1}"
PORT="${PORT:-8090}"
DOWNSTREAM_PORT="${DOWNSTREAM_PORT:-8091}"
AGENT_TIMEOUT="${PINPOINT_E2E_AGENT_TIMEOUT:-30}"
BASE_URL="http://${HOST}:${PORT}"
DOWNSTREAM_URL="http://${HOST}:${DOWNSTREAM_PORT}"

PASS_COUNT=0
FAIL_COUNT=0
HTTP_STATUS=""
HTTP_BODY=""
WORK_DIR=$(mktemp -d)
trap 'rm -rf "$WORK_DIR"' EXIT

pass() {
    PASS_COUNT=$((PASS_COUNT + 1))
    echo "  PASS  $1"
}

fail() {
    FAIL_COUNT=$((FAIL_COUNT + 1))
    echo "  FAIL  $1" >&2
    if [[ $# -gt 1 ]]; then
        echo "        $2" >&2
    fi
}

http_request() {
    local method=$1
    local url=$2
    shift 2
    local body_file="$WORK_DIR/response-${PASS_COUNT}-${FAIL_COUNT}-$$"
    : > "$body_file"
    HTTP_STATUS=$(curl -sS --max-time 10 -X "$method" -o "$body_file" \
        -w '%{http_code}' "$@" "$url") || HTTP_STATUS="000"
    HTTP_BODY=$(<"$body_file")
}

assert_status() {
    local name=$1 expected=$2
    if [[ "$HTTP_STATUS" == "$expected" ]]; then
        pass "$name status=$expected"
    else
        fail "$name expected HTTP $expected, got $HTTP_STATUS" "$HTTP_BODY"
    fi
}

assert_contains() {
    local name=$1 needle=$2
    if [[ "$HTTP_BODY" == *"$needle"* ]]; then
        pass "$name contains $needle"
    else
        fail "$name missing $needle" "$HTTP_BODY"
    fi
}

wait_for_body() {
    local name=$1 url=$2 needle=$3
    local elapsed=0
    while [[ $elapsed -lt $AGENT_TIMEOUT ]]; do
        http_request GET "$url"
        if [[ "$HTTP_STATUS" == "200" && "$HTTP_BODY" == *"$needle"* ]]; then
            pass "$name ready in ${elapsed}s"
            return 0
        fi
        sleep 1
        elapsed=$((elapsed + 1))
    done
    fail "$name readiness timed out after ${AGENT_TIMEOUT}s" \
        "last status=$HTTP_STATUS body=$HTTP_BODY"
    return 1
}

echo "==========================================="
echo " Pinpoint Go Agent - Correctness Smoke Test"
echo "==========================================="
echo "Upstream:   $BASE_URL"
echo "Downstream: $DOWNSTREAM_URL"
echo "Collector:  ${PINPOINT_GO_COLLECTOR_HOST:-unset}"
echo ""

wait_for_body "upstream agent" "$BASE_URL/ready" '"agent_enabled":true' || true
wait_for_body "HTTP downstream agent" "$DOWNSTREAM_URL/health" \
    '"agent_enabled":true' || true
wait_for_body "gRPC downstream propagation" "$BASE_URL/grpc-unary" \
    '"propagated":true' || true

echo ""
echo "Public API and HTTP metadata"
http_request GET "$BASE_URL/features" \
    -H 'User-Agent: pinpoint-go-smoke' \
    -H 'Content-Type: application/json' \
    -H 'X-Request-ID: go-e2e-feature-001' \
    -H 'X-Forwarded-For: 192.0.2.20, 192.0.2.21' \
    -H 'Pinpoint-ProxyNginx: t=1 D=2' \
    -H 'Cookie: session_id=smoke-session; token=smoke-token'
assert_status "features" 200
assert_contains "features sampled" '"sampled":true'
assert_contains "active event" '"active_event_observed":true'
assert_contains "logging context" '"logging_context":true'
assert_contains "context injection" '"context_injected":true'
assert_contains "async completion" '"async_complete":true'
assert_contains "async trace" '"async_trace_matches":true'

echo ""
echo "HTTP distributed tracing"
http_request GET "$BASE_URL/http-client" -H 'X-Request-ID: http-chain-001'
assert_status "HTTP downstream call" 200
assert_contains "HTTP trace propagation" '"propagated":true'
assert_contains "HTTP trace id" '"trace_id_matches":true'
assert_contains "HTTP parent span" '"parent_span_matches":true'
assert_contains "HTTP downstream status" '"downstream_status":200'

http_request GET "$BASE_URL/http-client?error=1" -H 'X-Request-ID: http-chain-error'
assert_status "HTTP downstream expected error wrapper" 200
assert_contains "HTTP error trace propagation" '"propagated":true'
assert_contains "HTTP expected downstream 503" '"downstream_status":503'

http_request GET "$DOWNSTREAM_URL/trace" -H 'Pinpoint-Sampled: s0'
assert_status "unsampled downstream" 200
assert_contains "unsampled span" '"sampled":false'
assert_contains "unsampled s0 propagation" '"incoming_sampled":"s0"'

# An unsampled request that calls onward must keep the decision travelling:
# the check above only covers a hop that receives s0, not one that forwards it.
http_request GET "$BASE_URL/http-client" -H 'Pinpoint-Sampled: s0'
assert_status "unsampled upstream call" 200
assert_contains "unsampled upstream span" '"sampled":false'
assert_contains "unsampled downstream reached" '"downstream_status":200'
assert_contains "s0 forwarded to next hop" '"incoming_sampled":"s0"'

echo ""
echo "gRPC distributed tracing (all RPC shapes)"
for endpoint in grpc-unary grpc-stream 'grpc-client-stream?count=4' 'grpc-bidi?count=4'; do
    http_request GET "$BASE_URL/$endpoint"
    assert_status "$endpoint" 200
    assert_contains "$endpoint propagation" '"propagated":true'
done
http_request GET "$BASE_URL/grpc-all"
assert_status "grpc-all" 200
assert_contains "grpc-all success" '"ok":true'
assert_contains "grpc-all propagation" '"propagated":true'
assert_contains "grpc-all four methods" '"methods":4'

http_request GET "$BASE_URL/grpc-error"
assert_status "gRPC expected error" 200
assert_contains "gRPC error observed" '"expected_error":true'

echo ""
echo "Span limits, SQL metadata, and expected HTTP error"
http_request GET "$BASE_URL/deep?depth=32&inject=1"
assert_status "deep span-event limit" 200
assert_contains "deep response" 'depth=32'
# Past Span.MaxCallStackDepth the events are discarded. A call made from that
# depth must still carry a full trace context or the trace would be cut here.
assert_contains "overflowed event injects context" 'overflow_context=true'
http_request GET "$BASE_URL/wide?width=256"
assert_status "wide span-event limit" 200
assert_contains "wide response" 'width=256'
http_request GET "$BASE_URL/db-batch?size=3"
assert_status "SQL batch" 200
assert_contains "SQL batch response" '"batch_size":3'
http_request GET "$BASE_URL/db-complex"
assert_status "SQL complex" 200
assert_contains "SQL complex response" '"queries":"complex"'
http_request GET "$BASE_URL/error"
assert_status "expected HTTP error" 500
assert_contains "expected HTTP error body" 'error'

echo ""
echo "URL and method filters"
# A filtered request gets a plain noop tracer (span id 0); an unsampled one
# still carries a real id, so "traced" separates filtering from sampling.
for path in exact prefix/deep/leaf seg/one mid/ant/x/y query; do
    http_request GET "$BASE_URL/filter/$path"
    assert_status "excluded /filter/$path" 200
    assert_contains "excluded /filter/$path not traced" '"traced":false'
done
http_request GET "$BASE_URL/filter/kept"
assert_status "unmatched /filter/kept" 200
assert_contains "unmatched /filter/kept still traced" '"traced":true'
http_request OPTIONS "$BASE_URL/filter/method"
assert_status "excluded OPTIONS method" 200
assert_contains "excluded OPTIONS not traced" '"traced":false'

echo ""
echo "Config reload and sampling"
http_request POST "$BASE_URL/agent/reload?counter_rate=2" --max-time 60
assert_status "reload counter sampling" 200
assert_contains "reload counter rate" '"counter_rate":2'
# A reload restarts the agent and NewAgent returns before registration
# completes, so probing straight away would only see noop tracers.
wait_for_body "reloaded upstream agent" "$BASE_URL/ready" \
    '"agent_enabled":true' || true
http_request GET "$BASE_URL/sampling-probe?count=40"
assert_status "sampling probe" 200
sampled=$(printf '%s' "$HTTP_BODY" | sed -n 's/.*"sampled":\([0-9][0-9]*\).*/\1/p')
unsampled=$(printf '%s' "$HTTP_BODY" | sed -n 's/.*"unsampled":\([0-9][0-9]*\).*/\1/p')
if [[ -n "$sampled" && -n "$unsampled" && "$sampled" -gt 0 && "$unsampled" -gt 0 ]]; then
    pass "counter sampling produced sampled=$sampled unsampled=$unsampled"
else
    fail "counter sampling did not produce both decisions" "$HTTP_BODY"
fi
http_request POST "$BASE_URL/agent/reload?counter_rate=1" --max-time 60
assert_status "restore full sampling" 200
wait_for_body "restored upstream agent" "$BASE_URL/ready" \
    '"agent_enabled":true' || true

echo ""
echo "Config file watcher"
# Production reconfiguration flows through the watcher, not through an agent
# restart. The endpoint points a fresh agent at a config file it owns, edits the
# sampling rate, and waits for the running agent to follow. It restarts the
# agent twice, so it needs more than the default request timeout.
http_request POST "$BASE_URL/agent/watch-reload" --max-time 120
assert_status "config file watcher" 200
assert_contains "watched agent started" '"started":true'
assert_contains "watcher applied the new rate" '"reloaded":true'

echo ""
echo "Agent shutdown and restart"
http_request POST "$BASE_URL/agent/shutdown"
assert_status "agent shutdown" 200
assert_contains "agent shutdown response" '"status":"shutdown"'
http_request GET "$BASE_URL/stats"
assert_status "stats after shutdown" 200
assert_contains "agent disabled after shutdown" '"agent_enabled":false'
http_request POST "$BASE_URL/agent/start"
assert_status "agent restart" 200
wait_for_body "restarted upstream agent" "$BASE_URL/ready" \
    '"agent_enabled":true' || true
http_request GET "$BASE_URL/simple"
assert_status "trace after restart" 200

echo ""
echo "==========================================="
echo "Smoke results: $PASS_COUNT passed, $FAIL_COUNT failed"
echo "==========================================="

if [[ $FAIL_COUNT -ne 0 ]]; then
    exit 1
fi
