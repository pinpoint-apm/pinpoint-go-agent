#!/usr/bin/env python3
"""Load test for the e2e integration server.

With --rps: constant-arrival-rate mode — requests are paced by monotonic
deadlines rather than completion rate, and --concurrency bounds in-flight
requests (saturated arrivals are dropped).

Without --rps: unthrottled maximum-throughput mode — --concurrency workers
reuse connections and issue requests back to back after an unmeasured
--warmup window.
"""

import argparse
import concurrent.futures
import http.client
import json
import math
import os
import subprocess
import sys
import threading
import time
from collections import Counter
from dataclasses import dataclass
from typing import Dict, List, Optional, Sequence, Tuple
from urllib.parse import urlsplit


@dataclass(frozen=True)
class Endpoint:
    path: str
    expected_status: int = 200


WORKLOADS: Dict[str, Tuple[Endpoint, ...]] = {
    "simple": (Endpoint("/simple"),),
    "deep": (
        Endpoint("/deep?depth=10"),
        Endpoint("/deep?depth=30"),
        Endpoint("/deep?depth=50"),
    ),
    "wide": (
        Endpoint("/wide?width=20"),
        Endpoint("/wide?width=100"),
        Endpoint("/wide?width=300"),
    ),
    "annotated": (Endpoint("/annotated"),),
    "features": (Endpoint("/features"),),
    "http": (
        Endpoint("/http-client"),
        Endpoint("/http-client?error=1"),
    ),
    "limits": (
        Endpoint("/deep?depth=32"),
        Endpoint("/wide?width=256"),
    ),
    "mixed": (
        Endpoint("/simple"),
        Endpoint("/deep?depth=10"),
        Endpoint("/deep?depth=30"),
        Endpoint("/wide?width=20"),
        Endpoint("/wide?width=100"),
        Endpoint("/annotated"),
        Endpoint("/features"),
        Endpoint("/mixed"),
        Endpoint("/error", expected_status=500),
    ),
    "stress": (),  # Populated from mixed below; RPS controls stress intensity.
    "db-batch": (
        Endpoint("/db-batch?size=10"),
        Endpoint("/db-batch?size=50"),
        Endpoint("/db-batch?size=100"),
    ),
    "db-complex": (Endpoint("/db-complex"),),
    "db-all": (
        Endpoint("/db-batch?size=10"),
        Endpoint("/db-batch?size=50"),
        Endpoint("/db-complex"),
    ),
    "grpc-unary": (Endpoint("/grpc-unary"),),
    "grpc-stream": (Endpoint("/grpc-stream"),),
    "grpc-client-stream": (Endpoint("/grpc-client-stream?count=5"),),
    "grpc-bidi": (
        Endpoint("/grpc-bidi?count=3"),
        Endpoint("/grpc-bidi?count=10"),
    ),
    "grpc-all": (
        Endpoint("/grpc-unary"),
        Endpoint("/grpc-stream"),
        Endpoint("/grpc-client-stream?count=5"),
        Endpoint("/grpc-bidi?count=3"),
        Endpoint("/grpc-all"),
    ),
    "full": (
        Endpoint("/simple"),
        Endpoint("/deep?depth=10"),
        Endpoint("/deep?depth=30"),
        Endpoint("/wide?width=20"),
        Endpoint("/wide?width=100"),
        Endpoint("/annotated"),
        Endpoint("/features"),
        Endpoint("/mixed"),
        Endpoint("/error", expected_status=500),
        Endpoint("/http-client"),
        Endpoint("/grpc-unary"),
        Endpoint("/grpc-stream"),
        Endpoint("/grpc-client-stream?count=5"),
        Endpoint("/grpc-bidi?count=3"),
        Endpoint("/grpc-all"),
        Endpoint("/db-batch?size=20"),
        Endpoint("/db-complex"),
    ),
}
WORKLOADS["stress"] = WORKLOADS["mixed"]


def positive_float(value: str) -> float:
    parsed = float(value)
    if not math.isfinite(parsed) or parsed <= 0:
        raise argparse.ArgumentTypeError("must be a finite number greater than zero")
    return parsed


def non_negative_float(value: str) -> float:
    parsed = float(value)
    if not math.isfinite(parsed) or parsed < 0:
        raise argparse.ArgumentTypeError(
            "must be a finite number greater than or equal to zero"
        )
    return parsed


