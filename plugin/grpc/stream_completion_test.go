package ppgrpc

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

func streamFinishCallback(t *testing.T, opts []grpc.CallOption) func(error) {
	t.Helper()
	for i := len(opts) - 1; i >= 0; i-- {
		if opt, ok := opts[i].(grpc.OnFinishCallOption); ok {
			return opt.OnFinish
		}
	}
	t.Fatal("stream interceptor did not register a completion callback")
	return nil
}

type completionTracer struct {
	*recordingTracer
	child *completionTracer
	ends  int
	done  chan struct{}
}

func (r *completionTracer) NewGoroutineTracer() pinpoint.Tracer { return r.child }
func (r *completionTracer) NewSpanEvent(s string) pinpoint.Tracer {
	r.recordingTracer.NewSpanEvent(s)
	return r
}
func (r *completionTracer) EndSpan() {
	r.ends++
	if r.done != nil {
		close(r.done)
	}
}

// gRPC calls OnFinish before canceling its stream context, including when
// finishing successfully. It may complete before the streamer returns.
func TestStreamClientInterceptor_RecordsFinalStatus(t *testing.T) {
	startAgent(t)
	for _, result := range []error{nil, status.Error(codes.Unavailable, "backend failed"), context.Canceled, context.DeadlineExceeded} {
		for _, early := range []bool{false, true} {
			child := &completionTracer{recordingTracer: newRecordingTracer()}
			caller := &completionTracer{recordingTracer: newRecordingTracer(), child: child}
			fake := newFakeClientStream(t, io.EOF)
			var finish func(error)
			stream, err := StreamClientInterceptor()(
				pinpoint.NewContext(context.Background(), caller), &grpc.StreamDesc{ServerStreams: true},
				lazyConn(t, "localhost:8080"), "/test/Stream",
				func(_ context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
					finish = streamFinishCallback(t, opts)
					if early {
						finish(result)
						fake.cancel()
					}
					return fake, nil
				})
			require.NoError(t, err)
			if !early {
				finish(result)
				fake.cancel()
			}
			require.ErrorIs(t, stream.RecvMsg(nil), io.EOF)
			require.Equal(t, result, child.event.err)
			require.Equal(t, 1, child.ends, "callback and RecvMsg must end the span only once")
		}
	}
}

// Exercise the real gRPC completion callback over an in-memory transport,
// including a canceled stream whose caller never calls RecvMsg again.
func TestStreamClientInterceptor_RealStreamCompletion(t *testing.T) {
	startAgent(t)
	for _, code := range []codes.Code{codes.OK, codes.Unavailable, codes.Canceled} {
		t.Run(code.String(), func(t *testing.T) {
			listener := bufconn.Listen(1024 * 1024)
			server := grpc.NewServer()
			server.RegisterService(&grpc.ServiceDesc{
				ServiceName: "completion.Test",
				HandlerType: (*interface{})(nil),
				Streams: []grpc.StreamDesc{{
					StreamName: "Run", ServerStreams: true,
					Handler: func(_ interface{}, stream grpc.ServerStream) error {
						switch code {
						case codes.Canceled:
							<-stream.Context().Done()
							return stream.Context().Err()
						case codes.Unavailable:
							return status.Error(code, "backend unavailable")
						default:
							return stream.SendMsg(&emptypb.Empty{})
						}
					},
				}},
			}, struct{}{})
			go server.Serve(listener)
			t.Cleanup(func() { server.Stop(); listener.Close() })
			cc, err := grpc.NewClient("passthrough:///completion",
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
				grpc.WithStreamInterceptor(StreamClientInterceptor()))
			require.NoError(t, err)
			t.Cleanup(func() { cc.Close() })
			child := &completionTracer{recordingTracer: newRecordingTracer(), done: make(chan struct{})}
			caller := &completionTracer{recordingTracer: newRecordingTracer(), child: child}
			ctx, cancel := context.WithTimeout(pinpoint.NewContext(context.Background(), caller), 5*time.Second)
			defer cancel()
			stream, err := cc.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/completion.Test/Run")
			require.NoError(t, err)
			require.NoError(t, stream.CloseSend())
			if code == codes.Canceled {
				cancel()
			} else {
				err = stream.RecvMsg(&emptypb.Empty{})
				if code == codes.OK {
					require.NoError(t, err)
					require.ErrorIs(t, stream.RecvMsg(&emptypb.Empty{}), io.EOF)
				} else {
					require.Equal(t, code, status.Code(err))
				}
			}
			select {
			case <-child.done:
			case <-time.After(5 * time.Second):
				t.Fatal("RPC completion did not end its span")
			}
			require.Equal(t, code, status.Code(child.event.err))
			require.Equal(t, 1, child.ends)
		})
	}
}
