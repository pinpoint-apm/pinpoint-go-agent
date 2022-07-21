package pinpoint

import (
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"time"
)

var gDump *GoroutineDump

func (agent *agent) runCommandService() {
	log("cmd").Info("command service goroutine start")
	defer agent.wg.Done()

	cmdStream := agent.cmdGrpc.newCommandStreamWithRetry()

	for true {
		if !agent.enable {
			break
		}

		err := cmdStream.sendCommandMessage()
		if err != nil {
			log("cmd").Errorf("fail to sendCommandMessage(): %v", err)
			cmdStream.close()
			cmdStream = agent.cmdGrpc.newCommandStreamWithRetry()
			continue
		}

		for true {
			err = cmdStream.recvCommandRequest()
			if err != nil {
				log("cmd").Errorf("fail to recvCommandRequest(): %v", err)
				break
			}

			cmdReq := cmdStream.cmdReq
			reqId := cmdReq.GetRequestId()
			log("cmd").Debugf("command service request: %v, %v", cmdReq, reqId)

			switch cmdReq.Command.(type) {
			case *pb.PCmdRequest_CommandEcho:
				msg := cmdReq.GetCommandEcho().GetMessage()
				agent.cmdGrpc.sendEcho(reqId, msg)
				break
			case *pb.PCmdRequest_CommandActiveThreadCount:
				atcStream := agent.cmdGrpc.newActiveThreadCountStream(reqId)
				go sendActiveThreadCount(atcStream)
				break
			case *pb.PCmdRequest_CommandActiveThreadDump:
				if c := cmdReq.GetCommandActiveThreadDump(); c != nil && gDump != nil {
					limit := c.GetLimit()
					threadName := c.GetThreadName()
					localId := c.GetLocalTraceId()
					agent.cmdGrpc.sendActiveThreadDump(reqId, limit, threadName, localId, gDump)
				}
				break
			case *pb.PCmdRequest_CommandActiveThreadLightDump:
				limit := cmdReq.GetCommandActiveThreadLightDump().GetLimit()
				if gDump = dumpGoroutine(); gDump != nil {
					agent.cmdGrpc.sendActiveThreadLightDump(reqId, limit, gDump)
				}
				break
			case nil:
				// The field is not set.
			default:
			}
		}

		if err != nil {
			cmdStream.close()
			cmdStream = agent.cmdGrpc.newCommandStreamWithRetry()
		}
	}

	cmdStream.close()
	log("cmd").Info("command service goroutine finish")
}

func sendActiveThreadCount(s *activeThreadCountStream) {
	for true {
		err := s.sendActiveThreadCount()
		if err != nil {
			log("cmd").Errorf("fail to sendActiveThreadCount(): %d, %v", s.reqId, err)
			break
		}
		time.Sleep(1 * time.Second)
	}
	s.close()
}
