package ppgohbase

import (
	"context"
	"errors"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

			require.NoError(t, tt.call(client, pinpoint.NewContext(context.Background(), tracer)))

			require.Equal(t, 1, fake.calls, "the underlying client must be called exactly once")
			require.Len(t, tracer.events, 1, "one operation must produce exactly one span event")
			e := tracer.events[0]
			assert.Equal(t, tt.operation, e.operation)
			assert.Equal(t, int32(pinpoint.ServiceTypeHbaseClient), e.serviceType)
			assert.Equal(t, "HBASE", e.destination)
			assert.Equal(t, "zk1.example:2181", e.endPoint, "the ZooKeeper quorum is the endpoint")
			assert.Equal(t, "rowKey: rowkey", e.annotations[pinpoint.AnnotationHbaseClientParams])
			assert.NoError(t, e.err, "a successful operation must not fail the span event")
			assert.True(t, e.ended, "the span event was left open")
		})
	}
}

// A scan covers a key range rather than one row, so both ends of the range
// belong in the annotation.
func TestClient_Scan(t *testing.T) {
	client, fake := newClient(t)
	tracer := newRecordingTracer()

	s, err := hrpc.NewScanRangeStr(pinpoint.NewContext(context.Background(), tracer), "table", "aaa", "zzz")
	require.NoError(t, err)
	client.Scan(s)

	require.Equal(t, 1, fake.calls, "the underlying client must be called exactly once")
	require.Len(t, tracer.events, 1)
	e := tracer.events[0]
	assert.Equal(t, "hbase.Scan", e.operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeHbaseClient), e.serviceType)
	assert.Equal(t, "startRowKey: aaa, stopRowKey: zzz", e.annotations[pinpoint.AnnotationHbaseClientParams])
	assert.True(t, e.ended, "the span event was left open")
}

// An open-ended scan has no bounds to name, and must still be recorded rather
// than skipped.
func TestClient_ScanWithoutARange(t *testing.T) {
	client, fake := newClient(t)
	tracer := newRecordingTracer()

	s, err := hrpc.NewScanStr(pinpoint.NewContext(context.Background(), tracer), "table")
	require.NoError(t, err)
	client.Scan(s)

	require.Equal(t, 1, fake.calls)
	require.Len(t, tracer.events, 1)
	assert.Equal(t, "startRowKey: , stopRowKey: ",
		tracer.events[0].annotations[pinpoint.AnnotationHbaseClientParams])
}

// A failed operation has to reach the caller unchanged and be marked on the
// span event; a silent failure would hide the very calls tracing is for.
func TestClient_RecordsTheOperationError(t *testing.T) {
	fake := &fakeClient{err: errors.New("region unavailable")}
	client := &Client{Client: fake, host: "zk1.example:2181"}
	tracer := newRecordingTracer()

	g, err := hrpc.NewGetStr(pinpoint.NewContext(context.Background(), tracer), "table", "rowkey")
	require.NoError(t, err)

	_, err = client.Get(g)
	assert.ErrorIs(t, err, fake.err, "the operation's error must come back unchanged")

	require.Len(t, tracer.events, 1)
	assert.ErrorIs(t, tracer.events[0].err, fake.err)
	assert.True(t, tracer.events[0].ended, "a failed operation must still close its span event")
}

// Every operation has to mark its own failure, not only Get.
func TestClient_EveryOperationRecordsItsError(t *testing.T) {
	want := errors.New("region unavailable")

	for _, tt := range []struct {
		operation string
		call      func(*Client, context.Context) error
	}{
		{"hbase.Put", func(c *Client, ctx context.Context) error {
			p, err := hrpc.NewPutStr(ctx, "table", "rowkey", values())
			require.NoError(t, err)
			_, err = c.Put(p)
			return err
		}},
		{"hbase.Delete", func(c *Client, ctx context.Context) error {
			d, err := hrpc.NewDelStr(ctx, "table", "rowkey", values())
			require.NoError(t, err)
			_, err = c.Delete(d)
			return err
		}},
		{"hbase.Increment", func(c *Client, ctx context.Context) error {
			i, err := hrpc.NewIncStr(ctx, "table", "rowkey", values())
			require.NoError(t, err)
			_, err = c.Increment(i)
			return err
		}},
	} {
		t.Run(tt.operation, func(t *testing.T) {
			client := &Client{Client: &fakeClient{err: want}, host: "zk1.example:2181"}
			tracer := newRecordingTracer()

			err := tt.call(client, pinpoint.NewContext(context.Background(), tracer))

			assert.ErrorIs(t, err, want)
			require.Len(t, tracer.events, 1)
			assert.ErrorIs(t, tracer.events[0].err, want)
			assert.True(t, tracer.events[0].ended)
		})
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
			tracer := newRecordingTracer()

			g, err := hrpc.NewGetStr(tt.ctx, "table", "rowkey")
			require.NoError(t, err)
			_, err = client.Get(g)
			require.NoError(t, err)

			p, err := hrpc.NewPutStr(tt.ctx, "table", "rowkey", values())
			require.NoError(t, err)
			_, err = client.Put(p)
			require.NoError(t, err)

			s, err := hrpc.NewScanStr(tt.ctx, "table")
			require.NoError(t, err)
			client.Scan(s)

			assert.Equal(t, 3, fake.calls, "every operation must still reach the underlying client")
			assert.Empty(t, tracer.events, "an untraced operation must not record a span event")
		})
	}
}

func Test_keyString(t *testing.T) {
	assert.Equal(t, "rowKey: rowkey", keyString([]byte("rowkey")))
	assert.Equal(t, "rowKey: ", keyString(nil))
	assert.Equal(t, "startRowKey: aaa, stopRowKey: zzz", scanKeyString([]byte("aaa"), []byte("zzz")))
	// An open-ended scan has empty bounds rather than absent ones.
	assert.Equal(t, "startRowKey: , stopRowKey: ", scanKeyString(nil, nil))
	assert.Equal(t, "startRowKey: aaa, stopRowKey: ", scanKeyString([]byte("aaa"), nil))
}

// NewClient keeps the ZooKeeper quorum it was given, which is what every span
// event reports as the endpoint.
func TestNewClient_KeepsTheQuorum(t *testing.T) {
	c := NewClient("zk1.example:2181,zk2.example:2181")
	t.Cleanup(c.Close)

	assert.Equal(t, "zk1.example:2181,zk2.example:2181", c.host)
	assert.Implements(t, (*hbase.Client)(nil), c, "the wrapper must still be a gohbase.Client")
}
