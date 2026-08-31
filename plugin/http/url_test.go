package pphttp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHttpExcludeUrlMatch(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		kind    patternKind
		match   []string
		noMatch []string
	}{
		{
			name:    "no wildcard is an exact match",
			pattern: "/exclude.html",
			kind:    patternExact,
			match:   []string{"/exclude.html"},
			noMatch: []string{"/exclude.htm", "/exclude.htmlx", "/a/exclude.html", ""},
		},
		{
			name:    "doc example: ? matches exactly one character",
			pattern: "/??/exclude.html",
			kind:    patternAnt,
			match:   []string{"/ab/exclude.html", "/12/exclude.html", "/../exclude.html"},
			noMatch: []string{
				"/a/exclude.html",   // too few
				"/abc/exclude.html", // too many
				"/exclude.html",     // the old regexp made '??' a lazy optional on '/'
				"//exclude.html",
			},
		},
		{
			name:    "? never matches the path separator",
			pattern: "/a?c",
			kind:    patternAnt,
			match:   []string{"/abc", "/a.c"},
			noMatch: []string{"/a/c", "/ac", "/abbc"},
		},
		{
			name:    "? matches one Unicode character",
			pattern: "/?",
			kind:    patternAnt,
			match:   []string{"/한", "/é", "/🙂"},
			noMatch: []string{"/", "//", "/한글"},
		},
		{
			name:    "doc example: * stays inside one segment",
			pattern: "/aa/*.html",
			kind:    patternAnt,
			match:   []string{"/aa/.html", "/aa/index.html", "/aa/a.b.html"},
			noMatch: []string{"/aa/bb/index.html", "/aa/index.htm", "/bb/index.html"},
		},
		{
			name:    "Unicode literals and * stay inside one segment",
			pattern: "/한*/끝",
			kind:    patternAnt,
			match:   []string{"/한/끝", "/한글🙂/끝"},
			noMatch: []string{"/한글/중간/끝", "/한글/끗", "/글/끝"},
		},
		{
			name:    "trailing * is a segment prefix",
			pattern: "/aa/*",
			kind:    patternSegmentPrefix,
			match:   []string{"/aa/", "/aa/index.html"},
			noMatch: []string{"/aa", "/aa/bb/index.html", "/bb/"},
		},
		{
			name:    "trailing ** is a plain prefix",
			pattern: "/aa/**",
			kind:    patternPrefix,
			match:   []string{"/aa/", "/aa/index.html", "/aa/bb/cc/index.html"},
			noMatch: []string{"/aa", "/bb/index.html"},
		},
		{
			name:    "** alone matches everything",
			pattern: "**",
			kind:    patternPrefix,
			match:   []string{"", "/", "/a/b/c"},
		},
		{
			name:    "* alone matches a single segment",
			pattern: "*",
			kind:    patternSegmentPrefix,
			match:   []string{"", "a", "abc"},
			noMatch: []string{"/", "/a", "a/b"},
		},
		{
			name:    "** in the middle spans segments",
			pattern: "/aa/**/index.html",
			kind:    patternAnt,
			match: []string{
				"/aa/index.html", // '**/' collapses to nothing
				"/aa/bb/index.html",
				"/aa/bb/cc/index.html",
			},
			noMatch: []string{
				"/aa/index.htm",
				"/bb/index.html",
				"/aaindex.html",
				"/aa/not-index.html",
				"/aa/fooindex.html",
			},
		},
		{
			name:    "leading ** spans segments",
			pattern: "**/exclude.html",
			kind:    patternAnt,
			match:   []string{"/exclude.html", "/a/b/exclude.html", "exclude.html"},
			noMatch: []string{"/exclude.html/x", "/excludexhtml"},
		},
		{
			name:    "mixed wildcards",
			pattern: "/api/v?/**/*.json",
			kind:    patternAnt,
			match:   []string{"/api/v1/a.json", "/api/v2/x/y/z.json"},
			noMatch: []string{"/api/v/a.json", "/api/v10/a.json", "/api/v1/a.xml"},
		},
		{
			// The regexp converter escaped only . + ^ [ ] { } — these leaked through.
			name:    "regexp metacharacters are literal",
			pattern: "/a(b)|c$d\\e.f+g",
			kind:    patternExact,
			match:   []string{"/a(b)|c$d\\e.f+g"},
			noMatch: []string{"/ab", "/c$d", "/aXbc$dXeXfg", "/a(b)|c$d\\e.fg"},
		},
		{
			// Compiling this as a regexp fails; the filter used to vanish silently.
			name:    "unbalanced regexp metacharacters still filter",
			pattern: "/foo(*",
			kind:    patternSegmentPrefix,
			match:   []string{"/foo(", "/foo(bar"},
			noMatch: []string{"/foo", "/foobar", "/foo(bar/baz"},
		},
		{
			name:    "bracket metacharacters are literal, not a character class",
			pattern: "/[ab]/*.html",
			kind:    patternAnt,
			match:   []string{"/[ab]/x.html"},
			noMatch: []string{"/a/x.html", "/b/x.html"},
		},
		{
			name:    "quantifier metacharacters are literal",
			pattern: "/a{2}?/x",
			kind:    patternAnt,
			match:   []string{"/a{2}b/x"},
			noMatch: []string{"/aa/x", "/a{2}/x"},
		},
		{
			// Degenerate input: '***' tokenizes as '**' + '*', so the following
			// '/' stays a literal and is not collapsed the way '**/' is.
			name:    "consecutive stars beyond two",
			pattern: "/a/***/b",
			kind:    patternAnt,
			match:   []string{"/a/x/b", "/a/x/y/b"},
			noMatch: []string{"/a/b", "/a/b/c"},
		},
		{
			// A wildcard before the '**' suffix keeps the pattern out of the
			// prefix fast path; it has to compile to the token matcher.
			name:    "wildcard before a ** suffix falls back to Ant",
			pattern: "/a?/**",
			kind:    patternAnt,
			match:   []string{"/ab/", "/ab/c/d"},
			noMatch: []string{"/a/", "/abc/", "/ab"},
		},
		{
			name:    "wildcard before a * suffix falls back to Ant",
			pattern: "/a?/*",
			kind:    patternAnt,
			match:   []string{"/ab/", "/ab/c"},
			noMatch: []string{"/ab/c/d", "/a/", "/abc/c"},
		},
		{
			// '**' with nothing after it inside the pattern spans segments and
			// then has to reach the end of the url.
			name:    "trailing literal after a spanning **",
			pattern: "/a/**b",
			kind:    patternAnt,
			match:   []string{"/a/b", "/a/xb", "/a/x/y/b"},
			noMatch: []string{"/a/bx", "/a"},
		},
		{
			name:    "? at the very end",
			pattern: "/a/b?",
			kind:    patternAnt,
			match:   []string{"/a/bc", "/a/b한"},
			noMatch: []string{"/a/b", "/a/bcd", "/a/b/"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.kind, newHttpExcludeUrl(tt.pattern).kind,
				"newHttpExcludeUrl(%q) classified the pattern into the wrong kind", tt.pattern)

			// Match through the filter so every kind takes the dispatch path a
			// request takes.
			f := setupHttpUrlFilter([]string{tt.pattern})
			for _, url := range tt.match {
				assert.True(t, f.isFiltered(url), "pattern %q should match %q", tt.pattern, url)
			}
			for _, url := range tt.noMatch {
				assert.False(t, f.isFiltered(url), "pattern %q should not match %q", tt.pattern, url)
			}
		})
	}
}

