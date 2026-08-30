package ppgohbase

import (
	"context"
	"errors"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	hbase "github.com/tsuna/gohbase"
	"github.com/tsuna/gohbase/hrpc"
)

// recordingTracer captures what the wrapper records on a span event. A real
// tracer's recorders are write-only, so this stands in for one.
type recordingTracer struct {
	pinpoint.Tracer
	events []*recordedEvent
}

func newRecordingTracer() *recordingTracer {
	return &recordingTracer{Tracer: pinpoint.NoopTracer()}
}

func (t *recordingTracer) IsSampled() bool { return true }

func (t *recordingTracer) NewSpanEvent(operation string) pinpoint.Tracer {
	t.events = append(t.events, &recordedEvent{
		SpanEventRecorder: t.Tracer.SpanEvent(),
		operation:         operation,
		annotations:       map[int32]string{},
	})
	return t
}

func (t *recordingTracer) SpanEvent() pinpoint.SpanEventRecorder { return t.last() }

func (t *recordingTracer) EndSpanEvent() { t.last().ended = true }

func (t *recordingTracer) last() *recordedEvent { return t.events[len(t.events)-1] }

type recordedEvent struct {
	pinpoint.SpanEventRecorder
	operation   string
	serviceType int32
	destination string
	endPoint    string
	err         error
	annotations map[int32]string
	ended       bool
}

func (e *recordedEvent) SetServiceType(typ int32)        { e.serviceType = typ }
func (e *recordedEvent) SetDestination(id string)        { e.destination = id }
func (e *recordedEvent) SetEndPoint(endPoint string)     { e.endPoint = endPoint }
func (e *recordedEvent) SetError(err error, _ ...string) { e.err = err }

func (e *recordedEvent) Annotations() pinpoint.Annotation {
	return recordedAnnotation{Annotation: e.SpanEventRecorder.Annotations(), into: e.annotations}
}

type recordedAnnotation struct {
	pinpoint.Annotation
	into map[int32]string
}

func (a recordedAnnotation) AppendString(key int32, s string) { a.into[key] = s }

// fakeClient stands in for a real HBase cluster: it records the call and
// returns whatever the test asked for.
type fakeClient struct {
	hbase.Client
	calls int
	err   error
}

func (c *fakeClient) Get(*hrpc.Get) (*hrpc.Result, error)    { c.calls++; return nil, c.err }
func (c *fakeClient) Put(*hrpc.Mutate) (*hrpc.Result, error) { c.calls++; return nil, c.err }
func (c *fakeClient) Delete(*hrpc.Mutate) (*hrpc.Result, error) {
	c.calls++
	return nil, c.err
}
func (c *fakeClient) Append(*hrpc.Mutate) (*hrpc.Result, error) {
	c.calls++
	return nil, c.err
}
func (c *fakeClient) Increment(*hrpc.Mutate) (int64, error) { c.calls++; return 7, c.err }
func (c *fakeClient) CheckAndPut(*hrpc.Mutate, string, string, []byte) (bool, error) {
	c.calls++
	return true, c.err
}
func (c *fakeClient) Scan(*hrpc.Scan) hrpc.Scanner { c.calls++; return nil }
func (c *fakeClient) Close()                       {}

func newClient(t *testing.T) (*Client, *fakeClient) {
	t.Helper()
	fake := &fakeClient{}
	return &Client{Client: fake, host: "zk1.example:2181"}, fake
}

func values() map[string]map[string][]byte {
	return map[string]map[string][]byte{"cf": {"a": []byte("1")}}
}