def positive_int(value: str) -> int:
    parsed = int(value)
    if parsed <= 0:
        raise argparse.ArgumentTypeError("must be greater than zero")
    return parsed


def percentage(value: str) -> float:
    parsed = float(value)
    if not math.isfinite(parsed) or not 0 <= parsed <= 100:
        raise argparse.ArgumentTypeError("must be between 0 and 100")
    return parsed


@dataclass(frozen=True)
class ServerAddress:
    scheme: str
    host: str
    port: int
    base_path: str

    def target(self, endpoint_path: str) -> str:
        return self.base_path.rstrip("/") + endpoint_path


def parse_server_address(base_url: str) -> ServerAddress:
    parsed = urlsplit(base_url)
    if parsed.scheme not in ("http", "https"):
        raise ValueError("base URL scheme must be http or https")
    if not parsed.hostname:
        raise ValueError("base URL must include a host")
    if parsed.query or parsed.fragment:
        raise ValueError("base URL must not include a query or fragment")
    port = parsed.port or (443 if parsed.scheme == "https" else 80)
    return ServerAddress(parsed.scheme, parsed.hostname, port, parsed.path.rstrip("/"))


class HttpClient:
    """One reusable HTTP connection per worker thread."""

    def __init__(
        self,
        server: ServerAddress,
        timeout: float,
        user_agent: str = "pinpoint-e2e-load-test/1.0",
    ) -> None:
        self.server = server
        self.timeout = timeout
        self.user_agent = user_agent
        self.local = threading.local()

    def _new_connection(self) -> http.client.HTTPConnection:
        connection_type = (
            http.client.HTTPSConnection
            if self.server.scheme == "https"
            else http.client.HTTPConnection
        )
        return connection_type(self.server.host, self.server.port, timeout=self.timeout)

    def _connection(self) -> http.client.HTTPConnection:
        connection = getattr(self.local, "connection", None)
        if connection is None:
            connection = self._new_connection()
            self.local.connection = connection
        return connection

    def get(self, path: str) -> int:
        connection = self._connection()
        try:
            connection.request(
                "GET",
                self.server.target(path),
                headers={"User-Agent": self.user_agent},
            )
            response = connection.getresponse()
            response.read()
            status = response.status
            if response.will_close:
                connection.close()
                self.local.connection = None
            return status
        except Exception:
            connection.close()
            self.local.connection = None
            raise


def get_json(server: ServerAddress, path: str, timeout: float) -> dict:
    connection_type = (
        http.client.HTTPSConnection
        if server.scheme == "https"
        else http.client.HTTPConnection
    )
    connection = connection_type(server.host, server.port, timeout=timeout)
    try:
        connection.request(
            "GET",
            server.target(path),
            headers={"User-Agent": "pinpoint-e2e-load-test/1.0"},
        )
        response = connection.getresponse()
        body = response.read().decode("utf-8", errors="replace")
        if not 200 <= response.status < 300:
            raise RuntimeError(f"GET {path} returned HTTP {response.status}: {body}")
        decoded = json.loads(body)
        if not isinstance(decoded, dict):
            raise RuntimeError(f"GET {path} did not return a JSON object")
        return decoded
    finally:
        connection.close()


def preflight(server: ServerAddress, timeout: float, require_agent: bool) -> dict:
    stats = get_json(server, "/stats", min(timeout, 5.0))
    missing_stats = {"total_requests", "active_requests"} - stats.keys()
    if missing_stats:
        raise RuntimeError(
            "/stats is missing required fields: " + ", ".join(sorted(missing_stats))
        )
    if require_agent:
        get_json(server, "/ready", min(timeout, 5.0))
    return stats


