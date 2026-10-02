package pphttp

import (
	"net/http"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingAnnotation captures the key/name/value triples the header recorders
// append, keyed by annotation key so request and response headers stay apart.
type recordingAnnotation struct {
	pinpoint.Annotation // nil: only AppendStringString is called
	got                 map[int32]map[string]string
}

func newRecordingAnnotation() *recordingAnnotation {
	return &recordingAnnotation{got: map[int32]map[string]string{}}
}

func (a *recordingAnnotation) AppendStringString(key int32, s1 string, s2 string) {
	if a.got[key] == nil {
		a.got[key] = map[string]string{}
	}
	a.got[key][s1] = s2
}

func (a *recordingAnnotation) values(key int32) map[string]string {
	if v := a.got[key]; v != nil {
		return v
	}
	return map[string]string{}
}

func (a *recordingAnnotation) cookies() map[string]string {
	return a.values(pinpoint.AnnotationHttpCookie)
}

// testCookie is a Cookie that counts how often the recorder walks it: the
// default recorder makes exactly one pass so a lazily parsed Cookie header is
// not re-parsed once per configured name.
type testCookie struct {
	cookies map[string]string
	visits  int
}

func (c *testCookie) VisitAll(f func(name string, value string)) {
	c.visits++
	for name, value := range c.cookies {
		f(name, value)
	}
}

func requestWithCookies(t *testing.T, cookies ...*http.Cookie) cookie {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "/", nil)
	require.NoError(t, err)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return cookie{req}
}

func Test_defaultHttpHeaderRecorder_recordCookie(t *testing.T) {
	c := requestWithCookies(t,
		&http.Cookie{Name: "session", Value: "s1"},
		&http.Cookie{Name: "userid", Value: "u1"},
		&http.Cookie{Name: "other", Value: "x"},
	)

	a := newRecordingAnnotation()
	// "Session" also checks the case-insensitive match.
	newDefaultHttpHeaderRecorder([]string{"Session", "userid"}).recordCookie(a, c)

	assert.Equal(t, map[string]string{"session": "s1", "userid": "u1"}, a.cookies())
}

// The per-name loop this replaced stopped at the first configured name that
// matched, so a second configured cookie went unrecorded.
func Test_defaultHttpHeaderRecorder_recordCookie_RecordsEveryConfiguredName(t *testing.T) {
	c := &testCookie{cookies: map[string]string{"a": "1", "b": "2", "c": "3", "d": "4"}}

	a := newRecordingAnnotation()
	newDefaultHttpHeaderRecorder([]string{"a", "b", "c"}).recordCookie(a, c)

	assert.Equal(t, map[string]string{"a": "1", "b": "2", "c": "3"}, a.cookies())
	assert.Equal(t, 1, c.visits, "the cookies must be walked exactly once, not once per configured name")
}

func Test_defaultHttpHeaderRecorder_recordHeader(t *testing.T) {
	h := http.Header{}
	h.Add("X-Trace", "one")
	h.Add("X-Trace", "two") // multiple values join with a comma
	h.Set("X-Other", "ignored")

	a := newRecordingAnnotation()
	// "x-trace" checks that lookup goes through http.Header's canonical form.
	newDefaultHttpHeaderRecorder([]string{"x-trace", "X-Missing"}).
		recordHeader(a, pinpoint.AnnotationHttpResponseHeader, header{h})

	assert.Equal(t, map[string]string{"x-trace": "one,two"},
		a.values(pinpoint.AnnotationHttpResponseHeader))
	assert.NotContains(t, a.values(pinpoint.AnnotationHttpResponseHeader), "X-Missing",
		"a header that is absent must not be annotated at all")
}

func Test_allHttpHeaderRecorder(t *testing.T) {
	h := http.Header{}
	h.Add("X-A", "1")
	h.Add("X-A", "2")
	h.Set("X-B", "3")

	a := newRecordingAnnotation()
	newAllHttpHeaderRecorder().recordHeader(a, pinpoint.AnnotationHttpRequestHeader, header{h})

	assert.Equal(t, map[string]string{"X-A": "1,2", "X-B": "3"},
		a.values(pinpoint.AnnotationHttpRequestHeader))
}

func Test_allHttpHeaderRecorder_recordCookie(t *testing.T) {
	c := requestWithCookies(t,
		&http.Cookie{Name: "session", Value: "s1"},
		&http.Cookie{Name: "other", Value: "x"},
	)

	a := newRecordingAnnotation()
	newAllHttpHeaderRecorder().recordCookie(a, c)

	assert.Equal(t, map[string]string{"session": "s1", "other": "x"}, a.cookies())
}

// makeHttpHeaderRecorder picks the recorder from the option value; picking the
// wrong one either records nothing or records every header of every request.
func TestMakeHttpHeaderRecorder(t *testing.T) {
	startAgent(t,
		WithHttpServerRecordRequestHeader([]string{"headers-all"}), // case-insensitive
		WithHttpServerRecordRespondHeader([]string{"X-Trace"}),
		WithHttpServerRecordRequestCookie([]string{}),
		WithHttpClientRecordRequestHeader([]string{" HEADERS-ALL "}), // trimmed before the compare
	)

	assert.IsType(t, &allHttpHeaderRecorder{}, makeHttpHeaderRecorder(CfgHttpServerRecordRequestHeader))
	assert.IsType(t, &defaultHttpHeaderRecorder{}, makeHttpHeaderRecorder(CfgHttpServerRecordResponseHeader))
	assert.IsType(t, &noopHttpHeaderRecorder{}, makeHttpHeaderRecorder(CfgHttpServerRecordRequestCookie))
	assert.IsType(t, &allHttpHeaderRecorder{}, makeHttpHeaderRecorder(CfgHttpClientRecordRequestHeader))
}

// stringAnnotation also keeps the single string annotations (HTTP.URL,
// HTTP.PARAM) the plain recorder ignores.
type stringAnnotation struct {
	*recordingAnnotation
	strs map[int32][]string
}

func newStringAnnotation() *stringAnnotation {
	return &stringAnnotation{recordingAnnotation: newRecordingAnnotation(), strs: map[int32][]string{}}
}

func (a *stringAnnotation) AppendString(key int32, s string) { a.strs[key] = append(a.strs[key], s) }

// annotationTracer hands before() a span event whose annotations are captured.
type annotationTracer struct {
	pinpoint.Tracer
	a *stringAnnotation
}

type annotationSpanEvent struct {
	pinpoint.SpanEventRecorder
	a *stringAnnotation
}

func (t *annotationTracer) SpanEvent() pinpoint.SpanEventRecorder {
	return &annotationSpanEvent{t.Tracer.SpanEvent(), t.a}
}

func (se *annotationSpanEvent) Annotations() pinpoint.Annotation { return se.a }
