package pinpoint

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_sqlNormalizer_DefaultSqlNormalizerCases(t *testing.T) {
	tests := []struct {
		name       string
		sql        string
		normalized string
		params     string
	}{
		{
			name:       "complex literals",
			sql:        "select * from table a = 1 and b=50 and c=? and d='11'",
			normalized: "select * from table a = 0# and b=1# and c=? and d='2$'",
			params:     "1,50,11",
		},
		{
			name:       "negative literals",
			sql:        "select * from table a = -1 and b=-50 and c=? and d='-11'",
			normalized: "select * from table a = -0# and b=-1# and c=? and d='2$'",
			params:     "1,50,-11",
		},
		{
			name:       "positive literals",
			sql:        "select * from table a = +1 and b=+50 and c=? and d='+11'",
			normalized: "select * from table a = +0# and b=+1# and c=? and d='2$'",
			params:     "1,50,+11",
		},
		{
			name:       "comments around literals",
			sql:        "select * from table a = 1/*test*/ and b=50/*test*/ and c=? and d='11'",
			normalized: "select * from table a = 0#/*test*/ and b=1#/*test*/ and c=? and d='2$'",
			params:     "1,50,11",
		},
		{
			name:       "plain identifiers",
			sql:        "select ZIPCODE,CITY from ZIPCODE",
			normalized: "select ZIPCODE,CITY from ZIPCODE",
		},
		{
			name:       "qualified identifiers",
			sql:        "select a.ZIPCODE,a.CITY from ZIPCODE as a",
			normalized: "select a.ZIPCODE,a.CITY from ZIPCODE as a",
		},
		{
			name:       "projection number",
			sql:        "select ZIPCODE,123 from ZIPCODE",
			normalized: "select ZIPCODE,0# from ZIPCODE",
			params:     "123",
		},
		{
			name:       "subtraction expression",
			sql:        "SELECT * from table a=123 and b='abc' and c=1-3",
			normalized: "SELECT * from table a=0# and b='1$' and c=2#-3#",
			params:     "123,abc,1,3",
		},
		{
			name:       "function arguments",
			sql:        "SYSTEM_RANGE(1, 10)",
			normalized: "SYSTEM_RANGE(0#, 1#)",
			params:     "1,10",
		},
		{
			name:       "identifier with dot",
			sql:        "test.abc",
			normalized: "test.abc",
		},
		{
			name:       "identifier with digits",
			sql:        "test.abc123",
			normalized: "test.abc123",
		},
		{
			name:       "dot before digits",
			sql:        "test.123",
			normalized: "test.123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertNormalize(t, tt.sql, tt.normalized, tt.params)
		})
	}
}

// 64KB cap to SqlCacheService, which abbreviates only the text it publishes. A
// normalizer that stopped at the cap would drop every bind value behind it and
// hand the UID a truncated string.
func Test_sqlNormalizer_NormalizesPastTheMetadataCap(t *testing.T) {
	t.Run("unchanged sql", func(t *testing.T) {
		raw := strings.Repeat("x", maxSqlSize+100)
		nsql, param := newSqlNormalizer(raw, false).run()
		assert.True(t, raw == nsql, "the whole sql must be returned untruncated")
		assert.Empty(t, param)
	})

	t.Run("normalized sql", func(t *testing.T) {
		prefix := strings.Repeat("x", maxSqlSize/2) + " = 2 " + strings.Repeat("x", maxSqlSize)
		nsql, param := newSqlNormalizer(prefix+" = 1", false).run()
		want := strings.Replace(prefix, "= 2", "= 0#", 1) + " = 1#"
		assert.True(t, want == nsql, "sql past the cap must still be normalized")
		assert.Equal(t, "2,1", param, "a bind value past the cap must still be reported")
	})

	t.Run("literal parameter", func(t *testing.T) {
		literal := strings.Repeat("x", maxSqlSize+100)
		nsql, param := newSqlNormalizer("select '"+literal+"'", false).run()
		assert.Equal(t, "select '0$'", nsql)
		assert.True(t, literal == param, "the server refills placeholders from param, so it must stay whole")
	})
}

