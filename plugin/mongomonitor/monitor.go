// Package ppmongomonitor holds what the mongodriver and mongodriverv2 plugins
// share: the Mongo.* options, the span map that pairs a command's started
// event with its finished one, and the host and JSON helpers. Each plugin
// supplies the half that reads its driver's event types. An application
// installs a plugin's NewMonitor, not this package.
package ppmongomonitor

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

// DefaultCommandMaxSize is the default of Mongo.CommandMaxSize: the size the
// command annotation is cut to, and the BSON size above which the command is
// not converted at all. MarshalExtJSON buffers the whole result, so a command
// whose source BSON already reaches the limit is skipped instead of
// converted.
const DefaultCommandMaxSize = 64 * 1024

// Mongo.RecordCommand records the command document as the span event's
// AnnotationMongoJasonData, converted to extended JSON; Mongo.CommandMaxSize
// bounds it. The conversion runs on the request goroutine for every sampled
// command, and a 64 KB document costs hundreds of microseconds, so an
// application that does not need the document in the trace turns it off, and
// one that only needs its head lowers the size: a command above the size is
// not converted at all.
const (
	CfgMongoRecordCommand  = "Mongo.RecordCommand"
	CfgMongoCommandMaxSize = "Mongo.CommandMaxSize"
)

func init() {
	pinpoint.AddConfig(CfgMongoRecordCommand, pinpoint.CfgBool, true, true)
	pinpoint.AddConfig(CfgMongoCommandMaxSize, pinpoint.CfgInt, DefaultCommandMaxSize, true)
}

// WithMongoRecordCommand sets whether the command document is recorded on the
// span event (Mongo.RecordCommand, default true).
func WithMongoRecordCommand(record bool) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(CfgMongoRecordCommand, record)
	}
}

// WithMongoCommandMaxSize sets the size in bytes the recorded command document
// is cut to; a command larger than this is not converted at all
// (Mongo.CommandMaxSize, default 65536). A value of 0 or less keeps the default.
func WithMongoCommandMaxSize(size int) pinpoint.ConfigOption {
	return func(c *pinpoint.Config) {
		c.Set(CfgMongoCommandMaxSize, size)
	}
}

// CommandSettings reads the two options; a size of 0 or less is the default.
func CommandSettings(cfg *pinpoint.Config) (record bool, maxSize int) {
	maxSize = cfg.Int(CfgMongoCommandMaxSize)
	if maxSize <= 0 {
		maxSize = DefaultCommandMaxSize
	}
	return cfg.Bool(CfgMongoRecordCommand), maxSize
}

// MaxPendingSpans bounds the span map. The driver pairs every started event
// with a finished one, but a pairing that never arrives would otherwise hold
// its tracer forever; past the cap the oldest-unknown entry is evicted.
const MaxPendingSpans = 4096

// ErrSpanEvicted ends the span event of a command evicted at MaxPendingSpans.
var ErrSpanEvicted = errors.New("mongo monitor: too many commands in flight, span evicted before its finished event")

// Key identifies a command in flight: the driver reports its started and
// finished events with the same connection and request id.
type Key struct {
	ConnectionID string
	RequestID    int64
}

// Monitor pairs the span event a command's started event opened with the
// finished event that ends it. One Monitor serves every connection of a
// client, so it is safe for concurrent use.
type Monitor struct {
	mu    sync.Mutex
	spans map[Key]pinpoint.Tracer

	// pending mirrors len(spans) so Finished can skip the lock when nothing
	// is tracked - every unsampled command finishes with a guaranteed miss.
	pending atomic.Int64
}

// NewMonitor returns an empty Monitor.
func NewMonitor() *Monitor {
	return &Monitor{spans: make(map[Key]pinpoint.Tracer)}
}

// Started keeps tracer, whose span event the command opened, until Finished
// is called with the same key. Past MaxPendingSpans one pending command is
// evicted first, its event ended with ErrSpanEvicted.
func (m *Monitor) Started(key Key, tracer pinpoint.Tracer) {
	var evicted pinpoint.Tracer
	m.mu.Lock()
	// ponytail: arbitrary eviction at the cap; per-entry deadlines if evicted
	// spans ever show up in real traces.
	if len(m.spans) >= MaxPendingSpans {
		for k, t := range m.spans {
			delete(m.spans, k)
			evicted = t
			break
		}
	}
	m.spans[key] = tracer
	m.pending.Store(int64(len(m.spans)))
	m.mu.Unlock()

	if evicted != nil {
		evicted.SpanEvent().SetError(ErrSpanEvicted)
		evicted.EndSpanEvent()
	}
}

// Finished ends the span event of the command key, recording err on it. A key
// Started never saw - an unsampled command, a driver's own heartbeat - is a
// no-op.
func (m *Monitor) Finished(key Key, err error) {
	// Only sampled commands were inserted in Started; when nothing is in
	// flight - the whole workload, under a zero-hit sampling rate - skip the
	// lock instead of serializing every ack on it for a guaranteed miss.
	if m.pending.Load() == 0 {
		return
	}

	m.mu.Lock()
	tracer, ok := m.spans[key]
	if !ok {
		m.mu.Unlock()
		return
	}

	defer tracer.EndSpanEvent()
	delete(m.spans, key)
	m.pending.Store(int64(len(m.spans)))
	m.mu.Unlock()
	tracer.SpanEvent().SetError(err)
}

// Pending is the number of commands in flight.
func (m *Monitor) Pending() int {
	return int(m.pending.Load())
}

// Host is the bare host of a driver connection id, which the driver reports
// as an address with a pool index appended ("localhost:27017[-13]").
func Host(connID string) string {
	hostname, _, _ := strings.Cut(connID, "[-")
	if host, _, err := net.SplitHostPort(hostname); err == nil {
		return host
	}
	return strings.Trim(hostname, "[]")
}

// AbbreviateJSON cuts b to length bytes including a "...(length)" marker, on
// a rune start: a proto3 string field rejects invalid UTF-8 and drops the span.
func AbbreviateJSON(b []byte, length int) string {
	if len(b) <= length {
		return string(b)
	}

	// Reserve the marker so the annotation stays inside length.
	marker := "...(" + strconv.Itoa(length) + ")"
	cut := length - len(marker)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(b[cut]) {
		cut--
	}
	return string(b[:cut]) + marker
}
