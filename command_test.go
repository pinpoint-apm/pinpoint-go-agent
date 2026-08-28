package pinpoint

import (
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// drainAtcStreams shuts the agent down and asserts that every stream left the
// registry, which is the agent shutdown arm of the three exit paths.
func drainAtcStreams(t *testing.T, agent *agent) {
	t.Helper()

	agent.signalShutdown()
	waitFor(t, "active thread count streams to drain on shutdown", func() bool {
		return agent.cmdGrpc.atcStreams.count() == 0
	})
}

func Test_agent_handleActiveThreadCountRejectsBeyondLimit(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	cmd, client := newMockCmdGrpc(agent)
	defer drainAtcStreams(t, agent)

	base := runtime.NumGoroutine()
	for i := 0; i < maxActiveThreadCountStreams; i++ {
		agent.handleActiveThreadCount(int32(i), cmd)
	}
	assert.Equal(t, maxActiveThreadCountStreams, agent.cmdGrpc.atcStreams.count())

	const excess = 20
	for i := 0; i < excess; i++ {
		agent.handleActiveThreadCount(int32(maxActiveThreadCountStreams+i), cmd)
	}

	assert.Equal(t, maxActiveThreadCountStreams, agent.cmdGrpc.atcStreams.count())
	// Rejected requests open no stream and leave no goroutine behind.
	assert.Equal(t, maxActiveThreadCountStreams, client.streamCount())
	assert.LessOrEqual(t, runtime.NumGoroutine()-base, maxActiveThreadCountStreams+2)

	fails := cmd.stream.(*mockCmdStream).failMessages()
	assert.Len(t, fails, excess)
	for i, f := range fails {
		assert.Equal(t, int32(maxActiveThreadCountStreams+i), f.GetResponseId())
		assert.Equal(t, "too many active thread count streams", f.GetMessage().GetValue())
	}
}

func Test_agent_handleActiveThreadCountReplacesSameRequestId(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	cmd, client := newMockCmdGrpc(agent)
	defer drainAtcStreams(t, agent)

	agent.handleActiveThreadCount(7, cmd)
	waitFor(t, "first stream to sample", func() bool { return client.stream(0).sendCount() > 0 })

	agent.handleActiveThreadCount(7, cmd)

	waitFor(t, "superseded stream to close", func() bool { return client.stream(0).isClosed() })
	waitFor(t, "superseded stream to leave the registry", func() bool {
		return agent.cmdGrpc.atcStreams.count() == 1
	})
	assert.Equal(t, 2, client.streamCount())
	assert.False(t, client.stream(1).isClosed())
	// A re-issue is served, not rejected.
	assert.Empty(t, cmd.stream.(*mockCmdStream).failMessages())
}

func Test_agent_handleActiveThreadCountReleasesSlotOnSendError(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	cmd, client := newMockCmdGrpc(agent)
	defer drainAtcStreams(t, agent)

	client.sendErr = errors.New("send failed")
	agent.handleActiveThreadCount(1, cmd)

	waitFor(t, "failed stream to leave the registry", func() bool {
		return agent.cmdGrpc.atcStreams.count() == 0
	})
	assert.True(t, client.stream(0).isClosed())
}

func Test_agent_handleActiveThreadCountReleasesSlotWhenStreamCannotOpen(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	cmd, client := newMockCmdGrpc(agent)
	defer drainAtcStreams(t, agent)

	client.openErr = errors.New("open failed")
	agent.handleActiveThreadCount(1, cmd)

	assert.Equal(t, 0, agent.cmdGrpc.atcStreams.count())
	assert.Equal(t, 0, client.streamCount())
}
