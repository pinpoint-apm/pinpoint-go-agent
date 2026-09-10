package pinpoint

import (
	"strconv"
	"strings"
)

// sqlNormalizer walks the statement one byte at a time, indexing the string
//
// Bytes, not runes: every decision the parser makes is on an ASCII character,
// multibyte character all fall through to the same branch a whole rune would.
// Decoding buys nothing and costs fidelity - an invalid UTF-8 byte would decode
// to U+FFFD and be written back as three different bytes, rewriting a statement
// the collector is supposed to receive verbatim.
//
// The output is materialized lazily. Until the first byte that differs from the
// input (a removed comment, a literal turned into a placeholder), the output is
// by definition sql[:pos], so nothing is written: emit drops the byte and
// materialize copies the prefix in one Grow-sized write when a change arrives.
// A statement with no literals - the common shape of placeholder-based Go SQL -
// therefore allocates nothing and is returned as it came in; before, its whole
// text was copied byte by byte through a growing builder and then discarded.
type sqlNormalizer struct {
	sql          string
	pos          int
	output       strings.Builder
	materialized bool
	param        strings.Builder
	paramIndex   int
	isChanged    bool
	// removeComments drops comments from the output instead of copying them,
	removeComments bool
}

// maxSqlNormalizeLength is the hard memory cap on SQL normalization, in bytes:
// a statement longer than this is not normalized at all (see sqlNormalizable).
// It is not the metadata cap - maxSqlSize (64KB) bounds only the text cacheSql
// and cacheSqlUid publish, and a statement between the two is still normalized
// of sqlCache / sqlUidCache / rawSqlCache and the key field of every queued
// sqlMeta / sqlUidMeta, had no bound at all, so one huge generated statement
// broke the memory guarantee of every one of those.
//
// rest, which loses the placeholder when the cut lands inside a literal and so
// yields a SQL id / UID no other agent computes (gap N1 of the cross-agent
// review). Dropping the statement instead never diverges - an over-cap
// same value and the same drop policy (see doc/java_parity.md).
const maxSqlNormalizeLength = 1 << 20

// sqlNormalizable reports whether sql is within maxSqlNormalizeLength. It
// measures the raw input, so no cut is involved and a multibyte character
// straddling the cap simply puts the statement past it.
func sqlNormalizable(sql string) bool {
	return len(sql) <= maxSqlNormalizeLength
}

func newSqlNormalizer(sql string, removeComments bool) *sqlNormalizer {
	return &sqlNormalizer{sql: sql, removeComments: removeComments}
}

