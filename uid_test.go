package pinpoint

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

var goldenVectors = []struct {
	uuid   string
	base64 string
}{
	{"00000000-0000-0000-0000-000000000000", "AAAAAAAAAAAAAAAAAAAAAA"},
	{"ffffffff-ffff-ffff-ffff-ffffffffffff", "_____________________w"},
	{"12345678-90ab-cdef-1234-567890abcdef", "EjRWeJCrze8SNFZ4kKvN7w"},
	{"00112233-4455-6677-8899-aabbccddeeff", "ABEiM0RVZneImaq7zN3u_w"},
	{"0192f1a0-7e8b-7c3d-9f2e-1a2b3c4d5e6f", "AZLxoH6LfD2fLhorPE1ebw"},
	{"deadbeef-dead-beef-dead-beefdeadbeef", "3q2-796tvu_erb7v3q2-7w"},
}

func TestEncodeUID_GoldenVectors(t *testing.T) {
	for _, v := range goldenVectors {
		u := uuid.MustParse(v.uuid)
		got := encodeUID(u)
		assert.Equal(t, v.base64, got, "encode %s", v.uuid)
		assert.Len(t, got, uidBase64Len, "length of %s", v.uuid)
	}
}
