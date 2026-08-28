package pinpoint

import (
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"io"
	"sync"
	"time"
)

const (
	// maxActiveThreadCountStreams caps the active thread count streams running
	// at once. Every ACTIVE_THREAD_COUNT command costs a goroutine and a gRPC
	// stream, and the web UI re-requests one whenever a user opens the
	// real-time view, so without a cap a re-request loop grows both without
	// bound. Deliberately a constant, not a config key: the C++ agent keeps the
	// same value as a tuning constant, and 10 concurrent real-time viewers of a
	// single agent is already well past what the UI produces.
	maxActiveThreadCountStreams = 10

	// activeThreadCountInterval is how long a stream waits between samples.
	activeThreadCountInterval = 1 * time.Second
)

// atcStreams holds the running active thread count streams, so a re-issued
// request id can reclaim its predecessor and so the total can be capped.
type atcStreams struct {
	agent   *agent
	mu      sync.Mutex
	streams map[*activeThreadCountStream]struct{}
}

// add registers s and reports whether it may run. Streams are keyed by identity
// rather than by request id: a predecessor that has been told to stop stays
// registered until its goroutine returns, so it keeps counting against the cap
// and a re-request loop cannot outrun the teardown.
func (r *atcStreams) add(s *activeThreadCountStream) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	// The collector re-issues a command for a request id it already owns, e.g.
	// after a command stream reconnect. Signal the predecessor to stop; it
	// removes itself once its goroutine returns.
	for old := range r.streams {
		if old.reqId == s.reqId {
			old.requestStop()
		}
	}

	if len(r.streams) >= maxActiveThreadCountStreams {
		return false
	}
	if r.streams == nil {
		r.streams = make(map[*activeThreadCountStream]struct{})
	}
	r.streams[s] = struct{}{}
	r.publishCount()
	return true
}

func (r *atcStreams) remove(s *activeThreadCountStream) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.streams, s)
	r.publishCount()
}

func (r *atcStreams) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.streams)
}

// publishCount republishes the stream count for addRealTime*ActiveSpan, which
// reads it on every span start and cannot afford to take this lock. Callers
// must hold r.mu.
func (r *atcStreams) publishCount() {
	r.agent.atcStreamCount.Store(int32(len(r.streams)))
}

type activeSpanInfo struct {
	startTime  time.Time
	txId       string
	entryPoint string
	sampled    bool
}

func (agent *agent) runCommandService() {
	Log("cmd").Infof("start command goroutine")
	defer agent.workerWg.Done()

	stop := agent.stopSignal().Done()

	for attempt := 0; agent.enable.Load(); attempt++ {
		if attempt > 0 {
			// Pace consecutive stream failures. newCommandStreamWithRetry's
			// back-off only waits while the connection is not ready, so a
			// collector whose channel is READY but whose command stream fails
			// immediately (unimplemented, instant close) would otherwise spin
			// this loop hot, opening streams and sending handshakes
			// continuously inside the host application.
			t := time.NewTimer(backOffSleep(attempt - 1))
			select {
			case <-stop:
				t.Stop()
				Log("cmd").Infof("end command goroutine")
				return
			case <-t.C:
			}
		}

		stream := agent.cmdGrpc.newCommandStreamWithRetry()
		err := stream.sendCommandMessage()
		if err != nil {
			if err != io.EOF {
				Log("cmd").Errorf("send command message - %v", err)
			}
			stream.close()
			continue
		}

		for agent.enable.Load() {
			cmdReq, err := stream.recvCommandRequest()
			if err != nil {
				if agent.enable.Load() && err != io.EOF {
					Log("cmd").Warnf("recv command request - %v", err)
				}
				break
			}
			attempt = 0 // the stream is healthy; restart the back-off

			reqId := cmdReq.GetRequestId()
			Log("cmd").Infof("command request: %v, %v", cmdReq, reqId)

			switch cmdReq.Command.(type) {
			case *pb.PCmdRequest_CommandEcho:
				msg := cmdReq.GetCommandEcho().GetMessage()
				agent.cmdGrpc.sendEcho(reqId, msg)
				break
			case *pb.PCmdRequest_CommandActiveThreadCount:
				agent.handleActiveThreadCount(reqId, stream)
				break
			case *pb.PCmdRequest_CommandActiveThreadDump:
				if c := cmdReq.GetCommandActiveThreadDump(); c != nil {
					limit := c.GetLimit()
					threadName := c.GetThreadName()
					localId := c.GetLocalTraceId()
					agent.cmdGrpc.sendActiveThreadDump(reqId, limit, threadName, localId, dumpGoroutine(agent))
				}
				break
			case *pb.PCmdRequest_CommandActiveThreadLightDump:
				if c := cmdReq.GetCommandActiveThreadLightDump(); c != nil {
					agent.cmdGrpc.sendActiveThreadLightDump(reqId, c.GetLimit(), dumpGoroutine(agent))
				}
				break
			case nil:
			default:
				break
			}
		}

		stream.close()
	}

	Log("cmd").Infof("end command goroutine")
}

