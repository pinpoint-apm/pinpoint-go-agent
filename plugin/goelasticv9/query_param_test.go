package ppgoelasticv9

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// queryParam finds the DSL among the other parameters an elastic call carries
// without building the whole url.Values map.
func Test_queryParam(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want string
	}{
		{"filter_path=hits&q=name%3Afoo&refresh=true", "name:foo"},
		{"q=a+b", "a b"},
		{"q=", ""},
		{"refresh=true", ""},
		{"", ""},
		{"q=%zz", ""},
	} {
		assert.Equal(t, tt.want, queryParam(tt.raw, "q"), "queryParam(%q)", tt.raw)
	}
}
