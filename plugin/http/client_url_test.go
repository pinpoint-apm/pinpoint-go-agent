package pphttp

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A password in the URL's userinfo is masked in the recorded URL.
func TestClientUrl_MasksThePassword(t *testing.T) {
	u, err := url.Parse("https://user:secret@example.com/p?q=1")
	if err != nil {
		t.Fatal(err)
	}
	got := ClientUrl(http.MethodGet, u)
	assert.NotContains(t, got, "secret")
	assert.Contains(t, got, "user:xxxxx@example.com/p")
}
