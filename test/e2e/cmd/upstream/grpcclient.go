package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	ppgrpc "github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/grpc/example/testapp"
	"github.com/pinpoint-apm/pinpoint-go-agent/test/e2e/internal/e2e"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var grpcClient testapp.HelloClient

func initGrpcClient() error {
	conn, err := grpc.NewClient(grpcTarget,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(ppgrpc.UnaryClientInterceptor()),
		grpc.WithStreamInterceptor(ppgrpc.StreamClientInterceptor()),
	)
	if err != nil {
		return err
	}
	grpcClient = testapp.NewHelloClient(conn)
	return nil
}

type grpcOutcome struct {
	ok         bool
	propagated bool
	count      int
	traceID    string
	err        string
}

// parseTrace pulls the trace context the downstream encoded into its reply.
// testapp.Greeting carries only a message field, so the propagation evidence
// travels as "|trace_id=..|span_id=..|sampled=.." appended to it.
func parseTrace(msg string) (traceID, spanID string, sampled bool) {
	for _, part := range strings.Split(msg, "|") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch key {
		case "trace_id":
			traceID = value
		case "span_id":
			spanID = value
		case "sampled":
			sampled = value == "true"
		}
	}
	return traceID, spanID, sampled
}

// contextMatches reports whether the downstream joined this transaction.
func contextMatches(tracer pinpoint.Tracer, msg string) bool {
	traceID, spanID, sampled := parseTrace(msg)
	return tracer.IsSampled() && sampled && traceID != "" &&
		traceID == tracer.TransactionId().String() && spanID != "" && spanID != "0"
}

func grpcContext(tracer pinpoint.Tracer) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	return pinpoint.NewContext(ctx, tracer), cancel
}

func callUnary(tracer pinpoint.Tracer, message string) grpcOutcome {
	ctx, cancel := grpcContext(tracer)
	defer cancel()

	resp, err := grpcClient.UnaryCallUnaryReturn(ctx, &testapp.Greeting{Msg: message})
	if err != nil {
		return grpcOutcome{err: err.Error()}
	}
	traceID, _, _ := parseTrace(resp.GetMsg())
	return grpcOutcome{ok: true, propagated: contextMatches(tracer, resp.GetMsg()), count: 1, traceID: traceID}
}

func callServerStream(tracer pinpoint.Tracer) grpcOutcome {
	ctx, cancel := grpcContext(tracer)
	defer cancel()

	stream, err := grpcClient.UnaryCallStreamReturn(ctx, &testapp.Greeting{Msg: "Stream greetings from go-e2e"})
	if err != nil {
		return grpcOutcome{err: err.Error()}
	}
	count, propagated, traceID := 0, true, ""
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return grpcOutcome{count: count, err: err.Error()}
		}
		propagated = propagated && contextMatches(tracer, resp.GetMsg())
		traceID, _, _ = parseTrace(resp.GetMsg())
		count++
	}
	return grpcOutcome{ok: true, propagated: count == 3 && propagated, count: count, traceID: traceID}
}

func callClientStream(tracer pinpoint.Tracer, count int) grpcOutcome {
	ctx, cancel := grpcContext(tracer)
	defer cancel()

	stream, err := grpcClient.StreamCallUnaryReturn(ctx)
	if err != nil {
		return grpcOutcome{err: err.Error()}
	}
	written := 0
	for i := 0; i < count; i++ {
		if err := stream.Send(&testapp.Greeting{Msg: fmt.Sprintf("Client stream %d", i)}); err != nil {
			break
		}
		written++
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return grpcOutcome{count: written, err: err.Error()}
	}
	traceID, _, _ := parseTrace(resp.GetMsg())
	return grpcOutcome{
		ok:         true,
		propagated: written == count && contextMatches(tracer, resp.GetMsg()),
		count:      written,
		traceID:    traceID,
	}
}

