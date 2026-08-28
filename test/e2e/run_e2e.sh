#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR=""
HOST="127.0.0.1"
PORT=8090
DOWNSTREAM_PORT=8091
GRPC_PORT=50051
LOAD_MODE=""
LOAD_DURATION=30
LOAD_CONCURRENCY=5
LOAD_RPS=""
MAX_ERROR_RATE=""
PROFILE=false
PROFILE_OUTPUT=""
PROFILE_SECONDS=""
LOCAL_COLLECTOR=false
KEEP_LOGS=false
LOG_DIR=""
SKIP_BUILD=false

usage() {
    cat <<USAGE
Usage: $0 [OPTIONS]

Build and run the live-collector end-to-end suite.

Options:
      --bin-dir DIR         Directory holding prebuilt binaries (default: build here)
      --skip-build          Reuse the binaries already in --bin-dir
      --host HOST           HTTP bind/check host (default: $HOST)
      --port PORT           Upstream HTTP port (default: $PORT)
      --downstream-port N   Downstream HTTP port (default: $DOWNSTREAM_PORT)
      --grpc-port N         gRPC port (default: $GRPC_PORT)
      --local-collector     Start the bundled stub collector instead of using a
                            real one (self-test of the stack; records nothing)
      --load-mode MODE      Load workload to run after the smoke checks
                            (unthrottled maximum throughput unless --load-rps)
      --load-duration SEC   Load duration (default: $LOAD_DURATION)
      --load-concurrency N  Load workers, or fixed-RPS max in-flight requests
                            (default: $LOAD_CONCURRENCY)
      --load-rps RPS        Use constant-arrival-rate load at this target RPS
      --max-error-rate PCT  Tolerated load-phase error rate, in percent
                            (default: 0 -- any failed request fails the run)
      --profile             Capture a CPU profile of the upstream server during
                            the load phase through its pprof endpoint
      --profile-output PATH Profile output file (default: under the log dir)
      --profile-seconds N   Profile duration (default: the load duration)
      --log-dir DIR         Store process logs in DIR
      --keep-logs           Keep an auto-created log directory on success
  -h, --help                Show this help

Environment:
  PINPOINT_GO_COLLECTOR_HOST must name the collector host, unless
  --local-collector is given.
USAGE
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --bin-dir) BIN_DIR=$2; shift 2 ;;
        --skip-build) SKIP_BUILD=true; shift ;;
        --host) HOST=$2; shift 2 ;;
        --port) PORT=$2; shift 2 ;;
        --downstream-port) DOWNSTREAM_PORT=$2; shift 2 ;;
        --grpc-port) GRPC_PORT=$2; shift 2 ;;
        --local-collector) LOCAL_COLLECTOR=true; shift ;;
        --load-mode) LOAD_MODE=$2; shift 2 ;;
        --load-duration) LOAD_DURATION=$2; shift 2 ;;
        --load-concurrency) LOAD_CONCURRENCY=$2; shift 2 ;;
        --load-rps) LOAD_RPS=$2; shift 2 ;;
        --max-error-rate) MAX_ERROR_RATE=$2; shift 2 ;;
        --profile) PROFILE=true; shift ;;
        --profile-output) PROFILE_OUTPUT=$2; shift 2 ;;
        --profile-seconds) PROFILE_SECONDS=$2; shift 2 ;;
        --log-dir) LOG_DIR=$2; shift 2 ;;
        --keep-logs) KEEP_LOGS=true; shift ;;
        -h|--help) usage; exit 0 ;;
        *) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
    esac
done

if [[ -n "$LOAD_RPS" && -z "$LOAD_MODE" ]]; then
    LOAD_MODE="mixed"
fi
if $PROFILE && [[ -z "$LOAD_MODE" ]]; then
    echo "--profile requires a load phase (--load-mode or --load-rps)." >&2
    exit 2
fi
if ! $PROFILE && [[ -n "$PROFILE_OUTPUT$PROFILE_SECONDS" ]]; then
    echo "--profile-output/--profile-seconds require --profile." >&2
    exit 2
fi
if [[ -n "$MAX_ERROR_RATE" && -z "$LOAD_MODE" ]]; then
    echo "--max-error-rate requires a load phase (--load-mode or --load-rps)." >&2
    exit 2
fi

if $LOCAL_COLLECTOR; then
    export PINPOINT_GO_COLLECTOR_HOST="$HOST"
elif [[ -z "${PINPOINT_GO_COLLECTOR_HOST:-}" ]]; then
    echo "PINPOINT_GO_COLLECTOR_HOST must be set (or pass --local-collector)." >&2
    exit 2
fi

if [[ -z "$BIN_DIR" ]]; then
    BIN_DIR="$SCRIPT_DIR/bin"
fi
mkdir -p "$BIN_DIR"
BIN_DIR="$(cd "$BIN_DIR" && pwd)"

