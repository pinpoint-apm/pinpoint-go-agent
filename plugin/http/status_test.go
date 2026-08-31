package pphttp

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The pre-bitmap implementation, kept verbatim as the oracle the bit table is
// compared against and as the "before" side of the benchmark. It walked a slice
// of interface values and made one dynamic call per configured entry.

type legacyStatusCode interface {
	isError(code int) bool
}

type legacyStatusInformational struct{}

func (h *legacyStatusInformational) isError(code int) bool { return 100 <= code && code <= 199 }

type legacyStatusSuccess struct{}

func (h *legacyStatusSuccess) isError(code int) bool { return 200 <= code && code <= 299 }

type legacyStatusRedirection struct{}

func (h *legacyStatusRedirection) isError(code int) bool { return 300 <= code && code <= 399 }

type legacyStatusClientError struct{}

func (h *legacyStatusClientError) isError(code int) bool { return 400 <= code && code <= 499 }

type legacyStatusServerError struct{}

func (h *legacyStatusServerError) isError(code int) bool { return 500 <= code && code <= 599 }

type legacyStatusDefault struct{ statusCode int }

func (h *legacyStatusDefault) isError(code int) bool { return h.statusCode == code }

type legacyStatusError struct {
	errors []legacyStatusCode
}

func newLegacyStatusError(cfg []string) *legacyStatusError {
	var errors []legacyStatusCode

	for _, s := range trimStringSlice(cfg) {
		if strings.EqualFold(s, "5xx") {
			errors = append(errors, &legacyStatusServerError{})
		} else if strings.EqualFold(s, "4xx") {
			errors = append(errors, &legacyStatusClientError{})
		} else if strings.EqualFold(s, "3xx") {
			errors = append(errors, &legacyStatusRedirection{})
		} else if strings.EqualFold(s, "2xx") {
			errors = append(errors, &legacyStatusSuccess{})
		} else if strings.EqualFold(s, "1xx") {
			errors = append(errors, &legacyStatusInformational{})
		} else {
			c, e := strconv.Atoi(s)
			if e != nil {
				c = -1
			}
			errors = append(errors, &legacyStatusDefault{statusCode: c})
		}
	}

	return &legacyStatusError{errors: errors}
}

func (h *legacyStatusError) isError(code int) bool {
	for _, h := range h.errors {
		if h.isError(code) {
			return true
		}
	}
	return false
}

// statusTokenSets covers every token shape the parser accepts, plus the ones it
// rejects: a status class in either case, single codes, codes on and past the
// edge of the bit table, duplicates, whitespace, and garbage.
var statusTokenSets = [][]string{
	nil,
	{},
	{"5xx"},
	{"5XX"},
	{"5Xx"},
	{"4xx"},
	{"3xx"},
	{"2xx"},
	{"1xx"},
	{"1xx", "2xx", "3xx", "4xx", "5xx"},
	{"4xx", "5xx"},
	{"4Xx", "3xX"},
	{"404"},
	{"404", "500", "503"},
	{"5xx", "302", "404"},
	{"  501  ", "\t404"},
	{"404", "404", "4xx"},
	{"0"},
	{"99"},
	{"100"},
	{"599"},
	{"600"},
	{"639"},
	{"640"},
	{"641"},
	{"999"},
	{"1000"},
	{"+404"},
	{"-1"},
	{"-404"},
	{"0404"},
	{""},
	{"abc"},
	{"6xx"},
	{"0xx"},
	{"xx"},
	{"5x"},
	{"5xxx"},
	{"x5x"},
	{"40 4"},
	{"404.0"},
	{"99999999999999999999"},
	{"5xx", "abc", "", "1000", "-7"},
}

// TestHttpStatusErrorMatchesLegacy is the equivalence check: for every token set
// the bit table must return exactly what the interface slice returned. The range
// runs past 0-599 on both sides so the table edge, negatives and codes the
// exported RecordHttpServerResponse could still be handed are covered too.
func TestHttpStatusErrorMatchesLegacy(t *testing.T) {
	for _, tokens := range statusTokenSets {
		t.Run(strings.Join(tokens, ","), func(t *testing.T) {
			legacy := newLegacyStatusError(tokens)
			bitmap := parseHttpStatusErrors(tokens)

			for code := -1100; code <= 1100; code++ {
				require.Equalf(t, legacy.isError(code), bitmap.isError(code),
					"isError(%d) disagrees with the legacy matcher (tokens %q)", code, tokens)
			}
		})
	}
}

