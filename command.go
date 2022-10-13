package pinpoint

import (
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

var (
	gAtcStreamCount    int32
	realTimeActiveSpan sync.Map
)

type activeSpanInfo struct {
	startTime  time.Time
	txId       string
	entryPoint string
	sampled    bool
}

func (agent *agent) runCommandService() {
	Log("cmd").Info("start command goroutine")
	defer agent.wg.Done()
	gAtcStreamCount = 0

	for agent.enable {
		stream := agent.cmdGrpc.newCommandStreamWithRetry()
		err := stream.sendCommandMessage()
		if err != nil {
			if err != io.EOF {
				Log("cmd").Errorf("send command message - %v", err)
			}
			stream.close()
			continue
		}

		for agent.enable {
			cmdReq, err := stream.recvCommandRequest()
			if err != nil {
				if agent.enable && err != io.EOF {
					Log("cmd").Warnf("recv command request - %v", err)
				}
				break
			}

			reqId := cmdReq.GetRequestId()
			Log("cmd").Infof("command request: %v, %v", cmdReq, reqId)

			switch cmdReq.Command.(type) {
			case *pb.PCmdRequest_CommandEcho:
				msg := cmdReq.GetCommandEcho().GetMessage()
				agent.cmdGrpc.sendEcho(reqId, msg)
				break
			case *pb.PCmdRequest_CommandActiveThreadCount:
				atcStream := agent.cmdGrpc.newActiveThreadCountStream(reqId)
				go agent.sendActiveThreadCount(atcStream)
				break
			case *pb.PCmdRequest_CommandActiveThreadDump:
				if c := cmdReq.GetCommandActiveThreadDump(); c != nil {
					limit := c.GetLimit()
					threadName := c.GetThreadName()
					localId := c.GetLocalTraceId()
					agent.cmdGrpc.sendActiveThreadDump(reqId, limit, threadName, localId, dumpGoroutine())
				}
				break
			case *pb.PCmdRequest_CommandActiveThreadLightDump:
				if c := cmdReq.GetCommandActiveThreadLightDump(); c != nil {
					agent.cmdGrpc.sendActiveThreadLightDump(reqId, c.GetLimit(), dumpGoroutine())
				}
				break
			case nil:
			default:
				break
			}
		}

		stream.close()
	}

	Log("cmd").Info("end command goroutine")
}

func (agent *agent) sendActiveThreadCount(s *activeThreadCountStream) {
	atomic.AddInt32(&gAtcStreamCount, 1)
	Log("cmd").Infof("active thread count stream goroutine start: %d, %d", s.reqId, gAtcStreamCount)

	for agent.enable {
		err := s.sendActiveThreadCount()
		if err != nil {
			if err != io.EOF {
				Log("cmd").Errorf("send active thread count - %d, %v", s.reqId, err)
			}
			break
		}
		time.Sleep(1 * time.Second)
	}
	s.close()

	atomic.AddInt32(&gAtcStreamCount, -1)
	Log("cmd").Infof("active thread count stream goroutine finish: %d, %d", s.reqId, gAtcStreamCount)
}

func addRealTimeSampledActiveSpan(span *span) {
	if gAtcStreamCount > 0 {
		span.goroutineId = curGoroutineID()
		s := activeSpanInfo{span.startTime, span.txId.String(), span.rpcName, true}
		realTimeActiveSpan.Store(span.goroutineId, s)
	}
}

func dropRealTimeSampledActiveSpan(span *span) {
	realTimeActiveSpan.Delete(span.goroutineId)
}

func addRealTimeUnSampledActiveSpan(span *noopSpan) {
	if gAtcStreamCount > 0 {
		span.goroutineId = curGoroutineID()
		s := activeSpanInfo{span.startTime, "", span.rpcName, false}
		realTimeActiveSpan.Store(span.goroutineId, s)
	}
}

func dropRealTimeUnSampledActiveSpan(span *noopSpan) {
	realTimeActiveSpan.Delete(span.goroutineId)
}

func getActiveSpanCount(now time.Time) []int32 {
	counts := []int32{0, 0, 0, 0}
	realTimeActiveSpan.Range(func(k, v interface{}) bool {
		s := v.(activeSpanInfo)
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
