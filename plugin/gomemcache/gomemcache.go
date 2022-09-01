package gomemcache

//Contributed by ONG-YA (https://github.com/ONG-YA)

import (
	"context"
	"github.com/bradfitz/gomemcache/memcache"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"strings"
	"time"
)

const (
	serviceTypeMemcached = 8050
)

type Client struct {
	*memcache.Client
	endpoint string
	tracer   pinpoint.Tracer
}

func NewClient(server ...string) *Client {
	client := memcache.New(server...)
	return &Client{Client: client, endpoint: strings.Join(server, ","), tracer: pinpoint.NoopTracer()}
}

func (c *Client) WithContext(ctx context.Context) {
	c.tracer = pinpoint.FromContext(ctx)
}

func (c *Client) newMemcacheSpanEvent(op string, key string, start time.Time, err error) {
	se := c.tracer.NewSpanEvent("gomemcache." + op).SpanEvent()
	se.SetServiceType(serviceTypeMemcached)
	se.SetDestination("MEMCACHED")
	se.SetEndPoint(c.endpoint)
	se.Annotations().AppendString(pinpoint.AnnotationArgs0, key)
	se.SetError(err)
	se.FixDuration(start, time.Now())
	c.tracer.EndSpanEvent()
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
