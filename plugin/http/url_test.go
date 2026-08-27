package pphttp

import "testing"

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
			name:    "doc example: * stays inside one segment",
			pattern: "/aa/*.html",
			kind:    patternAnt,
			match:   []string{"/aa/.html", "/aa/index.html", "/aa/a.b.html"},
			noMatch: []string{"/aa/bb/index.html", "/aa/index.htm", "/bb/index.html"},
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
			noMatch: []string{"/aa/index.htm", "/bb/index.html", "/aaindex.html"},
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHttpExcludeUrl(tt.pattern)
			if h.kind != tt.kind {
				t.Errorf("newHttpExcludeUrl(%q).kind = %d, want %d", tt.pattern, h.kind, tt.kind)
			}
			for _, url := range tt.match {
				if !h.match(url) {
					t.Errorf("pattern %q should match %q", tt.pattern, url)
				}
			}
			for _, url := range tt.noMatch {
				if h.match(url) {
					t.Errorf("pattern %q should not match %q", tt.pattern, url)
				}
			}
		})
	}
}

// The stack resident DP rows only cover urls up to antScratchLen; longer ones
// take the heap fallback and must still match.
func TestHttpExcludeUrlLongUrl(t *testing.T) {
	h := newHttpExcludeUrl("/a?c/**/x.html")

	long := "/abc"
	for len(long) < 2*antScratchLen {
		long += "/segment"
	}

	if !h.match(long + "/x.html") {
		t.Errorf("pattern should match long url of %d bytes", len(long)+7)
	}
	if h.match(long + "/x.htm") {
		t.Errorf("pattern should not match long url with a different suffix")
	}
}

func TestHttpUrlFilterIsFiltered(t *testing.T) {
	f := &httpUrlFilter{filters: []*httpExcludeUrl{
		newHttpExcludeUrl("/??/exclude.html"),
		newHttpExcludeUrl("/static/**"),
	}}

	for _, url := range []string{"/ab/exclude.html", "/static/js/app.js"} {
		if !f.isFiltered(url) {
			t.Errorf("isFiltered(%q) = false, want true", url)
		}
	}
	for _, url := range []string{"/exclude.html", "/statics/js/app.js"} {
		if f.isFiltered(url) {
			t.Errorf("isFiltered(%q) = true, want false", url)
		}
	}
}