class Results:
    """Latency/status accounting with 0.1 ms histograms.

    The histograms avoid retaining one float per request during long,
    high-throughput runs while keeping percentiles useful. Shared instances
    (fixed-RPS mode) are guarded by the lock; per-worker instances
    (unthrottled mode) never contend on it and are merged at the end.
    """

    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.started = 0
        self.completed = 0
        self.succeeded = 0
        self.failed = 0
        self.total_latency_ms = 0.0
        self.dropped = Counter()  # type: Counter[str]
        self.status_codes = Counter()  # type: Counter[int]
        self.latency_buckets = Counter()  # type: Counter[int]
        self.schedule_lag_buckets = Counter()  # type: Counter[int]
        self.error_samples = []  # type: List[str]

    def record_started(self, lag_seconds: float) -> None:
        with self.lock:
            self.started += 1
            self.schedule_lag_buckets[int(max(lag_seconds, 0.0) * 10000.0)] += 1

    def record_completed(
        self,
        endpoint: Endpoint,
        latency_seconds: float,
        status: Optional[int],
        error: Optional[str],
    ) -> None:
        latency_ms = latency_seconds * 1000.0
        with self.lock:
            self.completed += 1
            self.total_latency_ms += latency_ms
            self.latency_buckets[int(latency_ms * 10.0)] += 1
            if status is not None:
                self.status_codes[status] += 1
            if error is None and status == endpoint.expected_status:
                self.succeeded += 1
                return
            self.failed += 1
            if len(self.error_samples) < 5:
                if error is not None:
                    self.error_samples.append(error)
                else:
                    self.error_samples.append(
                        f"{endpoint.path}: expected HTTP {endpoint.expected_status}, "
                        f"received HTTP {status}"
                    )

    def record_dropped(self, reason: str) -> None:
        with self.lock:
            self.dropped[reason] += 1

    def snapshot(self) -> Tuple[int, int, int]:
        with self.lock:
            return self.started, self.completed, sum(self.dropped.values())


def merge_results(worker_results: Sequence[Results]) -> Results:
    merged = Results()
    for result in worker_results:
        merged.started += result.started
        merged.completed += result.completed
        merged.succeeded += result.succeeded
        merged.failed += result.failed
        merged.total_latency_ms += result.total_latency_ms
        merged.dropped.update(result.dropped)
        merged.status_codes.update(result.status_codes)
        merged.latency_buckets.update(result.latency_buckets)
        merged.schedule_lag_buckets.update(result.schedule_lag_buckets)
        for sample in result.error_samples:
            if len(merged.error_samples) >= 5:
                break
            merged.error_samples.append(sample)
    return merged


def histogram_percentile(buckets: Counter, total: int, percent: float) -> float:
    if total == 0:
        return 0.0
    threshold = math.ceil(total * percent / 100.0)
    observed = 0
    for bucket, count in sorted(buckets.items()):
        observed += count
        if observed >= threshold:
            return bucket / 10.0
    return max(buckets, default=0) / 10.0


def histogram_line(buckets: Counter, total: int) -> str:
    return (
        f"p50={histogram_percentile(buckets, total, 50):.2f}, "
        f"p95={histogram_percentile(buckets, total, 95):.2f}, "
        f"p99={histogram_percentile(buckets, total, 99):.2f}, "
        f"max={max(buckets, default=0) / 10.0:.2f}"
    )


class RssTracker:
    """Samples a process's resident set size (KB) via ps."""

    def __init__(self, pid: int) -> None:
        self.pid = pid
        self.samples = []  # type: List[int]

    def sample(self) -> Optional[int]:
        try:
            output = subprocess.run(
                ["ps", "-o", "rss=", "-p", str(self.pid)],
                capture_output=True,
                text=True,
                timeout=2.0,
            ).stdout.strip()
            kb = int(output)
        except Exception:
            return None
        self.samples.append(kb)
        return kb

    def report(self) -> None:
        if self.samples:
            print(
                f"Server RSS (KB):    first={self.samples[0]}, "
                f"max={max(self.samples)}, last={self.samples[-1]}"
            )


def server_active_requests(server: ServerAddress, timeout: float) -> str:
    try:
        return str(
            get_json(server, "/stats", min(timeout, 2.0)).get("active_requests", "?")
        )
    except Exception:
        return "?"


def print_common_results(results: Results, rss: Optional[RssTracker]) -> None:
    if results.dropped:
        print(
            "Dropped by reason:  "
            + ", ".join(
                f"{reason}={count}"
                for reason, count in sorted(results.dropped.items())
            )
        )
    if results.status_codes:
        print(
            "HTTP status codes:  "
            + ", ".join(
                f"{status}={count}"
                for status, count in sorted(results.status_codes.items())
            )
        )
    average_latency = (
        results.total_latency_ms / results.completed if results.completed else 0.0
    )
    print(
        f"Latency (ms):      avg={average_latency:.2f}, "
        + histogram_line(results.latency_buckets, results.completed)
    )
    if rss is not None:
        rss.report()
    for sample in results.error_samples:
        print(f"  ERROR: {sample}")


