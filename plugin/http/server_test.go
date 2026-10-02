package pphttp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type proxyValues struct {
	key          int32
	receivedTime int64
	code         int32
	duration     int32
	idle         int32
	busy         int32
	app          string
}

// proxyAnnotation captures every proxy header annotation setProxyHeader records,
// in order.
type proxyAnnotation struct {
	pinpoint.Annotation // nil: only AppendLongIntIntByteByteString is called
	got                 []proxyValues
}

func (a *proxyAnnotation) AppendLongIntIntByteByteString(key int32, l int64, i1 int32, i2 int32, b1 int32, b2 int32, s string) {
	a.got = append(a.got, proxyValues{key, l, i1, i2, b1, b2, s})
}

func Test_setProxyHeader(t *testing.T) {
	tests := []struct {
		name   string
		header string
		value  string
		want   *proxyValues // nil: no annotation
	}{
		{name: "no proxy header"},

		{name: "apache", header: "Pinpoint-ProxyApache", value: "t=1500968753503 D=125 i=51 b=48",
			want: &proxyValues{code: 3, receivedTime: 1500968753, duration: 125, idle: 51, busy: 48}},
		{name: "apache bare token before valid ones", header: "Pinpoint-ProxyApache", value: "t=1500968753503 D junk i=51 b",
			want: &proxyValues{code: 3, receivedTime: 1500968753, idle: 51, duration: -1, busy: -1}},
		{name: "apache extra spaces", header: "Pinpoint-ProxyApache", value: "  t=1500968753503   D=125  ",
			want: &proxyValues{code: 3, receivedTime: 1500968753, duration: 125, idle: -1, busy: -1}},
		{name: "apache unparsable numbers", header: "Pinpoint-ProxyApache", value: "t=1500968753503 D=x i=y b=z",
			want: &proxyValues{code: 3, receivedTime: 1500968753, duration: -1, idle: -1, busy: -1}},
		{name: "apache repeated keys keep the last", header: "Pinpoint-ProxyApache", value: "t=1500968753503 D=1 D=2",
			want: &proxyValues{code: 3, receivedTime: 1500968753, duration: 2, idle: -1, busy: -1}},
		{name: "apache negative D and out-of-range i, b are unset", header: "Pinpoint-ProxyApache", value: "t=1504230492763123 D=-7 i=101 b=-1",
			want: &proxyValues{code: 3, receivedTime: 1504230492763, duration: -1, idle: -1, busy: -1}},
		{name: "apache i, b at the [0, 100] bounds", header: "Pinpoint-ProxyApache", value: "t=1504230492763123 D=7 i=0 b=100",
			want: &proxyValues{code: 3, receivedTime: 1504230492763, duration: 7, idle: 0, busy: 100}},
		{name: "apache missing t", header: "Pinpoint-ProxyApache", value: "D=125 i=51 b=48"},
		{name: "apache bare token", header: "Pinpoint-ProxyApache", value: "t"},
		{name: "apache empty values", header: "Pinpoint-ProxyApache", value: "t= D= i= b="},
		{name: "apache t=0", header: "Pinpoint-ProxyApache", value: "t=0 D=125"},
		{name: "apache negative t", header: "Pinpoint-ProxyApache", value: "t=-1 D=125"},
		{name: "apache unparsable t", header: "Pinpoint-ProxyApache", value: "t=abc D=125"},
		// Apache's t= is in microseconds; anything under a millisecond rounds to 0.
		{name: "apache t under a millisecond", header: "Pinpoint-ProxyApache", value: "t=999"},

		// nginx t= and D= are seconds with exactly three decimals; D= is
		// reported in microseconds and a zero duration reads as unset.
		{name: "nginx", header: "Pinpoint-ProxyNginx", value: "t=1504230492.763 D=0.123",
			want: &proxyValues{code: 2, receivedTime: 1504230492763, duration: 123000, idle: -1, busy: -1}},
		{name: "nginx zero duration", header: "Pinpoint-ProxyNginx", value: "t=1504164327.484 D=0.000",
			want: &proxyValues{code: 2, receivedTime: 1504164327484, duration: -1, idle: -1, busy: -1}},
		{name: "nginx multi-second duration", header: "Pinpoint-ProxyNginx", value: "t=1504164327.484 D=12.345",
			want: &proxyValues{code: 2, receivedTime: 1504164327484, duration: 12345000, idle: -1, busy: -1}},
		{name: "nginx D with two decimals", header: "Pinpoint-ProxyNginx", value: "t=1504164327.484 D=0.1",
			want: &proxyValues{code: 2, receivedTime: 1504164327484, duration: -1, idle: -1, busy: -1}},
		{name: "nginx D without a decimal point", header: "Pinpoint-ProxyNginx", value: "t=1504164327.484 D=123",
			want: &proxyValues{code: 2, receivedTime: 1504164327484, duration: -1, idle: -1, busy: -1}},
		{name: "nginx D with four decimals", header: "Pinpoint-ProxyNginx", value: "t=1504164327.484 D=0.1234",
			want: &proxyValues{code: 2, receivedTime: 1504164327484, duration: -1, idle: -1, busy: -1}},
		{name: "nginx D unparsable", header: "Pinpoint-ProxyNginx", value: "t=1504164327.484 D=a.bcd",
			want: &proxyValues{code: 2, receivedTime: 1504164327484, duration: -1, idle: -1, busy: -1}},
		{name: "nginx negative D", header: "Pinpoint-ProxyNginx", value: "t=1504164327.484 D=-0.123",
			want: &proxyValues{code: 2, receivedTime: 1504164327484, duration: -1, idle: -1, busy: -1}},
		{name: "nginx D past int32 micros is unset, not wrapped", header: "Pinpoint-ProxyNginx", value: "t=1504164327.484 D=3000000.000",
			want: &proxyValues{code: 2, receivedTime: 1504164327484, duration: -1, idle: -1, busy: -1}},
		{name: "nginx bare token before valid one", header: "Pinpoint-ProxyNginx", value: "D t=1504164327.484",
			want: &proxyValues{code: 2, receivedTime: 1504164327484, duration: -1, idle: -1, busy: -1}},
		{name: "nginx t without a decimal point", header: "Pinpoint-ProxyNginx", value: "t=1504164327 D=0.123"},
		{name: "nginx t with two decimals", header: "Pinpoint-ProxyNginx", value: "t=1504164327.48 D=0.123"},
		{name: "nginx missing t", header: "Pinpoint-ProxyNginx", value: "D=0.123"},
		{name: "nginx bare token", header: "Pinpoint-ProxyNginx", value: "t"},
		{name: "nginx empty values", header: "Pinpoint-ProxyNginx", value: "t= D="},
		{name: "nginx t=0", header: "Pinpoint-ProxyNginx", value: "t=0.000 D=0.123"},
		{name: "nginx negative t", header: "Pinpoint-ProxyNginx", value: "t=-1.500"},
		{name: "nginx NaN", header: "Pinpoint-ProxyNginx", value: "t=NaN"},
		{name: "nginx Inf", header: "Pinpoint-ProxyNginx", value: "t=Inf"},
		{name: "nginx exponent", header: "Pinpoint-ProxyNginx", value: "t=1e400"},
		{name: "nginx overflow", header: "Pinpoint-ProxyNginx", value: "t=99999999999999999999.999"},
		{name: "nginx unparsable", header: "Pinpoint-ProxyNginx", value: "t=abc"},

		{name: "app", header: "Pinpoint-ProxyApp", value: "t=1500968753503 app=foo-bar",
			want: &proxyValues{code: 1, receivedTime: 1500968753503, app: "foo-bar", duration: -1, idle: -1, busy: -1}},
		{name: "app time is not divided by 1000", header: "Pinpoint-ProxyApp", value: "t=1500968753503",
			want: &proxyValues{code: 1, receivedTime: 1500968753503, duration: -1, idle: -1, busy: -1}},
		{name: "app bare token before valid one", header: "Pinpoint-ProxyApp", value: "app t=1500968753503",
			want: &proxyValues{code: 1, receivedTime: 1500968753503, duration: -1, idle: -1, busy: -1}},
		{name: "app at the length cap", header: "Pinpoint-ProxyApp", value: "t=1500968753503 app=" + strings.Repeat("a", proxyAppMaxLength),
			want: &proxyValues{code: 1, receivedTime: 1500968753503, app: strings.Repeat("a", proxyAppMaxLength), duration: -1, idle: -1, busy: -1}},
		{name: "app id characters", header: "Pinpoint-ProxyApp", value: "t=1500968753503 app=Foo.bar-1_2",
			want: &proxyValues{code: 1, receivedTime: 1500968753503, app: "Foo.bar-1_2", duration: -1, idle: -1, busy: -1}},
		// An app= that fails IdValidateUtils.validateId(app, 30) discards the header.
		{name: "app over the length cap", header: "Pinpoint-ProxyApp", value: "t=1500968753503 app=" + strings.Repeat("a", proxyAppMaxLength+1)},
		{name: "app with a disallowed character", header: "Pinpoint-ProxyApp", value: "t=1500968753503 app=foo/bar"},
		{name: "app with a non-ASCII rune", header: "Pinpoint-ProxyApp", value: "t=1500968753503 app=가"},
		{name: "app empty", header: "Pinpoint-ProxyApp", value: "t=1500968753503 app="},
		{name: "app missing t", header: "Pinpoint-ProxyApp", value: "app=foo"},
		{name: "app bare token", header: "Pinpoint-ProxyApp", value: "app"},
		{name: "app empty values", header: "Pinpoint-ProxyApp", value: "t= app="},
		{name: "app t=0", header: "Pinpoint-ProxyApp", value: "t=0 app=foo"},
		{name: "app negative t", header: "Pinpoint-ProxyApp", value: "t=-1 app=foo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.header != "" {
				req.Header.Set(tt.header, tt.value)
			}

			a := &proxyAnnotation{}
			setProxyHeader(a, header{req.Header})

			if tt.want == nil {
				assert.Empty(t, a.got, "%s: %q must not be recorded", tt.header, tt.value)
				return
			}
			want := *tt.want
			want.key = pinpoint.AnnotationHttpProxyHeader
			assert.Equal(t, []proxyValues{want}, a.got, "%s: %q", tt.header, tt.value)
		})
	}
}

