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
	"errors"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// The same errors redigo's own helpers return when a connection lacks the
// optional interface; the wrapper advertises both unconditionally, so it must
// answer for a base connection that has neither instead of panicking.
var (
	errTimeoutNotSupported = errors.New("redis: connection does not support ConnWithTimeout")
	errContextNotSupported = errors.New("redis: connection does not support ConnWithContext")
)

type wrappedConn struct {
	base     redis.Conn
	endpoint string

	// ctxMu guards the context bound by WithContext; opMu serializes span
	// event recording (see startSpanEvent). Two locks, because an operation
	// can hold opMu across a blocking Receive and must not block WithContext.
	ctxMu sync.Mutex
	ctx   context.Context
	opMu  sync.Mutex
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
	c.ctxMu.Lock()
	c.ctx = ctx
	c.ctxMu.Unlock()
}

func (c *wrappedConn) currentContext() context.Context {
	c.ctxMu.Lock()
	defer c.ctxMu.Unlock()
	return c.ctx
}

// WithContext passes the context to the provided redis.Conn.
// It is possible to trace only when the given context contains a pinpoint.Tracer.
func WithContext(c redis.Conn, ctx context.Context) {
	if wc, ok := c.(pinpointContext); ok {
		wc.WithContext(ctx)
	}
}

// makeWrappedConn derives the endpoint from the dial address. The split
// failing is not a dial failure: a unix-socket address has no host:port shape,
// but redis.Dial has already connected. Returning an error here dropped the
// live connection unclosed - one leaked fd per dial, steady under a
// redis.Pool's retries - and made the plugin unusable over unix sockets.
func makeWrappedConn(c redis.Conn, address string) redis.Conn {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	return wrapConn(c, host)
}

// Dial wraps redis.Dial and returns a new redis.Conn ready to instrument.
func Dial(network string, address string, options ...redis.DialOption) (redis.Conn, error) {
	c, err := redis.Dial(network, address, options...)
	if err != nil {
		return nil, err
	}

	return makeWrappedConn(c, address), nil
}

// DialContext wraps redis.DialContext and returns a new redis.Conn ready to instrument.
// It is possible to trace only when the given context contains a pinpoint.Tracer.
func DialContext(ctx context.Context, network string, address string, options ...redis.DialOption) (redis.Conn, error) {
	c, err := redis.DialContext(ctx, network, address, options...)
	if err != nil {
		return nil, err
	}

	return makeWrappedConn(c, address), nil
}

func makeWrappedConnURL(c redis.Conn, rawurl string) (redis.Conn, error) {
	var host string

	u, err := url.Parse(rawurl)
	if err == nil {
		// A redis URL may omit the port - redigo defaults it to 6379 - so the
		// split failing is not a dial failure. Keeping SplitHostPort's error
		// here reported an already-connected DialURL as failed, and the caller
		// dropped a live connection.
		if h, _, splitErr := net.SplitHostPort(u.Host); splitErr == nil {
			host = h
		} else {
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

func (c *wrappedConn) Send(cmd string, args ...interface{}) (err error) {
	end := c.startSpanEvent(c.currentContext(), "redigo.Send()", cmd)
	defer func() { end(err) }()

	err = c.base.Send(cmd, args...)
	return
}

func (c *wrappedConn) Flush() error {
	return c.base.Flush()
}

func (c *wrappedConn) Receive() (r interface{}, err error) {
	end := c.startSpanEvent(c.currentContext(), "redigo.Receive()", "")
	defer func() { end(err) }()

	r, err = c.base.Receive()
	return
}

func (c *wrappedConn) Do(cmd string, args ...interface{}) (r interface{}, err error) {
	end := c.startSpanEvent(c.currentContext(), "redigo.Do()", cmd)
	defer func() { end(err) }()

	r, err = c.base.Do(cmd, args...)
	return
}

func (c *wrappedConn) DoWithTimeout(readTimeout time.Duration, cmd string, args ...interface{}) (r interface{}, err error) {
	cwt, ok := c.base.(redis.ConnWithTimeout)
	if !ok {
		return nil, errTimeoutNotSupported
	}

	end := c.startSpanEvent(c.currentContext(), "redigo.DoWithTimeout()", cmd)
	defer func() { end(err) }()

	r, err = cwt.DoWithTimeout(readTimeout, cmd, args...)
	return
}

func (c *wrappedConn) ReceiveWithTimeout(timeout time.Duration) (r interface{}, err error) {
	cwt, ok := c.base.(redis.ConnWithTimeout)
	if !ok {
		return nil, errTimeoutNotSupported
	}

	end := c.startSpanEvent(c.currentContext(), "redigo.ReceiveWithTimeout()", "")
	defer func() { end(err) }()

	r, err = cwt.ReceiveWithTimeout(timeout)
	return
}

func (c *wrappedConn) DoContext(ctx context.Context, cmd string, args ...interface{}) (r interface{}, err error) {
	cwc, ok := c.base.(redis.ConnWithContext)
	if !ok {
		return nil, errContextNotSupported
	}

	end := c.startSpanEvent(ctx, "redigo.DoContext()", cmd)
	defer func() { end(err) }()

	r, err = cwc.DoContext(ctx, cmd, args...)
	return
}

func (c *wrappedConn) ReceiveContext(ctx context.Context) (r interface{}, err error) {
	cwc, ok := c.base.(redis.ConnWithContext)
	if !ok {
		return nil, errContextNotSupported
	}

	end := c.startSpanEvent(ctx, "redigo.ReceiveContext()", "")
	defer func() { end(err) }()

	r, err = cwc.ReceiveContext(ctx)
	return
}

// startSpanEvent records the operation on the tracer in ctx and returns the
// function that completes the recording. redigo supports one goroutine in
// Send/Flush concurrent with another blocked in Receive (the pub/sub pattern),
// but a pinpoint.Tracer is not goroutine-safe: interleaved NewSpanEvent/
// EndSpanEvent pairs from two goroutines corrupt its event stack. When another
// operation on this connection is already recording, this one proceeds
// untraced instead.
func (c *wrappedConn) startSpanEvent(ctx context.Context, operation string, cmd string) func(error) {
	if !c.opMu.TryLock() {
		return func(error) {}
	}

	tracer := pinpoint.FromContext(ctx)
	tracer.NewSpanEvent(operation)

	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeRedis)
	se.SetDestination("REDIS")
	se.SetEndPoint(c.endpoint)
	if cmd != "" {
		se.Annotations().AppendString(pinpoint.AnnotationArgs0, cmd)
	}

	return func(err error) {
		tracer.SpanEvent().SetError(err)
		tracer.EndSpanEvent()
		c.opMu.Unlock()
	}
}
