package pphttp

import (
	"strconv"
	"strings"
)

// httpStatusError is the set of response statuses that count as a failure,
// flattened from the configured tokens once when the config is parsed. A token
// that does not parse is kept as -1, which the old per-token matcher compared
// against the status: no response carries it, but the exported
// RecordHttpServerResponse takes any int, so the verdict stays what it was
// for every input.
type httpStatusError map[int]bool

// parseHttpStatusErrors expands the configured tokens - a status class, "1xx"
// through "5xx" case-insensitively, or a single status code - into the set.
func parseHttpStatusErrors(cfg []string) httpStatusError {
	h := httpStatusError{}
	setRange := func(min, max int) {
		for code := min; code <= max; code++ {
			h[code] = true
		}
	}
	for _, s := range trimStringSlice(cfg) {
		switch {
		case strings.EqualFold(s, "1xx"):
			setRange(100, 199)
		case strings.EqualFold(s, "2xx"):
			setRange(200, 299)
		case strings.EqualFold(s, "3xx"):
			setRange(300, 399)
		case strings.EqualFold(s, "4xx"):
			setRange(400, 499)
		case strings.EqualFold(s, "5xx"):
			setRange(500, 599)
		default:
			c, err := strconv.Atoi(s)
			if err != nil {
				c = -1
			}
			h[c] = true
		}
	}
	return h
}

func (h httpStatusError) isError(code int) bool {
	return h[code]
}
