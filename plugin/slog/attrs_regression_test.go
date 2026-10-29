package ppslog

import (
	"bytes"
	"context"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func TestHandler_WithAttrsPreservesSnapshot(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		var out bytes.Buffer
		fields := map[string]string{"state": "before"}
		var h slog.Handler = slog.NewJSONHandler(&out, nil)
		if wrapped {
			h = NewHandler(h)
		}
		log := slog.New(h).With("fields", fields).WithGroup("request")
		fields["state"] = "after"
		ctx := pinpoint.NewContext(context.Background(), pptest.NewRecordingTracer())
		log.InfoContext(ctx, "test")
		t.Logf("wrapped=%v output=%s", wrapped, out.String())
		if !strings.Contains(out.String(), `"state":"before"`) {
			t.Errorf("wrapped=%v: With attribute snapshot changed", wrapped)
		}
	}
}

type attributeCounter struct{ n int }

func (v *attributeCounter) LogValue() slog.Value { v.n++; return slog.IntValue(v.n) }
func TestHandler_WithAttrsResolvesOnce(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "wrapped"}[wrapped], func(t *testing.T) {
			var h slog.Handler = slog.NewJSONHandler(io.Discard, nil)
			if wrapped {
				h = NewHandler(h)
			}
			v := &attributeCounter{}
			log := slog.New(h).With("counter", v).WithGroup("request")
			ctx := pinpoint.NewContext(context.Background(), pptest.NewRecordingTracer())
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					for range 10 {
						log.InfoContext(ctx, "test")
					}
				})
			}
			wg.Wait()
			if v.n != 1 {
				t.Errorf("With value was evaluated %d times instead of once", v.n)
			}
		})
	}
}
