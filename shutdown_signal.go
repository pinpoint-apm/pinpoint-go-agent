package pinpoint

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// The os/signal calls behind ShutdownOnSignal, wrapped so a test can replace
// them - like getHostName in grpc.go. signalNotify is also what lets a test
// prove the default: without ShutdownOnSignal the agent never touches it.
var (
	signalNotify = signal.Notify
	signalStop   = signal.Stop

	// raiseSignal re-delivers sig to this process once the agent is down.
	// os.Process.Signal rather than syscall.Kill so the file builds on every
	// platform the agent does; on Windows only os.Kill is deliverable, and the
	// error is logged rather than turned into an exit - exiting is the host's
	// decision, never the library's.
	raiseSignal = func(sig os.Signal) error {
		p, err := os.FindProcess(os.Getpid())
		if err != nil {
			return err
		}
		return p.Signal(sig)
	}
)

// defaultShutdownSignals are the signals ShutdownOnSignal watches when the
// caller names none: SIGTERM is what container orchestrators and init systems
// send on a rollout or stop, SIGINT is Ctrl-C in a terminal.
var defaultShutdownSignals = []os.Signal{syscall.SIGTERM, os.Interrupt}

// ShutdownOnSignal calls agent.Shutdown() when the process receives one of
// sigs, then hands the signal back to the process so it dies the way it would
// have without the agent. Without it, a signal-terminated process runs no
// deferred functions: the spans still queued are never sent and the collector
// never learns the agent's end time, so the web UI keeps listing it as alive.
//
// This is opt-in and off by default because signal.Notify changes process-wide
// state - it disables the default handling of every signal it is given. A
// library must not do that silently: the host may have its own handler for the
// same signals, and a process whose SIGTERM is consumed and never re-raised
// does not exit until the orchestrator escalates to SIGKILL. With no sigs the
// helper watches SIGTERM and SIGINT (os.Interrupt).
//
// When a watched signal arrives the helper calls Shutdown(), which drains the
// span queue for at most shutdownTimeout, restores the default disposition of
// the signal with signal.Stop, and raises the same signal again. For the
// default disposition that terminates the process with exit status 128+signum,
// exactly as it would have without the helper. If the host has its own
// signal.Notify for the same signal, the re-raised signal is delivered to that
// channel instead and the host decides what to do next - the helper never
// calls os.Exit.
//
// The returned function stops watching: it calls signal.Stop and waits for the
// watcher goroutine to exit. Call it once the agent is no longer wanted to
// hand signal handling back to the host, or on a normal exit so no goroutine
// outlives the agent. It is safe to call more than once, and safe to combine
// with a `defer agent.Shutdown()`: Shutdown is idempotent, so the signal path
// and the explicit call may run concurrently.
//
//	agent, err := pinpoint.NewAgent(cfg)
//	if err != nil {
//	    log.Printf("pinpoint agent start failed: %v", err)
//	}
//	defer agent.Shutdown()
//	defer pinpoint.ShutdownOnSignal(agent)() // SIGTERM, SIGINT
//
// Nothing intercepts os.Exit: Go has no atexit, so a program that exits that
// way loses whatever is still queued no matter what the agent does. Call
// Shutdown() before os.Exit.
func ShutdownOnSignal(agent Agent, sigs ...os.Signal) (stop func()) {
	if len(sigs) == 0 {
		sigs = defaultShutdownSignals
	}
	// Buffered so a signal arriving between Notify and the receive below is
	// not dropped; os/signal never blocks on delivery.
	ch := make(chan os.Signal, 1)
	done := make(chan struct{})
	exited := make(chan struct{})
	signalNotify(ch, sigs...)

	go func() {
		defer close(exited)
		var sig os.Signal
		select {
		case sig = <-ch:
		case <-done:
			return
		}
		if agent != nil {
			agent.Shutdown()
		}
		// Stop before raising: with no other channel registered for sig this
		// restores the default disposition, so the re-raise terminates the
		// process with 128+signum. signal.Reset would do the same but would
		// also tear down a channel the host registered for the same signal,
		// which is not this helper's to touch.
		signalStop(ch)
		if err := raiseSignal(sig); err != nil {
			Log("agent").Warnf("re-raise %v after shutdown failed: %v", sig, err)
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			signalStop(ch)
			close(done)
		})
		<-exited
	}
}
