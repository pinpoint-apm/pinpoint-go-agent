package goredisv8

import (
	"bytes"
	"context"
	"strings"

	"github.com/go-redis/redis/v8"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
)

const serviceTypeRedis = 8200

type hook struct {
	endpoint string
}

func NewHook(opts *redis.Options) redis.Hook {
	h := hook{}

	if opts != nil {
		h.endpoint = opts.Addr
	} else {
		h.endpoint = "unknown"
	}

	return &h
}

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
	if tracer == nil {
		return ctx, nil
	}

	tracer.NewSpanEvent(makeMethodName("Cmd", []redis.Cmder{cmd}))
	return ctx, nil
}

func (r *hook) AfterProcess(ctx context.Context, cmd redis.Cmder) error {
	tracer := pinpoint.FromContext(ctx)
	if tracer == nil {
		return nil
	}

	span := tracer.SpanEvent()
	defer tracer.EndSpanEvent()

	span.SetServiceType(serviceTypeRedis)
	span.SetDestination("REDIS")
	span.SetEndPoint(r.endpoint)

	err := cmd.Err()
	if err != nil {
		span.SetError(err)
	}

	return nil
}

func (r *hook) BeforeProcessPipeline(ctx context.Context, cmds []redis.Cmder) (context.Context, error) {
	tracer := pinpoint.FromContext(ctx)
	if tracer == nil {
		return ctx, nil
	}

	tracer.NewSpanEvent(makeMethodName("Pipeline", cmds))
	return ctx, nil
}

func (r *hook) AfterProcessPipeline(ctx context.Context, cmds []redis.Cmder) error {
	tracer := pinpoint.FromContext(ctx)
	if tracer == nil {
		return nil
	}

	span := tracer.SpanEvent()
	defer tracer.EndSpanEvent()

	span.SetServiceType(serviceTypeRedis)
	span.SetDestination("REDIS")
	span.SetEndPoint(r.endpoint)

	for _, cmd := range cmds {
		err := cmd.Err()
		if err != nil {
			span.SetError(err)
			break
		}
	}

	return nil
}

func makeMethodName(operation string, cmds []redis.Cmder) string {
	var buf bytes.Buffer

	buf.WriteString("goredisv8.")
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
