package ppgohbase

import (
	"context"
	"errors"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	hbase "github.com/tsuna/gohbase"
	"github.com/tsuna/gohbase/hrpc"
)

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

func newClient(t *testing.T, err error) (*Client, *fakeClient) {
	t.Helper()
	fake := &fakeClient{err: err}
	return &Client{Client: fake, host: "zk1.example:2181"}, fake
}

func values() map[string]map[string][]byte {
	return map[string]map[string][]byte{"cf": {"a": []byte("1")}}
}

// The row key is the only detail that makes an HBase span event actionable, so
// each operation has to record its own key under the HBase parameter
// annotation, along with the ZooKeeper quorum it was addressed to. A failed
// operation has to reach the caller unchanged and be marked on the same event;
// a silent failure would hide the very calls tracing is for.
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
			for _, want := range []error{nil, errors.New("region unavailable")} {
				client, fake := newClient(t, want)
				tracer := pptest.NewRecordingTracer()

				err := tt.call(client, pinpoint.NewContext(context.Background(), tracer))
				require.ErrorIs(t, err, want, "the operation's error must come back unchanged")

				require.Equal(t, 1, fake.calls, "the underlying client must be called exactly once")
				require.Len(t, tracer.Events, 1, "one operation must produce exactly one span event")
				e := tracer.Events[0]
				assert.Equal(t, tt.operation, e.Operation)
				assert.Equal(t, int32(pinpoint.ServiceTypeHbaseClient), e.ServiceType)
				assert.Equal(t, "HBASE", e.Destination)
				assert.Equal(t, "zk1.example:2181", e.EndPoint, "the ZooKeeper quorum is the endpoint")
				assert.Equal(t, "rowKey: rowkey", e.Strings[pinpoint.AnnotationHbaseClientParams])
				assert.ErrorIs(t, e.Err, want, "the span event must carry the operation's verdict")
				assert.True(t, e.Ended, "the span event was left open")
			}
		})
	}
}

// A scan covers a key range rather than one row, so both ends of the range
// belong in the annotation.
func TestClient_Scan(t *testing.T) {
	client, fake := newClient(t, nil)
	tracer := pptest.NewRecordingTracer()

	s, err := hrpc.NewScanRangeStr(pinpoint.NewContext(context.Background(), tracer), "table", "aaa", "zzz")
	require.NoError(t, err)
	client.Scan(s)

	require.Equal(t, 1, fake.calls, "the underlying client must be called exactly once")
	require.Len(t, tracer.Events, 1)
	e := tracer.Events[0]
	assert.Equal(t, "hbase.Scan", e.Operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeHbaseClient), e.ServiceType)
	assert.Equal(t, "startRowKey: aaa, stopRowKey: zzz", e.Strings[pinpoint.AnnotationHbaseClientParams])
	assert.True(t, e.Ended, "the span event was left open")
}

// The wrapper replaces the application's client, so every operation must still
// run when there is no span to record it on - and must record nothing, or the
// span-event stack of whatever runs next on that goroutine unbalances.
func TestClient_PassesThroughWithoutASampledTracer(t *testing.T) {
	client, fake := newClient(t, nil)
	ctx := context.Background()

	g, err := hrpc.NewGetStr(ctx, "table", "rowkey")
	require.NoError(t, err)
	_, err = client.Get(g)
	require.NoError(t, err)

	p, err := hrpc.NewPutStr(ctx, "table", "rowkey", values())
	require.NoError(t, err)
	_, err = client.Put(p)
	require.NoError(t, err)

	s, err := hrpc.NewScanStr(ctx, "table")
	require.NoError(t, err)
	client.Scan(s)

	assert.Equal(t, 3, fake.calls, "every operation must still reach the underlying client")
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
}

// WrapClient is NewClient for a client created elsewhere (compile-time
// instrumentation); wrapping twice must not stack two layers.
func TestWrapClient(t *testing.T) {
	base := &fakeClient{}
	c := WrapClient(base, "zk1,zk2")
	assert.Equal(t, "zk1,zk2", c.host)
	assert.Same(t, base, c.Client)
	assert.Same(t, c, WrapClient(c, "other"), "an already wrapped client is returned as it is")
}