// A statement past the metadata cap is still keyed and hashed on its whole
// normalized text; only the published copy is abbreviated and marked with the
// original length.
func Test_sqlNormalizer_JavaEquivalence_SqlPastTheCap(t *testing.T) {
	const size = 70000
	head := "select * from t where a = 1 and b = '"
	literal := strings.Repeat("x", size-len(head)-len("'"))
	sql := head + literal + "'"
	if len(sql) != size {
		t.Fatalf("test sql is %d bytes, want %d", len(sql), size)
	}

	nsql, param := newSqlNormalizer(sql, false).run()
	assert.Equal(t, "select * from t where a = 0# and b = '1$'", nsql)
	assert.True(t, "1,"+literal == param, "every bind value must survive normalization")

	// Nothing to normalize away, so the metadata is what gets abbreviated.
	plain := strings.Repeat("x", size)
	nsql, param = newSqlNormalizer(plain, false).run()
	assert.Len(t, nsql, size)
	assert.Empty(t, param)

	a := newTestAgent(defaultConfig())
	uid := a.cacheSqlUid(nsql)
	assert.Equal(t, sqlUid(nsql), uid, "the uid must hash the untruncated normalized sql")
	assert.NotEqual(t, sqlUid(abbreviateString(nsql, maxSqlSize)), uid)

	md := (<-a.metaChan).(sqlUidMeta)
	assert.True(t, plain[:maxSqlSize]+"...("+strconv.Itoa(size)+")" == md.sql,
		"published sql must be abbreviated with the original length")
	assert.Equal(t, uid, md.uid)
}

func Test_sqlNormalizer_NumberState(t *testing.T) {
	tests := []struct {
		sql        string
		normalized string
		params     string
	}{
		{"123", "0#", "123"},
		{"-123", "-0#", "123"},
		{"+123", "+0#", "123"},
		{"1.23", "0#", "1.23"},
		{"1.23.34", "0#", "1.23.34"},
		{"123 456", "0# 1#", "123,456"},
		{"1.23 4.56", "0# 1#", "1.23,4.56"},
		{"1.23-4.56", "0#-1#", "1.23,4.56"},
		{"1<2", "0#<1#", "1,2"},
		{"1< 2", "0#< 1#", "1,2"},
		{"(1< 2)", "(0#< 1#)", "1,2"},
		{"-- 1.23", "-- 1.23", ""},
		{"- -1.23", "- -0#", "1.23"},
		{"--1.23", "--1.23", ""},
		{"/* 1.23 */", "/* 1.23 */", ""},
		{"/*1.23*/", "/*1.23*/", ""},
		{"/* 1.23 \n*/", "/* 1.23 \n*/", ""},
		{"test123", "test123", ""},
		{"test_123", "test_123", ""},
		{"test_ 123", "test_ 0#", "123"},
		{"123tst", "0#tst", "123"},
		{"1.23e", "0#", "1.23e"},
		{"1.23E", "0#", "1.23E"},
		{"1.4e-10", "0#-1#", "1.4e,10"},
		{"123 ", "0# ", "123"},
	}

	for _, tt := range tests {
		t.Run(tt.sql, func(t *testing.T) {
			assertNormalize(t, tt.sql, tt.normalized, tt.params)
		})
	}
}

func Test_sqlNormalizer_CommentState(t *testing.T) {
	tests := []struct {
		sql        string
		normalized string
		params     string
	}{
		{"--", "--", ""},
		{"//", "//", ""},
		{"--123", "--123", ""},
		{"//123", "//123", ""},
		{"--test", "--test", ""},
		{"//test", "//test", ""},
		{"--test\ntest", "--test\ntest", ""},
		{"--test\t\n", "--test\t\n", ""},
		{"--test\n123 test", "--test\n0# test", "123"},
		{"/**/", "/**/", ""},
		{"/* */", "/* */", ""},
		{"/* */abc", "/* */abc", ""},
		{"/* * */", "/* * */", ""},
		{"/* abc", "/* abc", ""},
		{"select * from table", "select * from table", ""},
		{"/*", "/*", ""},
		{"/*  ", "/*  ", ""},
		{"/*  \n  ", "/*  \n  ", ""},
		{"/* 'test' */", "/* 'test' */", ""},
		{"/* 'test'' */", "/* 'test'' */", ""},
		{"/* '' */", "/* '' */", ""},
		{"/*  */ 123 */", "/*  */ 0# */", "123"},
		{"' /* */'", "'0$'", " /* */"},
	}

	for _, tt := range tests {
		t.Run(displayName(tt.sql), func(t *testing.T) {
			assertNormalize(t, tt.sql, tt.normalized, tt.params)
		})
	}
}

func Test_sqlNormalizer_SymbolState(t *testing.T) {
	tests := []struct {
		sql        string
		normalized string
		params     string
	}{
		{"''", "''", ""},
		{"'abc'", "'0$'", "abc"},
		{"'a''bc'", "'0$'", "a''bc"},
		{"'a' 'bc'", "'0$' '1$'", "a,bc"},
		{"'a''bc' 'a''bc'", "'0$' '1$'", "a''bc,a''bc"},
		{"select * from table where a='a'", "select * from table where a='0$'", "a"},
	}

	for _, tt := range tests {
		t.Run(tt.sql, func(t *testing.T) {
			assertNormalize(t, tt.sql, tt.normalized, tt.params)
		})
	}
}

