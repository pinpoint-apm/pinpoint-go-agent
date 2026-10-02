#!/usr/bin/env bash
#
# Long-running fixed-rate soak for the live e2e stack, the Go counterpart of
# the C++ agent's test/e2e/soak_test.sh, with the same default shape:
# 12 h / 100 RPS / full mode / 30 % sampling.
#
# Two phases, because the smoke suite and a reduced-sampling soak cannot be one
# run: the smoke assertions hold only when every request is sampled, and the
# transport check reads debug-level lines a 12 h run cannot afford. So
# run_e2e.sh runs first, unmodified and at full sampling, as a gate; then
# run_e2e.sh --load-only is the soak proper, at the requested sampling and the
# config file's info level, while this script samples the upstream server.
#
# The soak's real output is the resource time series, not a pass/fail. A single
# first/max/last RSS triple cannot tell "warmed up, then flat" from "leaking
# slowly"; 720 samples can.
#
# Usage:
#   export PINPOINT_GO_COLLECTOR_HOST=your-collector-host
#   ./soak_test.sh
#
# Detached (survives logout); stdout is only a heartbeat, every artifact lands
# in the output directory:
#   nohup ./soak_test.sh >/dev/null 2>&1 &
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

OUT_DIR=""
MODE="full"
RPS=100
DURATION=43200            # 12 h
SAMPLING_RATE=30          # percent of requests traced
# The in-flight ceiling: an arrival past it is dropped rather than queued into
# a burst, and the load generator fails the run past 5 % dropped.
CONCURRENCY=16
# The load generator evaluates this once, after the run, so it only decides whether
# one transient blip in 4.3 M requests marks the soak failed. 0 (its default)
# is the wrong choice for a 12 h unattended run.
MAX_ERROR_RATE="0.1"
SAMPLE_INTERVAL=60
HEARTBEAT_EVERY=30        # samples between stdout heartbeats
RUN_VALIDATE=true
HOST=127.0.0.1
PORT=8090
DOWNSTREAM_PORT=8091
GRPC_PORT=50051

usage() {
    cat <<EOF
Usage: $0 [OPTIONS]

Run the smoke suite as a gate, then load the e2e stack at a fixed rate for
hours while sampling the upstream server's resources.

Options:
      --out-dir DIR       Artifact directory (default: soak-<timestamp> beside this script)
      --mode MODE         load generator mode (default: $MODE)
      --rps RPS           Fixed arrival rate (default: $RPS)
      --duration SEC      Soak duration (default: $DURATION = 12h)
      --sampling-rate PCT Trace sampling percent (default: $SAMPLING_RATE)
      --concurrency N     Max in-flight requests (default: $CONCURRENCY)
      --max-error-rate P  Tolerated final error rate (default: $MAX_ERROR_RATE)
      --sample-interval S Resource sampling period (default: $SAMPLE_INTERVAL)
      --skip-validate     Skip the phase-1 smoke gate (not recommended)
      --port N            Upstream port (default: $PORT)
      --downstream-port N Downstream port (default: $DOWNSTREAM_PORT)
      --grpc-port N       gRPC port (default: $GRPC_PORT)
  -h, --help              Show this help

Environment:
  PINPOINT_GO_COLLECTOR_HOST must name the collector host.
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --out-dir)          OUT_DIR=$2; shift 2 ;;
        --mode)             MODE=$2; shift 2 ;;
        --rps)              RPS=$2; shift 2 ;;
        --duration)         DURATION=$2; shift 2 ;;
        --sampling-rate)    SAMPLING_RATE=$2; shift 2 ;;
        --concurrency)      CONCURRENCY=$2; shift 2 ;;
        --max-error-rate)   MAX_ERROR_RATE=$2; shift 2 ;;
        --sample-interval)  SAMPLE_INTERVAL=$2; shift 2 ;;
        --skip-validate)    RUN_VALIDATE=false; shift ;;
        --port)             PORT=$2; shift 2 ;;
        --downstream-port)  DOWNSTREAM_PORT=$2; shift 2 ;;
        --grpc-port)        GRPC_PORT=$2; shift 2 ;;
        -h|--help)          usage; exit 0 ;;
        *) echo "Unknown option: $1" >&2; usage >&2; exit 2 ;;
    esac
done

