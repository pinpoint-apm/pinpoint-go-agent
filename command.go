package pinpoint

import (
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"os"
	"path/filepath"
	"runtime/pprof"
	"time"
)

var gDump *GoroutineDump

func commandService(agent *Agent) {
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
			log("cmd").Debugf("command service request: %v", cmdReq)

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
				limit := cmdReq.GetCommandActiveThreadDump().GetLimit()
				threadName := cmdReq.GetCommandActiveThreadDump().GetThreadName()
				localId := cmdReq.GetCommandActiveThreadDump().GetLocalTraceId()
				agent.cmdGrpc.sendActiveThreadDump(reqId, limit, threadName, localId, gDump)
				break
			case *pb.PCmdRequest_CommandActiveThreadLightDump:
				limit := cmdReq.GetCommandActiveThreadLightDump().GetLimit()
				gDump = dumpGoroutine()
				agent.cmdGrpc.sendActiveThreadLightDump(reqId, limit, gDump)
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

func dumpGoroutine() *GoroutineDump {
	//var b bytes.Buffer
	//f := bufio.NewWriter(&b)

	fn := filepath.Join("/tmp", "goroutine.pprof")
	f, err := os.Create(fn)
	if err != nil {
		log("cmd").Errorf("fail to os.Create(): %v", err)
	}

	if mp := pprof.Lookup("goroutine"); mp != nil {
		mp.WriteTo(f, 2)
	}
	f.Close()

	dump, err := loadProfile(fn)
	if err != nil {
		log("cmd").Errorf("fail to dumpGoroutine(): %v", err)
		return nil
	}

	return dump
}
