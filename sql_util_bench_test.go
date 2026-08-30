package pinpoint

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// ~200B: a typical ORM-generated point query with literals to extract.
var benchShortSQL = "SELECT id, name, email, created_at, updated_at FROM users " +
	"WHERE tenant_id = 12345 AND status = 'active' AND age > 20 " +
	"ORDER BY created_at DESC LIMIT 50 OFFSET 100 /* trace:abc123 */"

// ~8KB: a wide IN-list query, the shape that makes per-call normalization hurt.
var benchLongSQL = func() string {
	var sb strings.Builder
	sb.WriteString("SELECT o.id, o.user_id, o.total, o.status, i.sku, i.qty, i.price FROM orders o JOIN order_items i ON i.order_id = o.id WHERE o.status = 'shipped' AND o.id IN (")
	for i := 0; sb.Len() < 8*1024-64; i++ {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "%d", 1000000+i)
	}
	sb.WriteString(") ORDER BY o.created_at DESC")
	return sb.String()
}()

var benchHugeLiteralSQL = "SELECT '" + strings.Repeat("x", 1<<20) + "'"

func newNormalizeTestAgent() *agent {
	a := &agent{}
	a.rawSqlCache = newMetaCache[string, normalizedSql](cacheSize, hashStringKey)
	return a
}

// uniqueSQLs derives n distinct raw SQL texts from base by interpolating a
// different literal into each — the ORM-inlines-literals worst case.
func uniqueSQLs(base string, n int) []string {
	qs := make([]string, n)
	for i := range qs {
		qs[i] = base + " AND note = '" + fmt.Sprint(i) + "'"
	}
	return qs
}

// The most important test: on both the miss path and the hit path, the cached
// result must be byte-identical to what the uncached normalizer produces.
func TestNormalizeSqlCacheEquivalence(t *testing.T) {
	corpus := []string{
		"select * from table a = 1 and b=50 and c=? and d='11'",
		"select * from table a = -1 and b=-50 and c=? and d='-11'",
		"select 1",
		"select col from t where a = ?",
		"select /* c 1 */ 1 -- x\nfrom t",
		"select * from t where s = 'it''s' and n = 5",
		"select * from t where name = '한글' and id = 7",
		"",
		benchShortSQL,
		benchLongSQL,
	}

	a := newNormalizeTestAgent()
	for _, sql := range corpus {
		wantSql, wantParam := newSqlNormalizer(sql).run()
		for _, path := range []string{"miss", "hit"} {
			gotSql, gotParam := a.normalizeSql(sql)
			if gotSql != wantSql || gotParam != wantParam {
				t.Errorf("%s path: normalizeSql(%.60q) = (%q, %q), want (%q, %q)",
					path, sql, gotSql, gotParam, wantSql, wantParam)
			}
		}
	}
}

func TestNormalizeSqlCacheBypassesHugeSql(t *testing.T) {
	huge := strings.Repeat("select * from t where a = 'x' and b = 123 union all ", 2000) + "select 1"
	if len(huge) <= maxSqlSize {
		t.Fatalf("test sql too short: %d", len(huge))
	}

	a := newNormalizeTestAgent()
	wantSql, wantParam := newSqlNormalizer(huge).run()
	gotSql, gotParam := a.normalizeSql(huge)
	if gotSql != wantSql || gotParam != wantParam {
		t.Errorf("bypass path result differs from uncached normalizer")
	}
	if _, cached := a.rawSqlCache.peek(huge); cached {
		t.Errorf("sql longer than %d bytes must not be cached", maxSqlSize)
	}
}

