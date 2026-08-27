package pphttp

import (
	"slices"
	"strconv"
	"strings"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// statusTableSize is how many status codes the bit table covers. HTTP tops out
// at the 5xx class, so every code a response can carry fits below it.
const statusTableSize = 640 // 10 uint64 words

// httpStatusError decides whether a response status counts as a failure.
//
// The configured tokens are flattened into a bit table once, when the config is
// parsed, so a request pays a bounds check and one bit lookup instead of an
// interface call per configured entry.
type httpStatusError struct {
	codes [statusTableSize / 64]uint64

	// Codes the table cannot hold: a number outside it, or a token that does
	// not parse - which the old per-token matcher kept as -1 and compared
	// against the status. No net/http response carries one, but the exported
	// RecordHttpServerResponse takes any int, so they are kept rather than
	// dropped, and the verdict stays what it was for every input.
	outOfTable []int
}

func newHttpStatusError() *httpStatusError {
	return parseHttpStatusErrors(pinpoint.GetConfig().StringSlice(CfgHttpServerStatusCodeErrors))
}

// parseHttpStatusErrors expands the configured tokens - a status class, "1xx"
// through "5xx" case-insensitively, or a single status code - into the table.
func parseHttpStatusErrors(cfg []string) *httpStatusError {
	h := &httpStatusError{}

	for _, s := range trimStringSlice(cfg) {
		switch {
		case strings.EqualFold(s, "1xx"):
			h.setRange(100, 199)
		case strings.EqualFold(s, "2xx"):
			h.setRange(200, 299)
		case strings.EqualFold(s, "3xx"):
			h.setRange(300, 399)
		case strings.EqualFold(s, "4xx"):
			h.setRange(400, 499)
		case strings.EqualFold(s, "5xx"):
			h.setRange(500, 599)
		default:
			c, err := strconv.Atoi(s)
			if err != nil {
				c = -1
			}
			h.set(c)
		}
	}

	return h
}

func (h *httpStatusError) setRange(min, max int) {
	for code := min; code <= max; code++ {
		h.set(code)
	}
}

func (h *httpStatusError) set(code int) {
	if uint(code) < statusTableSize {
		h.codes[code/64] |= 1 << (uint(code) % 64)
	} else {
		h.outOfTable = append(h.outOfTable, code)
	}
}

func (h *httpStatusError) isError(code int) bool {
	if uint(code) < statusTableSize {
		return h.codes[code/64]&(1<<(uint(code)%64)) != 0
	}
	return slices.Contains(h.outOfTable, code)
}