func Test_sqlNormalizer_SeparatorAndEmptyChar(t *testing.T) {
	tests := []struct {
		name       string
		sql        string
		normalized string
		params     string
	}{
		{
			name:       "numbers separated by comma",
			sql:        "1234 456,7",
			normalized: "0# 1#,2#",
			params:     "1234,456,7",
		},
		{
			name:       "string containing comma",
			sql:        "'1234 456,7'",
			normalized: "'0$'",
			params:     "1234 456,,7",
		},
		{
			name:       "string containing escaped quote and comma",
			sql:        "'1234''456,7'",
			normalized: "'0$'",
			params:     "1234''456,,7",
		},
		{
			name:       "adjacent string literals",
			sql:        "'1234' '456,7'",
			normalized: "'0$' '1$'",
			params:     "1234,456,,7",
		},
		{
			name:       "empty string literal is preserved",
			sql:        "select u.user_no as userNo,ifnull(s.equipment,'') as equipment,ifnull(s.gender, '0') as gender from user u left join supply s on u.user_no = s.user_no where u.user_no = ?",
			normalized: "select u.user_no as userNo,ifnull(s.equipment,'') as equipment,ifnull(s.gender, '0$') as gender from user u left join supply s on u.user_no = s.user_no where u.user_no = ?",
			params:     "0",
		},
		{
			name:       "mixed empty and non-empty strings",
			sql:        "select u.user_no as userNo,ifnull(s.equipment,'test_str') as equipment,ifnull(s.gender, '0') as gender from user u left join supply s on u.user_no = s.user_no where u.user_no != ''",
			normalized: "select u.user_no as userNo,ifnull(s.equipment,'0$') as equipment,ifnull(s.gender, '1$') as gender from user u left join supply s on u.user_no = s.user_no where u.user_no != ''",
			params:     "test_str,0",
		},
		{
			name:       "concat with comma in string",
			sql:        "select concat ('hello,', u.name, ?)as hello, u.user_no as userNo from user u where 1 = 1 and u.user_no = '10010'",
			normalized: "select concat ('0$', u.name, ?)as hello, u.user_no as userNo from user u where 1# = 2# and u.user_no = '3$'",
			params:     "hello,,,1,1,10010",
		},
		{
			name:       "concat with space string",
			sql:        "select concat ('hello,', u.name, ' ')as hello, u.user_no as userNo from user u where 1 = 1 and u.user_no != ''",
			normalized: "select concat ('0$', u.name, '1$')as hello, u.user_no as userNo from user u where 2# = 3# and u.user_no != ''",
			params:     "hello,,, ,1,1",
		},
		{
			name:       "concat with age comparison",
			sql:        "select concat ('hello,', u.name, 'zhangsan')as hello, u.user_no as userNo from user u where 1 = 1 and u.user_no != '' and u.age > 20",
			normalized: "select concat ('0$', u.name, '1$')as hello, u.user_no as userNo from user u where 2# = 3# and u.user_no != '' and u.age > 4#",
			params:     "hello,,,zhangsan,1,1,20",
		},
		{
			name:       "nested select in concat",
			sql:        "select concat ('pinpoint,', u.name, (select s.user_no from user s where s.user_no = '8888'))as hello, u.user_no as userNo from user u where 1 = 1 and u.habit != '2768' and u.age > 20",
			normalized: "select concat ('0$', u.name, (select s.user_no from user s where s.user_no = '1$'))as hello, u.user_no as userNo from user u where 2# = 3# and u.habit != '4$' and u.age > 5#",
			params:     "pinpoint,,,8888,1,1,2768,20",
		},
		{
			name:       "ifnull query",
			sql:        "SELECT n.order_logistics_id, MAX(IF(IFNULL(n.id, '') != '', '2', '0')) AS is_ts FROM t_e_shipping_note n WHERE IFNULL(n.delflag, '') <> '1' AND IFNULL(n.document_require, '0') = '2' GROUP BY n.order_logistics_id",
			normalized: "SELECT n.order_logistics_id, MAX(IF(IFNULL(n.id, '') != '', '0$', '1$')) AS is_ts FROM t_e_shipping_note n WHERE IFNULL(n.delflag, '') <> '2$' AND IFNULL(n.document_require, '3$') = '4$' GROUP BY n.order_logistics_id",
			params:     "2,0,1,0,2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertNormalize(t, tt.sql, tt.normalized, tt.params)
		})
	}
}