// runs every parser: a request that crossed two proxies carries two annotations.
func Test_setProxyHeader_EveryHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Pinpoint-ProxyApache", "t=1500968753503000 D=125")
	req.Header.Set("Pinpoint-ProxyNginx", "t=1504164327.484 D=0.007")
	req.Header.Set("Pinpoint-ProxyApp", "t=1500968753503 app=foo")

	a := &proxyAnnotation{}
	setProxyHeader(a, header{req.Header})

	key := int32(pinpoint.AnnotationHttpProxyHeader)
	assert.Equal(t, []proxyValues{
		{key: key, code: 3, receivedTime: 1500968753503, duration: 125, idle: -1, busy: -1},
		{key: key, code: 2, receivedTime: 1504164327484, duration: 7000, idle: -1, busy: -1},
		{key: key, code: 1, receivedTime: 1500968753503, app: "foo", duration: -1, idle: -1, busy: -1},
	}, a.got)
}

// An invalid header is dropped on its own; the others are still recorded.
func Test_setProxyHeader_EveryHeader_oneInvalid(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Pinpoint-ProxyApache", "D=125")
	req.Header.Set("Pinpoint-ProxyNginx", "t=1504164327.484 D=0.007")

	a := &proxyAnnotation{}
	setProxyHeader(a, header{req.Header})

	assert.Equal(t, []proxyValues{
		{key: int32(pinpoint.AnnotationHttpProxyHeader), code: 2, receivedTime: 1504164327484, duration: 7000, idle: -1, busy: -1},
	}, a.got)
}

