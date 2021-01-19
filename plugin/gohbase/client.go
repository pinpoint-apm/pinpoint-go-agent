package gohbase

import (
	"context"

	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	hbase "github.com/tsuna/gohbase"
	"github.com/tsuna/gohbase/hrpc"
)

const (
	serviceTypeHbaseClient      = 8800
	annotationHbaseClientParams = 320
)

type Client struct {
	hbase.Client
	host string
}

func NewClient(zkquorum string, options ...hbase.Option) *Client {
	client := hbase.NewClient(zkquorum, options...)
	return &Client{Client: client, host: zkquorum}
}

func (c *Client) trace(op string, ctx context.Context) pinpoint.Tracer {
	tracer := pinpoint.FromContext(ctx)
	if tracer == nil {
		return nil
	}

	tracer.NewSpanEvent(op)
	tracer.SpanEvent().SetServiceType(serviceTypeHbaseClient)
	tracer.SpanEvent().SetDestination("HBASE")
	tracer.SpanEvent().SetEndPoint(c.host)

	return tracer
}

func keyString(key []byte) string {
	return "rowKey: " + string(key)
}

func scanKeyString(startKey []byte, stopKey []byte) string {
	return "startRowKey: " + string(startKey) + ", stopRowKey: " + string(stopKey)
}

func (c *Client) Get(g *hrpc.Get) (*hrpc.Result, error) {
	tracer := c.trace("hbase.Get", g.Context())
	if tracer == nil {
		return c.Client.Get(g)
	}

	defer tracer.EndSpanEvent()
	tracer.SpanEvent().Annotations().AppendString(annotationHbaseClientParams, keyString(g.Key()))

	r, e := c.Client.Get(g)
	if e != nil {
		tracer.SpanEvent().SetError(e)
	}
	return r, e
}

func (c *Client) Put(p *hrpc.Mutate) (*hrpc.Result, error) {
	tracer := c.trace("hbase.Put", p.Context())
	if tracer == nil {
		return c.Client.Put(p)
	}

	defer tracer.EndSpanEvent()
	tracer.SpanEvent().Annotations().AppendString(annotationHbaseClientParams, keyString(p.Key()))

	r, e := c.Client.Put(p)
	tracer.SpanEvent().SetError(e)
	return r, e
}

func (c *Client) Delete(d *hrpc.Mutate) (*hrpc.Result, error) {
	tracer := c.trace("hbase.Delete", d.Context())
	if tracer == nil {
		return c.Client.Delete(d)
	}

	defer tracer.EndSpanEvent()
	tracer.SpanEvent().Annotations().AppendString(annotationHbaseClientParams, keyString(d.Key()))

	r, e := c.Client.Delete(d)
	tracer.SpanEvent().SetError(e)
	return r, e
}

func (c *Client) Append(a *hrpc.Mutate) (*hrpc.Result, error) {
	tracer := c.trace("hbase.Append", a.Context())
	if tracer == nil {
		return c.Client.Append(a)
	}

	defer tracer.EndSpanEvent()
	tracer.SpanEvent().Annotations().AppendString(annotationHbaseClientParams, keyString(a.Key()))

	r, e := c.Client.Append(a)
	tracer.SpanEvent().SetError(e)
	return r, e
}

func (c *Client) Increment(i *hrpc.Mutate) (int64, error) {
	tracer := c.trace("hbase.Increment", i.Context())
	if tracer == nil {
		return c.Client.Increment(i)
	}

	defer tracer.EndSpanEvent()
	tracer.SpanEvent().Annotations().AppendString(annotationHbaseClientParams, keyString(i.Key()))

	r, e := c.Client.Increment(i)
	tracer.SpanEvent().SetError(e)
	return r, e
}

func (c *Client) CheckAndPut(p *hrpc.Mutate, family string, qualifier string, expectedValue []byte) (bool, error) {
	tracer := c.trace("hbase.CheckAndPut", p.Context())
	if tracer == nil {
		return c.Client.CheckAndPut(p, family, qualifier, expectedValue)
	}

	defer tracer.EndSpanEvent()
	tracer.SpanEvent().Annotations().AppendString(annotationHbaseClientParams, keyString(p.Key()))

	r, e := c.Client.CheckAndPut(p, family, qualifier, expectedValue)
	tracer.SpanEvent().SetError(e)
	return r, e
}

func (c *Client) Scan(s *hrpc.Scan) hrpc.Scanner {
	tracer := c.trace("hbase.Scan", s.Context())
	if tracer == nil {
		return c.Client.Scan(s)
	}

	defer tracer.EndSpanEvent()
	tracer.SpanEvent().Annotations().AppendString(annotationHbaseClientParams, scanKeyString(s.StartRow(), s.StopRow()))

	return c.Client.Scan(s)
}
