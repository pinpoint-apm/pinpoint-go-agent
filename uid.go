package pinpoint

import (
	"encoding/base64"

	"github.com/google/uuid"
)

// uidBase64Len is the fixed length of a base64-encoded UUID: 16 bytes encoded
// with URL-safe base64 without padding always yields 22 characters.
const uidBase64Len = 22

// encodeUID serializes a UUID to its 16-byte network-order layout (most
// significant bits first, big-endian, then the least significant bits) and
// encodes it with RFC 4648 §5 URL-and-filename-safe base64 without padding.
//
// This is byte-for-byte compatible with the Java agent's
// Base64Utils.encode(UUID): google/uuid stores the UUID in the same RFC 4122
// network byte order that Java produces via BytesUtils.writeLong(msb)/writeLong(lsb).
// The result is always exactly 22 characters.
func encodeUID(u uuid.UUID) string {
	// u is a [16]byte already in big-endian network order.
	return base64.RawURLEncoding.EncodeToString(u[:])
}

// decodeUID reverses encodeUID: it decodes a 22-character URL-safe base64
// string (no padding) back into the original UUID.
func decodeUID(s string) (uuid.UUID, error) {
	var u uuid.UUID
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return u, err
	}
	if err := u.UnmarshalBinary(b); err != nil {
		return u, err
	}
	return u, nil
}

// newAgentUID generates a time-based UUID (RFC 9562 version 7: 48-bit Unix
// epoch milliseconds prefix, version/variant bits set, remaining bits random),
// matching the Java agent's TimeBasedEpochGenerator.
func newAgentUID() (uuid.UUID, error) {
	return uuid.NewV7()
}