func Test_setProxyHeader_User(t *testing.T) {
	startAgent(t, WithHttpServerProxyUserHeaderNames([]string{" x-proxy-time ", "", "X-Other-Proxy"}))

	tests := []struct {
		name    string
		headers map[string]string
		want    []proxyValues
	}{
		{name: "user header", headers: map[string]string{"X-Proxy-Time": "t=1500968753503"},
			want: []proxyValues{{code: 4, receivedTime: 1500968753503, app: "X-Proxy-Time", duration: -1, idle: -1, busy: -1}}},
		{name: "extra tokens are ignored", headers: map[string]string{"X-Proxy-Time": "app=foo t=1500968753503 D=3"},
			want: []proxyValues{{code: 4, receivedTime: 1500968753503, duration: 3, app: "X-Proxy-Time", idle: -1, busy: -1}}},
		{name: "every configured header", headers: map[string]string{"X-Proxy-Time": "t=1000000000001", "X-Other-Proxy": "t=1000000000002"},
			want: []proxyValues{{code: 4, receivedTime: 1000000000001, app: "X-Proxy-Time", duration: -1, idle: -1, busy: -1}, {code: 4, receivedTime: 1000000000002, app: "X-Other-Proxy", duration: -1, idle: -1, busy: -1}}},
		{name: "alongside a standard header", headers: map[string]string{"Pinpoint-ProxyApp": "t=5 app=foo", "X-Proxy-Time": "t=1000000000001"},
			want: []proxyValues{{code: 1, receivedTime: 5, app: "foo", duration: -1, idle: -1, busy: -1}, {code: 4, receivedTime: 1000000000001, app: "X-Proxy-Time", duration: -1, idle: -1, busy: -1}}},
		// UserRequestParser infers the writer from the value's shape.
		{name: "apache shape: micros, D micros", headers: map[string]string{"X-Proxy-Time": "t=1504230492763123 D=1500"},
			want: []proxyValues{{code: 4, receivedTime: 1504230492763, duration: 1500, app: "X-Proxy-Time", idle: -1, busy: -1}}},
		{name: "nginx shape: sec.mmm, D sec.mmm", headers: map[string]string{"X-Proxy-Time": "t=1504230492.763 D=0.123"},
			want: []proxyValues{{code: 4, receivedTime: 1504230492763, duration: 123000, app: "X-Proxy-Time", idle: -1, busy: -1}}},
		{name: "app shape: millis", headers: map[string]string{"X-Proxy-Time": "t=1504230492763 D=42"},
			want: []proxyValues{{code: 4, receivedTime: 1504230492763, duration: 42, app: "X-Proxy-Time", idle: -1, busy: -1}}},
		{name: "shorter than a millis epoch", headers: map[string]string{"X-Proxy-Time": "t=150423049276"}},
		{name: "nginx dot too early", headers: map[string]string{"X-Proxy-Time": "t=15042304.9276"}},
		{name: "nginx shape with two decimals", headers: map[string]string{"X-Proxy-Time": "t=1504230492.76"}},
		{name: "millis-length zeros", headers: map[string]string{"X-Proxy-Time": "t=0000000000000"}},
		{name: "negative D unset", headers: map[string]string{"X-Proxy-Time": "t=1504230492763 D=-5"},
			want: []proxyValues{{code: 4, receivedTime: 1504230492763, app: "X-Proxy-Time", duration: -1, idle: -1, busy: -1}}},
		{name: "missing t", headers: map[string]string{"X-Proxy-Time": "1500968753503"}},
		{name: "t=0", headers: map[string]string{"X-Proxy-Time": "t=0"}},
		{name: "negative t", headers: map[string]string{"X-Proxy-Time": "t=-1"}},
		{name: "unconfigured header", headers: map[string]string{"X-Unknown-Proxy": "t=1500968753503"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			for i := range tt.want {
				tt.want[i].key = pinpoint.AnnotationHttpProxyHeader
			}

			a := &proxyAnnotation{}
			setProxyHeader(a, header{req.Header})

			if tt.want == nil {
				assert.Empty(t, a.got)
			} else {
				assert.Equal(t, tt.want, a.got)
			}
		})
	}
}

func Test_setProxyHeader_User_unconfigured(t *testing.T) {
	startAgent(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Proxy-Time", "t=1500968753503")

	a := &proxyAnnotation{}
	setProxyHeader(a, header{req.Header})
	assert.Empty(t, a.got, "no user header names configured")
}

func Test_headerFirst(t *testing.T) {
	h := http.Header{}
	h.Add("X-Multi", "first")
	h.Add("X-Multi", "second")
	h.Set("X-Empty", "")

	assert.Equal(t, "first", headerFirst(header{h}, "X-Multi"))
	assert.Equal(t, "", headerFirst(header{h}, "X-Empty"), "a present but empty header reads as empty")
	assert.Equal(t, "", headerFirst(header{h}, "X-Missing"))
}

func Test_resolveRemoteAddr(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		remoteAddr string
		want       string
	}{
		{
			name:       "the transport peer address is stripped of its port",
			remoteAddr: "10.0.0.1:54321",
			want:       "10.0.0.1",
		},
		{
			name:       "an IPv6 peer address is stripped of its port",
			remoteAddr: "[2001:db8::1]:54321",
			want:       "2001:db8::1",
		},
		{
			name:       "an address without a port is used as is",
			remoteAddr: "10.0.0.1",
			want:       "10.0.0.1",
		},
		{
			name:       "an empty peer address stays empty",
			remoteAddr: "",
			want:       "",
		},
		{
			name:       "X-Forwarded-For wins over the peer address",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.7"},
			remoteAddr: "10.0.0.1:54321",
			want:       "203.0.113.7",
		},
		{
			name:       "the first hop of X-Forwarded-For is the client",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.7, 10.0.0.2, 10.0.0.3"},
			remoteAddr: "10.0.0.1:54321",
			want:       "203.0.113.7",
		},
		{
			name:       "X-Forwarded-For is trimmed",
			headers:    map[string]string{"X-Forwarded-For": "  203.0.113.7  , 10.0.0.2"},
			remoteAddr: "10.0.0.1:54321",
			want:       "203.0.113.7",
		},
		{
			name:       "X-Real-Ip is the fallback when X-Forwarded-For is absent",
			headers:    map[string]string{"X-Real-Ip": "203.0.113.9"},
			remoteAddr: "10.0.0.1:54321",
			want:       "203.0.113.9",
		},
		{
			name:       "X-Forwarded-For wins over X-Real-Ip",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.7", "X-Real-Ip": "203.0.113.9"},
			remoteAddr: "10.0.0.1:54321",
			want:       "203.0.113.7",
		},
		{
			name:       "an empty X-Forwarded-For falls through",
			headers:    map[string]string{"X-Forwarded-For": "", "X-Real-Ip": "203.0.113.9"},
			remoteAddr: "10.0.0.1:54321",
			want:       "203.0.113.9",
		},
		{
			name:       "an empty X-Real-Ip falls through to the peer address",
			headers:    map[string]string{"X-Real-Ip": ""},
			remoteAddr: "10.0.0.1:54321",
			want:       "10.0.0.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tt.headers {
				h.Set(k, v)
			}
			assert.Equal(t, tt.want, resolveRemoteAddr(header{h}, tt.remoteAddr, defaultRealIpHeaders, ""))
		})
	}
}

type optionalResponseWriter struct {
	flushes int
	hijacks int
	pushes  int
}

func (w *optionalResponseWriter) Flush() {
	w.flushes++
}

func (w *optionalResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacks++
	return nil, nil, errOptionalResponseWriter
}

