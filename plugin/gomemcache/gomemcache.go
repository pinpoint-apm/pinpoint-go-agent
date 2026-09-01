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
	"strings"
	"sync/atomic"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// tracerBox exists because tracer implementations differ by concrete type,
// which an atomic.Value would reject; a typed pointer box takes any of them.
type tracerBox struct{ t pinpoint.Tracer }

// Client wraps memcache.Client.
type Client struct {
	*memcache.Client
	endpoint string

	// The memcache client is goroutine-safe and meant to be shared, so the
	// tracer bound by WithContext must be too: an unguarded interface field
	// written per request is a torn-readable data race. An atomic pointer
	// rather than a mutex - the lock serialized every operation of every
	// goroutine sharing the client just to read one field.
	tracer atomic.Pointer[tracerBox]
}

// NewClient wraps memcache.New and returns a memcache.Client wrapper ready to instrument.
func NewClient(server ...string) *Client {
	c := &Client{Client: memcache.New(server...), endpoint: strings.Join(server, ",")}
	c.tracer.Store(&tracerBox{pinpoint.NoopTracer()})
	return c
}

// WithContext returns a copy of the client bound to the tracer in the given
// context. It is possible to trace only when the given context contains a
// pinpoint.Tracer. Use the returned copy for the request's calls: the receiver
// is also updated for backward compatibility, but concurrent requests sharing
// the receiver record their commands on whichever tracer was bound last.
func (c *Client) WithContext(ctx context.Context) *Client {
	box := &tracerBox{pinpoint.FromContext(ctx)}
	c.tracer.Store(box)

	copied := &Client{Client: c.Client, endpoint: c.endpoint}
	copied.tracer.Store(box)
	return copied
}

func (c *Client) currentTracer() pinpoint.Tracer {
	return c.tracer.Load().t
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
	se.SetError(err)
	se.FixDuration(start, time.Now())
	tracer.EndSpanEvent()
}

func (c *Client) Add(item *memcache.Item) error {
	start := time.Now()
	err := c.Client.Add(item)
	c.newMemcacheSpanEvent("gomemcache.Add()", item.Key, start, err)
	return err
}

func (c *Client) Set(item *memcache.Item) error {
	start := time.Now()
	err := c.Client.Set(item)
	c.newMemcacheSpanEvent("gomemcache.Set()", item.Key, start, err)
	return err
}

func (c *Client) Replace(item *memcache.Item) error {
	start := time.Now()
	err := c.Client.Replace(item)
	c.newMemcacheSpanEvent("gomemcache.Replace()", item.Key, start, err)
	return err
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
		c.recordMemcacheSpanEvent(tracer, "gomemcache.GetMulti()", strings.Join(keys, ","), start, err)
	}
	return items, err
}

func (c *Client) Delete(key string) error {
	start := time.Now()
	err := c.Client.Delete(key)
	c.newMemcacheSpanEvent("gomemcache.Delete()", key, start, err)
	return err
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
	start := time.Now()
	err := c.Client.CompareAndSwap(item)
	c.newMemcacheSpanEvent("gomemcache.CompareAndSwap()", item.Key, start, err)
	return err
}

func (c *Client) Touch(key string, seconds int32) (err error) {
	start := time.Now()
	err = c.Client.Touch(key, seconds)
	c.newMemcacheSpanEvent("gomemcache.Touch()", key, start, err)
	return
}

func (c *Client) Ping() error {
	start := time.Now()
	err := c.Client.Ping()
	c.newMemcacheSpanEvent("gomemcache.Ping()", "", start, err)
	return err
}

func (c *Client) DeleteAll() error {
	start := time.Now()
	err := c.Client.DeleteAll()
	c.newMemcacheSpanEvent("gomemcache.DeleteAll()", "", start, err)
	return err
}

func (c *Client) FlushAll() error {
	start := time.Now()
	err := c.Client.FlushAll()
	c.newMemcacheSpanEvent("gomemcache.FlushAll()", "", start, err)
	return err
}
