package ppgocql

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBatchSQL_BoundsLargeStatements(t *testing.T) {
	for _, statements := range [][]string{
		{strings.Repeat("x", 4*1024*1024)},
		{strings.Repeat("x", maxBatchSQL-2), "SELECT 1"},
		{"SELECT 1", strings.Repeat("한", maxBatchSQL)},
	} {
		got := batchSQL(statements)
		if len(got) > maxBatchSQL || !utf8.ValidString(got) || !strings.HasSuffix(got, "...(truncated)") {
			t.Fatalf("invalid bounded annotation: bytes=%d utf8=%v", len(got), utf8.ValidString(got))
		}
	}
}