// The stack resident DP rows only cover urls up to antScratchLen; longer ones
// take the heap fallback and must still match.
func TestHttpExcludeUrlLongUrl(t *testing.T) {
	f := setupHttpUrlFilter([]string{"/a?c/**/x.html"})

	long := "/abc"
	for len(long) < 2*antScratchLen {
		long += "/segment"
	}

	assert.True(t, f.isFiltered(long+"/x.html"), "pattern should match long url of %d bytes", len(long)+7)
	assert.False(t, f.isFiltered(long+"/x.htm"), "pattern should not match long url with a different suffix")
}

// Every Ant filter of one request shares a scratch buffer that arrives dirty,
// so a pattern must not read the previous pattern's DP state as its own. Each
// token kind gets a turn at running second.
func TestHttpUrlFilterSharedScratchIsNotReadAsState(t *testing.T) {
	second := []string{
		"/b/x?z",         // tokenQuestion
		"/b/x*z",         // tokenStar
		"/b/**z",         // tokenDoubleStar
		"/b/**/z",        // tokenDoubleStarSlash
		"/b/xyz?",        // literal run ending in a wildcard
		"/b/?/**/*.json", // every kind in one pattern
	}

	for _, pattern := range second {
		t.Run(pattern, func(t *testing.T) {
			// "/a/**/deep.html" runs first and leaves its rows behind; the
			// pattern under test must produce the same verdicts either way.
			shared := setupHttpUrlFilter([]string{"/a/**/deep.html", pattern})
			alone := setupHttpUrlFilter([]string{pattern})

			for _, url := range []string{
				"/b/xyz", "/b/xz", "/b/x/z", "/b/z", "/b/a/b/z",
				"/b/xyz1", "/b/1/x/y.json", "/c/xyz",
			} {
				assert.Equal(t, alone.isFiltered(url), shared.isFiltered(url),
					"%q behaved differently after another Ant pattern ran on the shared scratch", url)
			}
		})
	}
}