def check_thresholds(failures: List[str]) -> int:
    if failures:
        for failure in failures:
            print(f"FAIL: {failure}", file=sys.stderr)
        return 1
    print("PASS: load test met its configured thresholds")
    return 0


def wait_until(deadline: float, stop: threading.Event) -> bool:
    while not stop.is_set():
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            return True
        if stop.wait(remaining):
            return False
    return False


# =============================================================================
# Fixed-RPS mode
# =============================================================================


def execute_request(
    client: HttpClient,
    endpoint: Endpoint,
    deadline: float,
    max_schedule_lag: float,
    results: Results,
    capacity: threading.Semaphore,
) -> None:
    started_at = time.monotonic()
    schedule_lag = started_at - deadline
    # Thread-pool startup or host scheduling can delay work after submission.
    # Skip a request that is already a full arrival period late so it cannot
    # become part of a catch-up burst.
    if schedule_lag >= max_schedule_lag:
        results.record_dropped("worker_lag")
        capacity.release()
        return

    results.record_started(schedule_lag)
    status = None  # type: Optional[int]
    error = None  # type: Optional[str]
    try:
        status = client.get(endpoint.path)
    except Exception as exc:  # The exception type is included in the report.
        error = f"{endpoint.path}: {type(exc).__name__}: {exc}"
    finally:
        results.record_completed(
            endpoint,
            time.monotonic() - started_at,
            status,
            error,
        )
        capacity.release()


def report_fixed_rps_progress(
    done: threading.Event,
    started_at: float,
    duration: float,
    interval: float,
    target_rps: float,
    server: ServerAddress,
    results: Results,
    rss: Optional[RssTracker],
) -> None:
    previous_started = 0
    previous_time = started_at
    print(
        "Elapsed | Target RPS | Started RPS | Completed | "
        "In flight | Dropped | Server active"
    )
    print(
        "--------|------------|-------------|-----------|"
        "-----------|---------|--------------"
    )
    while not done.wait(interval):
        now = time.monotonic()
        started, completed, dropped = results.snapshot()
        sample_duration = max(now - previous_time, 1e-9)
        sample_rps = (started - previous_started) / sample_duration
        if rss is not None:
            rss.sample()
        print(
            f"{min(now - started_at, duration):7.1f} | {target_rps:10.2f} | "
            f"{sample_rps:11.2f} | {completed:9d} | {started - completed:9d} | "
            f"{dropped:7d} | {server_active_requests(server, interval):>13}",
            flush=True,
        )
        previous_started = started
        previous_time = now


