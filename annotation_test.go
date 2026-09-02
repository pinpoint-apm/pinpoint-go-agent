package pinpoint

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAnnotationAppendBytesStringStringCopiesInput(t *testing.T) {
	annotation := &annotation{}
	input := []byte{1, 2, 3}

	annotation.AppendBytesStringString(AnnotationSqlUid, input, "param", "args")
	input[0] = 9

	list := annotation.getList()
	value := list[0].GetValue().GetBytesStringStringValue()
	assert.Equal(t, []byte{1, 2, 3}, value.GetBytesValue())
}