// The row key is the only detail that makes an HBase span event actionable, so
// each operation has to record its own key under the HBase parameter
// annotation, along with the ZooKeeper quorum it was addressed to.
func TestClient_RecordsTheRowKey(t *testing.T) {
	for _, tt := range []struct {
		operation string
		call      func(*Client, context.Context) error
	}{
		{"hbase.Get", func(c *Client, ctx context.Context) error {
			g, err := hrpc.NewGetStr(ctx, "table", "rowkey")
			if err != nil {
				return err
			}
			_, err = c.Get(g)
			return err
		}},
		{"hbase.Put", func(c *Client, ctx context.Context) error {
			p, err := hrpc.NewPutStr(ctx, "table", "rowkey", values())
			if err != nil {
				return err
			}
			_, err = c.Put(p)
			return err
		}},
		{"hbase.Delete", func(c *Client, ctx context.Context) error {
			d, err := hrpc.NewDelStr(ctx, "table", "rowkey", values())
			if err != nil {
				return err
			}
			_, err = c.Delete(d)
			return err
		}},
		{"hbase.Append", func(c *Client, ctx context.Context) error {
			a, err := hrpc.NewAppStr(ctx, "table", "rowkey", values())
			if err != nil {
				return err
			}
			_, err = c.Append(a)
			return err
		}},
		{"hbase.Increment", func(c *Client, ctx context.Context) error {
			i, err := hrpc.NewIncStr(ctx, "table", "rowkey", values())
			if err != nil {
				return err
			}
			_, err = c.Increment(i)
			return err
		}},
		{"hbase.CheckAndPut", func(c *Client, ctx context.Context) error {
			p, err := hrpc.NewPutStr(ctx, "table", "rowkey", values())
			if err != nil {
				return err
			}
			_, err = c.CheckAndPut(p, "cf", "a", []byte("1"))
			return err
		}},
	} {
		t.Run(tt.operation, func(t *testing.T) {
			client, fake := newClient(t)
			tracer := newRecordingTracer()

			if err := tt.call(client, pinpoint.NewContext(context.Background(), tracer)); err != nil {
				t.Fatal(err)
			}

			if fake.calls != 1 {
				t.Fatalf("the underlying client was called %d times, want 1", fake.calls)
			}
			if len(tracer.events) != 1 {
				t.Fatalf("recorded %d span events, want 1", len(tracer.events))
			}
			e := tracer.events[0]
			if e.operation != tt.operation {
				t.Errorf("operation = %q, want %q", e.operation, tt.operation)
			}
			if e.serviceType != pinpoint.ServiceTypeHbaseClient {
				t.Errorf("service type = %d, want %d", e.serviceType, pinpoint.ServiceTypeHbaseClient)
			}
			if e.destination != "HBASE" {
				t.Errorf("destination = %q, want HBASE", e.destination)
			}
			if e.endPoint != "zk1.example:2181" {
				t.Errorf("endpoint = %q, want %q", e.endPoint, "zk1.example:2181")
			}
			if got, want := e.annotations[pinpoint.AnnotationHbaseClientParams], "rowKey: rowkey"; got != want {
				t.Errorf("params annotation = %q, want %q", got, want)
			}
			if !e.ended {
				t.Error("the span event was left open")
			}
		})
	}
}

// A scan covers a key range rather than one row, so both ends of the range
// belong in the annotation.
func TestClient_Scan(t *testing.T) {
	client, fake := newClient(t)
	tracer := newRecordingTracer()

	s, err := hrpc.NewScanRangeStr(pinpoint.NewContext(context.Background(), tracer), "table", "aaa", "zzz")
	if err != nil {
		t.Fatal(err)
	}
	client.Scan(s)

	if fake.calls != 1 {
		t.Fatalf("the underlying client was called %d times, want 1", fake.calls)
	}
	if len(tracer.events) != 1 {
		t.Fatalf("recorded %d span events, want 1", len(tracer.events))
	}
	e := tracer.events[0]
	if e.operation != "hbase.Scan" {
		t.Errorf("operation = %q, want hbase.Scan", e.operation)
	}
	if got, want := e.annotations[pinpoint.AnnotationHbaseClientParams], "startRowKey: aaa, stopRowKey: zzz"; got != want {
		t.Errorf("params annotation = %q, want %q", got, want)
	}
	if !e.ended {
		t.Error("the span event was left open")
	}
}

// A failed operation has to reach the caller unchanged and be marked on the
// span event; a silent failure would hide the very calls tracing is for.
func TestClient_RecordsTheOperationError(t *testing.T) {
	fake := &fakeClient{err: errors.New("region unavailable")}
	client := &Client{Client: fake, host: "zk1.example:2181"}
	tracer := newRecordingTracer()

	g, err := hrpc.NewGetStr(pinpoint.NewContext(context.Background(), tracer), "table", "rowkey")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(g); !errors.Is(err, fake.err) {
		t.Errorf("Get() = %v, want %v", err, fake.err)
	}
	if !errors.Is(tracer.events[0].err, fake.err) {
		t.Errorf("recorded error = %v, want %v", tracer.events[0].err, fake.err)
	}
}

// The wrapper replaces the application's client, so every operation must still
// run when there is no span to record it on - and must record nothing, or the
// span-event stack of whatever runs next on that goroutine unbalances.
func TestClient_PassesThroughWithoutASampledTracer(t *testing.T) {
	for _, tt := range []struct {
		name string
		ctx  context.Context
	}{
		{"background context", context.Background()},
		{"noop tracer", pinpoint.NewContext(context.Background(), pinpoint.NoopTracer())},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, fake := newClient(t)

			g, err := hrpc.NewGetStr(tt.ctx, "table", "rowkey")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Get(g); err != nil {
				t.Fatal(err)
			}
			p, err := hrpc.NewPutStr(tt.ctx, "table", "rowkey", values())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Put(p); err != nil {
				t.Fatal(err)
			}

			if fake.calls != 2 {
				t.Errorf("the underlying client was called %d times, want 2", fake.calls)
			}
		})
	}
}

func Test_keyString(t *testing.T) {
	if got, want := keyString([]byte("rowkey")), "rowKey: rowkey"; got != want {
		t.Errorf("keyString() = %q, want %q", got, want)
	}
	if got, want := scanKeyString([]byte("aaa"), []byte("zzz")), "startRowKey: aaa, stopRowKey: zzz"; got != want {
		t.Errorf("scanKeyString() = %q, want %q", got, want)
	}
	// An open-ended scan has empty bounds rather than absent ones.
	if got, want := scanKeyString(nil, nil), "startRowKey: , stopRowKey: "; got != want {
		t.Errorf("scanKeyString(nil, nil) = %q, want %q", got, want)
	}
}