def run_fixed_rps(args, server: ServerAddress, initial_stats: dict) -> int:
    endpoints = WORKLOADS[args.mode]
    interval = 1.0 / args.rps
    planned = math.floor(args.duration * args.rps + 1e-9)
    if planned < 1:
        print(
            "ERROR: duration and RPS produce no scheduled requests; increase either value",
            file=sys.stderr,
        )
        return 2

    print("=" * 64)
    print(" Pinpoint Go Agent - Fixed RPS Load Test")
    print("=" * 64)
    print(f"Server:        {args.base_url.rstrip('/')}")
    print(f"Mode:          {args.mode}")
    print(f"Target RPS:    {args.rps:.2f}")
    print(f"Duration:      {args.duration:.2f}s")
    print(f"Planned:       {planned} requests")
    print(f"Max in flight: {args.concurrency}")
    print(f"Endpoints:     {len(endpoints)} (deterministic round-robin)")
    print("=" * 64)

    client = HttpClient(server, args.timeout)
    results = Results()
    rss = RssTracker(args.rss_pid) if args.rss_pid else None
    if rss is not None:
        rss.sample()
    capacity = threading.BoundedSemaphore(args.concurrency)
    reporter_done = threading.Event()
    interrupted = False
    executor = concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency)
    # Avoid charging the first scheduled arrival for lazy worker-thread startup.
    executor.submit(lambda: None).result()
    start = time.monotonic()
    reporter = threading.Thread(
        target=report_fixed_rps_progress,
        args=(
            reporter_done,
            start,
            args.duration,
            args.report_interval,
            args.rps,
            server,
            results,
            rss,
        ),
        daemon=True,
    )
    reporter.start()

    try:
        for request_index in range(planned):
            deadline = start + (request_index + 1) * interval
            delay = deadline - time.monotonic()
            if delay > 0:
                time.sleep(delay)

            # Do not catch up by emitting a burst when an entire arrival period
            # has already elapsed. Such a request is reported as scheduler lag.
            if time.monotonic() - deadline >= interval:
                results.record_dropped("scheduler_lag")
                continue
            if not capacity.acquire(blocking=False):
                results.record_dropped("max_in_flight")
                continue

            endpoint = endpoints[request_index % len(endpoints)]
            try:
                executor.submit(
                    execute_request,
                    client,
                    endpoint,
                    deadline,
                    interval,
                    results,
                    capacity,
                )
            except Exception:
                capacity.release()
                results.record_dropped("submit_error")
                raise
    except KeyboardInterrupt:
        interrupted = True
        print("\nInterrupted; waiting for in-flight requests...", file=sys.stderr)
    finally:
        executor.shutdown(wait=True)
        reporter_done.set()
        reporter.join(timeout=args.report_interval + 1.0)

    finished = time.monotonic()
    if rss is not None:
        rss.sample()
    try:
        final_stats = get_json(server, "/stats", min(args.timeout, 5.0))
    except Exception:
        final_stats = {}

    started, completed, dropped = results.snapshot()
    error_rate = (results.failed / completed * 100.0) if completed else 100.0
    drop_rate = dropped / planned * 100.0
    achieved_rps = started / args.duration
    elapsed = finished - start
    server_delta = None
    if "total_requests" in initial_stats and "total_requests" in final_stats:
        server_delta = final_stats["total_requests"] - initial_stats["total_requests"]

    print("\n" + "=" * 64)
    print(" Results")
    print("=" * 64)
    print(f"Planned arrivals:   {planned}")
    print(f"Started requests:   {started}")
    print(f"Completed requests: {completed}")
    print(f"Successful:         {results.succeeded}")
    print(f"Failed:             {results.failed} ({error_rate:.2f}%)")
    print(f"Dropped:            {dropped} ({drop_rate:.2f}%)")
    print(f"Achieved start RPS: {achieved_rps:.2f}")
    print(f"Total wall time:    {elapsed:.2f}s")
    if server_delta is not None:
        print(f"Server request delta: {server_delta}")
    print(
        "Schedule lag (ms): "
        + histogram_line(results.schedule_lag_buckets, started)
    )
    print_common_results(results, rss)

    if interrupted:
        return 130

    failures = []
    if started == 0:
        failures.append("no workload requests were started")
    if error_rate > args.max_error_rate:
        failures.append(
            f"error rate {error_rate:.2f}% exceeds {args.max_error_rate:.2f}%"
        )
    if drop_rate > args.rps_tolerance:
        failures.append(
            f"dropped-arrival rate {drop_rate:.2f}% exceeds RPS tolerance "
            f"{args.rps_tolerance:.2f}%"
        )
    if completed != started:
        failures.append(f"only {completed} of {started} started requests completed")
    return check_thresholds(failures)


# =============================================================================
# Unthrottled maximum-throughput mode
# =============================================================================


@dataclass
class TestWindow:
    warmup_start: float = 0.0
    measurement_start: float = 0.0
    end: float = 0.0


def scalar_snapshot(worker_results: Sequence[Results]) -> tuple:
    return (
        sum(result.completed for result in worker_results),
        sum(result.failed for result in worker_results),
    )