func callBidi(tracer pinpoint.Tracer, count int) grpcOutcome {
	ctx, cancel := grpcContext(tracer)
	defer cancel()

	stream, err := grpcClient.StreamCallStreamReturn(ctx)
	if err != nil {
		return grpcOutcome{err: err.Error()}
	}
	received, propagated, traceID := 0, true, ""
	for i := 0; i < count; i++ {
		if err := stream.Send(&testapp.Greeting{Msg: fmt.Sprintf("Message %d", i)}); err != nil {
			break
		}
		resp, err := stream.Recv()
		if err != nil {
			break
		}
		propagated = propagated && contextMatches(tracer, resp.GetMsg())
		traceID, _, _ = parseTrace(resp.GetMsg())
		received++
	}
	stream.CloseSend()
	return grpcOutcome{ok: true, propagated: received == count && propagated, count: received, traceID: traceID}
}

func writeGrpcResponse(w http.ResponseWriter, r *http.Request, tracer pinpoint.Tracer,
	method string, outcome grpcOutcome, expectedError bool) {
	status := http.StatusOK
	if !outcome.ok && !expectedError {
		status = http.StatusBadGateway
	}
	setTraceHeaders(w, tracer)
	e2e.WriteJSON(w, status, map[string]any{
		"method":         method,
		"ok":             outcome.ok,
		"propagated":     outcome.propagated,
		"count":          outcome.count,
		"trace_id":       outcome.traceID,
		"error":          outcome.err,
		"expected_error": expectedError,
	})
	finishSpan(w, r, tracer, status)
}

func onGrpcUnary(w http.ResponseWriter, r *http.Request) {
	defer track()()
	tracer := newSpan(r)
	writeGrpcResponse(w, r, tracer, "unary", callUnary(tracer, "Hello from go-e2e unary"), false)
}

func onGrpcServerStream(w http.ResponseWriter, r *http.Request) {
	defer track()()
	tracer := newSpan(r)
	writeGrpcResponse(w, r, tracer, "server_stream", callServerStream(tracer), false)
}

func onGrpcClientStream(w http.ResponseWriter, r *http.Request) {
	defer track()()
	tracer := newSpan(r)
	count := e2e.IntParam(r, "count", 3, 1, 20)
	writeGrpcResponse(w, r, tracer, "client_stream", callClientStream(tracer, count), false)
}

func onGrpcBidi(w http.ResponseWriter, r *http.Request) {
	defer track()()
	tracer := newSpan(r)
	count := e2e.IntParam(r, "count", 3, 1, 20)
	writeGrpcResponse(w, r, tracer, "bidi", callBidi(tracer, count), false)
}

func onGrpcError(w http.ResponseWriter, r *http.Request) {
	defer track()()
	tracer := newSpan(r)
	outcome := callUnary(tracer, "force-error")
	writeGrpcResponse(w, r, tracer, "error", outcome, !outcome.ok && outcome.err != "")
}

func onGrpcAll(w http.ResponseWriter, r *http.Request) {
	defer track()()
	tracer := newSpan(r)

	unary := callUnary(tracer, "all-test unary")
	serverStream := callServerStream(tracer)
	clientStream := callClientStream(tracer, 3)
	bidi := callBidi(tracer, 3)

	ok := unary.ok && serverStream.ok && clientStream.ok && bidi.ok
	propagated := unary.propagated && serverStream.propagated && clientStream.propagated && bidi.propagated
	status := http.StatusOK
	if !ok {
		status = http.StatusBadGateway
	}

	setTraceHeaders(w, tracer)
	e2e.WriteJSON(w, status, map[string]any{
		"method":              "all",
		"ok":                  ok,
		"propagated":          propagated,
		"methods":             4,
		"server_stream_count": serverStream.count,
		"client_stream_count": clientStream.count,
		"bidi_count":          bidi.count,
	})
	finishSpan(w, r, tracer, status)
}

func logGrpcTarget() {
	log.Printf("gRPC client endpoints enabled (target=%s)", grpcTarget)
	log.Printf("HTTP client endpoints enabled (target=%s)", httpTarget)
}
