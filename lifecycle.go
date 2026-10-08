package pinpoint

import (
	"slices"
	"sync/atomic"
)

// agentPhase is the agent's lifecycle phase, held as one value rather than a
// pair of flags so the phases are mutually exclusive by construction.
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
	// need different responses from an operator - wait, or fix the config.
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
// agent can never come back to life.
var validTransitions = map[agentPhase][]agentPhase{
	phaseRegistering: {phaseRunning, phaseFailed, phaseStopped},
	phaseRunning:     {phaseStopping},
	phaseStopping:    {phaseStopped},
	phaseFailed:      {phaseStopped},
}

// lifecycle holds the phase as one atomic integer rather than a mutex-guarded
// enum: tracingEnabled is read on the request path, where a lock would cost one
// acquisition per span. Transitions are a CAS, so two writers racing for the
// same edge see exactly one win.
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
	if l.tryTransitionTo(to) {
		return true
	}
	Log("agent").Warnf("agent phase transition refused: %s -> %s", l.current(), to)
	return false
}

// tryTransitionTo is transitionTo without the warning, for the edges a
// concurrent Shutdown can legitimately make impossible: a registration that
// finishes, or fails, against an agent Shutdown has already moved to stopped.
// There the refusal is the expected outcome of the race, not a bug to report.
func (l *lifecycle) tryTransitionTo(to agentPhase) bool {
	for {
		from := l.current()
		if !slices.Contains(validTransitions[from], to) {
			return false
		}
		if l.phase.CompareAndSwap(int32(from), int32(to)) {
			Log("agent").Infof("agent phase: %s -> %s", from, to)
			return true
		}
	}
}

// beginShutdown moves a running agent to stopping, where the workers drain,
// and an agent that never ran - still registering, or failed - straight to
// stopped, since it has nothing to drain. An agent already stopping or stopped
// is left alone. One CAS loop over the phase it finds, rather than trying the
// running edge and falling back: the fallback refused and warned on every
// Shutdown of an agent that was never running, which is a normal path.
func (l *lifecycle) beginShutdown() {
	for {
		from := l.current()
		var to agentPhase
		switch from {
		case phaseRunning:
			to = phaseStopping
		case phaseRegistering, phaseFailed:
			to = phaseStopped
		default:
			return
		}
		if l.phase.CompareAndSwap(int32(from), int32(to)) {
			Log("agent").Infof("agent phase: %s -> %s", from, to)
			return
		}
	}
}

// tracingEnabled reports whether the request path may record: create spans,
// register metadata, queue spans and url stats. True while running only. It
// turns false as soon as shutdown is signalled: a request that arrives while
// the workers drain gets a noop tracer, and a span still open at the signal
// has its final chunk refused, so the drain sends what was queued before the
// signal and nothing produced after it. Enable() exposes it.
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

// stopping reports whether shutdown has begun, and stays true once stopped.
// Registration and reconnect back-off loops end on it; a failed registration
// does not count.
func (agent *agent) stopping() bool {
	p := agent.enable.current()
	return p == phaseStopping || p == phaseStopped
}