func (w *optionalResponseWriter) Push(string, *http.PushOptions) error {
	w.pushes++
	return errOptionalResponseWriter
}

var errOptionalResponseWriter = errors.New("optional response writer called")

type (
	testResponseWriterF struct {
		http.ResponseWriter
		http.Flusher
	}
	testResponseWriterH struct {
		http.ResponseWriter
		http.Hijacker
	}
	testResponseWriterP struct {
		http.ResponseWriter
		http.Pusher
	}
	testResponseWriterFH struct {
		http.ResponseWriter
		http.Flusher
		http.Hijacker
	}
	testResponseWriterFP struct {
		http.ResponseWriter
		http.Flusher
		http.Pusher
	}
	testResponseWriterHP struct {
		http.ResponseWriter
		http.Hijacker
		http.Pusher
	}
	testResponseWriterFHP struct {
		http.ResponseWriter
		http.Flusher
		http.Hijacker
		http.Pusher
	}
)

func responseWriterWithOptionalInterfaces(base http.ResponseWriter, optional *optionalResponseWriter, mask int) http.ResponseWriter {
	switch mask {
	case 7:
		return testResponseWriterFHP{base, optional, optional, optional}
	case 6:
		return testResponseWriterHP{base, optional, optional}
	case 5:
		return testResponseWriterFP{base, optional, optional}
	case 4:
		return testResponseWriterP{base, optional}
	case 3:
		return testResponseWriterFH{base, optional, optional}
	case 2:
		return testResponseWriterH{base, optional}
	case 1:
		return testResponseWriterF{base, optional}
	default:
		return struct{ http.ResponseWriter }{base}
	}
}

func Test_responseWriter_PreservesOptionalInterfaces(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		t.Run(fmt.Sprintf("mask%03b", mask), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			optional := &optionalResponseWriter{}
			original := responseWriterWithOptionalInterfaces(recorder, optional, mask)
			status := 0
			wrapped := WrapResponseWriter(original, &status)

			flusher, flushes := wrapped.(http.Flusher)
			hijacker, hijacks := wrapped.(http.Hijacker)
			pusher, pushes := wrapped.(http.Pusher)
			require.Equal(t, mask&1 != 0, flushes, "http.Flusher must be reachable exactly when the original implements it")
			require.Equal(t, mask&2 != 0, hijacks, "http.Hijacker must be reachable exactly when the original implements it")
			require.Equal(t, mask&4 != 0, pushes, "http.Pusher must be reachable exactly when the original implements it")

			if flushes {
				flusher.Flush()
			}
			if hijacks {
				_, _, err := hijacker.Hijack()
				assert.ErrorIs(t, err, errOptionalResponseWriter, "Hijack() must reach the original writer")
			}
			if pushes {
				assert.ErrorIs(t, pusher.Push("/asset", nil), errOptionalResponseWriter, "Push() must reach the original writer")
			}
			assert.Equal(t, mask&1, optional.flushes, "delegated Flush calls")
			assert.Equal(t, (mask>>1)&1, optional.hijacks, "delegated Hijack calls")
			assert.Equal(t, (mask>>2)&1, optional.pushes, "delegated Push calls")

			wrapped.WriteHeader(http.StatusCreated)
			wantStatus := http.StatusCreated
			if flushes {
				wantStatus = http.StatusOK
			}
			assert.Equal(t, wantStatus, status, "the wrapper must publish the status sent by the original writer")
			assert.Equal(t, wantStatus, recorder.Code, "the status must still reach the original writer")

			unwrapper, ok := wrapped.(interface{ Unwrap() http.ResponseWriter })
			require.True(t, ok, "http.ResponseController needs Unwrap")
			assert.Equal(t, original, unwrapper.Unwrap(), "Unwrap must return the underlying writer")
		})
	}
}

type closeNotifyWriter struct {
	http.ResponseWriter
	closed chan bool
}

func (w closeNotifyWriter) CloseNotify() <-chan bool { return w.closed }

// gin's c.Stream asserts http.CloseNotifier unchecked, so every wrapper shape
// must have it: delegated to a writer that has it, as net/http's own do, and
// never firing for one that does not.
func Test_responseWriter_CloseNotifier(t *testing.T) {
	status := 0
	closed := make(chan bool, 1)
	wrapped := WrapResponseWriter(closeNotifyWriter{httptest.NewRecorder(), closed}, &status)
	cn, ok := wrapped.(http.CloseNotifier) //nolint:staticcheck // the interface under test
	require.True(t, ok)
	closed <- true
	assert.True(t, <-cn.CloseNotify(), "delegated to the underlying writer")

	for mask := 0; mask < 8; mask++ {
		original := responseWriterWithOptionalInterfaces(httptest.NewRecorder(), &optionalResponseWriter{}, mask)
		cn, ok := WrapResponseWriter(original, &status).(http.CloseNotifier) //nolint:staticcheck // the interface under test
		require.True(t, ok, "mask%03b", mask)
		select {
		case <-cn.CloseNotify():
			t.Fatalf("mask%03b: fired for a writer that cannot notify", mask)
		default:
		}
	}
}

