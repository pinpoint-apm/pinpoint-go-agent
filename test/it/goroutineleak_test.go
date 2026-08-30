package it

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"time"
)

// leakSettleDelay gives the shutdown paths that every test's t.Cleanup just
// started time to finish unwinding, so a goroutine that is about to exit is not
// sampled mid-teardown.
//
// ponytail: fixed delay, not a condition wait. The suite's goroutine count is
// already back to its baseline by the time m.Run returns; if this ever proves
// too short, poll runtime.NumGoroutine until it stops falling instead.
const leakSettleDelay = 2 * time.Second

// reportGoroutineLeaks writes Go 1.26's goroutineleak profile after the whole
// suite has run and reports whether any goroutine was left blocked on a
// concurrency primitive nothing can reach any more - the shape an agent worker
// that outlived Shutdown would have.
//
// It is a no-op unless the test binary was built with
// GOEXPERIMENT=goroutineleakprofile, which is what the goroutine-leak CI job
// does; every other build has no such profile registered and skips the check.
//
// WriteTo, not Count, is what triggers the leak-detecting GC cycle: Count alone
// reads the previous cycle's result and always reports 0 on the first call.
func reportGoroutineLeaks() int {
	p := pprof.Lookup("goroutineleak")
	if p == nil {
		return 0
	}

	time.Sleep(leakSettleDelay)

	var buf bytes.Buffer
	if err := p.WriteTo(&buf, 1); err != nil {
		fmt.Fprintf(os.Stderr, "goroutine leak profile: %v\n", err)
		return 1
	}
	if p.Count() == 0 {
		return 0
	}

	fmt.Fprintf(os.Stderr, "\n%d leaked goroutine(s) after the suite finished "+
		"(%d still running); each stack below is blocked on a primitive nothing can reach:\n\n%s",
		p.Count(), runtime.NumGoroutine(), buf.String())
	return 1
}
