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
	"sync"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// Client wraps memcache.Client.
type Client struct {
	*memcache.Client
	endpoint string

	// The memcache client is goroutine-safe and meant to be shared, so the
	// tracer bound by WithContext must be too: an unguarded interface field
	// written per request is a torn-readable data race.
	mu     sync.Mutex
	tracer pinpoint.Tracer
}

// NewClient wraps memcache.New and returns a memcache.Client wrapper ready to instrument.
func NewClient(server ...string) *Client {
	client := memcache.New(server...)
	return &Client{Client: client, endpoint: strings.Join(server, ","), tracer: pinpoint.NoopTracer()}
}

// WithContext returns a copy of the client bound to the tracer in the given
// context. It is possible to trace only when the given context contains a
// pinpoint.Tracer. Use the returned copy for the request's calls: the receiver
// is also updated for backward compatibility, but concurrent requests sharing
// the receiver record their commands on whichever tracer was bound last.
func (c *Client) WithContext(ctx context.Context) *Client {
	tracer := pinpoint.FromContext(ctx)
	c.mu.Lock()
	c.tracer = tracer
	c.mu.Unlock()
	return &Client{Client: c.Client, endpoint: c.endpoint, tracer: tracer}
}

func (c *Client) currentTracer() pinpoint.Tracer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tracer
}

func (c *Client) newMemcacheSpanEvent(op string, key string, start time.Time, err error) {
	tracer := c.currentTracer()
	se := tracer.NewSpanEvent("gomemcache." + op).SpanEvent()
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
	c.newMemcacheSpanEvent("Add()", item.Key, start, err)
	return err
}

func (c *Client) Set(item *memcache.Item) error {
	start := time.Now()
	err := c.Client.Set(item)
	c.newMemcacheSpanEvent("Set()", item.Key, start, err)
	return err
}

func (c *Client) Replace(item *memcache.Item) error {
	start := time.Now()
	err := c.Client.Replace(item)
	c.newMemcacheSpanEvent("Replace()", item.Key, start, err)
	return err
}

func (c *Client) Get(key string) (item *memcache.Item, err error) {
	start := time.Now()
	item, err = c.Client.Get(key)
	c.newMemcacheSpanEvent("Get()", key, start, err)
	return
}

func (c *Client) GetMulti(keys []string) (map[string]*memcache.Item, error) {
	start := time.Now()
	items, err := c.Client.GetMulti(keys)
	c.newMemcacheSpanEvent("GetMulti()", strings.Join(keys, ","), start, err)
	return items, err
}

func (c *Client) Delete(key string) error {
	start := time.Now()
	err := c.Client.Delete(key)
	c.newMemcacheSpanEvent("Delete()", key, start, err)
	return err
}

func (c *Client) Increment(key string, delta uint64) (uint64, error) {
	start := time.Now()
	newValue, err := c.Client.Increment(key, delta)
	c.newMemcacheSpanEvent("Increment()", key, start, err)
	return newValue, err
}

func (c *Client) Decrement(key string, delta uint64) (uint64, error) {
	start := time.Now()
	newValue, err := c.Client.Decrement(key, delta)
	c.newMemcacheSpanEvent("Decrement()", key, start, err)
	return newValue, err
}

func (c *Client) CompareAndSwap(item *memcache.Item) error {
	start := time.Now()
	err := c.Client.CompareAndSwap(item)
	c.newMemcacheSpanEvent("CompareAndSwap()", item.Key, start, err)
	return err
}

func (c *Client) Touch(key string, seconds int32) (err error) {
	start := time.Now()
	err = c.Client.Touch(key, seconds)
	c.newMemcacheSpanEvent("Touch()", key, start, err)
	return
}

func (c *Client) Ping() error {
	start := time.Now()
	err := c.Client.Ping()
	c.newMemcacheSpanEvent("Ping()", "", start, err)
	return err
}

func (c *Client) DeleteAll() error {
	start := time.Now()
	err := c.Client.DeleteAll()
	c.newMemcacheSpanEvent("DeleteAll()", "", start, err)
	return err
}

func (c *Client) FlushAll() error {
	start := time.Now()
	err := c.Client.FlushAll()
	c.newMemcacheSpanEvent("FlushAll()", "", start, err)
	return err
}
