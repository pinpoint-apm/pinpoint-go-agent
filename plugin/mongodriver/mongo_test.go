package ppmongo

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/event"
)

const abbreviationMarker = "...(65536)"

var commandAnnotationSink string

func TestCommandAnnotation(t *testing.T) {
	t.Run("small command keeps extended JSON", func(t *testing.T) {
		evt := commandStartedEvent(t, "find", "widgets", strings.Repeat("x", 32))
		want, err := bson.MarshalExtJSON(evt.Command, false, false)
		if err != nil {
			t.Fatal(err)
		}

		if got := commandAnnotation(evt, "widgets"); got != string(want) {
			t.Fatalf("commandAnnotation() = %q, want %q", got, want)
		}
	})

	t.Run("expanded extended JSON is abbreviated", func(t *testing.T) {
		// Control bytes are escaped as \uXXXX, so BSON below the gate still
		// converts to extended JSON above maxJsonSize.
		evt := commandStartedEvent(t, "insert", "widgets", strings.Repeat("\x01", 60<<10))
		if len(evt.Command) > maxBsonSize {
			t.Fatalf("test command size = %d, want at most %d", len(evt.Command), maxBsonSize)
		}
		b, err := bson.MarshalExtJSON(evt.Command, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) <= maxJsonSize {
			t.Fatalf("test extended JSON size = %d, want greater than %d", len(b), maxJsonSize)
		}

		got := commandAnnotation(evt, "widgets")
		if !strings.HasSuffix(got, abbreviationMarker) {
			t.Fatalf("commandAnnotation() = %.80q, want the abbreviation marker", got)
		}
		if len(got) > maxJsonSize {
			t.Fatalf("commandAnnotation() = %d bytes, want at most %d", len(got), maxJsonSize)
		}
		if want := string(b[:maxJsonSize-len(abbreviationMarker)]) + abbreviationMarker; got != want {
			t.Fatalf("commandAnnotation() = %d bytes, want the first %d JSON bytes plus the marker", len(got), len(want)-len(abbreviationMarker))
		}
	})

	t.Run("abbreviation keeps valid UTF-8", func(t *testing.T) {
		// Escaped control bytes push the cut into the multi-byte run that follows.
		evt := commandStartedEvent(t, "insert", "widgets", strings.Repeat("\x01", 10900)+strings.Repeat("\uac00", 200))
		b, err := bson.MarshalExtJSON(evt.Command, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if cut := maxJsonSize - len(abbreviationMarker); utf8.RuneStart(b[cut]) {
			t.Fatalf("test payload does not straddle the cut at %d", cut)
		}

		got := commandAnnotation(evt, "widgets")
		if !utf8.ValidString(got) {
			t.Fatalf("commandAnnotation() is not valid UTF-8 (%d bytes)", len(got))
		}
		if !strings.HasSuffix(got, abbreviationMarker) {
			t.Fatalf("commandAnnotation() = %.80q, want the abbreviation marker", got)
		}
	})

	t.Run("large command skips extended JSON", func(t *testing.T) {
		evt := commandStartedEvent(t, "insert", "widgets", strings.Repeat("x", maxBsonSize))
		if len(evt.Command) <= maxBsonSize {
			t.Fatalf("test command size = %d, want greater than %d", len(evt.Command), maxBsonSize)
		}

		want := fmt.Sprintf("[MongoDB command omitted: command=insert, collection=widgets, bsonSize=%d]", len(evt.Command))
		if got := commandAnnotation(evt, "widgets"); got != want {
			t.Fatalf("commandAnnotation() = %q, want %q", got, want)
		}
	})
}

// Allocations must stay flat once the command exceeds maxBsonSize; the 1KB case
// keeps the normal conversion path measured so a regression there still shows up.
func BenchmarkCommandAnnotation(b *testing.B) {
	for _, size := range []int{1 << 10, 128 << 10, 1 << 20, 8 << 20} {
		b.Run(fmt.Sprintf("BSON_%dKB", size>>10), func(b *testing.B) {
			evt := commandStartedEvent(b, "insert", "widgets", strings.Repeat("x", size))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				commandAnnotationSink = commandAnnotation(evt, "widgets")
			}
		})
	}
}

func commandStartedEvent(t testing.TB, name, collection, payload string) *event.CommandStartedEvent {
	t.Helper()
	command, err := bson.Marshal(bson.D{
		{Key: name, Value: collection},
		{Key: "payload", Value: payload},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &event.CommandStartedEvent{
		Command:     command,
		CommandName: name,
	}
}
