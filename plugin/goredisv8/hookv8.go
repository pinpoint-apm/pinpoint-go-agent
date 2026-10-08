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
	"errors"
	"strings"

	"github.com/go-redis/redis/v8"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
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

// recorderKey carries the span event recorder from BeforeProcess to
// AfterProcess in the context go-redis threads through the hooks. The error
// is recorded on that recorder: reading it back after the call would land on
// whichever event is on top by then, another goroutine's under a fan-out on
// one request.
type recorderKey struct{}

func (r *hook) BeforeProcess(ctx context.Context, cmd redis.Cmder) (context.Context, error) {
	return r.before(ctx, "go-redis/v8.Process()", cmd.Name()), nil
}

func (r *hook) AfterProcess(ctx context.Context, cmd redis.Cmder) error {
	after(ctx, cmd.Err())
	return nil
}

func (r *hook) BeforeProcessPipeline(ctx context.Context, cmds []redis.Cmder) (context.Context, error) {
	return r.before(ctx, "go-redis/v8.ProcessPipeline()", cmdName(cmds)), nil
}

func (r *hook) AfterProcessPipeline(ctx context.Context, cmds []redis.Cmder) error {
	after(ctx, pipeError(cmds))
	return nil
}

// before opens the span event of a sampled command and hands its recorder to
// after through the returned context; an unsampled one gets ctx back as is.
func (r *hook) before(ctx context.Context, operation string, cmd string) context.Context {
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return ctx
	}

	tracer.NewSpanEvent(operation)
	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeRedis)
	se.SetDestination("REDIS")
	se.SetEndPoint(r.endpoint)
	se.Annotations().AppendString(pinpoint.AnnotationArgs0, cmd)
	return context.WithValue(ctx, recorderKey{}, se)
}

// after records err on the event before opened and ends it.
func after(ctx context.Context, err error) {
	se, ok := ctx.Value(recorderKey{}).(pinpoint.SpanEventRecorder)
	if !ok {
		return
	}
	// A cache miss is a normal outcome, not a failure: redis.Nil must not
	// mark the span errored (nor walk the stack with Error.TraceCallStack on).
	if !errors.Is(err, redis.Nil) {
		se.SetError(err)
	}
	pinpoint.FromContext(ctx).EndSpanEvent()
}

// cmdName lists the pipeline's commands, bounded: the pipeline size is
// caller-controlled, so listing every command would grow the span with it.
func cmdName(cmds []redis.Cmder) string {
	return pinpoint.AnnotationList(len(cmds), func(i int) string { return cmds[i].Name() })
}

func pipeError(cmds []redis.Cmder) error {
	for _, cmd := range cmds {
		// redis.Nil is a miss, not a pipeline failure.
		if err := cmd.Err(); err != nil && !errors.Is(err, redis.Nil) {
			return err
		}
	}
	return nil
}
