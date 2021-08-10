package memtracer

import (
	"context"
	"github.com/bradfitz/gomemcache/memcache"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"strings"
)

const (
	serviceTypeMemcacheClient      = 9900
	annotationMemcacheClientParams = 390
)

type MemcacheTracer interface {

	// Add writes the given item, if no value already exists for its
	// key. ErrNotStored is returned if that condition is not met.
	Add(ctx context.Context, item *memcache.Item) error

	// Set writes the given item, unconditionally.
	Set(ctx context.Context, item *memcache.Item) error

	// Replace writes the given item, but only if the server *does*
	// already hold data for this key
	Replace(ctx context.Context, item *memcache.Item) error

	// Get gets the item for the given key. ErrCacheMiss is returned for a
	// memcache cache miss. The key must be at most 250 bytes in length.
	Get(ctx context.Context, key string) (item *memcache.Item, err error)

	// GetMulti is a batch version of Get. The returned map from keys to
	// items may have fewer elements than the input slice, due to memcache
	// cache misses. Each key must be at most 250 bytes in length.
	// If no error is returned, the returned map will also be non-nil.
	GetMulti(ctx context.Context, keys []string) (map[string]*memcache.Item, error)

	// Delete deletes the item with the provided key. The error ErrCacheMiss is
	// returned if the item didn't already exist in the cache.
	Delete(ctx context.Context, key string) error

	// Increment atomically increments key by delta. The return value is
	// the new value after being incremented or an error. If the value
	// didn't exist in memcached the error is ErrCacheMiss. The value in
	// memcached must be an decimal number, or an error will be returned.
	// On 64-bit overflow, the new value wraps around.
	Increment(ctx context.Context, key string, delta uint64) (newValue uint64, err error)

	// Decrement atomically decrements key by delta. The return value is
	// the new value after being decremented or an error. If the value
	// didn't exist in memcached the error is ErrCacheMiss. The value in
	// memcached must be an decimal number, or an error will be returned.
	// On underflow, the new value is capped at zero and does not wrap
	// around.
	Decrement(ctx context.Context, key string, delta uint64) (newValue uint64, err error)

	// CompareAndSwap writes the given item that was previously returned
	// by Get, if the value was neither modified or evicted between the
	// Get and the CompareAndSwap calls. The item's Key should not change
	// between calls but all other item fields may differ. ErrCASConflict
	// is returned if the value was modified in between the
	// calls. ErrNotStored is returned if the value was evicted in between
	// the calls.
	CompareAndSwap(ctx context.Context, item *memcache.Item) error

	// Touch updates the expiry for the given key. The seconds parameter is either
	// a Unix timestamp or, if seconds is less than 1 month, the number of seconds
	// into the future at which time the item will expire. Zero means the item has
	// no expiration time. ErrCacheMiss is returned if the key is not in the cache.
	// The key must be at most 250 bytes in length.
	Touch(ctx context.Context, key string, seconds int32) (err error)

	// Ping checks all instances if they are alive. Returns error if any
	// of them is down.
	Ping(ctx context.Context) error

	// DeleteAll deletes all items in the cache.
	DeleteAll(ctx context.Context) error

	// FlushAll flush all items in the cache.
	FlushAll(ctx context.Context) error
}

type Client struct {
	*memcache.Client
	endpoint string
}

func NewClient(server ...string) *Client {
	client := memcache.New(server...)
	return &Client{Client: client, endpoint: strings.Join(server, ",")}
}

func (c *Client) trace(op string, ctx context.Context) pinpoint.Tracer {
	tracer := pinpoint.FromContext(ctx)
	if tracer == nil {
		return nil
	}

	tracer.NewSpanEvent(op)
	tracer.SpanEvent().SetServiceType(serviceTypeMemcacheClient)
	tracer.SpanEvent().SetDestination("MEMCACHE")
	tracer.SpanEvent().SetEndPoint(c.endpoint)

	return tracer
}

func (c *Client) Add(ctx context.Context, item *memcache.Item) error {
	tracer := c.trace("memcache.Add", ctx)
	if tracer == nil {
		return c.Client.Add(item)
	}
	defer tracer.EndSpanEvent()
	tracer.SpanEvent().Annotations().AppendString(annotationMemcacheClientParams, item.Key)
	err := c.Client.Add(item)
	tracer.SpanEvent().SetError(err)
	return err
}

func (c *Client) Set(ctx context.Context, item *memcache.Item) error {
	tracer := c.trace("memcache.Set", ctx)
	if tracer == nil {
		return c.Client.Set(item)
	}
	defer tracer.EndSpanEvent()
	tracer.SpanEvent().Annotations().AppendString(annotationMemcacheClientParams, item.Key)
	err := c.Client.Set(item)
	tracer.SpanEvent().SetError(err)
	return err
}

