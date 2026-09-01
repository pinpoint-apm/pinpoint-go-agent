// Package ppgoredis instruments the go-redis/redis package (https://github.com/go-redis/redis).
//
// This package instruments the go-redis calls.
// Use the NewClient as the redis.NewClient.
//
//	rc = ppgoredis.NewClient(redisOpts)
//
// It is necessary to pass the context containing the pinpoint.Tracer to Client using Client.WithContext.
// WithContext returns a per-request copy; use that copy for the request's calls
// and keep the original client shared:
//
//	c := rc.WithContext(pinpoint.NewContext(context.Background(), tracer))
//	c.Pipeline()
package ppgoredis

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-redis/redis"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// Client wraps redis.Client.
type Client struct {
	*redis.Client
	endpoint string
}

// NewClient returns a new Client ready to instrument.
func NewClient(opt *redis.Options) *Client {
	return &Client{Client: redis.NewClient(opt), endpoint: opt.Addr}
}

// WithContext returns a copy of the client bound to the given context.
// It is possible to trace only when the given context contains a pinpoint.Tracer.
// The receiver is not modified: the wrapped client is typically shared by
// concurrent requests, and rebinding it in place both races the field write
// and records one request's commands on another request's tracer.
func (c *Client) WithContext(ctx context.Context) *Client {
	copied := &Client{Client: c.Client.WithContext(ctx), endpoint: c.endpoint}
	copied.WrapProcess(process(ctx, c.endpoint))
	copied.WrapProcessPipeline(processPipeline(ctx, c.endpoint))
	return copied
}

// ClusterClient wraps redis.ClusterClient.
type ClusterClient struct {
	*redis.ClusterClient
	endpoint string
}

// NewClusterClient returns a new ClusterClient ready to instrument.
func NewClusterClient(opt *redis.ClusterOptions) *ClusterClient {
	endpoint := strings.Join(opt.Addrs, ",")
	return &ClusterClient{ClusterClient: redis.NewClusterClient(opt), endpoint: endpoint}
}

// WithContext returns a copy of the client bound to the given context.
// It is possible to trace only when the given context contains a pinpoint.Tracer.
// The receiver is not modified, for the same reason as Client.WithContext.
func (c *ClusterClient) WithContext(ctx context.Context) *ClusterClient {
	copied := &ClusterClient{ClusterClient: c.ClusterClient.WithContext(ctx), endpoint: c.endpoint}
	copied.WrapProcess(process(ctx, c.endpoint))
	copied.WrapProcessPipeline(processPipeline(ctx, c.endpoint))
	return copied
}

func process(ctx context.Context, endpoint string) func(oldProcess func(cmd redis.Cmder) error) func(cmd redis.Cmder) error {
	return func(oldProcess func(cmd redis.Cmder) error) func(cmd redis.Cmder) error {
		return func(cmd redis.Cmder) error {
			tracer := pinpoint.FromContext(ctx)
			if !tracer.IsSampled() {
				return oldProcess(cmd)
			}

			defer newSpanEvent(tracer, "go-redis.Process()", endpoint, cmd.Name()).EndSpanEvent()
			err := oldProcess(cmd)
			setSpanError(tracer, err)
			return err
		}
	}
}

func processPipeline(ctx context.Context, endpoint string) func(oldProcess func(cmds []redis.Cmder) error) func(cmds []redis.Cmder) error {
	return func(oldProcess func(cmds []redis.Cmder) error) func(cmds []redis.Cmder) error {
		return func(cmds []redis.Cmder) error {
			tracer := pinpoint.FromContext(ctx)
			if !tracer.IsSampled() {
				return oldProcess(cmds)
			}

			defer newSpanEvent(tracer, "go-redis.ProcessPipeline()", endpoint, cmdName(cmds)).EndSpanEvent()
			err := oldProcess(cmds)
			setSpanError(tracer, err)
			return err
		}
	}
}

func newSpanEvent(tracer pinpoint.Tracer, operation string, endpoint string, cmd string) pinpoint.Tracer {
	tracer.NewSpanEvent(operation)
	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeRedis)
	se.SetDestination("REDIS")
	se.SetEndPoint(endpoint)
	se.Annotations().AppendString(pinpoint.AnnotationArgs0, cmd)
	return tracer
}

// setSpanError records err on the span, except a cache miss: redis.Nil is a
// normal outcome, and recording it marked every miss as a failure (and walked
// the stack per miss with Error.TraceCallStack on).
func setSpanError(tracer pinpoint.Tracer, err error) {
	if err != nil && err != redis.Nil {
		tracer.SpanEvent().SetError(err)
	}
}

// maxListedCmds bounds the pipeline annotation: the pipeline size is
// caller-controlled, so listing every command would grow the span with it.
const maxListedCmds = 32

func cmdName(cmds []redis.Cmder) string {
	var b strings.Builder
	for i, cmd := range cmds {
		if i == maxListedCmds {
			b.WriteString(", ...(")
			b.WriteString(strconv.Itoa(len(cmds) - maxListedCmds))
			b.WriteString(" more)")
			break
		}
		if i != 0 {
			b.WriteString(", ")
		}
		b.WriteString(cmd.Name())
	}
	return b.String()
}
