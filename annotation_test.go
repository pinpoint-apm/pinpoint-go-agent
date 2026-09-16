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

// ===========================================================================
// Locked invariants - behaviour pinned against the Java and C++ agents. The
// cross-agent rationale and references live in doc/development.md.
// ===========================================================================

// Test_AnnotationKeys locks the annotation keys the agent emits.
// The collector and the web tier read them by number.
func Test_AnnotationKeys(t *testing.T) {
	assert.Equal(t, 12, AnnotationApi)
	assert.Equal(t, 20, AnnotationSqlId)
	assert.Equal(t, 25, AnnotationSqlUid)
	assert.Equal(t, 40, AnnotationHttpUrl)
	assert.Equal(t, 46, AnnotationHttpStatusCode)
	assert.Equal(t, 300, AnnotationHttpProxyHeader)
	assert.Equal(t, -52, AnnotationExceptionChainId, "Java AnnotationKey.EXCEPTION_CHAIN_ID")
}
