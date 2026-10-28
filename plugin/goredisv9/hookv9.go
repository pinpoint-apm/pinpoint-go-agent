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
	"context"
	"errors"
	"strings"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/redis/go-redis/v9"
)

type hook struct {
	dbInfo pinpoint.DBInfo
}

// redisInfo describes the Redis endpoint the span events report.
func redisInfo(endpoint string) pinpoint.DBInfo {
	return pinpoint.DBInfo{QueryType: pinpoint.ServiceTypeRedis, DBName: "REDIS", DBHost: endpoint}
}

// NewHook creates a redis.Hook to instrument redis calls.
func NewHook(opts *redis.Options) redis.Hook {
	endpoint := "unknown"
	if opts != nil {
		endpoint = opts.Addr
	}
	return &hook{dbInfo: redisInfo(endpoint)}
}

// NewClusterHook creates a redis.Hook to instrument redis cluster calls.
func NewClusterHook(opts *redis.ClusterOptions) redis.Hook {
	endpoint := "unknown"
	if opts != nil {
		endpoint = strings.Join(opts.Addrs, ",")
	}
	return &hook{dbInfo: redisInfo(endpoint)}
}

// DialHook records nothing, so hand the next hook back untouched instead of
// adding a frame to every dial.
func (r *hook) DialHook(hook redis.DialHook) redis.DialHook {
	return hook
}

func (r *hook) ProcessHook(hook redis.ProcessHook) redis.ProcessHook {
	opName := "go-redis/v9.Process()"

	return func(ctx context.Context, cmd redis.Cmder) error {
		tracer := pinpoint.FromContext(ctx)
		if !tracer.IsSampled() {
			return hook(ctx, cmd)
		}
		ctx, fork := onOwnGoroutine(ctx, tracer)
		if fork != nil {
			defer fork.EndSpan()
			tracer = fork
		}

		defer tracer.EndSpanEvent()
		se := r.newSpanEvent(ctx, opName, cmd.Name())
		err := hook(ctx, cmd)
		setSpanError(se, err)
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
		ctx, fork := onOwnGoroutine(ctx, tracer)
		if fork != nil {
			defer fork.EndSpan()
			tracer = fork
		}

		defer tracer.EndSpanEvent()
		se := r.newSpanEvent(ctx, opName, cmdName(cmds))
		err := hook(ctx, cmds)
		setSpanError(se, err)
		return err
	}
}

// onOwnGoroutine hands the hook a context whose tracer it may record on here,
// and the goroutine tracer it forked for that, to end, if it had to: go-redis's
// async AutoPipeliner runs a command it sends apart - a blocking one, one with
// a read timeout - through the hook chain on a goroutine of its own, with the
// caller's context, while the caller carries on, and recorded on the caller's
// tracer from there the event interleaved with the caller's own.
func onOwnGoroutine(ctx context.Context, tracer pinpoint.Tracer) (context.Context, pinpoint.Tracer) {
	if pinpoint.OnTracerGoroutine(tracer) {
		return ctx, nil
	}
	fork := tracer.NewGoroutineTracer()
	return pinpoint.NewContext(ctx, fork), fork
}

// newSpanEvent opens the event and returns its recorder, which the error is
// recorded on: reading the recorder back after the call lands on whichever
// event is on top by then, another goroutine's under a fan-out on one
// request.
func (r *hook) newSpanEvent(ctx context.Context, operation string, cmd string) pinpoint.SpanEventRecorder {
	se := pinpoint.NewDatabaseTracer(ctx, operation, &r.dbInfo).SpanEvent()
	se.Annotations().AppendString(pinpoint.AnnotationArgs0, cmd)
	return se
}

// setSpanError records err on the span event, except a cache miss: redis.Nil
// is a normal outcome, and recording it marked every miss as a failure (and
// walked the stack per miss with Error.TraceCallStack on).
func setSpanError(se pinpoint.SpanEventRecorder, err error) {
	if err != nil && !errors.Is(err, redis.Nil) {
		se.SetError(err)
	}
}

// cmdName lists the pipeline's commands, bounded: the pipeline size is
// caller-controlled, so listing every command would grow the span with it.
func cmdName(cmds []redis.Cmder) string {
	return pinpoint.AnnotationList(len(cmds), func(i int) string { return cmds[i].Name() })
}