func (c *Client) Replace(ctx context.Context, item *memcache.Item) error {
	tracer := c.trace("memcache.Replace", ctx)
	if tracer == nil {
		return c.Client.Replace(item)
	}
	defer tracer.EndSpanEvent()
	tracer.SpanEvent().Annotations().AppendString(annotationMemcacheClientParams, item.Key)
	err := c.Client.Replace(item)
	tracer.SpanEvent().SetError(err)
	return err
}

func (c *Client) Get(ctx context.Context, key string) (item *memcache.Item, err error) {
	tracer := c.trace("memcache.Get", ctx)
	if tracer == nil {
		return c.Client.Get(key)
	}
	defer tracer.EndSpanEvent()
	tracer.SpanEvent().Annotations().AppendString(annotationMemcacheClientParams, key)
	item, err = c.Client.Get(key)
	tracer.SpanEvent().SetError(err)
	return
}

func (c *Client) GetMulti(ctx context.Context, keys []string) (map[string]*memcache.Item, error) {
	tracer := c.trace("memcache.GetMulti", ctx)
	if tracer == nil {
		return c.Client.GetMulti(keys)
	}
	defer tracer.EndSpanEvent()
	tracer.SpanEvent().Annotations().AppendString(annotationMemcacheClientParams, strings.Join(keys, ","))
	items, err := c.Client.GetMulti(keys)
	tracer.SpanEvent().SetError(err)
	return items, err
}

func (c *Client) Delete(ctx context.Context, key string) error {
	tracer := c.trace("memcache.Delete", ctx)
	if tracer == nil {
		return c.Client.Delete(key)
	}
	defer tracer.EndSpanEvent()
	tracer.SpanEvent().Annotations().AppendString(annotationMemcacheClientParams, key)
	err := c.Client.Delete(key)
	tracer.SpanEvent().SetError(err)
	return err
}

func (c *Client) Increment(ctx context.Context, key string, delta uint64) (newValue uint64, err error) {
	tracer := c.trace("memcache.Increment", ctx)
	if tracer == nil {
		return c.Client.Increment(key, delta)
	}
	defer tracer.EndSpanEvent()
	tracer.SpanEvent().Annotations().AppendString(annotationMemcacheClientParams, key)
	newValue, err = c.Client.Increment(key, delta)
	tracer.SpanEvent().SetError(err)
	return
}

func (c *Client) Decrement(ctx context.Context, key string, delta uint64) (newValue uint64, err error) {
	tracer := c.trace("memcache.Decrement", ctx)
	if tracer == nil {
		return c.Client.Decrement(key, delta)
	}
	defer tracer.EndSpanEvent()
	tracer.SpanEvent().Annotations().AppendString(annotationMemcacheClientParams, key)
	newValue, err = c.Client.Decrement(key, delta)
	tracer.SpanEvent().SetError(err)
	return
}

func (c *Client) CompareAndSwap(ctx context.Context, item *memcache.Item) error {
	tracer := c.trace("memcache.CompareAndSwap", ctx)
	if tracer == nil {
		return c.Client.CompareAndSwap(item)
	}
	defer tracer.EndSpanEvent()
	tracer.SpanEvent().Annotations().AppendString(annotationMemcacheClientParams, item.Key)
	err := c.Client.CompareAndSwap(item)
	tracer.SpanEvent().SetError(err)
	return err
}

func (c *Client) Touch(ctx context.Context, key string, seconds int32) (err error) {
	tracer := c.trace("memcache.Touch", ctx)
	if tracer == nil {
		return c.Client.Touch(key, seconds)
	}
	defer tracer.EndSpanEvent()
	tracer.SpanEvent().Annotations().AppendString(annotationMemcacheClientParams, key)
	err = c.Client.Touch(key, seconds)
	tracer.SpanEvent().SetError(err)
	return
}

func (c *Client) Ping(ctx context.Context) error {
	tracer := c.trace("memcache.Ping", ctx)
	if tracer == nil {
		return c.Client.Ping()
	}
	defer tracer.EndSpanEvent()
	err := c.Client.Ping()
	tracer.SpanEvent().SetError(err)
	return err
}

func (c *Client) DeleteAll(ctx context.Context) error {
	tracer := c.trace("memcache.DeleteAll", ctx)
	if tracer == nil {
		return c.Client.DeleteAll()
	}
	defer tracer.EndSpanEvent()
	err := c.Client.DeleteAll()
	tracer.SpanEvent().SetError(err)
	return err
}

func (c *Client) FlushAll(ctx context.Context) error {
	tracer := c.trace("memcache.FlushAll", ctx)
	if tracer == nil {
		return c.Client.FlushAll()
	}
	defer tracer.EndSpanEvent()
	err := c.Client.FlushAll()
	tracer.SpanEvent().SetError(err)
	return err
}
