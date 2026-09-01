// Package ppgoredisv8 instruments the go-redis/redis/v8 package (https://github.com/go-redis/redis).
//
// This package instruments the go-redis/v8 calls.
// Use the NewHook as the redis.Hook.
//
//	rc = redis.NewClient(redisOpts)
//	rc.AddHook(ppgoredisv8.NewHook(redisOpts))
//
// It is necessary to pass the context containing the pinpoint.Tracer to redis.Client.
//
//	rc = rc.WithContext(pinpoint.NewContext(context.Background(), tracer))
//	rc.Pipeline()
package ppgoredisv8

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-redis/redis/v8"
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

	tracer.NewSpanEvent("go-redis/v8.Process()")
	return ctx, nil
}

func (r *hook) AfterProcess(ctx context.Context, cmd redis.Cmder) error {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return nil
	}

	r.setSpanEvent(tracer, cmd.Name(), cmd.Err())
	return nil
}

func (r *hook) BeforeProcessPipeline(ctx context.Context, cmds []redis.Cmder) (context.Context, error) {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return ctx, nil
	}

	tracer.NewSpanEvent("go-redis/v8.ProcessPipeline()")
	return ctx, nil
}

func (r *hook) AfterProcessPipeline(ctx context.Context, cmds []redis.Cmder) error {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return nil
	}

	r.setSpanEvent(tracer, cmdName(cmds), pipeError(cmds))
	return nil
}

func (r *hook) setSpanEvent(tracer pinpoint.Tracer, cmd string, err error) {
	defer tracer.EndSpanEvent()
	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeRedis)
	se.SetDestination("REDIS")
	se.SetEndPoint(r.endpoint)
	// A cache miss is a normal outcome, not a failure: redis.Nil must not
	// mark the span errored (nor walk the stack with Error.TraceCallStack on).
	if err != redis.Nil {
		se.SetError(err)
	}
	se.Annotations().AppendString(pinpoint.AnnotationArgs0, cmd)
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

func pipeError(cmds []redis.Cmder) error {
	for _, cmd := range cmds {
		// redis.Nil is a miss, not a pipeline failure.
		if err := cmd.Err(); err != nil && err != redis.Nil {
			return err
		}
	}
	return nil
}
