package pinpoint

import (
	"bufio"
	"strconv"
	"strings"
)

type sqlNormalizer struct {
	r          *bufio.Reader
	output     strings.Builder
	param      strings.Builder
	paramIndex int
	sql        string
	isChanged  bool
	// removeComments drops comments from the output instead of copying them,
	// as the Java agent does by default (profiler.jdbc.removecomments).
	removeComments bool
}

func newSqlNormalizer(sql string, removeComments bool) *sqlNormalizer {
	normalizer := sqlNormalizer{}

	normalizer.r = bufio.NewReader(strings.NewReader(sql))
	normalizer.paramIndex = 0
	normalizer.sql = sql
	normalizer.isChanged = false
	normalizer.removeComments = removeComments

	return &normalizer
}

// run normalizes the whole statement, as the Java agent's DefaultSqlNormalizer
// does. The 64KB cap belongs to the metadata text alone (see cacheSql and
// cacheSqlUid): a statement past the cap must still hash to the UID Java
// computes from the full normalized SQL, and param must stay whole because the
// server splits it on ',' to refill the <idx>#/<idx>$ placeholders - a cut
// param leaves placeholders exposed.
func (s *sqlNormalizer) run() (string, string) {
	numberTokenStartEnable := true

	for {
		if ch := s.read(); ch == eof {
			break
		} else if ch == '/' {
			// The comment markers are decided before ch is written: under
			// removeComments the marker itself must not reach the output.
			// Neither branch touches numberTokenStartEnable, as in Java - a
			// comment is not a number token boundary either way.
			if s.lookahead('/') {
				s.consumeSingleLineComment(ch)
			} else if s.lookahead('*') {
				s.consumeMultiLineComment(ch)
			} else {
				s.output.WriteRune(ch)
				numberTokenStartEnable = true
			}
		} else if ch == '-' {
			if s.lookahead('-') {
				s.consumeSingleLineComment(ch)
			} else {
				s.output.WriteRune(ch)
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
		} else if ch == '$' {
			// Java turns the flag off only for a positional placeholder
			// ($1, $2, ...); a '$' followed by anything else leaves it as it
			// was. Forcing it off here would swallow a literal that Java still
			// extracts, e.g. "$'x'1" - neither a string literal nor a comment
			// touches the flag on the way to the digit.
			if s.lookaheadDigit() {
				numberTokenStartEnable = false
			}
			s.output.WriteRune(ch)
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

// consumeSingleLineComment consumes a // or -- comment. lead is the first
// character of the marker, already read but not yet written. The terminating
// newline is part of the comment, as in the Java agent - ParserContext reads it
// with "\n" as the end token - so removal leaves nothing at all in its place.
func (s *sqlNormalizer) consumeSingleLineComment(lead rune) {
	var ch rune

	if s.removeComments {
		// A statement whose only change is a dropped comment still has to
		// return the normalized text, not the original (Java's parameter.touch).
		s.isChanged = true
	} else {
		s.output.WriteRune(lead)
	}

	for {
		if ch = s.read(); ch == eof {
			break
		}
		if !s.removeComments {
			s.output.WriteRune(ch)
		}
		if ch == '\n' {
			break
		}
	}
}

// consumeMultiLineComment consumes a /* */ comment. lead is the '/', already
// read but not yet written.
func (s *sqlNormalizer) consumeMultiLineComment(lead rune) {
	var ch rune
	prev := eof

	if s.removeComments {
		s.isChanged = true
		s.read() /* consume '*' */
	} else {
		s.output.WriteRune(lead)
		s.output.WriteRune(s.read()) /* cousume '*' */
	}

	for {
		if ch = s.read(); ch == eof {
			break
		}
		if !s.removeComments {
			s.output.WriteRune(ch)
		}
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

// lookaheadDigit reports whether the next character is a digit, without
// consuming it. End of input is not a digit, as in Java, whose lookAhead1
// returns NEXT_TOKEN_NOT_EXIST there.
func (s *sqlNormalizer) lookaheadDigit() bool {
	ch, _, err := s.r.ReadRune()
	_ = s.r.UnreadRune()
	return err == nil && isDigit(ch)
}

func isLetter(ch rune) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

func isDigit(ch rune) bool {
	return ch >= '0' && ch <= '9'
}

var eof = rune(0)