def throughput_worker(
    worker_id: int,
    endpoints: Sequence[Endpoint],
    client: HttpClient,
    window: TestWindow,
    start: threading.Event,
    stop: threading.Event,
    results: Results,
) -> None:
    start.wait()
    if stop.is_set() or not wait_until(window.warmup_start, stop):
        return

    request_index = 0
    while not stop.is_set():
        started_at = time.monotonic()
        if started_at >= window.end:
            return

        endpoint = endpoints[(worker_id + request_index) % len(endpoints)]
        request_index += 1
        status = None  # type: Optional[int]
        error = None  # type: Optional[str]
        try:
            status = client.get(endpoint.path)
        except Exception as exc:
            error = f"{endpoint.path}: {type(exc).__name__}: {exc}"

        if started_at >= window.measurement_start:
            results.record_completed(
                endpoint, time.monotonic() - started_at, status, error
            )


def run_max_throughput(args, server: ServerAddress, initial_stats: dict) -> int:
    endpoints = WORKLOADS[args.mode]
    print("=" * 68)
    print(" Pinpoint Go Agent - Maximum Throughput Load Test")
    print("=" * 68)
    print(f"Server:       {args.base_url.rstrip('/')}")
    print(f"Mode:         {args.mode}")
    print(f"Concurrency:  {args.concurrency}")
    print(f"Warm-up:      {args.warmup:.2f}s (excluded from results)")
    print(f"Duration:     {args.duration:.2f}s")
    print(f"Endpoints:    {len(endpoints)} (deterministic rotation)")
    print("Rate limit:   none")
    print("=" * 68)

    client = HttpClient(
        server, args.timeout, user_agent="pinpoint-max-throughput-test/1.0"
    )
    rss = RssTracker(args.rss_pid) if args.rss_pid else None
    if rss is not None:
        rss.sample()
    start_event = threading.Event()
    stop_event = threading.Event()
    window = TestWindow()
    worker_results = [Results() for _ in range(args.concurrency)]
    executor = concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency)
    futures = []
    try:
        for worker_id in range(args.concurrency):
            futures.append(
                executor.submit(
                    throughput_worker,
                    worker_id,
                    endpoints,
                    client,
                    window,
                    start_event,
                    stop_event,
                    worker_results[worker_id],
                )
            )
    except Exception as exc:
        stop_event.set()
        start_event.set()
        executor.shutdown(wait=True)
        print(f"ERROR: failed to start load workers: {exc}", file=sys.stderr)
        return 2

    window.warmup_start = time.monotonic() + 0.1
    window.measurement_start = window.warmup_start + args.warmup
    window.end = window.measurement_start + args.duration
    start_event.set()
    interrupted = False
    measurement_stats = None

    try:
        if args.warmup > 0:
            print(f"Warming up at full load for {args.warmup:.2f}s...", flush=True)
        if not wait_until(window.measurement_start, stop_event):
            raise KeyboardInterrupt
        try:
            measurement_stats = get_json(server, "/stats", min(args.timeout, 2.0))
        except Exception:
            pass

        print("Elapsed | Interval RPS | Completed | Errors | Server active")
        print("--------|--------------|-----------|--------|--------------")
        previous_completed = 0
        previous_time = window.measurement_start
        next_report = min(
            window.measurement_start + args.report_interval, window.end
        )
        while not stop_event.is_set():
            if not wait_until(next_report, stop_event):
                break
            now = time.monotonic()
            completed, failed = scalar_snapshot(worker_results)
            sample_seconds = max(now - previous_time, 1e-9)
            sample_rps = (completed - previous_completed) / sample_seconds
            if rss is not None:
                rss.sample()
            print(
                f"{min(now - window.measurement_start, args.duration):7.1f} | "
                f"{sample_rps:12.2f} | {completed:9d} | {failed:6d} | "
                f"{server_active_requests(server, args.report_interval):>13}",
                flush=True,
            )
            previous_completed = completed
            previous_time = now
            if now >= window.end:
                break
            next_report = min(next_report + args.report_interval, window.end)
    except KeyboardInterrupt:
        interrupted = True
        print("\nInterrupted; waiting for active requests...", file=sys.stderr)
    finally:
        stop_event.set()
        executor.shutdown(wait=True)

    for future in futures:
        try:
            future.result()
        except Exception as exc:
            print(f"ERROR: load worker failed: {exc}", file=sys.stderr)
            return 1

    if rss is not None:
        rss.sample()
    try:
        final_stats = get_json(server, "/stats", min(args.timeout, 5.0))
    except Exception:
        final_stats = None

    results = merge_results(worker_results)
    achieved_rps = results.completed / args.duration
    error_rate = (
        results.failed / results.completed * 100.0 if results.completed else 100.0
    )
    server_delta = None
    if measurement_stats is not None and final_stats is not None:
        server_delta = (
            final_stats.get("total_requests", 0)
            - measurement_stats.get("total_requests", 0)
        )

    print("\n" + "=" * 68)
    print(" Results (warm-up excluded)")
    print("=" * 68)
    print(f"Completed requests: {results.completed}")
    print(f"Successful:         {results.succeeded}")
    print(f"Failed:             {results.failed} ({error_rate:.2f}%)")
    print(f"Average RPS:        {achieved_rps:.2f}")
    if server_delta is not None:
        print(f"Server request delta (approx.): {server_delta}")
    print_common_results(results, rss)

    if interrupted:
        return 130

    failures = []
    if results.completed == 0:
        failures.append("no workload requests completed")
    if error_rate > args.max_error_rate:
        failures.append(
            f"error rate {error_rate:.2f}% exceeds {args.max_error_rate:.2f}%"
        )
    if achieved_rps < args.min_rps:
        failures.append(
            f"average RPS {achieved_rps:.2f} is below minimum {args.min_rps:.2f}"
        )
    return check_thresholds(failures)


