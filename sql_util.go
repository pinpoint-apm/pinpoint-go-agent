package pinpoint

import (
	"strconv"
	"strings"
)

// sqlNormalizer walks the statement one byte at a time, indexing the string
// directly the way the Java agent's ParserContext walks it with charAt.
//
// Bytes, not runes: every decision the parser makes is on an ASCII character,
// and Java's isNumberTokenStart is itself an ASCII-only test, so the bytes of a
// multibyte character all fall through to the same branch a whole rune would.
// Decoding buys nothing and costs fidelity - an invalid UTF-8 byte would decode
// to U+FFFD and be written back as three different bytes, rewriting a statement
// the collector is supposed to receive verbatim.
type sqlNormalizer struct {
	sql        string
	pos        int
	output     strings.Builder
	param      strings.Builder
	paramIndex int
	isChanged  bool
	// removeComments drops comments from the output instead of copying them,
	// as the Java agent does by default (profiler.jdbc.removecomments).
	removeComments bool
}

func newSqlNormalizer(sql string, removeComments bool) *sqlNormalizer {
	return &sqlNormalizer{sql: sql, removeComments: removeComments}
}

// run normalizes the whole statement, as the Java agent's DefaultSqlNormalizer
// does. The 64KB cap belongs to the metadata text alone (see cacheSql and
// cacheSqlUid): a statement past the cap must still hash to the UID Java
// computes from the full normalized SQL, and param must stay whole because the
// server splits it on ',' to refill the <idx>#/<idx>$ placeholders - a cut
// param leaves placeholders exposed.
func (s *sqlNormalizer) run() (string, string) {
	numberTokenStartEnable := true

	for s.pos < len(s.sql) {
		ch := s.sql[s.pos]
		s.pos++

		if ch == '/' {
			// The comment markers are decided before ch is written: under
			// removeComments the marker itself must not reach the output.
			// Neither branch touches numberTokenStartEnable, as in Java - a
			// comment is not a number token boundary either way.
			if s.lookahead('/') {
				s.consumeSingleLineComment(ch)
			} else if s.lookahead('*') {
				s.consumeMultiLineComment(ch)
			} else {
				s.output.WriteByte(ch)
				numberTokenStartEnable = true
			}
		} else if ch == '-' {
			if s.lookahead('-') {
				s.consumeSingleLineComment(ch)
			} else {
				s.output.WriteByte(ch)
				numberTokenStartEnable = true
			}
		} else if ch == '\'' {
			s.output.WriteByte(ch)
			if s.lookahead('\'') {
				// An empty literal is copied through as it stands: Java neither
				// records a parameter for it nor marks the statement changed.
				s.output.WriteByte('\'')
				s.pos++
			} else {
				s.consumeCharLiteral()
			}
		} else if isDigit(ch) {
			if numberTokenStartEnable {
				s.consumeNumberLiteral(ch)
			} else {
				s.output.WriteByte(ch)
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
			s.output.WriteByte(ch)
		} else if isLetter(ch) || ch == '.' || ch == '_' || ch == '@' || ch == ':' {
			numberTokenStartEnable = false
			s.output.WriteByte(ch)
		} else {
			// Whitespace, operators and separators land here, and so does every
			// byte of a multibyte character - Java's isNumberTokenStart says the
			// same of a non-ASCII char, so "테이블1" yields "테이블0#" on both.
			numberTokenStartEnable = true
			s.output.WriteByte(ch)
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
func (s *sqlNormalizer) consumeSingleLineComment(lead byte) {
	if s.removeComments {
		// A statement whose only change is a dropped comment still has to
		// return the normalized text, not the original (Java's parameter.touch).
		s.isChanged = true
	} else {
		s.output.WriteByte(lead)
	}

	for s.pos < len(s.sql) {
		ch := s.sql[s.pos]
		s.pos++
		if !s.removeComments {
			s.output.WriteByte(ch)
		}
		if ch == '\n' {
			break
		}
	}
}

// consumeMultiLineComment consumes a /* */ comment. lead is the '/', already
// read but not yet written.
func (s *sqlNormalizer) consumeMultiLineComment(lead byte) {
	if s.removeComments {
		s.isChanged = true
	} else {
		s.output.WriteByte(lead)
		s.output.WriteByte('*')
	}
	s.pos++ /* consume '*' */

	// The '*' of the opening marker cannot close the comment: Java searches for
	// "*/" from behind it, so "/*/" runs to the end of the statement.
	prevStar := false
	for s.pos < len(s.sql) {
		ch := s.sql[s.pos]
		s.pos++
		if !s.removeComments {
			s.output.WriteByte(ch)
		}
		if prevStar && ch == '/' {
			break
		}
		prevStar = ch == '*'
	}
}

// consumeCharLiteral consumes a '...' literal, first being the opening quote,
// already written. An unterminated literal emits no placeholder, as in Java,
// but its content is still reported as a parameter.
func (s *sqlNormalizer) consumeCharLiteral() {
	s.isChanged = true
	if s.param.Len() > 0 {
		s.param.WriteByte(',')
	}

	for s.pos < len(s.sql) {
		ch := s.sql[s.pos]
		s.pos++

		if ch == ',' {
			// The server splits param on ',', so a comma inside a literal is
			// doubled (Java's ParameterBuilder.appendSeparatorCheck).
			s.param.WriteByte(ch)
		} else if ch == '\'' {
			if s.lookahead('\'') {
				s.param.WriteByte('\'')
				s.pos++
			} else {
				s.output.WriteString(strconv.Itoa(s.paramIndex))
				s.paramIndex++
				s.output.WriteByte('$')
				s.output.WriteByte('\'')
				break
			}
		}

		s.param.WriteByte(ch)
	}
}

// consumeNumberLiteral consumes a numeric literal, first being its leading
// digit, already read but not yet written.
func (s *sqlNormalizer) consumeNumberLiteral(first byte) {
	s.isChanged = true
	if s.param.Len() > 0 {
		s.param.WriteByte(',')
	}
	s.output.WriteString(strconv.Itoa(s.paramIndex))
	s.paramIndex++
	s.output.WriteByte('#')
	s.param.WriteByte(first)

	for s.pos < len(s.sql) {
		ch := s.sql[s.pos]
		if !isDigit(ch) && ch != '.' && ch != 'E' && ch != 'e' {
			break
		}
		s.param.WriteByte(ch)
		s.pos++
	}
}

// lookahead reports whether the next byte is expected, without consuming it.
func (s *sqlNormalizer) lookahead(expected byte) bool {
	return s.pos < len(s.sql) && s.sql[s.pos] == expected
}

// lookaheadDigit reports whether the next byte is a digit, without consuming
// it. End of input is not a digit, as in Java, whose lookAhead1 returns
// NEXT_TOKEN_NOT_EXIST there.
func (s *sqlNormalizer) lookaheadDigit() bool {
	return s.pos < len(s.sql) && isDigit(s.sql[s.pos])
}

func isLetter(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

func isDigit(ch byte) bool {
	return ch >= '0' && ch <= '9'
}
