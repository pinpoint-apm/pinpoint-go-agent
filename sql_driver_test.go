package pinpoint

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_writeBindValue_TruncatesOversizedValue(t *testing.T) {
	var b bytes.Buffer
	more := writeBindValue(&b, 0, strings.Repeat("x", 5000), 0, 1024)

	assert.False(t, more)
	assert.Equal(t, strings.Repeat("x", 1024)+"...(1024)", b.String())
}