func Test_sqlNormalizer_SequentialIndexes(t *testing.T) {
	tests := []struct {
		sql        string
		normalized string
		params     string
	}{
		{"123 345", "0# 1#", "123,345"},
		{"123 345 'test'", "0# 1# '2$'", "123,345,test"},
		{"1 2 3 4 5 6 7 8 9 10 11", "0# 1# 2# 3# 4# 5# 6# 7# 8# 9# 10#", "1,2,3,4,5,6,7,8,9,10,11"},
	}

	for _, tt := range tests {
		t.Run(displayName(tt.sql), func(t *testing.T) {
			assertNormalize(t, tt.sql, tt.normalized, tt.params)
		})
	}
}

func Test_sqlNormalizer_PostgresPositionalParameter(t *testing.T) {
	tests := []struct {
		sql        string
		normalized string
		params     string
	}{
		{
			sql:        "SELECT * FROM member WHERE user = 'Kim' AND id = $1 AND no = 10",
			normalized: "SELECT * FROM member WHERE user = '0$' AND id = $1 AND no = 1#",
			params:     "Kim,10",
		},
		{
			sql:        "SELECT * FROM member WHERE id = $122309 AND no = 122309",
			normalized: "SELECT * FROM member WHERE id = $122309 AND no = 0#",
			params:     "122309",
		},
		{
			sql:        "$value, 123",
			normalized: "$value, 0#",
			params:     "123",
		},
		{
			sql:        "'$123', 123",
			normalized: "'0$', 1#",
			params:     "$123,123",
		},
		{
			sql:        "$; 123",
			normalized: "$; 0#",
			params:     "123",
		},
		{
			sql:        "$(123); 123",
			normalized: "$(0#); 1#",
			params:     "123,123",
		},
		{
			sql:        "'$''123'",
			normalized: "'0$'",
			params:     "$''123",
		},
		// Neither a comment nor a string literal is a number token boundary, so
		// the flag '$' left alone still reaches the digit and the literal is
		// still extracted. Forcing the flag off at every '$' swallowed these.
		{
			sql:        "SELECT $/*c*/1 FROM t",
			normalized: "SELECT $/*c*/0# FROM t",
			params:     "1",
		},
		{
			sql:        "= $//c\n1",
			normalized: "= $//c\n0#",
			params:     "1",
		},
		{
			sql:        "= $'x'1",
			normalized: "= $'0$'1#",
			params:     "x,1",
		},
	}

	for _, tt := range tests {
		t.Run(displayName(tt.sql), func(t *testing.T) {
			assertNormalize(t, tt.sql, tt.normalized, tt.params)
		})
	}
}

func assertNormalize(t *testing.T, sql, normalized, params string) {
	t.Helper()

	actualNormalized, actualParams := newSqlNormalizer(sql, false).run()
	assert.Equal(t, normalized, actualNormalized, "normalized sql")
	assert.Equal(t, params, actualParams, "params")
}

func displayName(sql string) string {
	return strings.ReplaceAll(sql, "\n", "\\n")
}

// place, leaves the number-token-start flag alone, and swallows the newline
// that ends a line comment.
func TestNormalizeRemoveComments(t *testing.T) {
	tests := []struct {
		sql        string
		normalized string
		params     string
	}{
		{"SELECT /*+ INDEX(t idx) */ * FROM t WHERE id = 10", "SELECT  * FROM t WHERE id = 0#", "10"},
		{"SELECT * FROM t -- c\nWHERE id = 1", "SELECT * FROM t WHERE id = 0#", "1"},
		// The comment is not a number token boundary, so 1 stays a literal.
		{"SELECT/*c*/1", "SELECT1", ""},
		// A line comment at eof needs no terminating newline.
		{"SELECT 1 -- trailing", "SELECT 0# ", "1"},
		// Nothing but a comment: the normalized text, not the original.
		{"/* only */", "", ""},
		{"-- only", "", ""},
		// A '/' or '-' that starts no comment is still copied.
		{"SELECT 6/2, 1-1 FROM t", "SELECT 0#/1#, 2#-3# FROM t", "6,2,1,1"},
	}

	for _, tt := range tests {
		t.Run(displayName(tt.sql), func(t *testing.T) {
			nsql, params := newSqlNormalizer(tt.sql, true).run()
			assert.Equal(t, tt.normalized, nsql, "normalized sql")
			assert.Equal(t, tt.params, params, "params")
		})
	}
}