// The status pointer follows the first final response status, as net/http does.
func Test_responseWriter_StatusTracking(t *testing.T) {
	t.Run("first final WriteHeader wins", func(t *testing.T) {
		rec := httptest.NewRecorder()
		status := http.StatusOK
		wrapped := WrapResponseWriter(rec, &status)

		wrapped.WriteHeader(http.StatusTeapot)
		wrapped.WriteHeader(http.StatusBadGateway)
		assert.Equal(t, http.StatusTeapot, status)
		assert.Equal(t, http.StatusTeapot, rec.Code)
	})

	t.Run("Write commits an implicit 200", func(t *testing.T) {
		rec := httptest.NewRecorder()
		status := 0
		wrapped := WrapResponseWriter(rec, &status)

		_, err := wrapped.Write([]byte("hello"))
		require.NoError(t, err)
		wrapped.WriteHeader(http.StatusInternalServerError)

		assert.Equal(t, http.StatusOK, status)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

}

type handlerNameTestHandler struct{}

func (handlerNameTestHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func handlerNameTestFunc(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func TestWrapHandler_ConcreteHandler(t *testing.T) {
	tests := []struct {
		name     string
		handler  http.Handler
		wantName string
	}{
		{name: "function", handler: http.HandlerFunc(handlerNameTestFunc), wantName: "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2.handlerNameTestFunc()"},
		{name: "value", handler: handlerNameTestHandler{}, wantName: "pphttp.handlerNameTestHandler()"},
		{name: "pointer", handler: &handlerNameTestHandler{}, wantName: "*pphttp.handlerNameTestHandler()"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantName, HandlerFuncName(tt.handler))

			rec := httptest.NewRecorder()
			WrapHandler(tt.handler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			assert.Equal(t, http.StatusNoContent, rec.Code)
		})
	}
}

func TestHandlerFuncName_Nil(t *testing.T) {
	assert.Equal(t, "<nil>()", HandlerFuncName(nil), "a nil handler must not panic in reflect")
}

// NewServeMux instruments every handler registered on it, so both registration
// forms have to keep routing to the right handler and hand it the tracer.
func TestServeMux_TracesRegisteredHandlers(t *testing.T) {
	startAgent(t)

	for _, tt := range []struct {
		name     string
		register func(*serveMux, string, func(http.ResponseWriter, *http.Request))
	}{
		{"Handle", func(m *serveMux, p string, h func(http.ResponseWriter, *http.Request)) {
			m.Handle(p, http.HandlerFunc(h))
		}},
		{"HandleFunc", func(m *serveMux, p string, h func(http.ResponseWriter, *http.Request)) {
			m.HandleFunc(p, h)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mux := NewServeMux()
			var tracer pinpoint.Tracer
			tt.register(mux, "/hello", func(w http.ResponseWriter, r *http.Request) {
				tracer = pinpoint.TracerFromRequestContext(r)
				w.WriteHeader(http.StatusTeapot)
				_, _ = w.Write([]byte("hello"))
			})

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/hello", nil)
			req.Host = "myhost:8080"
			req.RemoteAddr = "10.0.0.1:4242"
			mux.ServeHTTP(rec, req)

			require.NotNil(t, tracer, "the handler did not run")
			assert.True(t, tracer.IsSampled(), "handler received an unsampled tracer")
			assert.Equal(t, http.StatusTeapot, rec.Code)
			assert.Equal(t, "hello", rec.Body.String())

			span := spanOf(t, tracer)
			assert.Equal(t, "/hello", span.RpcName, "the span's RPC name is the request path")
			assert.Equal(t, "myhost:8080", span.EndPoint, "the span's endpoint is the request Host")
			assert.Equal(t, "10.0.0.1", span.RemoteAddr, "the span's remote address is the peer, without its port")
		})
	}
}

// The mux routes on the registered pattern while the span names itself after
// the concrete path, so a wildcard route must not collapse every request into
// one span name.
func TestServeMux_SpanNameIsTheRequestPath(t *testing.T) {
	startAgent(t)

	mux := NewServeMux()
	var tracer pinpoint.Tracer
	mux.HandleFunc("/users/", func(w http.ResponseWriter, r *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(r)
	})

	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/users/42/profile", nil))

	require.NotNil(t, tracer)
	assert.Equal(t, "/users/42/profile", spanOf(t, tracer).RpcName)
}

// A request the config excludes must produce a noop tracer: the handler still
// runs and answers normally, but nothing is traced.
func TestExcludedRequestsAreNotTraced(t *testing.T) {
	startAgent(t,
		WithHttpServerExcludeUrl([]string{"/health", "/static/**"}),
		WithHttpServerExcludeMethod([]string{"options"}),
	)

	tests := []struct {
		name        string
		method      string
		path        string
		wantSampled bool
	}{
		{name: "an excluded exact url", method: http.MethodGet, path: "/health"},
		{name: "an excluded url pattern", method: http.MethodGet, path: "/static/js/app.js"},
		{name: "an excluded method", method: http.MethodOptions, path: "/api"},
		{name: "excluded matching is case-insensitive on the method", method: "options", path: "/api"},
		{name: "a traced request", method: http.MethodGet, path: "/api", wantSampled: true},
		{name: "the method filter does not exclude other methods", method: http.MethodPost, path: "/api", wantSampled: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tracer pinpoint.Tracer
			h := WrapHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tracer = pinpoint.TracerFromRequestContext(r)
				w.WriteHeader(http.StatusNoContent)
			}))

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			require.NotNil(t, tracer, "the handler must run whether or not the request is traced")
			assert.Equal(t, tt.wantSampled, tracer.IsSampled())
			assert.Equal(t, http.StatusNoContent, rec.Code, "excluding a request must not change the response")
		})
	}
}

// The status code annotation is what the Pinpoint UI shows, and the configured
// error classes are what turn a span red.
func TestRecordHttpServerResponse(t *testing.T) {
	startAgent(t, WithHttpServerStatusCodeError([]string{"5xx", "302"}))

	tests := []struct {
		status string
		code   int
		// wantErr is the whole PSpan.err mask: an error class is recorded under
		// ErrorCategory.HTTP_STATUS, and nothing else fails the span here.
		wantErr int
	}{
		{status: "200 is not an error", code: http.StatusOK},
		{status: "404 is not configured as an error", code: http.StatusNotFound},
		{status: "500 falls in the configured 5xx class", code: http.StatusInternalServerError,
			wantErr: int(pinpoint.ErrorCategoryHttpStatus)},
		{status: "302 is configured on its own", code: http.StatusFound,
			wantErr: int(pinpoint.ErrorCategoryHttpStatus)},
		{status: "301 is not 302", code: http.StatusMovedPermanently},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			var tracer pinpoint.Tracer
			h := WrapHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tracer = pinpoint.TracerFromRequestContext(r)
				w.WriteHeader(tt.code)
			})
			h(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

			require.NotNil(t, tracer)
			span := spanOf(t, tracer)
			assert.Equal(t, tt.wantErr, span.Err,
				"status %d should%s fail the span", tt.code, map[bool]string{true: "", false: " not"}[tt.wantErr != 0])
			assert.Contains(t, span.annotations(pinpoint.AnnotationHttpStatusCode, "IntValue"), float64(tt.code),
				"the status code must be annotated on the span")
		})
	}
}

// Span.ErrorMarkExclude drops one cause of failure and nothing else: a 5xx is
// still annotated and still classified as an error class here, it just does not
// mark the transaction as failed.
func TestRecordHttpServerResponse_ErrorMarkExcludeKeepsA5xxSuccessful(t *testing.T) {
	startAgent(t, WithHttpServerStatusCodeError([]string{"5xx"}),
		pinpoint.WithSpanErrorMarkExclude("http-status"))

	var tracer pinpoint.Tracer
	h := WrapHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(r)
		w.WriteHeader(http.StatusInternalServerError)
	})
	h(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	require.NotNil(t, tracer)
	span := spanOf(t, tracer)
	assert.Equal(t, 0, span.Err, "an excluded cause must not fail the span")
	assert.Contains(t, span.annotations(pinpoint.AnnotationHttpStatusCode, "IntValue"),
		float64(http.StatusInternalServerError), "the status code is still annotated")
}

