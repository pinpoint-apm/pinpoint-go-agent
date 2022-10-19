// Package ppgoredisv7 instruments the go-redis/redis/v7 package (https://github.com/go-redis/redis).
//
// This package instruments the go-redis/v7 calls.
// Use the NewHook as the redis.Hook.
//
//	rc = redis.NewClient(redisOpts)
//	rc.AddHook(ppgoredisv7.NewHook(redisOpts))
//
// It is necessary to pass the context containing the pinpoint.Tracer to redis.Client.
//
//	rc = rc.WithContext(pinpoint.NewContext(context.Background(), tracer))
//	rc.Pipeline()
package ppgoredisv7

import (
	"bytes"
	"context"
	"strings"

	"github.com/go-redis/redis/v7"
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

func (r *hook) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return ctx, nil
	}

	tracer.NewSpanEvent(makeMethodName("Process", []redis.Cmder{cmd}))
	return ctx, nil
}

func (r *hook) AfterProcess(ctx context.Context, cmd redis.Cmder) error {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return nil
	}

	defer tracer.EndSpanEvent()
	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeRedis)
	se.SetDestination("REDIS")
	se.SetEndPoint(r.endpoint)
	se.SetError(cmd.Err())

	return nil
}

func (r *hook) BeforeProcessPipeline(ctx context.Context, cmds []redis.Cmder) (context.Context, error) {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return ctx, nil
	}

	tracer.NewSpanEvent(makeMethodName("ProcessPipeline", cmds))
	return ctx, nil
}

func (r *hook) AfterProcessPipeline(ctx context.Context, cmds []redis.Cmder) error {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return nil
	}

	defer tracer.EndSpanEvent()
	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeRedis)
	se.SetDestination("REDIS")
	se.SetEndPoint(r.endpoint)
	se.SetError(pipeError(cmds))
	return nil
}

func makeMethodName(operation string, cmds []redis.Cmder) string {
	var buf bytes.Buffer

	buf.WriteString("go-redis/v7.")
	buf.WriteString(operation)
	buf.WriteString("(")
	for i, cmd := range cmds {
		if i != 0 {
			buf.WriteString(", ")
		}
		buf.WriteString("'")
		buf.WriteString(strings.ToLower(cmd.Name()))
		buf.WriteString("'")
	}
	buf.WriteString(")")

	return buf.String()
}

func pipeError(cmds []redis.Cmder) error {
	for _, cmd := range cmds {
		err := cmd.Err()
		if err != nil {
			return err
		}
	}
	return nil
}