// Run with -race: concurrent callers over more unique queries than the cache
// holds (forcing eviction churn) must each still get the exact result for
// their own query — no mixing of cached values across keys.
func TestNormalizeSqlCacheConcurrent(t *testing.T) {
	queries := uniqueSQLs("select * from t where a = 1 and s = 'v'", cacheSize+200)
	expected := make([]normalizedSql, len(queries))
	for i, q := range queries {
		nsql, param := newSqlNormalizer(q).run()
		expected[i] = normalizedSql{sql: nsql, param: param}
	}

	a := newNormalizeTestAgent()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for pass := 0; pass < 3; pass++ {
				for i := range queries {
					idx := (i + g*137) % len(queries)
					nsql, param := a.normalizeSql(queries[idx])
					if nsql != expected[idx].sql || param != expected[idx].param {
						t.Errorf("goroutine %d: mixed result for query %d", g, idx)
						return
					}
				}
			}
		}(g)
	}
	wg.Wait()
}

func benchmarkNormalize(b *testing.B, sql string) {
	b.SetBytes(int64(len(sql)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		nsql, param := newSqlNormalizer(sql).run()
		_, _ = nsql, param
	}
}

// Baseline: the uncached normalizer.
func BenchmarkSqlNormalizeShort(b *testing.B) { benchmarkNormalize(b, benchShortSQL) }
func BenchmarkSqlNormalizeLong(b *testing.B)  { benchmarkNormalize(b, benchLongSQL) }
func BenchmarkSqlNormalizeHugeLiteral(b *testing.B) {
	benchmarkNormalize(b, benchHugeLiteralSQL)
}

// Win case: the same statement repeats, every call after the first is a hit.
func benchmarkNormalizeCachedRepeat(b *testing.B, sql string) {
	a := newNormalizeTestAgent()
	a.normalizeSql(sql)
	b.SetBytes(int64(len(sql)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nsql, param := a.normalizeSql(sql)
		_, _ = nsql, param
	}
}

// Loss case: every statement is unique, every call is a miss plus an LRU
// insert and eviction. The delta against the uncached baseline is the cache's
// worst-case overhead.
func benchmarkNormalizeCachedUnique(b *testing.B, base string, n int) {
	queries := uniqueSQLs(base, n)
	a := newNormalizeTestAgent()
	b.SetBytes(int64(len(queries[0])))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nsql, param := a.normalizeSql(queries[i%len(queries)])
		_, _ = nsql, param
	}
}

func BenchmarkSqlNormalizeCachedShortRepeat(b *testing.B) {
	benchmarkNormalizeCachedRepeat(b, benchShortSQL)
}
func BenchmarkSqlNormalizeCachedLongRepeat(b *testing.B) {
	benchmarkNormalizeCachedRepeat(b, benchLongSQL)
}
func BenchmarkSqlNormalizeCachedShortUnique(b *testing.B) {
	benchmarkNormalizeCachedUnique(b, benchShortSQL, 4096)
}
func BenchmarkSqlNormalizeCachedLongUnique(b *testing.B) {
	benchmarkNormalizeCachedUnique(b, benchLongSQL, 2048)
}

// Concurrent hits: SetSQL runs on many request goroutines at once, so the hit
// path must scale, not serialize on the cache lock.
func BenchmarkSqlNormalizeCachedShortRepeatParallel(b *testing.B) {
	a := newNormalizeTestAgent()
	a.normalizeSql(benchShortSQL)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			nsql, param := a.normalizeSql(benchShortSQL)
			_, _ = nsql, param
		}
	})
}

// Uncached runs over the same unique-query sets, so the CachedUnique deltas
// above isolate the cache's miss overhead instead of the extra literal's cost.
func benchmarkNormalizeUniqueNoCache(b *testing.B, base string, n int) {
	queries := uniqueSQLs(base, n)
	b.SetBytes(int64(len(queries[0])))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nsql, param := newSqlNormalizer(queries[i%len(queries)]).run()
		_, _ = nsql, param
	}
}

func BenchmarkSqlNormalizeShortUniqueNoCache(b *testing.B) {
	benchmarkNormalizeUniqueNoCache(b, benchShortSQL, 4096)
}
func BenchmarkSqlNormalizeLongUniqueNoCache(b *testing.B) {
	benchmarkNormalizeUniqueNoCache(b, benchLongSQL, 2048)
}