// A recorded response header is read off the writer the handler wrote to, so
// the wrapper has to hand the real header map to the recorder.
func TestWrapHandler_RecordsConfiguredHeaders(t *testing.T) {
	startAgent(t,
		WithHttpServerRecordRequestHeader([]string{"X-Req"}),
		WithHttpServerRecordRespondHeader([]string{"X-Res"}),
		WithHttpServerRecordRequestCookie([]string{"session"}),
	)

	var tracer pinpoint.Tracer
	h := WrapHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(r)
		w.Header().Set("X-Res", "res-value")
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Req", "req-value")
	req.Header.Set("X-Ignored", "nope")
	req.AddCookie(&http.Cookie{Name: "session", Value: "s1"})
	req.AddCookie(&http.Cookie{Name: "ignored", Value: "nope"})
	h(httptest.NewRecorder(), req)

	require.NotNil(t, tracer)
	annotations := string(tracer.JsonString())
	assert.Contains(t, annotations, "req-value", "the configured request header must be recorded")
	assert.Contains(t, annotations, "res-value", "the configured response header must be recorded")
	assert.Contains(t, annotations, "s1", "the configured cookie must be recorded")
	assert.NotContains(t, annotations, "nope", "headers and cookies that are not configured must be left out")
}

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash net/http reports into a silent 200.
func TestWrapHandler_PanicPropagates(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	h := WrapHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(r)
		panic("boom")
	}))

	assert.PanicsWithValue(t, "boom", func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
	}, "the wrapper swallowed the handler panic")

	require.NotNil(t, tracer)
	assert.NotZero(t, spanOf(t, tracer).Err, "a panicking handler must fail the span")
}

// With no agent running the wrapper must be a straight pass-through.
func TestWrapHandler_PassesThroughWhenAgentDisabled(t *testing.T) {
	called := false
	h := WrapHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		assert.False(t, pinpoint.TracerFromRequestContext(r).IsSampled(), "a disabled agent produced a sampled tracer")
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello", nil))

	require.True(t, called, "the handler did not run")
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// RecordHttpHandlerError is how framework adapters report an error a handler
// returned instead of panicking; the option turns it off.
func TestRecordHttpHandlerError(t *testing.T) {
	for _, tt := range []struct {
		name    string
		record  bool
		wantErr bool
	}{
		{name: "recorded by default", record: true, wantErr: true},
		{name: "suppressed by the option", record: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			startAgent(t, WithHttpServerRecordHandlerError(tt.record))

			var tracer pinpoint.Tracer
			h := WrapHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tracer = pinpoint.TracerFromRequestContext(r)
				RecordHttpHandlerError(tracer, errors.New("handler failed"))
			})
			h(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

			require.NotNil(t, tracer)
			assert.Equal(t, tt.wantErr, spanOf(t, tracer).Err != 0)
		})
	}
}

// A pattern registered on the mux is collected as a URL statistic; WrapHandler
// has no pattern to report and must not collect one.
func TestCollectUrlStat(t *testing.T) {
	startAgent(t, pinpoint.WithHttpUrlStatEnable(true))

	var tracer pinpoint.Tracer
	mux := NewServeMux()
	mux.HandleFunc("/users/", func(w http.ResponseWriter, r *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(r)
	})
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/users/42", nil))
	require.NotNil(t, tracer)
	assert.True(t, tracer.IsSampled())
}

type metricCountingTracer struct {
	pinpoint.Tracer
	metrics int
}

func (t *metricCountingTracer) AddMetric(string, interface{}) { t.metrics++ }

// URL stats are off by default; CollectUrlStat must not build an entry that
// every consumer would drop, and IsUrlStatEnabled must let plugins skip their
// route-pattern lookup for the same reason.
func TestCollectUrlStat_GatedByUrlStatEnable(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		startAgent(t)
		tracer := &metricCountingTracer{Tracer: pinpoint.NoopTracer()}
		CollectUrlStat(tracer, "/users/", http.MethodGet, 200)
		assert.False(t, IsUrlStatEnabled())
		assert.Equal(t, 0, tracer.metrics, "a URL stat was reported while disabled")
	})

	t.Run("enabled", func(t *testing.T) {
		startAgent(t, pinpoint.WithHttpUrlStatEnable(true))
		tracer := &metricCountingTracer{Tracer: pinpoint.NoopTracer()}
		CollectUrlStat(tracer, "/users/", http.MethodGet, 200)
		assert.True(t, IsUrlStatEnabled())
		assert.Equal(t, 1, tracer.metrics, "the URL stat was not reported while enabled")
	})
}

// A proxy that blanks Pinpoint-SpanID instead of dropping it must not split the
// trace: net/http.Header keeps the header in its map, so the agent can tell it
// from an absent one and continues the caller's transaction. This is the path
// req.Header takes, and the one a blanking gateway actually breaks.
func TestNewHttpServerTracer_BlankSpanIdHeaderContinuesTheTrace(t *testing.T) {
	startAgent(t)

	caller := pinpoint.GetAgent().NewSpanTracer("HTTP Server", "/caller")
	caller.NewSpanEvent("call")
	outgoing := httptest.NewRequest(http.MethodGet, "/callee", nil)
	caller.Inject(outgoing.Header)
	caller.EndSpanEvent()
	defer caller.EndSpan()

	outgoing.Header.Set(pinpoint.HeaderSpanId, "")
	server := NewHttpServerTracer(outgoing, "HTTP Server")
	defer server.EndSpan()

	require.True(t, server.IsSampled())
	assert.Equal(t, caller.TransactionId().String(), server.TransactionId().String(),
		"a blanked span id header must not start a new transaction")

	// Dropped, not blanked, is the case that does start a new transaction.
	outgoing.Header.Del(pinpoint.HeaderSpanId)
	dropped := NewHttpServerTracer(outgoing, "HTTP Server")
	defer dropped.EndSpan()
	assert.NotEqual(t, caller.TransactionId().String(), dropped.TransactionId().String(),
		"an absent span id header starts a new transaction")
}

// spanJson is the subset of a span JsonString asserts against.
type spanJson struct {
	RpcName     string        `json:"RpcName"`
	EndPoint    string        `json:"EndPoint"`
	RemoteAddr  string        `json:"RemoteAddr"`
	Err         int           `json:"Err"`
	Annotations []interface{} `json:"Annotations"`
}

