// load is the end-to-end suite's load generator. With -rps it is a
// constant-arrival-rate test: requests start on fixed deadlines, -concurrency
// bounds the requests in flight, and an arrival that finds no free slot or
// comes a whole period late is dropped instead of bursting to catch up.
// Without -rps, -concurrency workers send requests back to back after an
// unmeasured -warmup.
//
// Exit status: 0 when the thresholds hold, 1 when one does not, 2 for a bad
// argument or a server that fails the pre-flight check, 130 when interrupted.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type endpoint struct {
	path   string
	status int // the status a successful response carries
}

var mixed = []endpoint{
	{"/simple", 200}, {"/deep?depth=10", 200}, {"/deep?depth=30", 200},
	{"/wide?width=20", 200}, {"/wide?width=100", 200}, {"/annotated", 200},
	{"/features", 200}, {"/mixed", 200}, {"/error", 500},
}

var workloads = map[string][]endpoint{
	"mixed": mixed,
	"full": append(slices.Clone(mixed),
		endpoint{"/http-client", 200}, endpoint{"/grpc-unary", 200}, endpoint{"/grpc-stream", 200},
		endpoint{"/grpc-client-stream?count=5", 200}, endpoint{"/grpc-bidi?count=3", 200},
		endpoint{"/grpc-all", 200}, endpoint{"/db-batch?size=20", 200}, endpoint{"/db-complex", 200}),
}

var (
	baseURL      = flag.String("base-url", "http://localhost:8090", "upstream server")
	rps          = flag.Float64("rps", 0, "constant arrival rate; 0 for unthrottled maximum throughput")
	duration     = flag.Float64("duration", 60, "measured seconds")
	concurrency  = flag.Int("concurrency", 100, "workers (unthrottled), or the most requests in flight (-rps)")
	mode         = flag.String("mode", "mixed", "mixed, full, or one endpoint path such as /deep?depth=30")
	warmup       = flag.Float64("warmup", 2, "unmeasured warm-up seconds, unthrottled only")
	maxErrorRate = flag.Float64("max-error-rate", 0, "fail past this percentage of failed requests")
	rpsTolerance = flag.Float64("rps-tolerance", 5, "fail past this percentage of dropped arrivals, -rps only")
	rssPid       = flag.Int("rss-pid", 0, "sample this process's RSS every report")

	client = &http.Client{Timeout: 30 * time.Second}
)

// results is the accounting every request goroutine writes into.
type results struct {
	mu                  sync.Mutex
	started, completed  int
	succeeded, failed   int
	dropped             map[string]int
	statuses            map[int]int
	latencies, lags     []float64 // ms
	errorSamples        []string
	rssFirst, rssMax    int
	rssLast, rssSamples int
}

func (r *results) drop(reason string) {
	r.mu.Lock()
	r.dropped[reason]++
	r.mu.Unlock()
}

func (r *results) start(lag time.Duration) {
	r.mu.Lock()
	r.started++
	r.lags = append(r.lags, ms(max(lag, 0)))
	r.mu.Unlock()
}

func (r *results) complete(e endpoint, took time.Duration, status int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completed++
	r.latencies = append(r.latencies, ms(took))
	if err == nil {
		r.statuses[status]++
		if status == e.status {
			r.succeeded++
			return
		}
	}
	r.failed++
	if len(r.errorSamples) < 5 {
		if err != nil {
			r.errorSamples = append(r.errorSamples, fmt.Sprintf("%s: %v", e.path, err))
		} else {
			r.errorSamples = append(r.errorSamples, fmt.Sprintf("%s: expected HTTP %d, received HTTP %d", e.path, e.status, status))
		}
	}
}

func (r *results) counts() (started, completed, failed, dropped int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range r.dropped {
		dropped += n
	}
	return r.started, r.completed, r.failed, dropped
}