func TestHttpUrlFilterIsFiltered(t *testing.T) {
	f := setupHttpUrlFilter([]string{
		"/Exact/한글",
		"/case-sensitive",
		"/Exact/한글", // exact duplicates collapse in the lookup map
		"/??/exclude.html",
		"/static/**",
	})

	require.Len(t, f.exact, 2, "exact filters")
	require.Len(t, f.prefix, 1, "prefix filters")
	require.Len(t, f.ant, 1, "ant filters")

	for _, url := range []string{"/Exact/한글", "/case-sensitive", "/ab/exclude.html", "/static/js/app.js"} {
		assert.True(t, f.isFiltered(url), "isFiltered(%q)", url)
	}
	for _, url := range []string{"/exact/한글", "/Case-Sensitive", "/exclude.html", "/statics/js/app.js"} {
		assert.False(t, f.isFiltered(url), "isFiltered(%q)", url)
	}
}

// An empty pattern would classify as patternExact and then filter every request
// whose path is "" — it is dropped at setup instead.
func TestHttpUrlFilterIgnoresEmptyPatterns(t *testing.T) {
	f := setupHttpUrlFilter([]string{"", "/keep/**", ""})

	assert.Empty(t, f.exact, "an empty pattern must not become an exact filter")
	require.Len(t, f.prefix, 1)
	assert.True(t, f.isFiltered("/keep/x"))
	assert.False(t, f.isFiltered(""))
}

// No pattern configured is the default: nothing is ever filtered, and the Ant
// scratch buffer is never touched.
func TestHttpUrlFilterWithoutPatterns(t *testing.T) {
	for _, cfg := range [][]string{nil, {}, {""}} {
		f := setupHttpUrlFilter(cfg)
		assert.Empty(t, f.ant)
		for _, url := range []string{"", "/", "/a/b/c.html"} {
			assert.False(t, f.isFiltered(url), "isFiltered(%q) with config %q", url, cfg)
		}
	}
}

var benchmarkFiltered bool

func TestHttpUrlFilterReusesLongAntScratch(t *testing.T) {
	f := setupHttpUrlFilter([]string{
		"/api/**/one.json",
		"/api/**/two.json",
		"/api/**/three.json",
		"/api/**/four.json",
		"/api/**/result.json",
	})
	url := "/api" + strings.Repeat("/segment", antScratchLen/4) + "/result.json"

	allocs := testing.AllocsPerRun(100, func() {
		benchmarkFiltered = f.isFiltered(url)
	})
	require.True(t, benchmarkFiltered, "long URL should match the last Ant pattern")
	assert.LessOrEqual(t, allocs, float64(1),
		"isFiltered allocated %.0f times, want at most one shared scratch allocation", allocs)
}

// A url that fits the stack rows must not allocate at all, whatever the
// configured pattern mix.
func TestHttpUrlFilterShortUrlDoesNotAllocate(t *testing.T) {
	f := setupHttpUrlFilter([]string{"/exact", "/static/**", "/api/v?/**/*.json"})

	assert.Zero(t, testing.AllocsPerRun(100, func() {
		benchmarkFiltered = f.isFiltered("/api/v2/users/1/profile.json")
	}), "matching a short url should stay on the stack rows")
	require.True(t, benchmarkFiltered)
}

func TestHttpMethodFilter(t *testing.T) {
	f := &httpMethodFilter{excludeMethod: trimStringSlice([]string{" put ", "DELETE"})}

	for _, method := range []string{"PUT", "put", "Put", "DELETE", "delete"} {
		assert.True(t, f.isExcludedMethod(method), "isExcludedMethod(%q) must be case-insensitive", method)
	}
	for _, method := range []string{"GET", "POST", "PU", "PUTX", ""} {
		assert.False(t, f.isExcludedMethod(method), "isExcludedMethod(%q)", method)
	}
}