// does. The 64KB cap belongs to the metadata text alone (see cacheSql and
// computes from the full normalized SQL, and param must stay whole because the
// server splits it on ',' to refill the <idx>#/<idx>$ placeholders - a cut
// param leaves placeholders exposed.
//
// A statement past maxSqlNormalizeLength is not walked at all and comes back
// empty. SetSQL checks sqlNormalizable first and drops such a statement before
// it gets here; this guard keeps the cap in force for any other caller.
func (s *sqlNormalizer) run() (string, string) {
	if !sqlNormalizable(s.sql) {
		return "", ""
	}

	numberTokenStartEnable := true

	for s.pos < len(s.sql) {
		ch := s.sql[s.pos]
		s.pos++

		if ch == '/' {
			// The comment markers are decided before ch is written: under
			// removeComments the marker itself must not reach the output.
			// comment is not a number token boundary either way.
			if s.lookahead('/') {
				s.consumeSingleLineComment(ch)
			} else if s.lookahead('*') {
				s.consumeMultiLineComment(ch)
			} else {
				s.emit(ch)
				numberTokenStartEnable = true
			}
		} else if ch == '-' {
			if s.lookahead('-') {
				s.consumeSingleLineComment(ch)
			} else {
				s.emit(ch)
				numberTokenStartEnable = true
			}
		} else if ch == '\'' {
			s.emit(ch)
			if s.lookahead('\'') {
				// records a parameter for it nor marks the statement changed.
				s.emit('\'')
				s.pos++
			} else {
				s.consumeCharLiteral()
			}
		} else if isDigit(ch) {
			if numberTokenStartEnable {
				s.consumeNumberLiteral(ch)
			} else {
				s.emit(ch)
			}
		} else if ch == '$' {
			// ($1, $2, ...); a '$' followed by anything else leaves it as it
			// extracts, e.g. "$'x'1" - neither a string literal nor a comment
			// touches the flag on the way to the digit.
			if s.lookaheadDigit() {
				numberTokenStartEnable = false
			}
			s.emit(ch)
		} else if isLetter(ch) || ch == '.' || ch == '_' || ch == '@' || ch == ':' {
			numberTokenStartEnable = false
			s.emit(ch)
		} else {
			// Whitespace, operators and separators land here, and so does every
			// same of a non-ASCII char, so "테이블1" yields "테이블0#" on both.
			numberTokenStartEnable = true
			s.emit(ch)
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

// emit writes ch to the output once it is materialized. Before that the byte
// is already accounted for: it sits in sql ahead of pos, and materialize copies
// it with the rest of the unchanged prefix.
func (s *sqlNormalizer) emit(ch byte) {
	if s.materialized {
		s.output.WriteByte(ch)
	}
}

// materialize starts the output at the first change, copying the unchanged
// prefix sql[:upto] in one write. upto is where the changed bytes begin, which
// is pos minus whatever the caller read but has not emitted (the lead byte of
// a comment marker or a number literal); every caller also marks isChanged, so
// run returns the output exactly when it was materialized.
func (s *sqlNormalizer) materialize(upto int) {
	if s.materialized {
		return
	}
	s.materialized = true
	s.output.Grow(len(s.sql))
	s.output.WriteString(s.sql[:upto])
}

// writeParamIndex appends the placeholder number without the string
// strconv.Itoa would allocate past its small-integer cache.
func (s *sqlNormalizer) writeParamIndex() {
	var buf [20]byte
	s.output.Write(strconv.AppendInt(buf[:0], int64(s.paramIndex), 10))
	s.paramIndex++
}

// consumeSingleLineComment consumes a // or -- comment. lead is the first
// character of the marker, already read but not yet written. The terminating
// with "\n" as the end token - so removal leaves nothing at all in its place.
func (s *sqlNormalizer) consumeSingleLineComment(lead byte) {
	if s.removeComments {
		// A statement whose only change is a dropped comment still has to
		s.isChanged = true
		s.materialize(s.pos - 1) // lead is read, not written
	} else {
		s.emit(lead)
	}

	for s.pos < len(s.sql) {
		ch := s.sql[s.pos]
		s.pos++
		if !s.removeComments {
			s.emit(ch)
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
		s.materialize(s.pos - 1) // lead is read, not written
	} else {
		s.emit(lead)
		s.emit('*')
	}
	s.pos++ /* consume '*' */

	// "*/" from behind it, so "/*/" runs to the end of the statement.
	prevStar := false
	for s.pos < len(s.sql) {
		ch := s.sql[s.pos]
		s.pos++
		if !s.removeComments {
			s.emit(ch)
		}
		if prevStar && ch == '/' {
			break
		}
		prevStar = ch == '*'
	}
}

// consumeCharLiteral consumes a '...' literal, first being the opening quote,
// but its content is still reported as a parameter.
func (s *sqlNormalizer) consumeCharLiteral() {
	s.isChanged = true
	s.materialize(s.pos) // the opening quote is already accounted for
	if s.param.Len() > 0 {
		s.param.WriteByte(',')
	}

	for s.pos < len(s.sql) {
		ch := s.sql[s.pos]
		s.pos++

		if ch == ',' {
			// The server splits param on ',', so a comma inside a literal is
			s.param.WriteByte(ch)
		} else if ch == '\'' {
			if s.lookahead('\'') {
				s.param.WriteByte('\'')
				s.pos++
			} else {
				s.writeParamIndex()
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
	s.materialize(s.pos - 1) // first is read, not written
	if s.param.Len() > 0 {
		s.param.WriteByte(',')
	}
	s.writeParamIndex()
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