// annotations returns every value annotated under key, read off the given
// leaf ("IntValue" or "StringValue"). The annotation list is untyped JSON -
// {"key":46,"value":{"Field":{"IntValue":500}}} - so each entry is matched on
// its key and then unwrapped; numbers come back as float64.
func (s spanJson) annotations(key int32, leaf string) []any {
	var values []any
	for _, a := range s.Annotations {
		m, ok := a.(map[string]interface{})
		if !ok {
			continue
		}
		if k, ok := m["key"].(float64); !ok || int32(k) != key {
			continue
		}
		value, _ := m["value"].(map[string]interface{})
		field, _ := value["Field"].(map[string]interface{})
		if v, ok := field[leaf]; ok {
			values = append(values, v)
		}
	}
	return values
}

func spanOf(t *testing.T, tracer pinpoint.Tracer) spanJson {
	t.Helper()
	var s spanJson
	require.NoError(t, json.Unmarshal(tracer.JsonString(), &s))
	return s
}

func TestRecordHttpServerRequest_Query(t *testing.T) {
	tests := []struct {
		name   string
		record bool
		url    string
		want   []any
	}{
		{"off by default", false, "/p?a=1&b=x%20y&empty=", nil},
		{"on", true, "/p?a=1&b=x%20y&empty=", []any{"a=1&b=x y&empty="}},
		{"on, no query", true, "/p", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts []pinpoint.ConfigOption
			if tt.record {
				opts = append(opts, WithHttpServerRecordRequestParam(true))
			}
			startAgent(t, opts...)
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			tracer := NewHttpServerTracer(req, "test")
			defer tracer.EndSpan()

			assert.Equal(t, tt.want, spanOf(t, tracer).annotations(pinpoint.AnnotationHttpParam, "StringValue"))
		})
	}
}

var defaultRealIpHeaders = makeProxyUserHeaderNames([]string{"X-Forwarded-For", "X-Real-Ip"})

func Test_resolveRemoteAddr_Configured(t *testing.T) {
	tests := []struct {
		name       string
		cfg        []string
		emptyValue string
		headers    map[string]string
		want       string
	}{
		{name: "custom order picks CF-Connecting-IP over a present XFF",
			cfg:     []string{"CF-Connecting-IP", "X-Forwarded-For"},
			headers: map[string]string{"CF-Connecting-IP": "1.1.1.1", "X-Forwarded-For": "2.2.2.2"}, want: "1.1.1.1"},
		{name: "an empty list trusts no header",
			cfg: []string{}, headers: map[string]string{"X-Forwarded-For": "2.2.2.2"}, want: "10.0.0.1"},
		{name: "the empty value skips a header and falls to the next",
			cfg: []string{"X-Forwarded-For", "X-Real-Ip"}, emptyValue: "unknown",
			headers: map[string]string{"X-Forwarded-For": "Unknown, 3.3.3.3", "X-Real-Ip": "4.4.4.4"}, want: "4.4.4.4"},
		{name: "Forwarded: first element's for=",
			cfg:     []string{"Forwarded"},
			headers: map[string]string{"Forwarded": "for=1.2.3.4;proto=https, for=10.0.0.1"}, want: "1.2.3.4"},
		{name: "Forwarded: quoted bracketed IPv6 keeps its brackets, loses its port",
			cfg:     []string{"Forwarded"},
			headers: map[string]string{"Forwarded": `for="[2001:db8::1]:4711"`}, want: "[2001:db8::1]"},
		{name: "Forwarded: For= is case-insensitive and the port is stripped",
			cfg:     []string{"Forwarded"},
			headers: map[string]string{"Forwarded": "For=192.0.2.60:8080"}, want: "192.0.2.60"},
		{name: "Forwarded without for= is skipped",
			cfg:     []string{"Forwarded"},
			headers: map[string]string{"Forwarded": "proto=https"}, want: "10.0.0.1"},
		{name: "a lowercase forwarded config name is still parsed as Forwarded",
			cfg:     []string{"forwarded"},
			headers: map[string]string{"Forwarded": "for=1.2.3.4"}, want: "1.2.3.4"},
		{name: "Forwarded without for= falls to X-Real-Ip",
			cfg:     []string{"Forwarded", "X-Real-Ip"},
			headers: map[string]string{"Forwarded": "proto=https", "X-Real-Ip": "4.4.4.4"}, want: "4.4.4.4"},
		{name: "malformed Forwarded values never panic",
			cfg:     []string{"Forwarded", "X-Real-Ip"},
			headers: map[string]string{"Forwarded": `;;for=;for="";=;,`, "X-Real-Ip": "4.4.4.4"}, want: "4.4.4.4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tt.headers {
				h.Set(k, v)
			}
			got := resolveRemoteAddr(header{h}, "10.0.0.1:54321", makeProxyUserHeaderNames(tt.cfg), tt.emptyValue)
			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_forwardedFor(t *testing.T) {
	assert.Equal(t, "", forwardedFor(""))
	assert.Equal(t, "", forwardedFor("for"))
	// RFC 7239 requires brackets around IPv6; a bare one is cut at its last ':'.
	assert.Equal(t, ":", forwardedFor("for=::1"))
	assert.Equal(t, "[::1]", forwardedFor("for=[::1]:80"))
	assert.Equal(t, "_hidden", forwardedFor(" by=proxy ; FOR = _hidden "))
}

// A reload changing the header list reaches the next recorded request.
func TestRecordHttpServerRequest_RealIpHeaderFollowsReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	write := func(body string) { require.NoError(t, os.WriteFile(path, []byte(body), 0o600)) }
	write("Http:\n  Server:\n    RealIpHeader: [X-Forwarded-For]\n")
	startAgent(t, pinpoint.WithConfigFile(path))

	record := func() string {
		req := httptest.NewRequest(http.MethodGet, "/p", nil)
		req.RemoteAddr = "10.0.0.1:54321"
		req.Header.Set("X-Forwarded-For", "2.2.2.2")
		req.Header.Set("CF-Connecting-IP", "1.1.1.1")
		tracer := NewHttpServerTracer(req, "test")
		defer tracer.EndSpan()
		return spanOf(t, tracer).RemoteAddr
	}
	require.Equal(t, "2.2.2.2", record())

	write("Http:\n  Server:\n    RealIpHeader: [CF-Connecting-IP]\n")
	require.Eventually(t, func() bool { return record() == "1.1.1.1" }, 3*time.Second, 10*time.Millisecond)

	write("Http:\n  Server:\n    RealIpHeader: []\n")
	require.Eventually(t, func() bool { return record() == "10.0.0.1" }, 3*time.Second, 10*time.Millisecond)
}

func TestRealIpOptions(t *testing.T) {
	startAgent(t)
	cfg := httpCfg()
	assert.Equal(t, defaultRealIpHeaders, cfg.srvRealIpHeaders)
	assert.Equal(t, "", cfg.srvRealIpEmptyValue)

	startAgent(t, WithHttpServerRealIpHeader([]string{" cf-connecting-ip ", "", "forwarded"}), WithHttpServerRealIpEmptyValue("unknown"))
	cfg = httpCfg()
	assert.Equal(t, []string{"Cf-Connecting-Ip", "Forwarded"}, cfg.srvRealIpHeaders)
	assert.Equal(t, "unknown", cfg.srvRealIpEmptyValue)

	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("Http:\n  Server:\n    RealIpHeader: []\n"), 0o600))
	t.Setenv("PINPOINT_GO_HTTP_SERVER_REALIPEMPTYVALUE", "none")
	startAgent(t, pinpoint.WithConfigFile(path))
	cfg = httpCfg()
	assert.Empty(t, cfg.srvRealIpHeaders, "[] parses to empty")
	assert.Equal(t, "none", cfg.srvRealIpEmptyValue)

	t.Setenv("PINPOINT_GO_HTTP_SERVER_REALIPHEADER", "True-Client-IP,X-Real-Ip")
	startAgent(t)
	assert.Equal(t, []string{"True-Client-Ip", "X-Real-Ip"}, httpCfg().srvRealIpHeaders)
}

