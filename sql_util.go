package pinpoint

import (
	"bufio"
	"strings"
)

type sqlNormalizer struct {
	r          *bufio.Reader
	output     *strings.Builder
	param      *strings.Builder
	paramIndex int
	sql        string
	isChanged  bool
}

func newSqlNormalizer(sql string) *sqlNormalizer {
	normalizer := sqlNormalizer{}

	normalizer.r = bufio.NewReader(strings.NewReader(sql))
	normalizer.output = &strings.Builder{}
	normalizer.param = &strings.Builder{}
	normalizer.paramIndex = 0
	normalizer.sql = sql
	normalizer.isChanged = false

	return &normalizer
}

func (s *sqlNormalizer) run() (string, string) {
	numberTokenStartEnable := false

	for {
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
		} else if isLetter(ch) || ch == '.' || ch == '_' || ch == '@' || ch == ':' {
			numberTokenStartEnable = false
			s.output.WriteRune(ch)
		} else {
			numberTokenStartEnable = true
			s.output.WriteRune(ch)
		}
	}

	if s.isChanged {
		if s.param.Len() > 0 {
			return s.output.String(), s.param.String()
		} else {
			return s.output.String(), ""
		}
	} else {
		return s.sql, ""
	}

}

func (s *sqlNormalizer) consumeSingleLineComment() {
	var ch rune

	for {
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
		if ch = s.read(); ch == eof {
			break
		}

		s.param.WriteRune(ch)
		if ch == ',' {
			s.param.WriteRune(ch)
		}

		if ch == '\'' {
			if s.lookahead('\'') {
				s.param.WriteRune(s.read())
			} else {
				s.paramIndex++
				s.output.WriteString(string(s.paramIndex))
				s.output.WriteRune('$')
				s.output.WriteRune('\'')
				break
			}
		}
	}
}

func (s *sqlNormalizer) consumeNumberLiteral() {
	var ch rune

	s.isChanged = true
	if s.param.Len() > 0 {
		s.param.WriteRune(',')
	}
	s.paramIndex++
	s.output.WriteString(string(s.paramIndex))
	s.output.WriteRune('#')

	for {
		if ch = s.read(); ch == eof {
			break
		}

		if isDigit(ch) || ch == '.' || ch == 'E' || ch == 'e' {
			s.param.WriteRune(ch)
		} else {
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
