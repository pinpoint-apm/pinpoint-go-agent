// Package ppredigo instruments the gomodule/redigo package (https://github.com/gomodule/redigo).
//
// This package instruments the gomodule/redigo calls.
// Use the Dial, DialContext (or DialURL, DialURLContext) as the redis.Dial.
//
//	c, err := ppredigo.Dial("tcp", "127.0.0.1:6379")
//
// It is necessary to pass the context containing the pinpoint.Tracer to redis.Conn.
//
//	ppredigo.WithContext(c, pinpoint.NewContext(context.Background(), tracer))
//	c.Do("SET", "vehicle", "truck")
//
// or
//
//	redis.DoContext(c, pinpoint.NewContext(context.Background(), tracer), "GET", "vehicle")
//
// or
//
//	c, err := ppredigo.DialContext(pinpoint.NewContext(context.Background(), tracer), "tcp", "127.0.0.1:6379")
//	c.Do("SET", "vehicle", "truck")
package ppredigo

import (
	"context"
	"net"
	"net/url"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

type wrappedConn struct {
	base     redis.Conn
	endpoint string
	ctx      context.Context
}

type pinpointContext interface {
	WithContext(ctx context.Context)
}

func wrapConn(c redis.Conn, addr string) redis.Conn {
	return &wrappedConn{
		base:     c,
		endpoint: addr,
		ctx:      context.Background(),
	}
}

func (c *wrappedConn) WithContext(ctx context.Context) {
	c.ctx = ctx
}

// WithContext passes the context to the provided redis.Conn.
// It is possible to trace only when the given context contains a pinpoint.Tracer.
func WithContext(c redis.Conn, ctx context.Context) {
	if wc, ok := c.(pinpointContext); ok {
		wc.WithContext(ctx)
	}
}

func makeWrappedConn(c redis.Conn, address string) (redis.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}

	return wrapConn(c, host), nil
}

// Dial wraps redis.Dial and returns a new redis.Conn ready to instrument.
func Dial(network string, address string, options ...redis.DialOption) (redis.Conn, error) {
	c, err := redis.Dial(network, address, options...)
	if err != nil {
		return nil, err
	}

	return makeWrappedConn(c, address)
}

// DialContext wraps redis.DialContext and returns a new redis.Conn ready to instrument.
// It is possible to trace only when the given context contains a pinpoint.Tracer.
func DialContext(ctx context.Context, network string, address string, options ...redis.DialOption) (redis.Conn, error) {
	c, err := redis.DialContext(ctx, network, address, options...)
	if err != nil {
		return nil, err
	}

	return makeWrappedConn(c, address)
}

func makeWrappedConnURL(c redis.Conn, rawurl string) (redis.Conn, error) {
	var host string

	u, err := url.Parse(rawurl)
	if err == nil {
		host, _, err = net.SplitHostPort(u.Host)
		if err != nil {
			host = u.Host
		}
		if host == "" {
			host = "localhost"
		}
	} else {
		host = "unknown"
	}

	return wrapConn(c, host), err
}

// DialURL wraps redis.DialURL and returns a new redis.Conn ready to instrument.
func DialURL(rawurl string, options ...redis.DialOption) (redis.Conn, error) {
	c, err := redis.DialURL(rawurl, options...)
	if err != nil {
		return nil, err
	}

	return makeWrappedConnURL(c, rawurl)
}

// DialURLContext wraps redis.DialURLContext and returns a new redis.Conn ready to instrument.
// It is possible to trace only when the given context contains a pinpoint.Tracer.
func DialURLContext(ctx context.Context, rawurl string, options ...redis.DialOption) (redis.Conn, error) {
	c, err := redis.DialURLContext(ctx, rawurl, options...)
	if err != nil {
		return nil, err
	}

	return makeWrappedConnURL(c, rawurl)
}

func (c *wrappedConn) Close() error {
	return c.base.Close()
}

func (c *wrappedConn) Err() error {
	return c.base.Err()
}

func (c *wrappedConn) Send(cmd string, args ...interface{}) error {
	tracer := c.newSpanEvent(c.ctx, "redigo.Send()", cmd)
	defer tracer.EndSpanEvent()

	err := c.base.Send(cmd, args...)
	tracer.SpanEvent().SetError(err)

	return err
}

func (c *wrappedConn) Flush() error {
	return c.base.Flush()
}

func (c *wrappedConn) Receive() (interface{}, error) {
	tracer := c.newSpanEvent(c.ctx, "redigo.Receive()", "")
	defer tracer.EndSpanEvent()

	r, err := c.base.Receive()
	tracer.SpanEvent().SetError(err)

	return r, err
}

func (c *wrappedConn) Do(cmd string, args ...interface{}) (interface{}, error) {
	tracer := c.newSpanEvent(c.ctx, "redigo.Do()", cmd)
	defer tracer.EndSpanEvent()

	r, err := c.base.Do(cmd, args...)
	tracer.SpanEvent().SetError(err)

	return r, err
}

func (c *wrappedConn) DoWithTimeout(readTimeout time.Duration, cmd string, args ...interface{}) (interface{}, error) {
	tracer := c.newSpanEvent(c.ctx, "redigo.DoWithTimeout()", cmd)
	defer tracer.EndSpanEvent()

	cwt, _ := c.base.(redis.ConnWithTimeout)
	r, err := cwt.DoWithTimeout(readTimeout, cmd, args...)
	tracer.SpanEvent().SetError(err)

	return r, err
}

func (c *wrappedConn) ReceiveWithTimeout(timeout time.Duration) (interface{}, error) {
	tracer := c.newSpanEvent(c.ctx, "redigo.ReceiveWithTimeout()", "")
	defer tracer.EndSpanEvent()

	cwt, _ := c.base.(redis.ConnWithTimeout)
	r, err := cwt.ReceiveWithTimeout(timeout)
	tracer.SpanEvent().SetError(err)

	return r, err
}

func (c *wrappedConn) DoContext(ctx context.Context, cmd string, args ...interface{}) (interface{}, error) {
	tracer := c.newSpanEvent(ctx, "redigo.DoContext()", cmd)
	defer tracer.EndSpanEvent()

	cwc, _ := c.base.(redis.ConnWithContext)
	r, err := cwc.DoContext(ctx, cmd, args...)
	tracer.SpanEvent().SetError(err)

	return r, err
}

func (c *wrappedConn) ReceiveContext(ctx context.Context) (interface{}, error) {
	tracer := c.newSpanEvent(ctx, "redigo.ReceiveContext()", "")
	defer tracer.EndSpanEvent()

	cwc, _ := c.base.(redis.ConnWithContext)
	r, err := cwc.ReceiveContext(ctx)
	tracer.SpanEvent().SetError(err)

	return r, err
}

func (c *wrappedConn) newSpanEvent(ctx context.Context, operation string, cmd string) pinpoint.Tracer {
	tracer := pinpoint.FromContext(ctx)
	tracer.NewSpanEvent(operation)

	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeRedis)
	se.SetDestination("REDIS")
	se.SetEndPoint(c.endpoint)
	if cmd != "" {
		se.Annotations().AppendString(pinpoint.AnnotationArgs0, cmd)
	}
	return tracer
}