// ParserContext: only a positional placeholder ($1, $2, ...) turns the
// number-token-start flag off. A '$' before anything else leaves the flag
// alone, and neither a string literal nor a comment touches it on the way to
// the next digit, so the digit is still extracted.
func TestNormalizeDollarNumberTokenStart(t *testing.T) {
	tests := []struct {
		sql        string
		normalized string
		params     string
	}{
		// $ before a digit: the placeholder is kept whole.
		{"$1", "$1", ""},
		{"where id = $122309 and no = 122309", "where id = $122309 and no = 0#", "122309"},
		// $ before anything else does not turn the flag off, and a string
		// literal on the way to the digit does not turn it back on either.
		{"$'x'1", "$'0$'1#", "x,1"},
		{"$$'x'1", "$$'0$'1#", "x,1"},
		// A '$' that follows an identifier character keeps the flag off, so a
		// digit after it stays part of the identifier (Oracle's V$SESSION1).
		{"V$SESSION1", "V$SESSION1", ""},
		{"a$1", "a$1", ""},
	}

	for _, tt := range tests {
		t.Run(displayName(tt.sql), func(t *testing.T) {
			assertNormalize(t, tt.sql, tt.normalized, tt.params)
		})
	}

	// A comment does not touch the flag either, in both comment modes.
	t.Run("comment", func(t *testing.T) {
		nsql, params := newSqlNormalizer("$/*c*/1", true).run()
		assert.Equal(t, "$0#", nsql)
		assert.Equal(t, "1", params)

		nsql, params = newSqlNormalizer("$/*c*/1", false).run()
		assert.Equal(t, "$/*c*/0#", nsql)
		assert.Equal(t, "1", params)
	})
}

// TestNormalizeByteFidelity pins the parser to bytes. The statement reaches the
// collector as the application wrote it, so a byte the parser does not act on
// has to come back out unchanged - including a byte that is not valid UTF-8 and
// a NUL, neither of which ends the statement.
func TestNormalizeByteFidelity(t *testing.T) {
	tests := []struct {
		name       string
		sql        string
		normalized string
		params     string
	}{
		{
			name:       "invalid utf-8 passes through",
			sql:        "select \xffcol from t where a = 1",
			normalized: "select \xffcol from t where a = 0#",
			params:     "1",
		},
		{
			name:       "invalid utf-8 inside a literal",
			sql:        "select * from t where a = '\xff\xfe'",
			normalized: "select * from t where a = '0$'",
			params:     "\xff\xfe",
		},
		{
			name:       "nul does not end the statement",
			sql:        "select 'a' \x00 and b = 1",
			normalized: "select '0$' \x00 and b = 1#",
			params:     "a,1",
		},
		{
			name:       "nul does not start a number token either",
			sql:        "select a\x001 from t",
			normalized: "select a\x000# from t",
			params:     "1",
		},
		{
			name:       "multibyte utf-8 is not a letter, as in java",
			sql:        "select 테이블1 from t",
			normalized: "select 테이블0# from t",
			params:     "1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertNormalize(t, tt.sql, tt.normalized, tt.params)
		})
	}
}

// maxSqlNormalizeLength is a hard memory cap on the raw input, distinct from
// the 64KB metadata cap above: a statement at the cap is normalized whole, one
// byte past it is not normalized at all. There is no cut - and so no partial
// character to worry about - a multibyte character straddling the cap puts
// the whole statement past it.
func Test_sqlNormalizer_DropsInputPastTheNormalizationCap(t *testing.T) {
	assert.Equal(t, 1<<20, maxSqlNormalizeLength, "matches the C++ agent's kMaxNormalizedSqlLength")
	assert.Greater(t, maxSqlNormalizeLength, maxSqlSize, "the memory cap sits above the metadata cap")

	head := "select 1 from t where a = '"
	tail := "'"
	body := func(size int) string {
		return head + strings.Repeat("x", size-len(head)-len(tail)) + tail
	}
	const want = "select 0# from t where a = '1$'"

	t.Run("one byte before the cap", func(t *testing.T) {
		raw := body(maxSqlNormalizeLength - 1)
		require.Len(t, raw, maxSqlNormalizeLength-1)
		assert.True(t, sqlNormalizable(raw))
		nsql, param := newSqlNormalizer(raw, false).run()
		assert.Equal(t, want, nsql)
		assert.Equal(t, "1,"+raw[len(head):len(raw)-len(tail)], param)
	})

	t.Run("exactly the cap", func(t *testing.T) {
		raw := body(maxSqlNormalizeLength)
		require.Len(t, raw, maxSqlNormalizeLength)
		assert.True(t, sqlNormalizable(raw))
		nsql, param := newSqlNormalizer(raw, false).run()
		assert.Equal(t, want, nsql)
		assert.Equal(t, "1,"+raw[len(head):len(raw)-len(tail)], param)
	})

	t.Run("one byte past the cap", func(t *testing.T) {
		raw := body(maxSqlNormalizeLength + 1)
		require.Len(t, raw, maxSqlNormalizeLength+1)
		assert.False(t, sqlNormalizable(raw))
		nsql, param := newSqlNormalizer(raw, false).run()
		assert.Empty(t, nsql, "a statement past the cap is not normalized")
		assert.Empty(t, param)
	})

	t.Run("multibyte character straddling the cap", func(t *testing.T) {
		// The literal ends with a three-byte character whose first byte is the
		// last byte within the cap: cutting there would leave invalid UTF-8,
		// dropping leaves nothing to cut.
		const ch = "한" // 3 bytes
		raw := head + strings.Repeat("x", maxSqlNormalizeLength-len(head)-1) + ch + tail
		require.Greater(t, len(raw), maxSqlNormalizeLength)
		require.True(t, utf8.RuneStart(raw[maxSqlNormalizeLength-1]))
		require.False(t, utf8.RuneStart(raw[maxSqlNormalizeLength]))
		assert.False(t, sqlNormalizable(raw))
		nsql, param := newSqlNormalizer(raw, false).run()
		assert.Empty(t, nsql)
		assert.Empty(t, param)
	})

	t.Run("multibyte character ending exactly at the cap", func(t *testing.T) {
		const ch = "한" // 3 bytes
		raw := head + strings.Repeat("x", maxSqlNormalizeLength-len(head)-len(tail)-len(ch)) + ch + tail
		require.Len(t, raw, maxSqlNormalizeLength)
		assert.True(t, sqlNormalizable(raw))
		nsql, _ := newSqlNormalizer(raw, false).run()
		assert.Equal(t, want, nsql)
		assert.True(t, utf8.ValidString(nsql))
	})
}

