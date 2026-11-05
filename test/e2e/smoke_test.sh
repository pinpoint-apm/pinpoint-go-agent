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
    local out
    out=$(curl -sS --max-time 10 -X "$method" -w $'\n%{http_code}' "$@" "$url") || out=$'\n000'
    HTTP_STATUS=${out##*$'\n'}
    HTTP_BODY=${out%$'\n'*}
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

# probe_sampling QUERY sets SAMPLED and UNSAMPLED from /sampling-probe.
probe_sampling() {
    http_request GET "$BASE_URL/sampling-probe?$1"
    SAMPLED=$(printf '%s' "$HTTP_BODY" | sed -n 's/.*"sampled":\([0-9][0-9]*\).*/\1/p')
    UNSAMPLED=$(printf '%s' "$HTTP_BODY" | sed -n 's/.*"unsampled":\([0-9][0-9]*\).*/\1/p')
}

assert_sampled_between() {
    local name=$1 lo=$2 hi=$3
    if [[ -n "$SAMPLED" && "$SAMPLED" -ge $lo && "$SAMPLED" -le $hi ]]; then
        pass "$name sampled=$SAMPLED unsampled=$UNSAMPLED"
    else
        fail "$name expected $lo..$hi sampled" "$HTTP_BODY"
    fi
}

# reload_sampler QUERY restarts the upstream agent through /agent/reload and
# waits for it to register again.
reload_sampler() {
    local name=$1 query=$2
    http_request POST "$BASE_URL/agent/reload?$query" --max-time 60
    assert_status "$name" 200
    assert_contains "$name enabled" '"agent_enabled":true'
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
http_request GET "$BASE_URL/features?mode=smoke&id=go-e2e-feature-001" \
    -H 'User-Agent: pinpoint-go-smoke' \
    -H 'Content-Type: application/json' \
    -H 'X-Request-ID: go-e2e-feature-001' \
    -H 'X-Forwarded-For: 192.0.2.20, 192.0.2.21' \
    -H 'Pinpoint-ProxyNginx: t=1 D=2' \
    -H "X-E2E-Proxy-Time: t=$(($(date +%s) * 1000)) D=150" \
    -H 'Cookie: session_id=smoke-session; token=smoke-token'
assert_status "features" 200
assert_contains "features sampled" '"sampled":true'
assert_contains "context injection" '"context_injected":true'
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
echo "Config file reload"
# Every agent polls its config file once a second and applies the dynamic
# options that changed, here one more Http.Server.ExcludeUrl entry, without a
# restart. run_e2e.sh hands the stack a copy of the file and names it in
# PINPOINT_E2E_MUTABLE_CONFIG; without one the section is skipped rather than
# rewrite a file someone else owns.
if [[ -n "${PINPOINT_E2E_MUTABLE_CONFIG:-}" && -w "${PINPOINT_E2E_MUTABLE_CONFIG}" ]]; then
    config_file=$PINPOINT_E2E_MUTABLE_CONFIG
    config_backup="$config_file.orig"
    cp "$config_file" "$config_backup"
    http_request GET "$BASE_URL/filter/reload"
    assert_contains "/filter/reload traced before the rewrite" '"traced":true'
    # Written aside and renamed, so a poll never reads a half-written file.
    sed 's|^\([[:space:]]*\)ExcludeUrl:.*|&\
\1  - "/filter/reload"|' "$config_backup" >"$config_file.tmp"
    mv "$config_file.tmp" "$config_file"
    wait_for_body "rewritten ExcludeUrl excludes /filter/reload" \
        "$BASE_URL/filter/reload" '"traced":false' || true
    http_request GET "$BASE_URL/filter/kept"
    assert_contains "/filter/kept still traced after the reload" '"traced":true'
    http_request GET "$BASE_URL/filter/exact"
    assert_contains "/filter/exact still excluded after the reload" '"traced":false'
    cp "$config_backup" "$config_file.tmp"
    mv "$config_file.tmp" "$config_file"
    rm -f "$config_backup"
    wait_for_body "restored ExcludeUrl traces /filter/reload" \
        "$BASE_URL/filter/reload" '"traced":true' || true
else
    echo "  SKIP  no PINPOINT_E2E_MUTABLE_CONFIG to rewrite (run through run_e2e.sh)"
fi

echo ""
echo "Sampling reload (agent restart)"
http_request POST "$BASE_URL/agent/reload?counter_rate=2" --max-time 60
assert_status "reload counter sampling" 200
assert_contains "reload counter rate" '"counter_rate":2'
# /agent/reload restarts the agent and waits up to 30s for it to register;
# confirm it did, or the probe below would only see noop tracers.
wait_for_body "reloaded upstream agent" "$BASE_URL/ready" \
    '"agent_enabled":true' || true
probe_sampling "count=40"
assert_status "sampling probe" 200
# A restarted counter sampler admits the first trace and every second one.
assert_sampled_between "counter sampling 1/2" 20 20

# The percent sampler is a counter too, so 50% of a fresh one is exact.
reload_sampler "reload percent sampling" "counter_rate=1&sampling_type=PERCENT&percent_rate=50"
assert_contains "reload percent type" '"sampling_type":"PERCENT"'
probe_sampling "count=40"
assert_sampled_between "percent sampling 50%" 20 20

# A throughput bucket holds one second of permits, 5 here, refilled at 5 per
# second, and the 40 probes take far less than the 200 ms one refill takes.
reload_sampler "reload new throughput" "counter_rate=1&new_throughput=5"
assert_contains "reload new throughput value" '"new_throughput":5'
probe_sampling "count=40"
assert_sampled_between "new throughput 5/s" 1 6
probe_sampling "count=40&continue=1"
assert_sampled_between "continued traces ignore new throughput" 40 40

reload_sampler "reload continue throughput" "counter_rate=1&continue_throughput=5"
assert_contains "reload continue throughput value" '"continue_throughput":5'
probe_sampling "count=40&continue=1"
assert_sampled_between "continue throughput 5/s" 1 6
probe_sampling "count=40"
assert_sampled_between "new traces ignore continue throughput" 40 40

reload_sampler "restore full sampling" "counter_rate=1"
assert_contains "restored counter type" '"sampling_type":"COUNTER"'
assert_contains "restored unlimited new throughput" '"new_throughput":0'
assert_contains "restored unlimited continue throughput" '"continue_throughput":0'
wait_for_body "restored upstream agent" "$BASE_URL/ready" \
    '"agent_enabled":true' || true

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
