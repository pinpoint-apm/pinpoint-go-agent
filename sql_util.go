package pinpoint

import (
	"bufio"
	"strconv"
	"strings"
)

type sqlNormalizer struct {
	r          *bufio.Reader
	output     *sqlNormalizerBuilder
	param      *sqlNormalizerBuilder
	paramIndex int
	sql        string
	isChanged  bool
}

// sqlNormalizerBuilder retains only the prefix used by SQL metadata and
// annotations. A small overflow sentinel is kept so abbreviateString can
// preserve the existing "...(limit)" marker. strings.Builder is a field rather
// than an embedded type: promoted Write/WriteByte methods would bypass the
// bound without any compile error at the call site.
type sqlNormalizerBuilder struct {
	sb strings.Builder
}

func (b *sqlNormalizerBuilder) Len() int {
	return b.sb.Len()
}

func (b *sqlNormalizerBuilder) WriteRune(r rune) {
	if b.sb.Len() <= maxSqlSize {
		_, _ = b.sb.WriteRune(r)
	}
}

func (b *sqlNormalizerBuilder) WriteString(value string) {
	remaining := maxSqlSize + 1 - b.sb.Len()
	if remaining <= 0 {
		return
	}
	if len(value) > remaining {
		value = value[:remaining]
	}
	_, _ = b.sb.WriteString(value)
}

func (b *sqlNormalizerBuilder) result() string {
	return abbreviateString(b.sb.String(), maxSqlSize)
}

// full reports that the builder stopped accepting input: WriteRune and
// WriteString are both no-ops past this point.
func (b *sqlNormalizerBuilder) full() bool {
	return b.sb.Len() > maxSqlSize
}

func newSqlNormalizer(sql string) *sqlNormalizer {
	normalizer := sqlNormalizer{}

	normalizer.r = bufio.NewReader(strings.NewReader(sql))
	normalizer.output = &sqlNormalizerBuilder{}
	normalizer.param = &sqlNormalizerBuilder{}
	normalizer.paramIndex = 0
	normalizer.sql = sql
	normalizer.isChanged = false

	return &normalizer
}

func (s *sqlNormalizer) run() (string, string) {
	numberTokenStartEnable := true

	for {
		// Once the output is full, nothing read past this point can still be
		// shown, so stop scanning instead of walking the rest of the input -
		// a multi-megabyte statement otherwise costs O(input) for a 64KB
		// result. Bind values whose placeholders fell past the cap are
		// dropped along with the SQL they belong to.
		if s.output.full() {
			break
		}
		if ch := s.read(); ch == eof {
			break
		} else if ch == '/' {
			s.output.WriteRune(ch)
			if s.lookahead('/') {
				s.consumeSingleLineComment()
			} else if s.lookahead('*') {
				s.consumeMultiLineComment()
			} else {
				numberTokenStartEnable = true
			}
		} else if ch == '-' {
			s.output.WriteRune(ch)
			if s.lookahead('-') {
				s.consumeSingleLineComment()
			} else {
				numberTokenStartEnable = true
			}
		} else if ch == '\'' {
			s.output.WriteRune(ch)
			if s.lookahead('\'') {
				s.output.WriteRune(s.read())
			} else {
				s.consumeCharLiteral()
			}
		} else if isDigit(ch) {
			if numberTokenStartEnable {
				s.unread()
				s.consumeNumberLiteral()
			} else {
				s.output.WriteRune(ch)
			}
		} else if isLetter(ch) || ch == '.' || ch == '_' || ch == '@' || ch == ':' || ch == '$' {
			numberTokenStartEnable = false
			s.output.WriteRune(ch)
		} else {
			numberTokenStartEnable = true
			s.output.WriteRune(ch)
		}
	}

	if s.isChanged {
		if s.param.Len() > 0 {
			return s.output.result(), s.param.result()
		} else {
			return s.output.result(), ""
		}
	} else {
		return abbreviateString(s.sql, maxSqlSize), ""
	}

}

func (s *sqlNormalizer) consumeSingleLineComment() {
	var ch rune

	for {
		if s.output.full() { // the run loop exits right after on the same test
			break
		}
		if ch = s.read(); ch == eof {
			break
		}
		s.output.WriteRune(ch)
		if ch == '\n' {
			break
		}
	}
}

func (s *sqlNormalizer) consumeMultiLineComment() {
	var ch rune
	prev := eof
	s.output.WriteRune(s.read()) /* cousume '*' */

	for {
		if s.output.full() { // the run loop exits right after on the same test
			break
		}
		if ch = s.read(); ch == eof {
			break
		}
		s.output.WriteRune(ch)
		if prev == '*' && ch == '/' {
			break
		}
		prev = ch
	}
}

func (s *sqlNormalizer) consumeCharLiteral() {
	var ch rune

	s.isChanged = true
	if s.param.Len() > 0 {
		s.param.WriteRune(',')
	}

	for {
		// Nothing can be recorded any more; the run loop exits right after.
		if s.output.full() && s.param.full() {
			break
		}
		if ch = s.read(); ch == eof {
			break
		}

		if ch == ',' {
			s.param.WriteRune(ch)
		} else if ch == '\'' {
			if s.lookahead('\'') {
				s.param.WriteRune(s.read())
			} else {
				s.output.WriteString(strconv.Itoa(s.paramIndex))
				s.paramIndex++
				s.output.WriteRune('$')
				s.output.WriteRune('\'')
				break
			}
		}

		s.param.WriteRune(ch)
	}
}

func (s *sqlNormalizer) consumeNumberLiteral() {
	var ch rune

	s.isChanged = true
	if s.param.Len() > 0 {
		s.param.WriteRune(',')
	}
	s.output.WriteString(strconv.Itoa(s.paramIndex))
	s.paramIndex++
	s.output.WriteRune('#')

	for {
		// Nothing can be recorded any more; the run loop exits right after.
		if s.output.full() && s.param.full() {
			break
		}
		if ch = s.read(); ch == eof {
			break
		}

		if isDigit(ch) || ch == '.' || ch == 'E' || ch == 'e' {
			s.param.WriteRune(ch)
		} else {
			s.unread()
			break
		}
	}
}

func (s *sqlNormalizer) read() rune {
	ch, _, err := s.r.ReadRune()
	if err != nil {
		return eof
	}
	return ch
}

func (s *sqlNormalizer) unread() {
	_ = s.r.UnreadRune()
}

func (s *sqlNormalizer) lookahead(expected rune) bool {
	ch, _, err := s.r.ReadRune()
	_ = s.r.UnreadRune()
	if err != nil {
		return false
	}
	return ch == expected
}

func isLetter(ch rune) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

func isDigit(ch rune) bool {
	return ch >= '0' && ch <= '9'
}

var eof = rune(0)