// sampleRSS records the RSS of -rss-pid, read with ps.
func (r *results) sampleRSS() {
	if *rssPid == 0 {
		return
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(*rssPid)).Output()
	kb, perr := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || perr != nil {
		return
	}
	r.mu.Lock()
	if r.rssSamples == 0 {
		r.rssFirst = kb
	}
	r.rssMax, r.rssLast = max(r.rssMax, kb), kb
	r.rssSamples++
	r.mu.Unlock()
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func get(path string) (int, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(*baseURL, "/")+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "pinpoint-e2e-load-test/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_, err = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode, err
}

// serverStats reads the upstream's /stats.
func serverStats() (map[string]any, error) {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(strings.TrimRight(*baseURL, "/") + "/stats")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var m map[string]any
	return m, json.NewDecoder(resp.Body).Decode(&m)
}

// requestDelta is how many requests the server counted since from was read.
func requestDelta(from map[string]any) (float64, bool) {
	to, err := serverStats()
	a, okA := from["total_requests"].(float64)
	b, okB := to["total_requests"].(float64)
	return b - a, err == nil && okA && okB
}

func activeRequests() string {
	if m, err := serverStats(); err == nil {
		if v, ok := m["active_requests"]; ok {
			return fmt.Sprint(v)
		}
	}
	return "?"
}

// percentiles renders p50/p95/p99/max the way Python's statistics.quantiles
// does with method="inclusive": linear interpolation between ranks.
func percentiles(values []float64) string {
	v := slices.Sorted(slices.Values(values))
	q := func(p float64) float64 {
		if len(v) == 0 {
			return 0
		}
		pos := p * float64(len(v)-1)
		lo := int(pos)
		if lo+1 >= len(v) {
			return v[lo]
		}
		return v[lo] + (v[lo+1]-v[lo])*(pos-float64(lo))
	}
	top := 0.0
	if len(v) > 0 {
		top = v[len(v)-1]
	}
	return fmt.Sprintf("p50=%.2f, p95=%.2f, p99=%.2f, max=%.2f", q(.50), q(.95), q(.99), top)
}

func main() {
	flag.Parse()
	endpoints := workloads[*mode]
	if strings.HasPrefix(*mode, "/") {
		endpoints = []endpoint{{*mode, 200}}
	}
	if len(endpoints) == 0 || *concurrency < 1 || *duration <= 0 {
		fmt.Fprintln(os.Stderr, "ERROR: -mode must be mixed, full or an endpoint path; -concurrency and -duration must be positive")
		os.Exit(2)
	}
	// A clone of the default transport, not a bare one: the bare one dropped the
	// default dial and TLS handshake timeouts, so one wedged socket parked a
	// worker for the whole client timeout.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = *concurrency
	client.Transport = transport

	initial, err := serverStats()
	if err == nil {
		_, err = get("/ready")
	}
	if err != nil || initial["total_requests"] == nil || initial["active_requests"] == nil {
		fmt.Fprintf(os.Stderr, "ERROR: e2e server pre-flight check failed: %v (stats %v)\n", err, initial)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r := &results{dropped: map[string]int{}, statuses: map[int]int{}}
	r.sampleRSS()
	var failures []string
	if *rps > 0 {
		failures = runFixedRate(ctx, endpoints, r, initial)
	} else {
		failures = runUnthrottled(ctx, endpoints, r)
	}

	if len(r.dropped) > 0 {
		fmt.Println("Dropped by reason:  " + joinCounts(r.dropped))
	}
	if len(r.statuses) > 0 {
		fmt.Println("HTTP status codes:  " + joinCounts(r.statuses))
	}
	avg := 0.0
	for _, l := range r.latencies {
		avg += l / float64(len(r.latencies))
	}
	fmt.Printf("Latency (ms):      avg=%.2f, %s\n", avg, percentiles(r.latencies))
	if r.rssSamples > 0 {
		fmt.Printf("Server RSS (KB):    first=%d, max=%d, last=%d\n", r.rssFirst, r.rssMax, r.rssLast)
	}
	for _, s := range r.errorSamples {
		fmt.Println("  ERROR: " + s)
	}

	if ctx.Err() != nil {
		os.Exit(130)
	}
	for _, f := range failures {
		fmt.Fprintln(os.Stderr, "FAIL: "+f)
	}
	if len(failures) > 0 {
		os.Exit(1)
	}
	fmt.Println("PASS: load test met its configured thresholds")
}

func joinCounts[K string | int](m map[K]int) string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(m)) {
		parts = append(parts, fmt.Sprintf("%v=%d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

// sleepUntil waits for t and reports false when interrupted first.
func sleepUntil(ctx context.Context, t time.Time) bool {
	timer := time.NewTimer(time.Until(t))
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func runFixedRate(ctx context.Context, endpoints []endpoint, r *results, initial map[string]any) []string {
	interval := time.Duration(float64(time.Second) / *rps)
	planned := int(math.Floor(*duration**rps + 1e-9))
	if planned < 1 {
		fmt.Fprintln(os.Stderr, "ERROR: duration and RPS produce no scheduled requests; increase either value")
		os.Exit(2)
	}
	line := strings.Repeat("=", 64)
	fmt.Printf("%s\n Pinpoint Go Agent - Fixed RPS Load Test\n%s\n", line, line)
	fmt.Printf("Server:        %s\nMode:          %s\nTarget RPS:    %.2f\nDuration:      %.2fs\n",
		strings.TrimRight(*baseURL, "/"), *mode, *rps, *duration)
	fmt.Printf("Planned:       %d requests\nMax in flight: %d\nEndpoints:     %d (deterministic round-robin)\n%s\n",
		planned, *concurrency, len(endpoints), line)

	start := time.Now()
	done := make(chan struct{})
	var reporter sync.WaitGroup
	reporter.Go(func() {
		fmt.Println("Elapsed | Target RPS | Started RPS | Completed | In flight | Dropped | Server active")
		fmt.Println("--------|------------|-------------|-----------|-----------|---------|--------------")
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		prevStarted, prevTime := 0, start
		for {
			select {
			case <-done:
				return
			case now := <-tick.C:
				started, completed, _, dropped := r.counts()
				r.sampleRSS()
				fmt.Printf("%7.1f | %10.2f | %11.2f | %9d | %9d | %7d | %13s\n",
					min(now.Sub(start).Seconds(), *duration), *rps,
					float64(started-prevStarted)/now.Sub(prevTime).Seconds(),
					completed, started-completed, dropped, activeRequests())
				prevStarted, prevTime = started, now
			}
		}
	})

	slots := make(chan struct{}, *concurrency)
	var inFlight sync.WaitGroup
	for i := range planned {
		deadline := start.Add(time.Duration(i+1) * interval)
		if !sleepUntil(ctx, deadline) {
			fmt.Fprintln(os.Stderr, "\nInterrupted; waiting for in-flight requests...")
			break
		}
		// No catch-up burst: an arrival a whole period late is dropped.
		if time.Since(deadline) >= interval {
			r.drop("scheduler_lag")
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			r.drop("max_in_flight")
			continue
		}
		e := endpoints[i%len(endpoints)]
		inFlight.Go(func() {
			defer func() { <-slots }()
			began := time.Now()
			r.start(began.Sub(deadline))
			status, err := get(e.path)
			r.complete(e, time.Since(began), status, err)
		})
	}
	inFlight.Wait()
	close(done)
	reporter.Wait()
	r.sampleRSS()

	started, completed, failed, dropped := r.counts()
	errorRate := 100.0
	if completed > 0 {
		errorRate = float64(failed) / float64(completed) * 100
	}
	dropRate := float64(dropped) / float64(planned) * 100
	fmt.Printf("\n%s\n Results\n%s\n", line, line)
	fmt.Printf("Planned arrivals:   %d\nStarted requests:   %d\nCompleted requests: %d\n", planned, started, completed)
	fmt.Printf("Successful:         %d\nFailed:             %d (%.2f%%)\nDropped:            %d (%.2f%%)\n",
		r.succeeded, failed, errorRate, dropped, dropRate)
	fmt.Printf("Achieved start RPS: %.2f\nTotal wall time:    %.2fs\n", float64(started) / *duration, time.Since(start).Seconds())
	if d, ok := requestDelta(initial); ok {
		fmt.Printf("Server request delta: %v\n", d)
	}
	fmt.Println("Schedule lag (ms): " + percentiles(r.lags))

	var failures []string
	if started == 0 {
		failures = append(failures, "no workload requests were started")
	}
	if errorRate > *maxErrorRate {
		failures = append(failures, fmt.Sprintf("error rate %.2f%% exceeds %.2f%%", errorRate, *maxErrorRate))
	}
	if dropRate > *rpsTolerance {
		failures = append(failures, fmt.Sprintf("dropped-arrival rate %.2f%% exceeds RPS tolerance %.2f%%", dropRate, *rpsTolerance))
	}
	return failures
}

func runUnthrottled(ctx context.Context, endpoints []endpoint, r *results) []string {
	line := strings.Repeat("=", 68)
	fmt.Printf("%s\n Pinpoint Go Agent - Maximum Throughput Load Test\n%s\n", line, line)
	fmt.Printf("Server:       %s\nMode:         %s\nConcurrency:  %d\nWarm-up:      %.2fs (excluded from results)\n",
		strings.TrimRight(*baseURL, "/"), *mode, *concurrency, *warmup)
	fmt.Printf("Duration:     %.2fs\nEndpoints:    %d (deterministic rotation)\nRate limit:   none\n%s\n",
		*duration, len(endpoints), line)

	measureFrom := time.Now().Add(time.Duration(*warmup * float64(time.Second)))
	end := measureFrom.Add(time.Duration(*duration * float64(time.Second)))
	ctx, cancel := context.WithDeadline(ctx, end)
	defer cancel()
	var workers sync.WaitGroup
	for w := range *concurrency {
		workers.Go(func() {
			for i := w; ctx.Err() == nil; i++ {
				e := endpoints[i%len(endpoints)]
				began := time.Now()
				status, err := get(e.path)
				if began.After(measureFrom) && began.Before(end) {
					r.complete(e, time.Since(began), status, err)
				}
			}
		})
	}

	if *warmup > 0 {
		fmt.Printf("Warming up at full load for %.2fs...\n", *warmup)
	}
	interrupted := !sleepUntil(ctx, measureFrom) && time.Now().Before(end)
	measureStats, _ := serverStats()
	if !interrupted {
		fmt.Println("Elapsed | Interval RPS | Completed | Errors | Server active")
		fmt.Println("--------|--------------|-----------|--------|--------------")
		prevCompleted, prevTime := 0, measureFrom
		for next := measureFrom.Add(time.Second); ; next = next.Add(time.Second) {
			if next.After(end) {
				next = end
			}
			if !sleepUntil(ctx, next) && time.Now().Before(end) {
				break
			}
			now := time.Now()
			_, completed, failed, _ := r.counts()
			r.sampleRSS()
			fmt.Printf("%7.1f | %12.2f | %9d | %6d | %13s\n", min(now.Sub(measureFrom).Seconds(), *duration),
				float64(completed-prevCompleted)/now.Sub(prevTime).Seconds(), completed, failed, activeRequests())
			prevCompleted, prevTime = completed, now
			if !now.Before(end) {
				break
			}
		}
	}
	cancel()
	workers.Wait()
	r.sampleRSS()

	_, completed, failed, _ := r.counts()
	errorRate := 100.0
	if completed > 0 {
		errorRate = float64(failed) / float64(completed) * 100
	}
	fmt.Printf("\n%s\n Results (warm-up excluded)\n%s\n", line, line)
	fmt.Printf("Completed requests: %d\nSuccessful:         %d\nFailed:             %d (%.2f%%)\nAverage RPS:        %.2f\n",
		completed, r.succeeded, failed, errorRate, float64(completed) / *duration)
	if d, ok := requestDelta(measureStats); ok {
		fmt.Printf("Server request delta (approx.): %v\n", d)
	}

	var failures []string
	if completed == 0 {
		failures = append(failures, "no workload requests completed")
	}
	if errorRate > *maxErrorRate {
		failures = append(failures, fmt.Sprintf("error rate %.2f%% exceeds %.2f%%", errorRate, *maxErrorRate))
	}
	return failures
}