# ==========================================================================
# Preflight -- every check here would otherwise surface hours in
# ==========================================================================
fail() { echo "PREFLIGHT FAIL: $*" >&2; exit 2; }
stamp() { date +%Y-%m-%dT%H:%M:%S%z; }
# python3 rather than nc or timeout, whose flags differ between macOS and Linux;
# the summary needs it anyway.
reachable() {
    python3 -c 'import socket, sys; socket.create_connection((sys.argv[1], int(sys.argv[2])), 5)' \
        "$1" "$2" 2>/dev/null
}

[[ -n "${PINPOINT_GO_COLLECTOR_HOST:-}" ]] || fail "PINPOINT_GO_COLLECTOR_HOST must be set."
for v in RPS DURATION SAMPLE_INTERVAL CONCURRENCY; do
    [[ "${!v}" =~ ^[0-9]+$ && "${!v}" -gt 0 ]] || fail "$v must be a positive integer."
done

# The collector is the dependency that makes the whole run worthless if absent,
# and phase 1 would only discover it after the smoke suite has run.
COLLECTOR_PORT="${PINPOINT_GO_COLLECTOR_AGENTPORT:-9991}"
reachable "$PINPOINT_GO_COLLECTOR_HOST" "$COLLECTOR_PORT" \
    || fail "collector $PINPOINT_GO_COLLECTOR_HOST:$COLLECTOR_PORT is not reachable."
for p in "$PORT" "$DOWNSTREAM_PORT" "$GRPC_PORT"; do
    reachable 127.0.0.1 "$p" && fail "port $p is already in use (another stack running?)."
done

: "${OUT_DIR:=$SCRIPT_DIR/soak-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$OUT_DIR"; OUT_DIR="$(cd "$OUT_DIR" && pwd)"
CSV="$OUT_DIR/resources.csv"
LOAD_LOG="$OUT_DIR/load.log"
VALIDATE_LOG="$OUT_DIR/validate.log"
SLOG="$OUT_DIR/logs"

# A Mac that idle-sleeps mid-run stops the arrivals, and the load generator then fails
# the run on dropped arrivals; hold the machine awake while this script lives.
if command -v caffeinate >/dev/null; then caffeinate -is -w $$ & fi

hours=$(awk "BEGIN {printf \"%.2f\", $DURATION / 3600}")
cat <<EOF
============================================================
 Pinpoint Go agent - fixed-rate soak
============================================================
Collector    : $PINPOINT_GO_COLLECTOR_HOST
Load         : $MODE at $RPS RPS, max $CONCURRENCY in flight
Duration     : ${DURATION}s (${hours} h)  ~$(( RPS * DURATION )) requests
Sampling     : PERCENT $SAMPLING_RATE %
Error budget : $MAX_ERROR_RATE %
Artifacts    : $OUT_DIR
Started      : $(stamp)
============================================================
EOF

# ==========================================================================
# Phase 1 -- the documented suite as a gate, at its own settings
# ==========================================================================
if $RUN_VALIDATE; then
    echo ""
    echo "--- phase 1/2: correctness gate (run_e2e.sh, full sampling, no load) ---"
    # No sampling and no log level from the caller: the smoke assertions need
    # every request sampled, and the transport check needs debug-level lines.
    if env -u PINPOINT_GO_SAMPLING_TYPE -u PINPOINT_GO_SAMPLING_COUNTERRATE \
           -u PINPOINT_GO_SAMPLING_PERCENTRATE -u PINPOINT_GO_LOG_LEVEL \
           "$SCRIPT_DIR/run_e2e.sh" \
           --port "$PORT" --downstream-port "$DOWNSTREAM_PORT" --grpc-port "$GRPC_PORT" \
           --log-dir "$OUT_DIR/validate" > "$VALIDATE_LOG" 2>&1; then
        echo "    $(grep -m1 'Smoke results' "$VALIDATE_LOG" || echo 'smoke passed')"
    else
        echo "phase 1 FAILED -- not starting a ${hours} h soak on a broken stack." >&2
        grep -E "FAIL|Smoke results" "$VALIDATE_LOG" | head -20 >&2
        echo "full log: $VALIDATE_LOG" >&2
        exit 1
    fi
fi

# ==========================================================================
# Phase 2 -- the soak
# ==========================================================================
if [[ "$(uname)" == Darwin ]]; then
    threads() { ps -M -p "$1" | awk 'END {print NR - 1}'; }
    fds() { lsof -n -P -p "$1" -Ff 2>/dev/null | grep -cE '^f[0-9]+$'; }
