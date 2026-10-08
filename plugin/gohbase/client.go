// Package ppgohbase instruments the tsuna/gohbase package (https://github.com/tsuna/gohbase).
//
// This package instruments the gohbase calls.
// Use the NewClient as the gohbase.NewClient.
//
//	client := ppgohbase.NewClient("localhost")
//
// It is necessary to pass the context containing the pinpoint.Tracer to gohbase.Client.
//
//	ctx := pinpoint.NewContext(context.Background(), tracer)
//	putRequest, _ := hrpc.NewPutStr(ctx, "table", "key", values)
//	client.Put(putRequest)
package ppgohbase

import (
	"context"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	hbase "github.com/tsuna/gohbase"
	"github.com/tsuna/gohbase/hrpc"
)

type Client struct {
	hbase.Client
	host string
}

// NewClient wraps gohbase.NewClient and returns a gohbase.Client ready to instrument.
func NewClient(zkquorum string, options ...hbase.Option) *Client {
	client := hbase.NewClient(zkquorum, options...)
	return &Client{Client: client, host: zkquorum}
}

// WrapClient returns a Client that records the operations of c as span
// events with zkquorum as the endpoint, as NewClient does for the client it
// creates. A *Client is returned as it is. It serves callers that created the
// gohbase client themselves, such as the compile-time instrumentation
// wrapping the result of gohbase.NewClient.
func WrapClient(c hbase.Client, zkquorum string) *Client {
	if w, ok := c.(*Client); ok {
		return w
	}
	return &Client{Client: c, host: zkquorum}
}

// trace starts the span event of op on the context's tracer and returns both,
// or a nil tracer when the request is not sampled.
func (c *Client) trace(op string, ctx context.Context) (pinpoint.Tracer, pinpoint.SpanEventRecorder) {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return nil, nil
	}

	se := tracer.NewSpanEvent(op).SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeHbaseClient)
	se.SetDestination("HBASE")
	se.SetEndPoint(c.host)
	return tracer, se
}

func keyString(key []byte) string {
	return "rowKey: " + string(key)
}

func scanKeyString(startKey []byte, stopKey []byte) string {
	return "startRowKey: " + string(startKey) + ", stopRowKey: " + string(stopKey)
}

// call runs f, an operation on the row key, as the span event op records.
func call[T any](c *Client, op string, ctx context.Context, key []byte, f func() (T, error)) (T, error) {
	tracer, se := c.trace(op, ctx)
	if tracer == nil {
		return f()
	}
	defer tracer.EndSpanEvent()
	se.Annotations().AppendString(pinpoint.AnnotationHbaseClientParams, keyString(key))

	r, err := f()
	se.SetError(err)
	return r, err
}

func (c *Client) Get(g *hrpc.Get) (*hrpc.Result, error) {
	return call(c, "hbase.Get", g.Context(), g.Key(), func() (*hrpc.Result, error) { return c.Client.Get(g) })
}

func (c *Client) Put(p *hrpc.Mutate) (*hrpc.Result, error) {
	return call(c, "hbase.Put", p.Context(), p.Key(), func() (*hrpc.Result, error) { return c.Client.Put(p) })
}

func (c *Client) Delete(d *hrpc.Mutate) (*hrpc.Result, error) {
	return call(c, "hbase.Delete", d.Context(), d.Key(), func() (*hrpc.Result, error) { return c.Client.Delete(d) })
}

func (c *Client) Append(a *hrpc.Mutate) (*hrpc.Result, error) {
	return call(c, "hbase.Append", a.Context(), a.Key(), func() (*hrpc.Result, error) { return c.Client.Append(a) })
}

func (c *Client) Increment(i *hrpc.Mutate) (int64, error) {
	return call(c, "hbase.Increment", i.Context(), i.Key(), func() (int64, error) { return c.Client.Increment(i) })
}

func (c *Client) CheckAndPut(p *hrpc.Mutate, family string, qualifier string, expectedValue []byte) (bool, error) {
	return call(c, "hbase.CheckAndPut", p.Context(), p.Key(), func() (bool, error) {
		return c.Client.CheckAndPut(p, family, qualifier, expectedValue)
	})
}

// Scan records the creation of the scanner, not the scan: gohbase's Scanner
// fetches rows lazily in Next, so the event is over - a few microseconds, and
// never an error - before any RPC is made. Keeping the event open across the
// caller's Next loop would end it out of nesting order with whatever the
// caller traces per row (doc/api_contracts.md 4), so the row fetches are
// deliberately left untraced; a slow or failing scan shows up in the caller's
// own event.
func (c *Client) Scan(s *hrpc.Scan) hrpc.Scanner {
	tracer, se := c.trace("hbase.Scan", s.Context())
	if tracer == nil {
		return c.Client.Scan(s)
	}

	defer tracer.EndSpanEvent()
	se.Annotations().AppendString(pinpoint.AnnotationHbaseClientParams, scanKeyString(s.StartRow(), s.StopRow()))

	return c.Client.Scan(s)
}
