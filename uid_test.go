package pinpoint

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// goldenVectors are Java-verified UUID -> base64(22 chars) pairs. They guard
// byte-for-byte compatibility with the Java agent's Base64Utils.encode(UUID).
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

func TestDecodeUID_GoldenVectors(t *testing.T) {
	for _, v := range goldenVectors {
		got, err := decodeUID(v.base64)
		assert.NoError(t, err, "decode %s", v.base64)
		assert.Equal(t, uuid.MustParse(v.uuid), got, "decode %s", v.base64)
	}
}

func TestEncodeDecodeUID_RoundTrip(t *testing.T) {
	for i := 0; i < 100; i++ {
		u, err := newAgentUID()
		assert.NoError(t, err)

		enc := encodeUID(u)
		assert.Len(t, enc, uidBase64Len)

		dec, err := decodeUID(enc)
		assert.NoError(t, err)
		assert.Equal(t, u, dec)
	}
}

func TestNewAgentUID_IsVersion7(t *testing.T) {
	u, err := newAgentUID()
	assert.NoError(t, err)
	assert.Equal(t, uuid.Version(7), u.Version(), "must be UUIDv7 (RFC 9562)")
	assert.Equal(t, uuid.RFC4122, u.Variant(), "must use RFC 4122 variant bits")
}

func TestDecodeUID_InvalidLength(t *testing.T) {
	_, err := decodeUID("tooshort")
	assert.Error(t, err)
}