def build_parser() -> argparse.ArgumentParser:
    default_base_url = os.environ.get(
        "BASE_URL",
        "http://{}:{}".format(
            os.environ.get("HOST", "localhost"), os.environ.get("PORT", "8090")
        ),
    )
    parser = argparse.ArgumentParser(
        description=(
            "Send requests to the end-to-end upstream server endpoints. With --rps requests are "
            "paced by monotonic deadlines at a constant arrival rate; without it "
            "workers saturate the server with no pacing."
        )
    )
    parser.add_argument("--base-url", default=default_base_url)
    parser.add_argument(
        "-r",
        "--rps",
        type=positive_float,
        default=None,
        help="constant arrival rate; omit for unthrottled maximum throughput",
    )
    parser.add_argument("-d", "--duration", type=positive_float, default=60.0)
    parser.add_argument(
        "-c",
        "--concurrency",
        type=positive_int,
        default=100,
        help=(
            "worker count (unthrottled), or maximum in-flight requests with "
            "--rps where saturated arrivals are dropped (default: 100)"
        ),
    )
    parser.add_argument("-m", "--mode", choices=sorted(WORKLOADS), default="mixed")
    parser.add_argument(
        "--warmup",
        type=non_negative_float,
        default=2.0,
        metavar="SEC",
        help="unmeasured full-load warm-up duration, unthrottled mode only (default: 2)",
    )
    parser.add_argument("--timeout", type=positive_float, default=30.0)
    parser.add_argument("--report-interval", type=positive_float, default=1.0)
    parser.add_argument(
        "--max-error-rate",
        type=percentage,
        default=0.0,
        metavar="PERCENT",
        help="fail when completed-request errors exceed this percentage (default: 0)",
    )
    parser.add_argument(
        "--rps-tolerance",
        type=percentage,
        default=5.0,
        metavar="PERCENT",
        help="allowed percentage of planned arrivals that may be dropped, "
        "--rps mode only (default: 5)",
    )
    parser.add_argument(
        "--min-rps",
        type=non_negative_float,
        default=0.0,
        help="optional minimum average throughput required to pass, "
        "unthrottled mode only",
    )
    parser.add_argument(
        "--rss-pid",
        type=positive_int,
        default=None,
        metavar="PID",
        help="sample this process's RSS each report interval and summarize it",
    )
    parser.add_argument(
        "--no-require-agent",
        action="store_true",
        help="do not require the server's /ready endpoint to report an enabled agent",
    )
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        server = parse_server_address(args.base_url)
    except ValueError as exc:
        print(f"ERROR: invalid --base-url: {exc}", file=sys.stderr)
        return 2

    try:
        initial_stats = preflight(server, args.timeout, not args.no_require_agent)
    except Exception as exc:
        print(f"ERROR: e2e server pre-flight check failed: {exc}", file=sys.stderr)
        return 2

    if args.rps is not None:
        return run_fixed_rps(args, server, initial_stats)
    return run_max_throughput(args, server, initial_stats)


if __name__ == "__main__":
    sys.exit(main())
