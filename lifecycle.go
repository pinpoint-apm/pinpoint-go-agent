package pinpoint

import "sync/atomic"

// agentPhase is the agent's lifecycle phase. It replaces the enable and
// shutdown bools, whose four combinations were these phases in disguise:
// registering was "neither set", running "enable only", stopping "both",
// stopped "shutdown only". Mirrors the C++ agent's started_/shutting_down_/
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
	// The C++ agent keeps it as init_failed_ for the same reason.
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
// agent cannot be revived, which is what the C++ lifecycle_mutex_ guards.
var validTransitions = map[agentPhase][]agentPhase{
	phaseRegistering: {phaseRunning, phaseFailed, phaseStopped},
	phaseRunning:     {phaseStopping},
	phaseStopping:    {phaseStopped},
	phaseFailed:      {phaseStopped},
}

// lifecycle holds the phase as one atomic integer rather than a mutex-guarded
// enum: tracingEnabled is read on the request path (NewSpanTracer, every
// cache and enqueue) and by every worker loop iteration, where the C++
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
// register metadata, queue spans and url stats. True while running and
// still true while stopping - the teardown drains what the request path
// produced up to the moment the phase reaches stopped, exactly as the enable
// flag stayed set until shutdownAgent's CompareAndSwap. Enable() exposes it.
func (agent *agent) tracingEnabled() bool {
	p := agent.enable.current()
	return p == phaseRunning || p == phaseStopping
}

// workerContinues is the worker loop condition: the same phases as
// tracingEnabled, kept apart because the question differs. A worker keeps
// polling through the stopping phase; the stop signal, not this predicate,
// is what wakes it out of a blocked wait, and the phase turning to stopped
// is what ends a loop that fell through the select.
func (agent *agent) workerContinues() bool {
	return agent.tracingEnabled()
}

// stopping reports whether shutdown has begun - the former shutdown flag,
// which was never cleared, so it stays true once stopped. Registration and
// reconnect back-off loops end on it; a failed registration does not count,
// as the flag was not set on that path either.
func (agent *agent) stopping() bool {
	p := agent.enable.current()
	return p == phaseStopping || p == phaseStopped
}
