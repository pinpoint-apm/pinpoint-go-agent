package pinpoint

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Load and Store are test seams only, which is why they live in a _test file:
// production code moves the phase through cas and reads it through
// the named predicates. Tests that build an agent by hand need to put it into
// a phase without going through registration or Shutdown, and the existing
// tests do so through the former enable bool's API: Store(true) is a running
// agent, Store(false) one that never registered, Load the request-path gate.
func (l *lifecycle) Load() bool {
	return l.current() == phaseRunning
}

func (l *lifecycle) Store(enabled bool) {
	if enabled {
		l.phase.Store(int32(phaseRunning))
	} else {
		l.phase.Store(int32(phaseRegistering))
	}
}

// The phases an operator needs to tell apart - still registering, running,
// and shutting down - are distinct values, where the enable bool alone read
// false for the first and the last.
func Test_lifecycle_PhasesAreDistinguishable(t *testing.T) {
	c, _ := NewConfig(WithAppName("test"), WithServiceName("test-service"))
	c.offGrpc = true
	a, err := NewAgent(c)
	require.NoError(t, err)
	agent := a.(*agent)

	assert.Equal(t, phaseRegistering, agent.enable.current(), "before registration")
	assert.False(t, agent.tracingEnabled())
	assert.False(t, agent.stopping())

	require.True(t, agent.enable.cas(phaseRegistering, phaseRunning))
	assert.Equal(t, phaseRunning, agent.enable.current())
	assert.True(t, agent.tracingEnabled())
	assert.True(t, agent.workerContinues())
	assert.False(t, agent.stopping())

	agent.signalShutdown()
	assert.Equal(t, phaseStopping, agent.enable.current(), "signalled, not yet drained")
	assert.False(t, agent.tracingEnabled(), "the request path is refused from the signal on, as the C++ isExiting gate does")
	assert.True(t, agent.workerContinues(), "the workers keep draining what was queued before the signal")
	assert.True(t, agent.stopping())

	agent.Shutdown()
	assert.Equal(t, phaseStopped, agent.enable.current())
	assert.False(t, agent.tracingEnabled())
	assert.False(t, agent.workerContinues())
	assert.True(t, agent.stopping())
}

// An agent that is shut down before it ever registered skips stopping: there
// is nothing to drain, and the request path must not open up for the window.
func Test_lifecycle_ShutdownBeforeRegistrationGoesStraightToStopped(t *testing.T) {
	c, _ := NewConfig(WithAppName("test"), WithServiceName("test-service"))
	c.offGrpc = true
	a, err := NewAgent(c)
	require.NoError(t, err)
	agent := a.(*agent)

	agent.Shutdown()
	assert.Equal(t, phaseStopped, agent.enable.current())
	assert.True(t, agent.stopping())
	assert.False(t, agent.tracingEnabled())
}

func Test_lifecycle_FailedRegistrationIsItsOwnPhase(t *testing.T) {
	agent := &agent{}
	require.True(t, agent.enable.cas(phaseRegistering, phaseFailed))
	assert.False(t, agent.tracingEnabled(), "a failed agent does not record")
	assert.False(t, agent.stopping(), "and is not shutting down either, as the shutdown flag was not set on this path")
	agent.enable.beginShutdown()
	assert.Equal(t, phaseStopped, agent.enable.current(), "Shutdown after a failure still lands on stopped")
}

// beginShutdown takes the one edge the current phase allows - running drains
// through stopping, an agent that never ran goes straight to stopped - and
// leaves an agent already on its way out alone, so a Shutdown of an agent in
// any phase is a normal path with no refused transition to warn about.
func Test_lifecycle_BeginShutdownPicksTheEdgeForThePhase(t *testing.T) {
	for _, tt := range []struct {
		from, want agentPhase
	}{
		{phaseRegistering, phaseStopped},
		{phaseFailed, phaseStopped},
		{phaseRunning, phaseStopping},
		{phaseStopping, phaseStopping},
		{phaseStopped, phaseStopped},
	} {
		t.Run(tt.from.String(), func(t *testing.T) {
			var l lifecycle
			l.phase.Store(int32(tt.from))
			l.beginShutdown()
			assert.Equal(t, tt.want, l.current())
		})
	}
}

// cas applies an edge only from its named source phase, and only once: of two
// writers racing for the same edge one wins and the other is refused.
func Test_lifecycle_CasNeedsItsSourcePhase(t *testing.T) {
	var l lifecycle
	assert.False(t, l.cas(phaseRunning, phaseStopping), "not running yet")
	assert.Equal(t, phaseRegistering, l.current(), "a refused cas leaves the phase where it was")
	assert.True(t, l.cas(phaseRegistering, phaseRunning))
	assert.True(t, l.cas(phaseRunning, phaseStopping))
	assert.False(t, l.cas(phaseRunning, phaseStopping), "the edge is taken once")
	assert.Equal(t, phaseStopping, l.current())
}

// From the shutdown signal on, the request path is refused while the workers
// drain: a new request gets a noop tracer and a span chunk is not queued.
// Recording through the drain could race teardown after the final flush.
func Test_lifecycle_StoppingRefusesTheRequestPath(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	require.Equal(t, phaseRunning, agent.enable.current())

	tracer := agent.NewSpanTracer("op", "/rpc")
	require.True(t, tracer.IsSampled(), "running: sampled")

	agent.signalShutdown()
	require.Equal(t, phaseStopping, agent.enable.current())

	assert.False(t, agent.NewSpanTracer("op", "/rpc").IsSampled(), "stopping: noop tracer")
	assert.False(t, agent.enqueueSpan(&spanChunk{}), "stopping: chunk refused")
	assert.Zero(t, agent.cacheError("boom"), "stopping: no metadata registered")
	assert.False(t, agent.enqueueUrlStat(&urlStat{}), "stopping: no url stat queued")
	assert.False(t, agent.Enable())
}