// The equivalence test above pins the bit table to the old implementation; this
// one pins both to what the option actually means, so a shared misreading of a
// token cannot pass unnoticed.
func TestHttpStatusErrorTokens(t *testing.T) {
	tests := []struct {
		name    string
		cfg     []string
		errors  []int
		notErrs []int
	}{
		{
			name:    "a class covers exactly its hundred",
			cfg:     []string{"5xx"},
			errors:  []int{500, 503, 599},
			notErrs: []int{499, 600, 200, 404},
		},
		{
			name:    "class tokens are case-insensitive",
			cfg:     []string{"4Xx", "3xX"},
			errors:  []int{400, 404, 499, 300, 302, 399},
			notErrs: []int{299, 500},
		},
		{
			name:    "single codes stand alone",
			cfg:     []string{"302", "404"},
			errors:  []int{302, 404},
			notErrs: []int{301, 303, 400, 403, 405, 500},
		},
		{
			name:    "a class and a single code combine",
			cfg:     []string{"5xx", "302"},
			errors:  []int{500, 502, 302},
			notErrs: []int{301, 200, 404},
		},
		{
			name:    "surrounding whitespace is trimmed",
			cfg:     []string{"  501  ", "\t404"},
			errors:  []int{501, 404},
			notErrs: []int{500, 400},
		},
		{
			name:    "duplicates are idempotent",
			cfg:     []string{"404", "404", "4xx"},
			errors:  []int{404, 400, 499},
			notErrs: []int{500},
		},
		{
			// An unparsable token became -1 in the old matcher and was then
			// compared against the status; nothing a response carries is -1,
			// so it must simply never fire.
			name:    "unparsable tokens never fire",
			cfg:     []string{"abc", "6xx", "5x", "40 4", ""},
			errors:  []int{-1},
			notErrs: []int{0, 200, 404, 500, 600, 666},
		},
		{
			name:    "every class at once flags any real status",
			cfg:     []string{"1xx", "2xx", "3xx", "4xx", "5xx"},
			errors:  []int{100, 200, 302, 404, 500, 599},
			notErrs: []int{99, 600, -1},
		},
		{
			// The bit table stops at statusTableSize; codes past it fall back
			// to the out-of-table slice and must still be honoured.
			name:    "codes past the bit table still match",
			cfg:     []string{"639", "640", "641", "1000"},
			errors:  []int{639, 640, 641, 1000},
			notErrs: []int{638, 642, 999, 1001},
		},
		{
			name:    "nothing configured flags nothing",
			cfg:     nil,
			notErrs: []int{100, 200, 302, 404, 500, -1, 1000},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := parseHttpStatusErrors(tt.cfg)
			for _, code := range tt.errors {
				assert.True(t, h.isError(code), "isError(%d) with config %q", code, tt.cfg)
			}
			for _, code := range tt.notErrs {
				assert.False(t, h.isError(code), "isError(%d) with config %q", code, tt.cfg)
			}
		})
	}
}

// The default is "5xx": a server error fails the span and nothing else does.
func TestHttpStatusErrorDefaultConfig(t *testing.T) {
	startAgent(t)

	h := newHttpStatusError()
	assert.True(t, h.isError(500))
	assert.True(t, h.isError(503))
	assert.False(t, h.isError(200))
	assert.False(t, h.isError(404))
}

// The option is what an application actually sets, so it has to reach the
// matcher a request consults.
func TestHttpStatusErrorFromAgentConfig(t *testing.T) {
	startAgent(t, WithHttpServerStatusCodeError([]string{"4xx", "302"}))

	h := newHttpStatusError()
	assert.True(t, h.isError(404))
	assert.True(t, h.isError(302))
	assert.False(t, h.isError(500), "5xx is no longer configured")
	assert.False(t, h.isError(301))
}

var (
	statusBenchCodes = []int{200, 201, 204, 301, 302, 400, 404, 500, 503}
	statusBenchSink  bool

	statusBenchConfigs = []struct {
		name string
		cfg  []string
	}{
		{"1entry", []string{"5xx"}},
		{"2entries", []string{"5xx", "404"}},
		{"8entries", []string{"1xx", "2xx", "3xx", "4xx", "5xx", "404", "500", "503"}},
	}
)

func BenchmarkHttpStatusErrorLegacy(b *testing.B) {
	for _, c := range statusBenchConfigs {
		h := newLegacyStatusError(c.cfg)
		b.Run(c.name, func(b *testing.B) {
			var hit bool
			for i := 0; i < b.N; i++ {
				for _, code := range statusBenchCodes {
					hit = h.isError(code)
				}
			}
			statusBenchSink = hit
		})
	}
}

func BenchmarkHttpStatusErrorBitmap(b *testing.B) {
	for _, c := range statusBenchConfigs {
		h := parseHttpStatusErrors(c.cfg)
		b.Run(c.name, func(b *testing.B) {
			var hit bool
			for i := 0; i < b.N; i++ {
				for _, code := range statusBenchCodes {
					hit = h.isError(code)
				}
			}
			statusBenchSink = hit
		})
	}
}
