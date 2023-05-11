package pprueidis

import (
	"bytes"
	"context"
	"strings"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/redis/rueidis"
	_ "github.com/redis/rueidis/rueidishook"
)

type Hook struct {
	endpoint string
}

// NewHook creates a ruedis.Hook to instrument redis calls.
func NewHook(opts rueidis.ClientOption) *Hook {
	h := Hook{}

	if opts.InitAddress == nil {
		h.endpoint = "unknown"
	} else {
		h.endpoint = strings.Join(opts.InitAddress, ",")
	}

	return &h
}

func (h *Hook) Do(client rueidis.Client, ctx context.Context, cmd rueidis.Completed) (resp rueidis.RedisResult) {
	tracer := h.newSpanEvent(ctx, "rueidis.Do()", strings.Join(cmd.Commands(), ","))
	defer tracer.EndSpanEvent()

	resp = client.Do(ctx, cmd)

	if resp.Error() != nil {
		tracer.SpanEvent().SetError(resp.Error())
	}
	return
}

func (h *Hook) DoMulti(client rueidis.Client, ctx context.Context, multi ...rueidis.Completed) (resps []rueidis.RedisResult) {
	tracer := h.newSpanEvent(ctx, "rueidis.DoMulti()", cmdCompletedName(multi))
	defer tracer.EndSpanEvent()

	resps = client.DoMulti(ctx, multi...)

	err := multiResultError(resps)
	if err != nil {
		tracer.SpanEvent().SetError(err)
	}
	return
}

func (h *Hook) DoCache(client rueidis.Client, ctx context.Context, cmd rueidis.Cacheable, ttl time.Duration) (resp rueidis.RedisResult) {
	tracer := h.newSpanEvent(ctx, "rueidis.DoCache()", strings.Join(cmd.Commands(), ","))
	defer tracer.EndSpanEvent()

	resp = client.DoCache(ctx, cmd, ttl)

	if resp.Error() != nil {
		tracer.SpanEvent().SetError(resp.Error())

	}
	return
}

func (h *Hook) DoMultiCache(client rueidis.Client, ctx context.Context, multi ...rueidis.CacheableTTL) (resps []rueidis.RedisResult) {
	tracer := h.newSpanEvent(ctx, "rueidis.DoMultiCache()", cmdCacheableName(multi))
	defer tracer.EndSpanEvent()

	resps = client.DoMultiCache(ctx, multi...)

	err := multiResultError(resps)
	if err != nil {
		tracer.SpanEvent().SetError(err)
	}
	return
}

func (h *Hook) Receive(client rueidis.Client, ctx context.Context, subscribe rueidis.Completed, fn func(msg rueidis.PubSubMessage)) (err error) {
	tracer := h.newSpanEvent(ctx, "rueidis.Receive()", strings.Join(subscribe.Commands(), ","))
	defer tracer.EndSpanEvent()

	err = client.Receive(ctx, subscribe, fn)

	if err != nil {
		tracer.SpanEvent().SetError(err)

	}
	return err
}

func (h *Hook) newSpanEvent(ctx context.Context, operation string, cmd string) pinpoint.Tracer {
	tracer := pinpoint.FromContext(ctx)

	se := tracer.NewSpanEvent(operation).SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeRedis)
	se.SetDestination("REDIS")
	se.SetEndPoint(h.endpoint)
	if cmd != "" {
		se.Annotations().AppendString(pinpoint.AnnotationArgs0, cmd)
	}

	return tracer
}

func cmdCompletedName(cmds []rueidis.Completed) string {
	var buf bytes.Buffer

	for i, cmd := range cmds {
		if i != 0 {
			buf.WriteString(", ")
		}
		buf.WriteString(strings.Join(cmd.Commands(), ","))
	}
	return buf.String()
}

func cmdCacheableName(cmds []rueidis.CacheableTTL) string {
	var buf bytes.Buffer

	for i, cmd := range cmds {
		if i != 0 {
			buf.WriteString(", ")
		}
		buf.WriteString(strings.Join(cmd.Cmd.Commands(), ","))
	}
	return buf.String()
}

func multiResultError(cmds []rueidis.RedisResult) error {
	for _, cmd := range cmds {
		err := cmd.Error()
		if err != nil {
			return err
		}
	}
	return nil
}
