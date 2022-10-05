package ppredigo

import (
	"bytes"
	"context"
	"github.com/gomodule/redigo/redis"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"net"
	"net/url"
	"strings"
	"time"
)

type wrappedConn struct {
	base     redis.Conn
	endpoint string
	tracer   pinpoint.Tracer
}

type pinpointContext interface {
	WithContext(ctx context.Context)
}

func wrapConn(c redis.Conn, addr string) redis.Conn {
	return &wrappedConn{
		base:     c,
		endpoint: addr,
		tracer:   nil,
	}
}

func (c *wrappedConn) WithContext(ctx context.Context) {
	c.tracer = pinpoint.FromContext(ctx)
}

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

func Dial(network string, address string, options ...redis.DialOption) (redis.Conn, error) {
	c, err := redis.Dial(network, address, options...)
	if err != nil {
		return nil, err
	}

	return makeWrappedConn(c, address)
}

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

func DialURL(rawurl string, options ...redis.DialOption) (redis.Conn, error) {
	c, err := redis.DialURL(rawurl, options...)
	if err != nil {
		return nil, err
	}

	return makeWrappedConnURL(c, rawurl)
}

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
	if c.tracer == nil {
		return c.base.Send(cmd, args...)
	}

	se := c.makeRedisSpanEvent("Send", cmd)
	defer c.tracer.EndSpanEvent()

	err := c.base.Send(cmd, args...)
	if err != nil {
		se.SetError(err)
	}
	return err
}

func (c *wrappedConn) Flush() error {
	return c.base.Flush()
}

func (c *wrappedConn) Receive() (reply interface{}, err error) {
	if c.tracer == nil {
		return c.base.Receive()
	}

	se := c.makeRedisSpanEvent("Receive", "")
	defer c.tracer.EndSpanEvent()

	r, err := c.base.Receive()
	if err != nil {
		se.SetError(err)
	}
	return r, err
}

func (c *wrappedConn) makeRedisSpanEvent(operation string, cmd string) pinpoint.SpanEventRecorder {
	c.tracer.NewSpanEvent(makeMethodName(operation, cmd))
	se := c.tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeRedis)
	se.SetDestination("REDIS")
	se.SetEndPoint(c.endpoint)

	return se
}

func makeMethodName(operation string, cmd string) string {
	var buf bytes.Buffer

	buf.WriteString("redigo.")
	buf.WriteString(operation)
	buf.WriteString("('")
	buf.WriteString(strings.ToLower(cmd))
	buf.WriteString("')")

	return buf.String()
}

func (c *wrappedConn) Do(cmd string, args ...interface{}) (interface{}, error) {
	if c.tracer == nil {
		return c.base.Do(cmd, args...)
	}

	se := c.makeRedisSpanEvent("Do", cmd)
	defer c.tracer.EndSpanEvent()

	r, err := c.base.Do(cmd, args...)
	if err != nil {
		se.SetError(err)
	}
	return r, err
}

func (c *wrappedConn) DoWithTimeout(readTimeout time.Duration, cmd string, args ...interface{}) (interface{}, error) {
	cwt, _ := c.base.(redis.ConnWithTimeout)

	if c.tracer == nil {
		return cwt.DoWithTimeout(readTimeout, cmd, args...)
	}

	se := c.makeRedisSpanEvent("DoWithTimeout", cmd)
	defer c.tracer.EndSpanEvent()

	r, err := cwt.DoWithTimeout(readTimeout, cmd, args...)
	if err != nil {
		se.SetError(err)
	}
	return r, err
}

func (c *wrappedConn) ReceiveWithTimeout(timeout time.Duration) (reply interface{}, err error) {
	cwt, _ := c.base.(redis.ConnWithTimeout)

	if c.tracer == nil {
		return cwt.ReceiveWithTimeout(timeout)
	}

	se := c.makeRedisSpanEvent("ReceiveWithTimeout", "")
	defer c.tracer.EndSpanEvent()

	r, err := cwt.ReceiveWithTimeout(timeout)
	if err != nil {
		se.SetError(err)
	}
	return r, err
}

func (c *wrappedConn) DoContext(ctx context.Context, cmd string, args ...interface{}) (interface{}, error) {
	cwc, _ := c.base.(redis.ConnWithContext)

	if tracer := pinpoint.FromContext(ctx); tracer != nil {
		c.tracer = tracer
	}
	if c.tracer == nil {
		return cwc.DoContext(ctx, cmd, args...)
	}

	se := c.makeRedisSpanEvent("DoContext", cmd)
	defer c.tracer.EndSpanEvent()

	r, err := cwc.DoContext(ctx, cmd, args...)
	if err != nil {
		se.SetError(err)
	}
	return r, err
}

func (c *wrappedConn) ReceiveContext(ctx context.Context) (interface{}, error) {
	cwc, _ := c.base.(redis.ConnWithContext)

	if tracer := pinpoint.FromContext(ctx); tracer != nil {
		c.tracer = tracer
	}
	if c.tracer == nil {
		return cwc.ReceiveContext(ctx)
	}

	se := c.makeRedisSpanEvent("ReceiveContext", "")
	defer c.tracer.EndSpanEvent()

	r, err := cwc.ReceiveContext(ctx)
	if err != nil {
		se.SetError(err)
	}
	return r, err
}
