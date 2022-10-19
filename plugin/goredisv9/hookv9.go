// Package ppgoredisv9 instruments the go-redis/redis/v9 package (https://github.com/go-redis/redis).
//
// This package instruments the go-redis/v9 calls.
// Use the NewHook as the redis.Hook.
//
//	rc = redis.NewClient(redisOpts)
//	rc.AddHook(ppgoredisv9.NewHook(redisOpts))
//
// It is necessary to pass the context containing the pinpoint.Tracer to redis.Client.
//
//	rc = rc.WithContext(pinpoint.NewContext(context.Background(), tracer))
//	rc.Pipeline()
package ppgoredisv9

import (
	"bytes"
	"context"
	"net"
	"strings"

	"github.com/go-redis/redis/v9"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

type hook struct {
	endpoint string
}

// NewHook creates a redis.Hook to instrument redis calls.
func NewHook(opts *redis.Options) redis.Hook {
	h := hook{}

	if opts != nil {
		h.endpoint = opts.Addr
	} else {
		h.endpoint = "unknown"
	}

	return &h
}

// NewClusterHook creates a redis.Hook to instrument redis cluster calls.
func NewClusterHook(opts *redis.ClusterOptions) redis.Hook {
	h := hook{}

	if opts != nil {
		h.endpoint = strings.Join(opts.Addrs, ",")
	} else {
		h.endpoint = "unknown"
	}

	return &h
}

func (r *hook) DialHook(hook redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return hook(ctx, network, addr)
	}
}

func (r *hook) ProcessHook(hook redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		tracer := pinpoint.FromContext(ctx)
		if !tracer.IsSampled() {
			return hook(ctx, cmd)
		}

		defer tracer.NewSpanEvent(makeMethodName("Process", []redis.Cmder{cmd})).EndSpanEvent()
		se := tracer.SpanEvent()
		se.SetServiceType(pinpoint.ServiceTypeRedis)
		se.SetDestination("REDIS")
		se.SetEndPoint(r.endpoint)
		//se.Annotations().AppendString(pinpoint.AnnotationArgs0, makeMethodName("Process", []redis.Cmder{cmd}))

		err := hook(ctx, cmd)
		se.SetError(err)
		return err
	}
}

func (r *hook) ProcessPipelineHook(hook redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		tracer := pinpoint.FromContext(ctx)
		if !tracer.IsSampled() {
			return hook(ctx, cmds)
		}

		defer tracer.NewSpanEvent(makeMethodName("ProcessPipeline", cmds)).EndSpanEvent()
		se := tracer.SpanEvent()
		se.SetServiceType(pinpoint.ServiceTypeRedis)
		se.SetDestination("REDIS")
		se.SetEndPoint(r.endpoint)

		err := hook(ctx, cmds)
		se.SetError(err)
		return err
	}
}

func makeMethodName(operation string, cmds []redis.Cmder) string {
	var buf bytes.Buffer

	buf.WriteString("go-redis/v9.")
	buf.WriteString(operation)
	buf.WriteString("(")
	for i, cmd := range cmds {
		if i != 0 {
			buf.WriteString(", ")
		}
		buf.WriteString("'")
		buf.WriteString(cmd.Name())
		buf.WriteString("'")
	}
	buf.WriteString(")")

	return buf.String()
}