// handleActiveThreadCount starts an active thread count stream for reqId, or
// rejects the request when maxActiveThreadCountStreams are already running.
// Mirrors the C++ agent's handle_active_thread_count().
func (agent *agent) handleActiveThreadCount(reqId int32, cmd *cmdStream) {
	s := newActiveThreadCountStream(agent, reqId)

	// Register before opening the gRPC stream: the cap and the stop of a
	// same-id predecessor are then decided under a single lock, and a request
	// that the cap rejects never opens a stream at all.
	if !agent.cmdGrpc.atcStreams.add(s) {
		Log("cmd").Warnf("reject active thread count stream: %d, %d", reqId, agent.cmdGrpc.atcStreams.count())
		if err := cmd.sendFailMessage(reqId, "too many active thread count streams"); err != nil {
			Log("cmd").Errorf("send fail message - %d, %v", reqId, err)
		}
		return
	}

	if !agent.cmdGrpc.openActiveThreadCountStream(s) {
		agent.cmdGrpc.atcStreams.remove(s)
		return
	}

	go agent.sendActiveThreadCount(s)
}

func (agent *agent) sendActiveThreadCount(s *activeThreadCountStream) {
	Log("cmd").Infof("active thread count stream goroutine start: %d, %d", s.reqId, agent.cmdGrpc.atcStreams.count())

	// Deferred so that every exit path - send error, stop request and agent
	// shutdown alike - frees the slot this stream holds.
	defer func() {
		s.close()
		agent.cmdGrpc.atcStreams.remove(s)
		Log("cmd").Infof("active thread count stream goroutine finish: %d, %d", s.reqId, agent.cmdGrpc.atcStreams.count())
	}()

	shutdown := agent.stopSignal().Done()
	for agent.enable.Load() && !s.stopped() {
		err := s.sendActiveThreadCount()
		if err != nil {
			if err != io.EOF {
				Log("cmd").Errorf("send active thread count - %d, %v", s.reqId, err)
			}
			return
		}

		// Returning from inside the select rather than falling through to the
		// loop condition: shutdown is signalled before agent.enable is cleared,
		// so a stopped stream that only re-tested enable would spin.
		select {
		case <-s.stop:
			return
		case <-shutdown:
			return
		case <-time.After(activeThreadCountInterval):
		}
	}
}

func addRealTimeSampledActiveSpan(span *span) {
	if span.agent.atcStreamCount.Load() > 0 {
		span.goroutineId = curGoroutineID()
		s := &activeSpanInfo{span.startTime, span.txId.String(), span.rpcName, true}
		span.agent.realTimeActiveSpan.Store(span.goroutineId, s)
	}
}

func dropRealTimeSampledActiveSpan(span *span) {
	span.agent.realTimeActiveSpan.Delete(span.goroutineId)
}

func addRealTimeUnSampledActiveSpan(span *noopSpan) {
	if span.agent.atcStreamCount.Load() > 0 {
		span.goroutineId = curGoroutineID()
		s := &activeSpanInfo{span.startTime, "", span.rpcName, false}
		span.agent.realTimeActiveSpan.Store(span.goroutineId, s)
	}
}

func dropRealTimeUnSampledActiveSpan(span *noopSpan) {
	span.agent.realTimeActiveSpan.Delete(span.goroutineId)
}

func (agent *agent) getActiveSpanCount(now time.Time) []int32 {
	counts := []int32{0, 0, 0, 0}
	agent.realTimeActiveSpan.Range(func(k, v interface{}) bool {
		s := v.(*activeSpanInfo)
		d := now.Sub(s.startTime).Seconds()

		if d < 1 {
			counts[0]++
		} else if d < 3 {
			counts[1]++
		} else if d < 5 {
			counts[2]++
		} else {
			counts[3]++
		}
		return true
	})

	return counts
}
