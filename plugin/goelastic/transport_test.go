package ppgoelastic

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Only MaxDslLength characters are recorded, so a large body must not be read
// (or inflated) in full just to build the annotation - but the request's own
// body must survive intact when there is no separate copy to read.
func Test_dslString_LimitsTheRead(t *testing.T) {
	huge := strings.Repeat("x", 1<<20)

	req, err := http.NewRequest(http.MethodPost, "http://es:9200/_bulk", strings.NewReader(huge))
	if err != nil {
		t.Fatal(err)
	}
	dsl, err := getBodyFromCopy(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(dsl) != maxBodyRead {
		t.Errorf("read %d bytes from the body copy, want %d", len(dsl), maxBodyRead)
	}

	// No GetBody: the body is consumed and restored, so it must stay whole.
	req, err = http.NewRequest(http.MethodPost, "http://es:9200/_bulk", strings.NewReader(huge))
	if err != nil {
		t.Fatal(err)
	}
	req.GetBody = nil
	if _, err = getBody(req); err != nil {
		t.Fatal(err)
	}
	sent, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sent, []byte(huge)) {
		t.Errorf("restored body is %d bytes, want %d", len(sent), len(huge))
	}
}