func TestHttpMethodFilterWithoutConfig(t *testing.T) {
	for _, cfg := range [][]string{nil, {}} {
		f := &httpMethodFilter{excludeMethod: cfg}
		for _, method := range []string{"GET", "POST", "PUT", ""} {
			assert.False(t, f.isExcludedMethod(method), "nothing is excluded when no method is configured")
		}
	}
}

// The two filters a request consults are built from the agent config, so the
// options have to reach them through pinpoint.GetConfig().
func TestUrlAndMethodFiltersComeFromAgentConfig(t *testing.T) {
	startAgent(t,
		WithHttpServerExcludeUrl([]string{" /skip/** ", "/??/exclude.html"}),
		WithHttpServerExcludeMethod([]string{" put ", "delete"}),
	)

	urlFilter := newHttpUrlFilter()
	assert.True(t, urlFilter.isFiltered("/skip/a/b"), "the configured prefix pattern should filter")
	assert.True(t, urlFilter.isFiltered("/ab/exclude.html"), "the configured Ant pattern should filter")
	assert.False(t, urlFilter.isFiltered("/keep/a/b"))

	methodFilter := newHttpExcludeMethod()
	assert.True(t, methodFilter.isExcludedMethod("PUT"), "surrounding whitespace must be trimmed off the config value")
	assert.True(t, methodFilter.isExcludedMethod("DELETE"))
	assert.False(t, methodFilter.isExcludedMethod("GET"))
}

// trimStringSlice must copy: the slice it is handed belongs to the published
// config snapshot and is shared by every reader.
func TestTrimStringSlice(t *testing.T) {
	cfg := []string{" a ", "\tb\n", "c"}
	trimmed := trimStringSlice(cfg)

	assert.Equal(t, []string{"a", "b", "c"}, trimmed)
	assert.Equal(t, []string{" a ", "\tb\n", "c"}, cfg, "trimStringSlice wrote through to the config's slice")
	assert.Empty(t, trimStringSlice(nil))
}

func benchmarkHttpUrlFilter(patterns ...string) *httpUrlFilter {
	return setupHttpUrlFilter(patterns)
}

func BenchmarkHttpUrlFilterExact(b *testing.B) {
	patterns := make([]string, 128)
	for i := range patterns {
		patterns[i] = fmt.Sprintf("/excluded/%03d", i)
	}
	f := benchmarkHttpUrlFilter(patterns...)
	url := patterns[len(patterns)-1]

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkFiltered = f.isFiltered(url)
	}
}

func BenchmarkHttpUrlFilterMixed(b *testing.B) {
	patterns := make([]string, 64, 71)
	for i := range patterns {
		patterns[i] = fmt.Sprintf("/excluded/%03d", i)
	}
	patterns = append(patterns,
		"/assets/**",
		"/temporary/*",
		"/api/v?/**/one.json",
		"/api/v?/**/two.json",
		"/api/v?/**/three.json",
		"/api/v?/**/four.json",
		"/api/v?/**/result.json",
	)
	f := benchmarkHttpUrlFilter(patterns...)
	url := "/api/v2/one/two/result.json"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkFiltered = f.isFiltered(url)
	}
}

func BenchmarkHttpUrlFilterLongAnt(b *testing.B) {
	f := benchmarkHttpUrlFilter(
		"/api/**/one.json",
		"/api/**/two.json",
		"/api/**/three.json",
		"/api/**/four.json",
		"/api/**/result.json",
	)
	url := "/api" + strings.Repeat("/segment", antScratchLen/4) + "/result.json"

	b.ReportAllocs()
	b.SetBytes(int64(len(url)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkFiltered = f.isFiltered(url)
	}
}

// BenchmarkHttpUrlFilterPrefixOnly pins the cost of a config with no Ant
// pattern: it must not pay for the Ant scratch buffer.
func BenchmarkHttpUrlFilterPrefixOnly(b *testing.B) {
	patterns := make([]string, 8)
	for i := range patterns {
		patterns[i] = fmt.Sprintf("/static%d/**", i)
	}
	f := benchmarkHttpUrlFilter(patterns...)
	url := "/api/v2/users/1234/profile"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkFiltered = f.isFiltered(url)
	}
}
