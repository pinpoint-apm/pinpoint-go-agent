package pphttp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Pins the bit table to what the option actually means.
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
			// Atoi accepts a sign and leading zeros, so these are plain 404s.
			name:    "Atoi-shaped tokens parse as their value",
			cfg:     []string{"+404", "0404"},
			errors:  []int{404},
			notErrs: []int{0, 4, 400, 403, 405},
		},
		{
			name:    "duplicates are idempotent",
			cfg:     []string{"404", "404", "4xx"},
			errors:  []int{404, 400, 499},
			notErrs: []int{500},
		},
		{
			// An unparsable token must never fire: parsing it to -1 and
			// comparing that against the status would match nothing a real
			// response carries, but it must not match anything at all.
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

	h := newHttpConfig().srvStatus
	assert.True(t, h.isError(500))
	assert.True(t, h.isError(503))
	assert.False(t, h.isError(200))
	assert.False(t, h.isError(404))
}

// The option is what an application actually sets, so it has to reach the
// matcher a request consults.
func TestHttpStatusErrorFromAgentConfig(t *testing.T) {
	startAgent(t, WithHttpServerStatusCodeError([]string{"4xx", "302"}))

	h := newHttpConfig().srvStatus
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

func BenchmarkHttpStatusErrorMap(b *testing.B) {
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
