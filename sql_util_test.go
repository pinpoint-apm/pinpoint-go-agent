package pinpoint

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
	}

	for _, tt := range tests {
		t.Run(displayName(tt.sql), func(t *testing.T) {
			assertNormalize(t, tt.sql, tt.normalized, tt.params)
		})
	}
}

func assertNormalize(t *testing.T, sql, normalized, params string) {
	t.Helper()

	actualNormalized, actualParams := newSqlNormalizer(sql).run()
	assert.Equal(t, normalized, actualNormalized, "normalized sql")
	assert.Equal(t, params, actualParams, "params")
}

func displayName(sql string) string {
	return strings.ReplaceAll(sql, "\n", "\\n")
}
