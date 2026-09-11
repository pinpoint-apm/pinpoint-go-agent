package pinpoint

import "sync/atomic"

// agentPhase is the agent's lifecycle phase. It replaces the enable and
// shutdown bools, whose four combinations were these phases in disguise:
// registering was "neither set", running "enable only", stopping "both",
// init_failed_ trio (agent.h), collapsed into one value so the phases are
// mutually exclusive by construction and can be told apart from outside.
type agentPhase int32

const (
	// phaseRegistering is the zero value, so an agent built as a struct
	// literal in a test starts here like one built by NewAgent.
	phaseRegistering agentPhase = iota
	phaseRunning
	phaseStopping
	phaseStopped
	// phaseFailed: registration gave up for a reason other than Shutdown (a
	// bad address, TLS setup). Kept apart from registering because the two
	// need different responses from an operator - wait, or fix the config -
	// and from the code: connectGrpcServer's release defer runs only here.
	phaseFailed
)

func (p agentPhase) String() string {
	switch p {
	case phaseRegistering:
		return "registering"
	case phaseRunning:
		return "running"
	case phaseStopping:
		return "stopping"
	case phaseStopped:
		return "stopped"
	case phaseFailed:
		return "failed"
	}
	return "unknown"
}

// validTransitions lists, per source phase, the phases it may move to. Every
// edge is forward: nothing returns to registering or running, so a torn-down
var validTransitions = map[agentPhase][]agentPhase{
	phaseRegistering: {phaseRunning, phaseFailed, phaseStopped},
	phaseRunning:     {phaseStopping},
	phaseStopping:    {phaseStopped},
	phaseFailed:      {phaseStopped},
}

// lifecycle holds the phase as one atomic integer rather than a mutex-guarded
// enum: tracingEnabled is read on the request path (NewSpanTracer, every
// agent's lifecycle_mutex_ would be a lock per span. Transitions are a CAS,
// so two writers racing for the same edge see exactly one win.
type lifecycle struct {
	phase atomic.Int32
}

func (l *lifecycle) current() agentPhase {
	return agentPhase(l.phase.Load())
}

// transitionTo moves to the given phase and reports whether it did. A move
// the table does not allow is refused and logged, never applied: the phase
// stays where it is so the caller can decide what that means for it.
func (l *lifecycle) transitionTo(to agentPhase) bool {
	for {
		from := l.current()
		if !allowedTransition(from, to) {
			Log("agent").Warnf("agent phase transition refused: %s -> %s", from, to)
			return false
		}
		if l.phase.CompareAndSwap(int32(from), int32(to)) {
			Log("agent").Infof("agent phase: %s -> %s", from, to)
			return true
		}
	}
}

func allowedTransition(from, to agentPhase) bool {
	for _, p := range validTransitions[from] {
		if p == to {
			return true
		}
	}
	return false
}

// The predicates below are the only readers of the phase outside the
// transitions. Each is named for what its callers ask, and each reproduces
// exactly the bool it replaced; a call site that wants a different answer
// is a behaviour change and belongs in its own commit.

// tracingEnabled reports whether the request path may record: create spans,
// register metadata, queue spans and url stats. True while running only. It
// turns false as soon as shutdown is signalled: a request that arrives while
// the workers drain gets a noop tracer, and a span still open at the signal
// has its final chunk refused, so the drain sends what was queued before the
// signal and nothing produced after it. It used to stay true through
// stopping, which let the drain race a request path that kept producing
// (see doc/java_parity.md, "Lifecycle phases"). Enable() exposes it.
func (agent *agent) tracingEnabled() bool {
	return agent.enable.current() == phaseRunning
}

// workerContinues is the worker loop condition: running or stopping. A
// worker keeps polling through the stopping phase to drain what the request
// path queued before the signal; the stop signal, not this predicate, is what
// wakes it out of a blocked wait, and the phase turning to stopped is what
// ends a loop that fell through the select.
func (agent *agent) workerContinues() bool {
	p := agent.enable.current()
	return p == phaseRunning || p == phaseStopping
}

// stopping reports whether shutdown has begun - the former shutdown flag,
// which was never cleared, so it stays true once stopped. Registration and
// reconnect back-off loops end on it; a failed registration does not count,
// as the flag was not set on that path either.
func (agent *agent) stopping() bool {
	p := agent.enable.current()
	return p == phaseStopping || p == phaseStopped
}
