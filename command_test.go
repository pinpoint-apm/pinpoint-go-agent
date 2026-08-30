package pinpoint

import (
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func Test_atcStreamsSharesOneSamplePerInterval(t *testing.T) {
	agent := &agent{}
	streams := atcStreams{agent: agent}
	now := time.Unix(100, 0)

	agent.realTimeActiveSpan.Store(int64(1), &activeSpanInfo{startTime: now.Add(-10 * time.Second)})
	assert.Equal(t, []int32{0, 0, 0, 1}, streams.activeSpanCount(now))

	// Any further caller in the same reporting interval reuses the sample, even
	// if the live map changed in between.
	agent.realTimeActiveSpan.Store(int64(2), &activeSpanInfo{startTime: now.Add(-10 * time.Second)})
	assert.Equal(t, []int32{0, 0, 0, 1}, streams.activeSpanCount(now.Add(activeThreadCountInterval-time.Nanosecond)))

	// Expiry refreshes the snapshot.
	agent.realTimeActiveSpan.Store(int64(3), &activeSpanInfo{startTime: now.Add(-10 * time.Second)})
	assert.Equal(t, []int32{0, 0, 0, 3}, streams.activeSpanCount(now.Add(2*activeThreadCountInterval)))
}

// Test_atcStreamsSampleIsSafeToShare exercises the claim the shared snapshot
// rests on: one published slice read by every stream at once, with the live
// span map churning underneath. Meaningful under -race.
func Test_atcStreamsSampleIsSafeToShare(t *testing.T) {
	agent := &agent{}
	streams := atcStreams{agent: agent}
	base := time.Now()
	agent.realTimeActiveSpan.Store(int64(-1), &activeSpanInfo{startTime: base})

	// Each generation must get its own slice, or a stream still marshaling the
	// previous sample would see its counts change underneath it.
	first := streams.activeSpanCount(base)
	second := streams.activeSpanCount(base.Add(activeThreadCountInterval))
	if &first[0] == &second[0] {
		t.Fatal("two sample generations share a backing array")
	}

	var wg sync.WaitGroup
	churn := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-churn:
				return
			default:
			}
			id := int64(i % 512)
			agent.realTimeActiveSpan.Store(id, &activeSpanInfo{startTime: base})
			agent.realTimeActiveSpan.Delete(id)
		}
	}()

	for s := 0; s < maxActiveThreadCountStreams; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A third of an interval apart, so callers both share and expire samples.
			for i := 0; i < 1000; i++ {
				counts := streams.activeSpanCount(base.Add(time.Duration(i) * activeThreadCountInterval / 3))
				if len(counts) != 4 {
					t.Errorf("sample has %d buckets, want 4", len(counts))
					return
				}
				_ = counts[0] + counts[1] + counts[2] + counts[3]
			}
		}()
	}

	close(churn)
	wg.Wait()
}

func Test_activeThreadCountStreamPreservesSequenceAndTimestamp(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	newMockCmdGrpc(agent)
	stream := newActiveThreadCountStream(&agent.cmdGrpc.atcStreams, 42)
	recorder := &mockAtcStream{}
	stream.stream = recorder
	stream.cancel = func() {}
	defer stream.close()

	before := time.Now().UnixNano() / int64(time.Millisecond)
	require.NoError(t, stream.sendActiveThreadCount())
	time.Sleep(2 * time.Millisecond)
	require.NoError(t, stream.sendActiveThreadCount())
	after := time.Now().UnixNano() / int64(time.Millisecond)

	responses := recorder.sentResponses()
	require.Len(t, responses, 2)
	assert.Equal(t, int32(42), responses[0].GetCommonStreamResponse().GetResponseId())
	assert.Equal(t, int32(1), responses[0].GetCommonStreamResponse().GetSequenceId())
	assert.Equal(t, int32(2), responses[1].GetCommonStreamResponse().GetSequenceId())
	assert.GreaterOrEqual(t, responses[0].GetTimeStamp(), before)
	assert.Less(t, responses[0].GetTimeStamp(), responses[1].GetTimeStamp())
	assert.LessOrEqual(t, responses[1].GetTimeStamp(), after)
}

// BenchmarkActiveThreadCountSampling models one reporting interval with the
// maximum number of count streams attached to an agent, all sharing one sample.
func BenchmarkActiveThreadCountSampling(b *testing.B) {
	const active = 10000
	agent := &agent{}
	now := time.Unix(100, 0)
	span := &activeSpanInfo{startTime: now.Add(-10 * time.Second)}
	for id := 0; id < active; id++ {
		agent.realTimeActiveSpan.Store(int64(id), span)
	}
	streams := atcStreams{agent: agent}
	sampleTime := now
	var counts []int32

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sampleTime = sampleTime.Add(activeThreadCountInterval)
		for stream := 0; stream < maxActiveThreadCountStreams; stream++ {
			counts = streams.activeSpanCount(sampleTime)
		}
	}
	benchmarkActiveThreadCount = counts
}

var benchmarkActiveThreadCount []int32