if ! $SKIP_BUILD; then
    echo "Building end-to-end binaries into $BIN_DIR"
    (cd "$SCRIPT_DIR" && go build -o "$BIN_DIR/" ./cmd/...)
fi

UPSTREAM_BIN="$BIN_DIR/upstream"
DOWNSTREAM_BIN="$BIN_DIR/downstream"
GRPC_BIN="$BIN_DIR/grpcserver"
STUB_BIN="$BIN_DIR/stubcollector"

for binary in "$UPSTREAM_BIN" "$DOWNSTREAM_BIN" "$GRPC_BIN"; do
    if [[ ! -x "$binary" ]]; then
        echo "Missing end-to-end binary: $binary" >&2
        echo "Build with: (cd $SCRIPT_DIR && go build -o bin/ ./cmd/...)" >&2
        exit 2
    fi
done
if $LOCAL_COLLECTOR && [[ ! -x "$STUB_BIN" ]]; then
    echo "Missing stub collector binary: $STUB_BIN" >&2
    exit 2
fi

if [[ -z "$LOG_DIR" ]]; then
    LOG_DIR=$(mktemp -d "${TMPDIR:-/tmp}/pinpoint-go-e2e.XXXXXX")
    AUTO_LOG_DIR=true
else
    mkdir -p "$LOG_DIR"
    LOG_DIR="$(cd "$LOG_DIR" && pwd)"
    AUTO_LOG_DIR=false
fi

export PINPOINT_GO_COLLECTOR_HOST
export PINPOINT_GO_CONFIGFILE="${PINPOINT_GO_CONFIGFILE:-$SCRIPT_DIR/pinpoint-config.yaml}"
# Debug level, because the transport evidence below reads the span-batch lines
# the agent only logs at that level.
export PINPOINT_GO_LOG_LEVEL="${PINPOINT_GO_LOG_LEVEL:-debug}"
export PINPOINT_E2E_AGENT_TIMEOUT="${PINPOINT_E2E_AGENT_TIMEOUT:-30}"

RUN_SUFFIX="$(date +%H%M%S)-$$"
UPSTREAM_PID=""
DOWNSTREAM_PID=""
GRPC_PID=""
STUB_PID=""

stop_process() {
    local pid=$1
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
        kill "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
    fi
}

# Waits up to 5s for a process asked to shut down to leave on its own, so its
# agent gets a chance to flush instead of being killed mid-batch.
wait_exit() {
    local pid=$1 waited=0
    [[ -n "$pid" ]] || return 0
    while kill -0 "$pid" 2>/dev/null && [[ $waited -lt 50 ]]; do
        sleep 0.1
        waited=$((waited + 1))
    done
}

cleanup() {
    curl -sS --max-time 2 -X POST "http://$HOST:$PORT/server/shutdown" >/dev/null 2>&1 || true
    curl -sS --max-time 2 -X POST "http://$HOST:$DOWNSTREAM_PORT/shutdown" >/dev/null 2>&1 || true
    wait_exit "$UPSTREAM_PID"
    wait_exit "$DOWNSTREAM_PID"
    # The gRPC server has no control channel; it leaves through its SIGTERM
    # handler, so signal it rather than waiting for an exit that never comes.
    stop_process "$UPSTREAM_PID"
    stop_process "$DOWNSTREAM_PID"
    stop_process "$GRPC_PID"
    stop_process "$STUB_PID"
}
trap cleanup EXIT INT TERM

echo "==========================================="
echo " Pinpoint Go Agent - End-to-End Test Stack"
echo "==========================================="
echo "Collector: $PINPOINT_GO_COLLECTOR_HOST$($LOCAL_COLLECTOR && echo ' (bundled stub)')"
echo "Config:    $PINPOINT_GO_CONFIGFILE"
echo "Binaries:  $BIN_DIR"
echo "Logs:      $LOG_DIR"
echo "Run ID:    $RUN_SUFFIX"
echo ""

if $LOCAL_COLLECTOR; then
    "$STUB_BIN" >"$LOG_DIR/stubcollector.log" 2>&1 &
    STUB_PID=$!
    sleep 1
fi

PINPOINT_GO_APPLICATIONNAME="go-e2e-grpc-downstream" \
PINPOINT_GO_AGENTNAME="e2e-grpc-$RUN_SUFFIX" \
PINPOINT_GO_AGENTID="e2e-grpc-$RUN_SUFFIX" \
    "$GRPC_BIN" "$GRPC_PORT" >"$LOG_DIR/grpcserver.log" 2>&1 &
GRPC_PID=$!

PINPOINT_GO_APPLICATIONNAME="go-e2e-http-downstream" \
PINPOINT_GO_AGENTNAME="e2e-down-$RUN_SUFFIX" \
PINPOINT_GO_AGENTID="e2e-down-$RUN_SUFFIX" \
    "$DOWNSTREAM_BIN" "$DOWNSTREAM_PORT" >"$LOG_DIR/downstream.log" 2>&1 &
