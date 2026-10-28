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
// The context DialContext and DialURLContext take bounds the dial alone, as in
// redigo: it is not bound to the connection, which a pool hands on to later
// requests, so a command on it is traced through one of the two above.
package ppredigo

import (
	"context"
	"errors"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

// The same errors redigo's own helpers return when a connection lacks the
// optional interface; the wrapper advertises both unconditionally, so it must
// answer for a base connection that has neither instead of panicking.
var (
	errTimeoutNotSupported = errors.New("redis: connection does not support ConnWithTimeout")
	errContextNotSupported = errors.New("redis: connection does not support ConnWithContext")
)

type wrappedConn struct {
	base   redis.Conn
	dbInfo pinpoint.DBInfo

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
		base:   c,
		dbInfo: pinpoint.DBInfo{QueryType: pinpoint.ServiceTypeRedis, DBName: "REDIS", DBHost: addr},
		ctx:    context.Background(),
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
//
// It binds a connection this package dialed or wrapped. A connection taken
// from a redis.Pool is redigo's own type in front of the wrapped one, which
// this cannot reach: a pooled connection is traced through redis.DoContext
// and redis.ReceiveContext, which the pool forwards to the wrapped
// connection with the context.
func WithContext(c redis.Conn, ctx context.Context) {
	if wc, ok := c.(pinpointContext); ok {
		wc.WithContext(ctx)
	} else if c != nil {
		pinpoint.Log("redigo").Debugf("WithContext: %T is not a connection this package wrapped (a pooled connection is traced through redis.DoContext)", c)
	}
}

// WrapConn returns a redis.Conn that records the operations of c as span
// events, with the endpoint derived from the dial address as Dial does. A
// connection this package already wrapped is returned as it is. It serves
// callers that dialed themselves, such as the compile-time instrumentation
// wrapping the result of redis.DialContext (every Dial variant funnels into
// it).
func WrapConn(c redis.Conn, address string) redis.Conn {
	if _, ok := c.(*wrappedConn); ok {
		return c
	}
	return makeWrappedConn(c, address)
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
// ctx bounds the dial alone; commands are traced through WithContext or redis.DoContext.
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
// ctx bounds the dial alone; commands are traced through WithContext or redis.DoContext.
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
	rec := c.startSpanEvent(c.currentContext(), "redigo.Send()", cmd)
	defer func() { rec.end(err) }()

	err = c.base.Send(cmd, args...)
	return
}

func (c *wrappedConn) Flush() error {
	return c.base.Flush()
}

func (c *wrappedConn) Receive() (r interface{}, err error) {
	rec := c.startSpanEvent(c.currentContext(), "redigo.Receive()", "")
	defer func() { rec.end(err) }()

	r, err = c.base.Receive()
	return
}

func (c *wrappedConn) Do(cmd string, args ...interface{}) (r interface{}, err error) {
	rec := c.startSpanEvent(c.currentContext(), "redigo.Do()", cmd)
	defer func() { rec.end(err) }()

	r, err = c.base.Do(cmd, args...)
	return
}

func (c *wrappedConn) DoWithTimeout(readTimeout time.Duration, cmd string, args ...interface{}) (r interface{}, err error) {
	cwt, ok := c.base.(redis.ConnWithTimeout)
	if !ok {
		return nil, errTimeoutNotSupported
	}

	rec := c.startSpanEvent(c.currentContext(), "redigo.DoWithTimeout()", cmd)
	defer func() { rec.end(err) }()

	r, err = cwt.DoWithTimeout(readTimeout, cmd, args...)
	return
}

func (c *wrappedConn) ReceiveWithTimeout(timeout time.Duration) (r interface{}, err error) {
	cwt, ok := c.base.(redis.ConnWithTimeout)
	if !ok {
		return nil, errTimeoutNotSupported
	}

	rec := c.startSpanEvent(c.currentContext(), "redigo.ReceiveWithTimeout()", "")
	defer func() { rec.end(err) }()

	r, err = cwt.ReceiveWithTimeout(timeout)
	return
}

func (c *wrappedConn) DoContext(ctx context.Context, cmd string, args ...interface{}) (r interface{}, err error) {
	cwc, ok := c.base.(redis.ConnWithContext)
	if !ok {
		return nil, errContextNotSupported
	}

	rec := c.startSpanEvent(ctx, "redigo.DoContext()", cmd)
	defer func() { rec.end(err) }()

	r, err = cwc.DoContext(ctx, cmd, args...)
	return
}

func (c *wrappedConn) ReceiveContext(ctx context.Context) (r interface{}, err error) {
	cwc, ok := c.base.(redis.ConnWithContext)
	if !ok {
		return nil, errContextNotSupported
	}

	rec := c.startSpanEvent(ctx, "redigo.ReceiveContext()", "")
	defer func() { rec.end(err) }()

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
func (c *wrappedConn) startSpanEvent(ctx context.Context, operation string, cmd string) recording {
	// An unsampled tracer records nothing: skip the lock. The zero recording
	// ends nothing.
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return recording{}
	}

	if !c.opMu.TryLock() {
		return recording{}
	}

	se := pinpoint.NewDatabaseTracer(ctx, operation, &c.dbInfo).SpanEvent()
	if cmd != "" {
		se.Annotations().AppendString(pinpoint.AnnotationArgs0, cmd)
	}
	return recording{se: se, tracer: tracer, c: c}
}

// recording is one operation being recorded: what end needs to complete it.
// A value rather than a closure, which escaped to the heap once per sampled
// operation; the callers' deferred end is open-coded and allocates nothing.
type recording struct {
	se     pinpoint.SpanEventRecorder
	tracer pinpoint.Tracer
	c      *wrappedConn
}

// end completes the recording with the operation's error; the zero recording
// does nothing. The error goes on the event opened in startSpanEvent, not on
// whatever is on top of the stack by the time the call ends - another
// goroutine's under a fan-out on one request.
func (r recording) end(err error) {
	if r.c == nil {
		return
	}
	r.se.SetError(err)
	r.tracer.EndSpanEvent()
	r.c.opMu.Unlock()
}
