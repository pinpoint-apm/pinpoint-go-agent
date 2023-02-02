// Package ppgoredisv9 instruments the redis/go-redis/v9 package (https://github.com/redis/go-redis).
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

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/redis/go-redis/v9"
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
	opName := "go-redis/v9.Process()"

	return func(ctx context.Context, cmd redis.Cmder) error {
		tracer := pinpoint.FromContext(ctx)
		if !tracer.IsSampled() {
			return hook(ctx, cmd)
		}

		defer r.newSpanEvent(tracer, opName, cmd.Name()).EndSpanEvent()
		err := hook(ctx, cmd)
		tracer.SpanEvent().SetError(err)
		return err
	}
}

func (r *hook) ProcessPipelineHook(hook redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	opName := "go-redis/v9.ProcessPipeline()"

	return func(ctx context.Context, cmds []redis.Cmder) error {
		tracer := pinpoint.FromContext(ctx)
		if !tracer.IsSampled() {
			return hook(ctx, cmds)
		}

		defer r.newSpanEvent(tracer, opName, cmdName(cmds)).EndSpanEvent()
		err := hook(ctx, cmds)
		tracer.SpanEvent().SetError(err)
		return err
	}
}

func (r *hook) newSpanEvent(tracer pinpoint.Tracer, operation string, cmd string) pinpoint.Tracer {
	tracer.NewSpanEvent(operation)
	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeRedis)
	se.SetDestination("REDIS")
	se.SetEndPoint(r.endpoint)
	se.Annotations().AppendString(pinpoint.AnnotationArgs0, cmd)
	return tracer
}

func cmdName(cmds []redis.Cmder) string {
	var buf bytes.Buffer

	for i, cmd := range cmds {
		if i != 0 {
			buf.WriteString(", ")
		}
		buf.WriteString(cmd.Name())
	}
	return buf.String()
}
