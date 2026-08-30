package pppgxv5

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type argStringer string

func (s argStringer) String() string { return "stringer:" + string(s) }

func TestWriteArgPreservesFormatting(t *testing.T) {
	values := []any{
		nil,
		"text",
		[]byte{0, 1, 127, 255},
		int64(-42),
		float64(1.25),
		true,
		time.Date(2026, time.August, 28, 1, 2, 3, 4, time.UTC),
		argStringer("value"),
	}

	var b bytes.Buffer
	for i, value := range values {
		if !writeArg(&b, i, value, len(values)-1, 4096) {
			t.Fatalf("writeArg stopped at value %d", i)
		}
	}

	want := make([]string, len(values))
	for i, value := range values {
		want[i] = fmt.Sprint(value)
	}
	if got, want := b.String(), strings.Join(want, ", "); got != want {
		t.Fatalf("writeArg() = %q, want %q", got, want)
	}
}

func TestWriteArgTruncatesOversizedValues(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
	}{
		{"string", strings.Repeat("x", 5000)},
		{"bytes", bytes.Repeat([]byte{255}, 5000)},
	} {
		t.Run(test.name, func(t *testing.T) {
			full := fmt.Sprint(test.value)
			var b bytes.Buffer
			if writeArg(&b, 0, test.value, 0, 1024) {
				t.Fatal("writeArg reported more values could be written")
			}
			// The marker is written inside the limit, not past it.
			if got, want := b.String(), full[:1024-len("...(1024)")]+"...(1024)"; got != want {
				t.Fatalf("writeArg() length = %d, want %d", len(got), len(want))
			}
		})
	}
}

// The separator between two values is written under the same limit as the
// values themselves, so a value landing on the boundary makes room for the
// marker instead of growing past the limit - and a zero limit keeps nothing.
func TestWriteArgTruncatesAtBoundary(t *testing.T) {
	for _, test := range []struct {
		name     string
		values   []any
		maxSize  int
		want     string
		wantMore bool
	}{
		{
			name:    "separator split by the limit",
			values:  []any{strings.Repeat("p", 1023), "z"},
			maxSize: 1024,
			want:    strings.Repeat("p", 1024-len("...(1024)")) + "...(1024)",
		},
		{
			name:     "everything fits",
			values:   []any{strings.Repeat("p", 1020), "z"},
			maxSize:  1024,
			want:     strings.Repeat("p", 1020) + ", z",
			wantMore: true,
		},
		{
			name:    "zero limit",
			values:  []any{"abc"},
			maxSize: 0,
			want:    "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var b bytes.Buffer
			more := true
			for i, v := range test.values {
				if more = writeArg(&b, i, v, len(test.values)-1, test.maxSize); !more {
					break
				}
			}
			if more != test.wantMore {
				t.Fatalf("writeArg() more = %v, want %v", more, test.wantMore)
			}
			if got := b.String(); got != test.want {
				t.Fatalf("writeArg() = %q, want %q", got, test.want)
			}
		})
	}
}

var benchmarkArgSink string

func BenchmarkWriteArgLarge(b *testing.B) {
	for _, benchmark := range []struct {
		name  string
		value any
	}{
		{"string", strings.Repeat("x", 1<<20)},
		{"bytes", bytes.Repeat([]byte{255}, 1<<20)},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			b.SetBytes(1 << 20)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var out bytes.Buffer
				writeArg(&out, 0, benchmark.value, 0, 1024)
				benchmarkArgSink = out.String()
			}
		})
	}
}

func TestWriteArgLimitsLargeValues(t *testing.T) {
	const maxSize = 65
	tests := []struct {
		name       string
		value      any
		wantPrefix string
	}{
		{name: "string", value: strings.Repeat("가", 1<<20), wantPrefix: "가"},
		{name: "bytes", value: bytes.Repeat([]byte{255}, 1<<20), wantPrefix: "[255 "},
		{name: "slice", value: make([]int32, 1<<20), wantPrefix: "[0 0 "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			more := writeArg(&b, 0, tt.value, 0, maxSize)

			if more {
				t.Fatal("writeArg reported that an oversized value fit")
			}
			if b.Len() > maxSize {
				t.Fatalf("result is %d bytes; max is %d", b.Len(), maxSize)
			}
			if b.Cap() > maxSize*2 {
				t.Fatalf("buffer retained %d bytes for a %d-byte limit", b.Cap(), maxSize)
			}
			if got := b.String(); !strings.HasPrefix(got, tt.wantPrefix) {
				t.Fatalf("result %q does not preserve prefix %q", got, tt.wantPrefix)
			}
			if got := b.String(); !strings.HasSuffix(got, "...(65)") {
				t.Fatalf("result %q has no truncation marker", got)
			}
			if got := b.String(); !utf8.ValidString(got) {
				t.Fatalf("result is not valid UTF-8: %q", got)
			}
		})
	}
}

func TestWriteArgLimitsMultipleValues(t *testing.T) {
	values := []any{"0123456789", "abcdefgh", "xyz"}
	var b bytes.Buffer
	for i, value := range values {
		if !writeArg(&b, i, value, len(values)-1, 20) {
			break
		}
	}

	if got, want := b.String(), "0123456789, a...(20)"; got != want {
		t.Fatalf("result = %q; want %q", got, want)
	}
	if b.Len() != 20 {
		t.Fatalf("result is %d bytes; want 20", b.Len())
	}
}
