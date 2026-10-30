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
	for _, s := range trimStringSlice(cfg) {
		if len(s) == 3 && '1' <= s[0] && s[0] <= '5' && strings.EqualFold(s[1:], "xx") {
			base := int(s[0]-'0') * 100
			for code := base; code < base+100; code++ {
				h[code] = true
			}
			continue
		}
		c, err := strconv.Atoi(s)
		if err != nil {
			c = -1
		}
		h[c] = true
	}
	return h
}

func (h httpStatusError) isError(code int) bool {
	return h[code]
}
