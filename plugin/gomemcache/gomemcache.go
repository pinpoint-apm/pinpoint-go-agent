// Package ppgomemcache instruments the bradfitz/gomemcache package (https://github.com/bradfitz/gomemcache).
//
// This package instruments the gomemcache calls.
// Use the NewClient as the memcache.New.
//
//	mc := ppgomemcache.NewClient(addr...)
//
// It is necessary to pass the context containing the pinpoint.Tracer to Client using Client.WithContext.
// WithContext returns a per-request copy; use that copy for the request's calls
// and keep the original client shared:
//
//	c := mc.WithContext(pinpoint.NewContext(context.Background(), tracer))
//	c.Get("foo")
package ppgomemcache

//Contributed by ONG-YA (https://github.com/ONG-YA)

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

// Client wraps memcache.Client.
type Client struct {
	*memcache.Client
	endpoint string

	// WithContext creates a new wrapper, so this field is never rebound.
	tracer pinpoint.Tracer
}

// NewClient wraps memcache.New and returns a memcache.Client wrapper ready to instrument.
func NewClient(server ...string) *Client {
	return WrapClient(memcache.New(server...), strings.Join(server, ","))
}

// WrapClient wraps a *memcache.Client created elsewhere the way NewClient
// wraps the one it creates; endpoint is what the span events report as the
// endpoint (NewClient joins the server addresses with ","). The compile-time
// instrumentation tool uses it from its memcache.New hook.
func WrapClient(client *memcache.Client, endpoint string) *Client {
	return &Client{Client: client, endpoint: endpoint}
}

// WithContext returns a copy of the client bound to the tracer in the given
// context. It is possible to trace only when the given context contains a
// pinpoint.Tracer. Use the returned copy for the request's calls; the receiver
// is left as it was. Rebinding it as well made concurrent requests sharing one
// client record their commands on whichever tracer was bound last, from
// another request's goroutine (doc/api_contracts.md 1).
func (c *Client) WithContext(ctx context.Context) *Client {
	return &Client{Client: c.Client, endpoint: c.endpoint, tracer: pinpoint.FromContext(ctx)}
}

// currentTracer returns the bound tracer, or the noop tracer for an unbound client.
func (c *Client) currentTracer() pinpoint.Tracer {
	if c.tracer != nil {
		return c.tracer
	}
	return pinpoint.NoopTracer()
}

func (c *Client) newMemcacheSpanEvent(op string, key string, start time.Time, err error) {
	tracer := c.currentTracer()
	if !tracer.IsSampled() {
		return
	}
	c.recordMemcacheSpanEvent(tracer, op, key, start, err)
}

func (c *Client) recordMemcacheSpanEvent(tracer pinpoint.Tracer, op string, key string, start time.Time, err error) {
	se := tracer.NewSpanEvent(op).SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeMemcached)
	se.SetDestination("MEMCACHED")
	se.SetEndPoint(c.endpoint)
	se.Annotations().AppendString(pinpoint.AnnotationArgs0, key)
	// A cache miss is a normal outcome, not a failure: ErrCacheMiss must not
	// mark the span errored (nor walk the stack with Error.TraceCallStack on).
	if !errors.Is(err, memcache.ErrCacheMiss) {
		se.SetError(err)
	}
	se.FixDuration(start, time.Now())
	tracer.EndSpanEvent()
}

// do runs call, one memcache operation that returns only an error, and
// records it as the op span event.
func (c *Client) do(op string, key string, call func() error) error {
	start := time.Now()
	err := call()
	c.newMemcacheSpanEvent(op, key, start, err)
	return err
}

func (c *Client) Add(item *memcache.Item) error {
	return c.do("gomemcache.Add()", item.Key, func() error { return c.Client.Add(item) })
}

func (c *Client) Set(item *memcache.Item) error {
	return c.do("gomemcache.Set()", item.Key, func() error { return c.Client.Set(item) })
}

func (c *Client) Replace(item *memcache.Item) error {
	return c.do("gomemcache.Replace()", item.Key, func() error { return c.Client.Replace(item) })
}

func (c *Client) Get(key string) (item *memcache.Item, err error) {
	start := time.Now()
	item, err = c.Client.Get(key)
	c.newMemcacheSpanEvent("gomemcache.Get()", key, start, err)
	return
}

func (c *Client) GetMulti(keys []string) (map[string]*memcache.Item, error) {
	start := time.Now()
	items, err := c.Client.GetMulti(keys)
	tracer := c.currentTracer()
	if tracer.IsSampled() {
		// Bounded: GetMulti has no key-count limit, so listing every key
		// would grow the span with the caller's batch.
		keyList := pinpoint.AnnotationList(len(keys), func(i int) string { return keys[i] })
		c.recordMemcacheSpanEvent(tracer, "gomemcache.GetMulti()", keyList, start, err)
	}
	return items, err
}

func (c *Client) Delete(key string) error {
	return c.do("gomemcache.Delete()", key, func() error { return c.Client.Delete(key) })
}

func (c *Client) Increment(key string, delta uint64) (uint64, error) {
	start := time.Now()
	newValue, err := c.Client.Increment(key, delta)
	c.newMemcacheSpanEvent("gomemcache.Increment()", key, start, err)
	return newValue, err
}

func (c *Client) Decrement(key string, delta uint64) (uint64, error) {
	start := time.Now()
	newValue, err := c.Client.Decrement(key, delta)
	c.newMemcacheSpanEvent("gomemcache.Decrement()", key, start, err)
	return newValue, err
}

func (c *Client) CompareAndSwap(item *memcache.Item) error {
	return c.do("gomemcache.CompareAndSwap()", item.Key, func() error { return c.Client.CompareAndSwap(item) })
}

func (c *Client) Touch(key string, seconds int32) error {
	return c.do("gomemcache.Touch()", key, func() error { return c.Client.Touch(key, seconds) })
}

func (c *Client) Ping() error {
	return c.do("gomemcache.Ping()", "", func() error { return c.Client.Ping() })
}

func (c *Client) DeleteAll() error {
	return c.do("gomemcache.DeleteAll()", "", func() error { return c.Client.DeleteAll() })
}

func (c *Client) FlushAll() error {
	return c.do("gomemcache.FlushAll()", "", func() error { return c.Client.FlushAll() })
}
