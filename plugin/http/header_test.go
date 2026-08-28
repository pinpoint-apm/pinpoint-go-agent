package pphttp

import (
	"maps"
	"net/http"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// cookieAnnotation captures the cookie annotations recordCookie appends.
type cookieAnnotation struct {
	got map[string]string
}

func (a *cookieAnnotation) AppendStringString(key int32, s1 string, s2 string) {
	if key == pinpoint.AnnotationHttpCookie {
		a.got[s1] = s2
	}
}

func (a *cookieAnnotation) AppendInt(int32, int32)                             {}
func (a *cookieAnnotation) AppendLong(int32, int64)                            {}
func (a *cookieAnnotation) AppendString(int32, string)                         {}
func (a *cookieAnnotation) AppendIntStringString(int32, int32, string, string) {}
func (a *cookieAnnotation) AppendBytesStringString(int32, []byte, string, string) {
}
func (a *cookieAnnotation) AppendLongIntIntByteByteString(int32, int64, int32, int32, int32, int32, string) {
}

func Test_defaultHttpHeaderRecorder_recordCookie(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "s1"})
	req.AddCookie(&http.Cookie{Name: "userid", Value: "u1"})
	req.AddCookie(&http.Cookie{Name: "other", Value: "x"})

	a := &cookieAnnotation{got: map[string]string{}}
	// "Session" also checks the case-insensitive match.
	newDefaultHttpHeaderRecorder([]string{"Session", "userid"}).recordCookie(a, cookie{req})

	want := map[string]string{"session": "s1", "userid": "u1"}
	if !maps.Equal(a.got, want) {
		t.Errorf("recordCookie got %v, want %v", a.got, want)
	}
}
