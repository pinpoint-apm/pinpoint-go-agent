package pinpoint

import (
	"strconv"
	"strings"
)

// sqlNormalizer replaces the literals of a SQL statement with placeholders and
// collects the removed values into a separate parameter string.
//
// It walks the statement one byte at a time rather than by rune: every decision
// is on an ASCII character, and decoding would corrupt a statement the collector
// is meant to receive verbatim, since an invalid byte decodes to U+FFFD and is
// written back as three different bytes.
//
// The output is materialized lazily. Until the first byte that differs from the
// input the output is by definition sql[:pos], so nothing is written: emit drops
// the byte and materialize copies the prefix in one Grow-sized write when a
// change arrives. A statement with no literals therefore allocates nothing and
// is returned as it came in.
type sqlNormalizer struct {
	sql          string
	pos          int
	output       strings.Builder
	materialized bool
	param        strings.Builder
	paramIndex   int
	isChanged    bool
	// removeComments drops comments from the output instead of copying them.
	removeComments bool
}

// maxSqlNormalizeLength is the hard memory cap on SQL normalization, in bytes.
// It is not the metadata cap: maxSqlSize (64KB) bounds only the text cacheSql
// and cacheSqlUid publish, while the normalized output, the cache keys and the
// queued metadata are bounded by this one.
//
// A statement past the cap is dropped whole rather than cut and normalized: a
// cut landing inside a literal loses that literal's placeholder and yields a
// SQL id / UID that no longer identifies the statement.
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

// run returns the normalized statement and the extracted parameters, the latter
// joined with ','. Neither is abbreviated: the id and UID are computed from the
// whole normalized text, and the server splits param on ',' to refill the
// <idx>#/<idx>$ placeholders, so a cut param leaves placeholders exposed.
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
			// The marker is decided before ch is written: under removeComments
			// it must not reach the output. Either way a comment is not a
			// number token boundary.
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
				// An empty literal '' is copied through: no parameter, no change.
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
			// A '$' before a digit is a bind marker ($1, $2, ...), not a number
			// literal; a '$' before anything else leaves the flag alone.
			if s.lookaheadDigit() {
				numberTokenStartEnable = false
			}
			s.emit(ch)
		} else if isLetter(ch) || ch == '.' || ch == '_' || ch == '@' || ch == ':' {
			numberTokenStartEnable = false
			s.emit(ch)
		} else {
			// Whitespace, operators, separators and every byte of a non-ASCII
			// character land here, so "테이블1" yields "테이블0#".
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
// newline belongs to the comment, so removal leaves nothing in its place.
func (s *sqlNormalizer) consumeSingleLineComment(lead byte) {
	if s.removeComments {
		// A dropped comment is a change even though it records no parameter.
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

	// The opening '*' cannot also close the comment, so "/*/" runs to the end
	// of the statement.
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

// consumeCharLiteral consumes a '...' literal whose opening quote the caller has
// already emitted, replacing the content with <idx>$ and recording it as a
// parameter.
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
			// doubled to escape it: written here and again below.
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

// lookaheadDigit reports whether the next byte is a digit, without consuming it.
func (s *sqlNormalizer) lookaheadDigit() bool {
	return s.pos < len(s.sql) && isDigit(s.sql[s.pos])
}

func isLetter(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

func isDigit(ch byte) bool {
	return ch >= '0' && ch <= '9'
}