// Output is materialized only at the first change, so an unchanged statement
// does not allocate and a changed statement pays one Grow-sized copy.
func Test_sqlNormalizer_LazyOutput(t *testing.T) {
	unchanged := "SELECT a.id, a.name FROM accounts a WHERE a.id = ? AND a.status = ? ORDER BY a.created_at DESC LIMIT ?"
	allocs := testing.AllocsPerRun(100, func() {
		sql, param := newSqlNormalizer(unchanged, false).run()
		if sql != unchanged || param != "" {
			t.Fatalf("unchanged statement rewritten: %q %q", sql, param)
		}
	})
	assert.Equal(t, 0.0, allocs, "a statement with no literals must not allocate")

	// The unchanged prefix is copied exactly once, at the first change, and
	// every byte after it goes through the materialized output.
	sql, param := newSqlNormalizer("SELECT /* c */ x FROM t WHERE a = 'v' AND b = 12 -- tail\n AND c = ?", true).run()
	assert.Equal(t, "SELECT  x FROM t WHERE a = '0$' AND b = 1#  AND c = ?", sql)
	assert.Equal(t, "v,12", param)

	// A change that arrives after a verbatim comment keeps the comment.
	sql, param = newSqlNormalizer("/* c */ SELECT 1", false).run()
	assert.Equal(t, "/* c */ SELECT 0#", sql)
	assert.Equal(t, "1", param)
}

// ===========================================================================
// Locked invariants - behaviour pinned against the Java and C++ agents. The
// cross-agent rationale and references live in doc/development.md.
// ===========================================================================

// sqlNormalizeCase is one golden case of the normalized SQL and parameter wire
// format.
type sqlNormalizeCase struct {
	name       string
	sql        string
	normalized string
	params     string
	// paramsUnsplittable marks a case whose param string cannot be split back
	// into one entry per placeholder: an unterminated literal writes its content
	// into param without emitting a placeholder, so the counts do not line up.
	// Only the placeholder-counting test below skips such a case, never the
	// byte-for-byte expectation.
	paramsUnsplittable bool
}