DOWNSTREAM_PID=$!

GRPC_TARGET="$HOST:$GRPC_PORT" HTTP_TARGET="$HOST:$DOWNSTREAM_PORT" \
PINPOINT_GO_APPLICATIONNAME="go-e2e-http-upstream" \
PINPOINT_GO_AGENTNAME="e2e-up-$RUN_SUFFIX" \
PINPOINT_GO_AGENTID="e2e-up-$RUN_SUFFIX" \
    "$UPSTREAM_BIN" "$PORT" >"$LOG_DIR/upstream.log" 2>&1 &
UPSTREAM_PID=$!

sleep 1
for process in "$GRPC_PID:$GRPC_BIN" "$DOWNSTREAM_PID:$DOWNSTREAM_BIN" \
               "$UPSTREAM_PID:$UPSTREAM_BIN"; do
    pid=${process%%:*}
    name=${process#*:}
    if ! kill -0 "$pid" 2>/dev/null; then
        echo "Process exited during startup: $name" >&2
        echo "See $LOG_DIR" >&2
        exit 1
    fi
done

set +e
env "HOST=$HOST" "PORT=$PORT" "DOWNSTREAM_PORT=$DOWNSTREAM_PORT" \
    bash "$SCRIPT_DIR/smoke_test.sh"
RESULT=$?
set -e

if [[ -n "$LOAD_MODE" ]]; then
    echo ""
    if [[ -n "$LOAD_RPS" ]]; then
        echo "Running fixed-RPS load mode: $LOAD_MODE at $LOAD_RPS RPS"
        LOAD_KIND="fixed-rps"
    else
        echo "Running maximum-throughput load mode: $LOAD_MODE"
        LOAD_KIND="max-throughput"
    fi

    PROFILE_PID=""
    if $PROFILE; then
        if [[ -z "$PROFILE_OUTPUT" ]]; then
            PROFILE_OUTPUT="$LOG_DIR/profiles/${LOAD_KIND}-${RUN_SUFFIX}.pprof"
            KEEP_LOGS=true
        fi
        mkdir -p "$(dirname "$PROFILE_OUTPUT")"
        SECONDS_ARG="${PROFILE_SECONDS:-$LOAD_DURATION}"
        echo "Capturing a ${SECONDS_ARG}s CPU profile to $PROFILE_OUTPUT"
        curl -sS --max-time $((SECONDS_ARG + 30)) -o "$PROFILE_OUTPUT" \
            "http://$HOST:$PORT/debug/pprof/profile?seconds=$SECONDS_ARG" &
        PROFILE_PID=$!
    fi

    set +e
    python3 "$SCRIPT_DIR/load_test.py" \
        --base-url "http://$HOST:$PORT" --mode "$LOAD_MODE" \
        --duration "$LOAD_DURATION" --concurrency "$LOAD_CONCURRENCY" \
        --rss-pid "$UPSTREAM_PID" \
        ${LOAD_RPS:+--rps "$LOAD_RPS"} \
        ${MAX_ERROR_RATE:+--max-error-rate "$MAX_ERROR_RATE"}
    LOAD_RESULT=$?
    set -e
    if [[ -n "$PROFILE_PID" ]]; then
        wait "$PROFILE_PID" || echo "profile capture failed" >&2
        echo "Inspect it with: go tool pprof $PROFILE_OUTPUT"
    fi
    if [[ $LOAD_RESULT -ne 0 ]]; then
        RESULT=$LOAD_RESULT
    fi
fi

# Give the async span sender time to flush before reading the transport log.
sleep 3

echo ""
echo "Collector transport evidence"
for log in upstream.log downstream.log grpcserver.log; do
    if grep -q 'success to register agent' "$LOG_DIR/$log"; then
        echo "  PASS  $log registered with collector"
    else
        echo "  FAIL  $log has no successful agent registration" >&2
        RESULT=1
    fi
done
if grep -q 'SendSpanBatch size=' "$LOG_DIR/upstream.log"; then
    echo "  PASS  upstream sent span batches to the collector"
else
    echo "  FAIL  upstream log shows no span batch" >&2
    RESULT=1
fi
if grep -q 'SendSpanBatch failed' "$LOG_DIR/upstream.log"; then
    echo "  FAIL  upstream log shows a failed span batch" >&2
    grep -m 3 'SendSpanBatch failed' "$LOG_DIR/upstream.log" >&2
    RESULT=1
else
    echo "  PASS  no span batch was rejected"
fi

if [[ $RESULT -ne 0 ]]; then
    echo ""
    echo "End-to-end test failed. Logs kept at: $LOG_DIR" >&2
    exit "$RESULT"
fi

echo ""
echo "End-to-end test passed."
if $KEEP_LOGS || ! $AUTO_LOG_DIR; then
    echo "Logs: $LOG_DIR"
else
    rm -rf "$LOG_DIR"
fi
