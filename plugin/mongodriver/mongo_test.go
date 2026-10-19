package ppmongo

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The command annotation is a proto3 string field, which rejects invalid
// UTF-8 and drops the whole span, so the cut has to land on a rune start and
// the result, marker included, has to stay inside the limit.
func Test_abbreviateJson(t *testing.T) {
	const limit = 64
	marker := "...(64)"

	t.Run("short input is returned as is", func(t *testing.T) {
		if got := abbreviateJson([]byte("{}"), limit); got != "{}" {
			t.Errorf("abbreviateJson() = %q, want %q", got, "{}")
		}
	})

	t.Run("ascii is cut inside the limit with the marker", func(t *testing.T) {
		got := abbreviateJson([]byte(strings.Repeat("x", 200)), limit)
		if !strings.HasSuffix(got, marker) {
			t.Fatalf("abbreviateJson() = %q, want the marker suffix", got)
		}
		if len(got) != limit {
			t.Errorf("abbreviateJson() = %d bytes, want %d", len(got), limit)
		}
	})

	t.Run("multi-byte runes are not split", func(t *testing.T) {
		// Three-byte runes never align with limit-len(marker), so a byte cut
		// would split one.
		got := abbreviateJson([]byte(strings.Repeat("가", 100)), limit)
		if !utf8.ValidString(got) {
			t.Fatalf("abbreviateJson() is not valid UTF-8: %q", got)
		}
		if !strings.HasSuffix(got, marker) {
			t.Errorf("abbreviateJson() = %q, want the marker suffix", got)
		}
		if len(got) > limit {
			t.Errorf("abbreviateJson() = %d bytes, want at most %d", len(got), limit)
		}
	})
}

// The driver reports the connection as an address with a pool index appended;
// the endpoint has to be the bare host.
func Test_getHost(t *testing.T) {
	for _, tt := range []struct{ connID, want string }{
		{"localhost:27017[-13]", "localhost"},
		{"mongo1.example:27017[-1]", "mongo1.example"},
		{"localhost:27017", "localhost"},
		{"localhost", "localhost"},
		{"", ""},
	} {
		if got := getHost(tt.connID); got != tt.want {
			t.Errorf("getHost(%q) = %q, want %q", tt.connID, got, tt.want)
		}
	}
}