else
    threads() { ps -o nlwp= -p "$1" | tr -d ' '; }
    fds() { ls "/proc/$1/fd" 2>/dev/null | awk 'END {print NR}'; }
fi
json_field() { grep -o "\"$1\":[a-z0-9]*" <<<"$2" | cut -d: -f2; }

sampler() {
    # A monitoring loop must outlive what it monitors: with errexit and pipefail
    # inherited, the first /stats that did not answer would end the series at
    # exactly the moment it got interesting.
    set +e
    echo "iso_time,elapsed_s,rss_kb,threads,fds,cpu_s,goroutines,heap_live_kb,agent_enabled,total_requests" > "$CSV"
    local start n=0 now rss cpu thr fd st gor heap enabled total
    start=$(date +%s)
    while kill -0 "$UPSTREAM_PID" 2>/dev/null; do
        read -r rss cpu <<<"$(ps -o rss=,time= -p "$UPSTREAM_PID")"
        [[ -n "$rss" ]] || break
        # [[dd-]hh:]mm:ss[.ss] on Linux and macOS alike, to seconds.
        cpu=$(awk -F'[-:]' '{m = 1; for (i = NF; i > 0; i--) {s += $i * m; m *= (m < 3600 ? 60 : 24)}
                            printf "%.2f", s}' <<<"$cpu")
        thr=$(threads "$UPSTREAM_PID")
        # An fd leak is a classic soak finding and is invisible in RSS.
        fd=$(fds "$UPSTREAM_PID")
        # agent_enabled going false mid-soak is the single most important signal
        # here: the agent stopping silently looks identical to a healthy run in
        # every request-side metric. A /stats that did not answer leaves the
        # in-process columns empty rather than zero.
        st=$(curl -sS --max-time 5 "http://$HOST:$PORT/stats" 2>/dev/null)
        enabled=$(json_field agent_enabled "$st"); : "${enabled:=unknown}"
        total=$(json_field total_requests "$st")
        gor=$(json_field goroutines "$st")
        heap=$(json_field heap_live_bytes "$st"); [[ -n "$heap" ]] && heap=$(( heap / 1024 ))
        now=$(date +%s)
        printf '%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n' "$(stamp)" "$(( now - start ))" \
            "$rss" "$thr" "$fd" "$cpu" "$gor" "$heap" "$enabled" "$total" >> "$CSV"
        # The latest goroutine dump and heap profile, so a GROWING verdict can
        # be traced to its stacks without rerunning the soak.
        curl -sf --max-time 10 -o "$OUT_DIR/goroutines.tmp" \
            "http://$HOST:$PORT/debug/pprof/goroutine?debug=1" \
            && mv "$OUT_DIR/goroutines.tmp" "$OUT_DIR/goroutines.txt"
        curl -sf --max-time 10 -o "$OUT_DIR/heap.tmp" "http://$HOST:$PORT/debug/pprof/heap" \
            && mv "$OUT_DIR/heap.tmp" "$OUT_DIR/heap.pprof"
        n=$(( n + 1 ))
        if (( n % HEARTBEAT_EVERY == 0 )); then
            printf '[%s] +%dh%02dm  rss=%d MB  heap=%s MB  goroutines=%s  thr=%s  fds=%s  cpu=%ss  agent=%s  req=%s\n' \
                "$(date +%H:%M:%S)" "$(( (now-start)/3600 ))" "$(( ((now-start)%3600)/60 ))" \
                "$(( rss / 1024 ))" "$(( ${heap:-0} / 1024 ))" "$gor" "$thr" "$fd" "${cpu%.*}" \
                "$enabled" "$total"
        fi
        sleep "$SAMPLE_INTERVAL"
    done
}

