package pinpoint

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// IsInjected is what a client plugin asks before starting its event, so it has
// to say "injected" for exactly the headers Inject writes - by presence, since
// the agent's own injected header may carry an empty value.
func TestIsInjected(t *testing.T) {
	for _, tt := range []struct {
		name string
		hdr  http.Header
		want bool
	}{
		{"nothing", http.Header{}, false},
		{"trace id", http.Header{"Pinpoint-Traceid": {"app^1^2"}}, true},
		{"empty trace id is still a context", http.Header{"Pinpoint-Traceid": {""}}, true},
		{"unsampled marker", http.Header{"Pinpoint-Sampled": {"s0"}}, true},
		{"other pinpoint header alone", http.Header{"Pinpoint-Host": {"db"}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsInjected(HttpHeaderReader(tt.hdr)))
		})
	}
}

// RemoteHost strips a port only when there is one, keeps a bare IP, and falls
// back to loopback for a peer that is no network address.
func TestRemoteHost(t *testing.T) {
	for addr, want := range map[string]string{
		"10.0.0.1:12345": "10.0.0.1",
		"[::1]:8000":     "::1",
		"10.0.0.1":       "10.0.0.1",
		"::1":            "::1",
		"/tmp/grpc.sock": "127.0.0.1",
		"":               "127.0.0.1",
	} {
		assert.Equal(t, want, RemoteHost(addr), "RemoteHost(%q)", addr)
	}
}

// AnnotationList names every item up to the cap and counts the rest, so an
// annotation cannot grow with the caller's batch.
func TestAnnotationList(t *testing.T) {
	item := func(i int) string { return "c" + strconv.Itoa(i) }

	assert.Equal(t, "", AnnotationList(0, item))
	assert.Equal(t, "c0", AnnotationList(1, item))
	assert.Equal(t, "c0, c1, c2", AnnotationList(3, item))

	atCap := AnnotationList(maxAnnotationListItems, item)
	assert.Equal(t, maxAnnotationListItems, strings.Count(atCap, "c"), "a batch at the cap is listed whole")
	assert.NotContains(t, atCap, "more)")

	over := AnnotationList(maxAnnotationListItems+3, item)
	assert.True(t, strings.HasSuffix(over, ", ...(3 more)"), "got %q", over)
	assert.Equal(t, len(atCap)+len(", ...(3 more)"), len(over), "the cap names the same items and counts the rest")
}