func sqlNormalizeCases() []sqlNormalizeCase {
	return []sqlNormalizeCase{
		{
			name:       "number, escaped quote and double-quoted identifier",
			sql:        `select * from t where a = 1.5e3 and b = 'it''s' and c = "col1" -- comment`,
			normalized: `select * from t where a = 0# and b = '1$' and c = "col1" -- comment`,
			params:     `1.5e3,it''s`,
		},
		{
			name:       "unary minus, exponent and hex literal",
			sql:        `a = -1 and b = 1e-3 and c = 0x1F`,
			normalized: `a = -0# and b = 1#-2# and c = 3#x1F`,
			params:     `1,1e,3,0`,
		},
		{
			name:       "dotted identifier and comma inside a literal",
			sql:        `select t1.col2, t1.5 from t where id = 10 and name = 'a,b' and n2 = 'x''y'`,
			normalized: `select t1.col2, t1.5 from t where id = 0# and name = '1$' and n2 = '2$'`,
			params:     `10,a,,b,x''y`,
		},
		{
			name:               "backslash does not escape a quote",
			sql:                `s = 'a\'b' and n = 3`,
			normalized:         `s = '0$'b'`,
			params:             `a\, and n = 3`,
			paramsUnsplittable: true,
		},
		{
			name:       "hint, empty literal and multi-line comment",
			sql:        "select /*+ INDEX(t idx) */ 1, \"2\", '' , 'z' from t /* multi\n line 42 */ where x = ?",
			normalized: "select /*+ INDEX(t idx) */ 0#, \"1#\", '' , '2$' from t /* multi\n line 42 */ where x = ?",
			params:     `1,2,z`,
		},
		{
			name:       "multibyte identifiers enable a number token",
			sql:        `SELECT/*c*/1 FROM t WHERE 테이블1 = 2 and 名前 = '値'`,
			normalized: `SELECT/*c*/1 FROM t WHERE 테이블0# = 1# and 名前 = '2$'`,
			params:     `1,2,値`,
		},
		{
			name:       "dollar followed by a digit is an identifier",
			sql:        `select $1, $2 from t where a=$3 and b = 4`,
			normalized: `select $1, $2 from t where a=$3 and b = 0#`,
			params:     `4`,
		},
		{
			name:               "unterminated literal emits no placeholder",
			sql:                `select 'abc`,
			normalized:         `select '`,
			params:             `abc`,
			paramsUnsplittable: true,
		},
		{
			name:       "value tuples and a line comment",
			sql:        `insert into t values (1,2,3), ('a','b','c') // trailing`,
			normalized: `insert into t values (0#,1#,2#), ('3$','4$','5$') // trailing`,
			params:     `1,2,3,a,b,c`,
		},
		{
			name:       "empty literal consumes no index",
			sql:        `select '''' from t where a = 1`,
			normalized: `select '''' from t where a = 0#`,
			params:     `1`,
		},
		{
			name:       "block comment end token is searched past the opener",
			sql:        `/*/`,
			normalized: `/*/`,
			params:     ``,
		},
		{
			name:       "dollar not followed by a digit keeps the flag",
			sql:        `V$SESSION1`,
			normalized: `V$SESSION1`,
			params:     ``,
		},
		{
			name:       "shared index counter across numbers and literals",
			sql:        `$'x'1`,
			normalized: `$'0$'1#`,
			params:     `x,1`,
		},
		{
			name:       "exponent sign is a separate token",
			sql:        `1.4e-10`,
			normalized: `0#-1#`,
			params:     `1.4e,10`,
		},
		{
			name:       "digits after a dot are part of the identifier",
			sql:        `test.123`,
			normalized: `test.123`,
			params:     ``,
		},
		{
			name:       "underscore then space re-enables the number token",
			sql:        `test_ 123`,
			normalized: `test_ 0#`,
			params:     `123`,
		},
		{
			name:       "hash is not a comment",
			sql:        `select #1 from t`,
			normalized: `select #0# from t`,
			params:     `1`,
		},
		{
			name:       "bind markers are preserved",
			sql:        `select * from t where a in (?, ?, ?)`,
			normalized: `select * from t where a in (?, ?, ?)`,
			params:     ``,
		},
		{
			name:       "an IN list of literals is one statement per arity",
			sql:        `select * from t where a in (1,2,3)`,
			normalized: `select * from t where a in (0#,1#,2#)`,
			params:     `1,2,3`,
		},
	}
}

func Test_SqlNormalizerGoldenCases(t *testing.T) {
	for _, tc := range sqlNormalizeCases() {
		t.Run(tc.name, func(t *testing.T) {
			normalized, params := newSqlNormalizer(tc.sql, false).run()
			assert.Equal(t, tc.normalized, normalized, "normalized SQL is the id/UID cache key and PSqlMetaData.sql; it must match Java byte for byte")
			assert.Equal(t, tc.params, params, "param is split on ',' by the server to refill the placeholders")
		})
	}
}

// Test_SqlNormalizerIsNotIdempotent locks that normalizing an
// already-normalized statement changes it again: `0#` becomes `0##`.
func Test_SqlNormalizerIsNotIdempotent(t *testing.T) {
	once, _ := newSqlNormalizer(`select 1`, false).run()
	assert.Equal(t, `select 0#`, once)

	twice, _ := newSqlNormalizer(once, false).run()
	assert.Equal(t, `select 0##`, twice, "normalization is not idempotent in any of the three agents")
}

// Test_SqlNormalizerWhitespaceIsNotNormalized locks that runs of
// whitespace survives verbatim, so statements differing only in spacing have
// different SQL IDs.
func Test_SqlNormalizerWhitespaceIsNotNormalized(t *testing.T) {
	normalized, _ := newSqlNormalizer("select   *\n\tfrom  t", false).run()
	assert.Equal(t, "select   *\n\tfrom  t", normalized)
}