// A request whose context already carries a tracer — WrapHandler inside
// WrapHandler here; a framework middleware inside a wrapped handler, or
// compile-time instrumentation outside the wrapper, in practice — is one
// request and gets one span: the inner layer records its span event on the
// outer's span, records no second status, and its EndSpan does not end it
// (Java: DefaultTraceFactory.checkAndGet keeps the existing Trace).
func TestWrapHandler_ReusesTheContextTracer(t *testing.T) {
	startAgent(t)

	var outer, inner pinpoint.Tracer
	handler := WrapHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outer = pinpoint.FromContext(r.Context())
		WrapHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inner = pinpoint.FromContext(r.Context())
			w.WriteHeader(http.StatusNotFound)
		})).ServeHTTP(w, r)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nested", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)

	require.NotNil(t, outer)
	require.NotNil(t, inner)
	assert.False(t, pinpoint.IsNestedTracer(outer), "the outer wrapper owns the span")
	assert.True(t, pinpoint.IsNestedTracer(inner), "the inner wrapper must reuse the outer's span")
	assert.Equal(t, outer.TransactionId().String(), inner.TransactionId().String())
	assert.Equal(t, outer.SpanId(), inner.SpanId(), "one request, one span")

	var span map[string]interface{}
	require.NoError(t, json.Unmarshal(outer.JsonString(), &span))
	assert.Equal(t, "/nested", span["RpcName"])
	statusCodes := 0
	for _, a := range span["Annotations"].([]interface{}) {
		if key, _ := a.(map[string]interface{})["key"].(float64); int(key) == pinpoint.AnnotationHttpStatusCode {
			statusCodes++
		}
	}
	assert.Equal(t, 1, statusCodes, "the status is recorded once, by the layer that owns the span")
}

func TestFormatRequestParams(t *testing.T) {
	long := strings.Repeat("v", 100)
	var many []string
	for i := 0; i < 100; i++ {
		many = append(many, "k"+strings.Repeat("0", 2)+"=vvvvvvvvvv")
	}

	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"plain", "a=1&b=x%20y&empty=", "a=1&b=x y&empty="},
		{"blank items skipped", "&&a=1&&", "a=1"},
		{"bare key", "a+b=c+d&flag", "a b=c d&flag="},
		{"undecodable kept verbatim", "bad=%zz%4&ok=%41", "bad=%zz%4&ok=A"},
		{"long value cut", "k=" + long, "k=" + strings.Repeat("v", 64) + "..."},
		{"long key cut", long + "=1", strings.Repeat("v", 64) + "...=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FormatRequestParams(tt.in))
		})
	}

	got := FormatRequestParams(strings.Join(many, "&"))
	assert.True(t, strings.HasSuffix(got, "&..."), got)
	assert.LessOrEqual(t, len(got), 512+4)
}

func TestClientUrl(t *testing.T) {
	u, err := url.Parse("https://h/p?token=x#frag")
	require.NoError(t, err)

	startAgent(t)
	assert.Equal(t, "GET https://h/p#frag", ClientUrl("GET", u))
	assert.Equal(t, "GET https://h/p#frag", ClientUrlString("GET", "https://h/p?token=x#frag"))
	assert.Equal(t, "GET https://h/p", ClientUrlString("GET", "https://h/p?token=x"))
	assert.Equal(t, "GET https://h/p", ClientUrlString("GET", "https://h/p"))
	assert.Equal(t, "GET", ClientUrl("GET", nil))
	assert.Equal(t, "https://h/p?token=x#frag", u.String(), "the caller's URL must not be modified")

	startAgent(t, WithHttpClientRecordUrlQuery(true))
	assert.Equal(t, "GET https://h/p?token=x#frag", ClientUrl("GET", u))
	assert.Equal(t, "GET https://h/p?token=x#frag", ClientUrlString("GET", "https://h/p?token=x#frag"))

	// A password in the URL's userinfo is masked in the recorded URL.
	u, err = url.Parse("https://user:secret@example.com/p?q=1")
	require.NoError(t, err)
	got := ClientUrl(http.MethodGet, u)
	assert.NotContains(t, got, "secret")
	assert.Contains(t, got, "user:xxxxx@example.com/p")
}

// A nil handler is refused at registration, as net/http's ServeMux refuses
// it: wrapped into a live HandlerFunc it registered fine and panicked on
// every request instead.
func TestServeMux_NilHandlerPanicsAtRegistration(t *testing.T) {
	mux := NewServeMux()
	assert.PanicsWithValue(t, "http: nil handler", func() { mux.Handle("/x", nil) })
	assert.PanicsWithValue(t, "http: nil handler", func() { mux.HandleFunc("/y", nil) })
	assert.PanicsWithValue(t, "http: nil handler", func() { WrapHandler(nil) })
}