summarise() {
    # Nothing in here may cut the summary short, an empty grep least of all.
    set +e
    local status=$1
    echo ""
    echo "============================================================"
    echo " Soak summary   (phase 2 exit $status)"
    echo "============================================================"
    [[ -s "$LOAD_LOG" ]] && sed -n '/Results/,$p' "$LOAD_LOG" | grep -vE '^ *[0-9]+\.[0-9]+ \|'
    echo ""
    # Grouped by signature rather than counted: full mode deliberately crosses
    # the e2e config's reduced Span.MaxCallStackDepth/MaxCallStackSequence, so
    # a bare count reads as a problem when the commonest entry is an expected
    # one. The agent throttles a repeating warning to one line a minute, so the
    # line count stays small over 12 h even though the event count does not.
    local warnfile="$OUT_DIR/warnings.txt" warns
    grep -hE 'level=(warning|error)' "$SLOG"/*.log 2>/dev/null \
        | sed -E 's/^time="[^"]*" //; s/[0-9]+/N/g' | sort | uniq -c | sort -rn > "$warnfile"
    warns=$(awk '{s += $1} END {print s + 0}' "$warnfile")
    echo "warning/error log lines across the three server logs: $warns"
    [[ "$warns" -gt 0 ]] && sed -n '1,5p' "$warnfile" | cut -c1-200 | sed 's/^/    /'
    if [[ -s "$CSV" ]]; then
        python3 - "$CSV" <<'PY'
import sys, csv
rows = list(csv.DictReader(open(sys.argv[1])))
if len(rows) < 4:
    print("resource series too short to summarise"); sys.exit()

def series(key, scale=1.0):
    # (elapsed, value) pairs; a sample whose /stats did not answer is a gap.
    return [(float(r["elapsed_s"]), float(r[key]) / scale) for r in rows if r[key]]

# Least-squares slope and its standard error, both per hour. The obvious
# alternative -- last value minus the window minimum -- is positive for any
# series that merely oscillates, so it cannot tell jitter from a trend and
# reports growth on a healthy run.
def trend_per_hour(pts):
    n = len(pts)
    if n < 3:
        return 0.0, float("inf")
    mx = sum(x for x, _ in pts)/n
    my = sum(y for _, y in pts)/n
    den = sum((x-mx)**2 for x, _ in pts)
    if den == 0:
        return 0.0, float("inf")
    b = sum((x-mx)*(y-my) for x, y in pts)/den
    a = my - b*mx
    resid = sum((y - (a + b*x))**2 for x, y in pts)
    return b*3600, (((resid/(n-2))/den) ** 0.5) * 3600

# +0.5/h is +12/day, still worth a look. But magnitude alone is not enough: a
# slope fitted to pure jitter is not zero either, and its noise floor grows with
# the jitter's amplitude, so a wide-but-flat band can clear any fixed threshold
# by luck. Growth must also stand clear of its own standard error.
SLOPE_LIMIT = 0.5
SLOPE_SIGMA = 2.0

el = [float(r["elapsed_s"]) for r in rows]
hours = el[-1]/3600
# Judged on the second half only: the first carries the allocator, pool and
# connection warm-up, a one-off step that would read as a trend against sample 0.
half = el[len(el)//2]
print(f"\nResource series: {len(rows)} samples over {hours:.2f} h")
growing = []
for key, label, scale, unit in (("rss_kb", "RSS", 1024, " MB"),
                                ("heap_live_kb", "live heap", 1024, " MB"),
                                ("goroutines", "goroutines", 1, ""),
                                ("threads", "threads", 1, ""),
                                ("fds", "fds", 1, "")):
    pts = series(key, scale)
    sec = [p for p in pts if p[0] >= half]
    if len(sec) < 3:
        print(f"  {label:10s} too few samples"); continue
    v = [y for _, y in pts]
    drift, err = trend_per_hour(sec)
    f = ".1f" if unit else ".0f"
    flag = drift > SLOPE_LIMIT and drift > SLOPE_SIGMA * err
    if flag:
        growing.append(label)
    # A wide band with a flat slope is a busy pool idling, which is what
    # goroutines and fds normally look like here; +0.9 +/- 0.8 per hour is
    # noise wearing a trend's clothes.
    print(f"  {label:10s} first {v[0]:7{f}}{unit:3s}  max {max(v):7{f}}  last {v[-1]:7{f}}"
          f"   2nd half {min(y for _, y in sec):{f}}-{max(y for _, y in sec):{f}}"
          f" at {drift:+.3f}+/-{err:.3f}{unit}/h" + ("   <-- GROWING" if flag else ""))
cpu = series("cpu_s")
span = cpu[-1][0] - cpu[0][0]
used = cpu[-1][1] - cpu[0][1]
print(f"  CPU        {used:.1f} s over the series ({used/span*100:.2f} % of one core)")
reqs = series("total_requests")
if len(reqs) > 1 and reqs[-1][1] > reqs[0][1]:
    n = reqs[-1][1] - reqs[0][1]
    print(f"  CPU/req    {used/n*1e6:.0f} us   over {n:,.0f} server-side requests")
bad = [r["iso_time"] for r in rows if r["agent_enabled"] != "true"]
print(f"  agent_enabled != true at {len(bad)} of {len(rows)} samples"
      + (f"   FIRST: {bad[0]}" if bad else ""))
if hours < 1:
    print("\n  Verdict: run too short for a leak verdict -- still warming up.")
elif growing:
    print(f"\n  Verdict: sustained growth in {', '.join(growing)} -- investigate"
          " (goroutines.txt, go tool pprof heap.pprof).")
else:
    print("\n  Verdict: flat -- no leak signal.")
PY
    fi
    cat <<EOF

Artifacts:
  this summary   $OUT_DIR/summary.txt
  phase-2 log    $LOAD_LOG
  resource CSV   $CSV
  server logs    $SLOG
  last profiles  $OUT_DIR/goroutines.txt, $OUT_DIR/heap.pprof
  phase-1 log    $VALIDATE_LOG
Finished       $(stamp)
EOF
    return 0
}

echo ""
echo "--- phase 2/2: ${hours} h soak at ${SAMPLING_RATE} % sampling ---"

export PINPOINT_GO_SAMPLING_TYPE=PERCENT
export PINPOINT_GO_SAMPLING_PERCENTRATE="$SAMPLING_RATE"

RUN_PID=""; SAMPLER_PID=""; UPSTREAM_PID=""
on_signal() {
    set +e
    echo "" >&2
    echo "interrupted at $(stamp) -- stopping and summarising what we have" >&2
    # The load generator runs as a background job, which ignores SIGINT, and
    # run_e2e.sh holds its own TERM until that foreground child ends: TERM both,
    # and run_e2e.sh shuts the stack down on its way out. Not a bare wait,
    # which would also wait for caffeinate, itself waiting for this script.
    pkill -TERM -P "$RUN_PID" -f bin/load 2>/dev/null
    kill -TERM "$RUN_PID" 2>/dev/null
    wait "$RUN_PID" 2>/dev/null
    [[ -n "$SAMPLER_PID" ]] && kill "$SAMPLER_PID" 2>/dev/null && wait "$SAMPLER_PID" 2>/dev/null
    summarise 130 | tee "$OUT_DIR/summary.txt"
    exit 130
}
trap on_signal INT TERM

"$SCRIPT_DIR/run_e2e.sh" --load-only \
    --load-mode "$MODE" --load-rps "$RPS" --load-duration "$DURATION" \
    --load-concurrency "$CONCURRENCY" --max-error-rate "$MAX_ERROR_RATE" \
    --port "$PORT" --downstream-port "$DOWNSTREAM_PORT" --grpc-port "$GRPC_PORT" \
    --log-dir "$SLOG" > "$LOAD_LOG" 2>&1 &
RUN_PID=$!

# Sampled from the moment the agent is online, which is when run_e2e.sh starts
# the load; before it, agent_enabled would read false for reasons that are not
# a finding. The upstream reports its own pid, so no process matching is needed.
while kill -0 "$RUN_PID" 2>/dev/null; do
    st=$(curl -sS --max-time 2 "http://$HOST:$PORT/stats" 2>/dev/null) || true
    if [[ "$st" == *'"agent_enabled":true'* ]]; then
        UPSTREAM_PID=$(json_field pid "$st") || true
        break
    fi
    sleep 1
done
if [[ -n "$UPSTREAM_PID" ]]; then
    echo "    agent online, upstream pid $UPSTREAM_PID; heartbeat every $(( HEARTBEAT_EVERY * SAMPLE_INTERVAL / 60 )) min"
    sampler &
    SAMPLER_PID=$!
fi

set +e      # from here on nothing may cut the summary short
wait "$RUN_PID"
STATUS=$?
trap - INT TERM
[[ -n "$SAMPLER_PID" ]] && kill "$SAMPLER_PID" 2>/dev/null && wait "$SAMPLER_PID" 2>/dev/null
[[ -n "$UPSTREAM_PID" ]] || { echo "the agent never came online:" >&2; tail -20 "$LOAD_LOG" >&2; }
# Also on file: a detached run's stdout is usually /dev/null.
summarise "$STATUS" | tee "$OUT_DIR/summary.txt"
exit "$STATUS"