// Test_SqlNormalizerRemoveComments locks the agent default
// dropped rather than copied, and a statement that is nothing but a comment
// normalizes to the empty string.
func Test_SqlNormalizerRemoveComments(t *testing.T) {
	normalized, params := newSqlNormalizer(`SELECT/*c*/1 FROM t`, true).run()
	assert.Equal(t, `SELECT1 FROM t`, normalized, "the comment is dropped and the digit stays an identifier digit: a comment does not re-enable the number token")
	assert.Equal(t, ``, params)

	normalized, params = newSqlNormalizer(`/* only */`, true).run()
	assert.Equal(t, ``, normalized)
	assert.Equal(t, ``, params)
}

// splitOutputParams is the agent-side counterpart of the server's
// OutputParameterParser: it splits param on ',' and un-escapes the doubled
// commas the normalizer writes for a comma inside a literal.
func splitOutputParams(params string) []string {
	if params == "" {
		return nil
	}
	var (
		out []string
		cur strings.Builder
	)
	for i := 0; i < len(params); i++ {
		if params[i] != ',' {
			cur.WriteByte(params[i])
			continue
		}
		if i+1 < len(params) && params[i+1] == ',' {
			cur.WriteByte(',')
			i++
			continue
		}
		out = append(out, cur.String())
		cur.Reset()
	}
	out = append(out, cur.String())
	return out
}

// scanPlaceholderIndices returns the placeholder indices of a normalized
// statement in the order they appear. `<n>#` marks a number and `<n>$` a
// character literal; both draw from one shared counter, which is what makes
// the server able to refill them from a single comma-separated param string.
func scanPlaceholderIndices(normalized string) []int {
	var digits strings.Builder
	out := []int{}
	for i := 0; i < len(normalized); i++ {
		ch := normalized[i]
		if ch >= '0' && ch <= '9' {
			digits.WriteByte(ch)
			continue
		}
		if (ch == '#' || ch == '$') && digits.Len() > 0 {
			n := 0
			for _, d := range digits.String() {
				n = n*10 + int(d-'0')
			}
			out = append(out, n)
		}
		digits.Reset()
	}
	return out
}

// Test_SqlNormalizerSharedIndexCounter locks the invariant the
// server depends on: placeholders are numbered 0..n-1 from one counter shared
// by numbers and literals, and there are exactly as many of them as there are
// params.
func Test_SqlNormalizerSharedIndexCounter(t *testing.T) {
	for _, tc := range sqlNormalizeCases() {
		if tc.paramsUnsplittable {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			normalized, params := newSqlNormalizer(tc.sql, false).run()
			indices := scanPlaceholderIndices(normalized)

			want := make([]int, len(indices))
			for i := range want {
				want[i] = i
			}
			assert.Equal(t, want, indices, "placeholder indices must run 0..n-1 in order")
			assert.Len(t, splitOutputParams(params), len(indices), "one param per placeholder")
		})
	}
}

// Test_SqlNormalizerInputCapDropsTheWholeStatement locks the
// input limit. A statement longer than maxSqlNormalizeLength (1 << 20) is
// dropped whole: run() returns empty normalized text and parameters, never a
// cut. A cut can leave a literal without its placeholder and produce a different
// SQL ID or UID.
//
// The boundary cases - one byte either side of the cap, a multibyte character
// straddling it - belong to Test_sqlNormalizer_DropsInputPastTheNormalizationCap
// above and are not repeated here.
func Test_SqlNormalizerInputCapDropsTheWholeStatement(t *testing.T) {
	assert.Equal(t, 1<<20, maxSqlNormalizeLength)
	assert.Greater(t, maxSqlNormalizeLength, maxSqlSize, "the memory cap sits above the metadata cap")

	// A literal that runs past the cap: precisely the shape where a cut would
	// leave the opening quote without its placeholder.
	over := "select 1 from t where a = '" + strings.Repeat("x", maxSqlNormalizeLength) + "'"
	assert.Greater(t, len(over), maxSqlNormalizeLength)
	assert.False(t, sqlNormalizable(over))

	normalized, params := newSqlNormalizer(over, false).run()
	assert.Equal(t, "", normalized, "an over-cap statement is dropped whole, not cut")
	assert.Equal(t, "", params, "a cut param would leave placeholders the server cannot refill")

	// The cap measures the raw input and changes nothing else: a statement
	// within it normalizes exactly as any other.
	normalized, params = newSqlNormalizer("select 1", false).run()
	assert.Equal(t, "select 0#", normalized)
	assert.Equal(t, "1", params)
}
