package pphttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

type proxyValues struct {
	called       bool
	key          int32
	receivedTime int64
	code         int32
	duration     int32
	idle         int32
	busy         int32
	app          string
}

// proxyAnnotation captures the single proxy header annotation setProxyHeader records.
type proxyAnnotation struct {
	got proxyValues
}

func (a *proxyAnnotation) AppendLongIntIntByteByteString(key int32, l int64, i1 int32, i2 int32, b1 int32, b2 int32, s string) {
	a.got = proxyValues{true, key, l, i1, i2, b1, b2, s}
}

func (a *proxyAnnotation) AppendInt(int32, int32)                                {}
func (a *proxyAnnotation) AppendLong(int32, int64)                               {}
func (a *proxyAnnotation) AppendString(int32, string)                            {}
func (a *proxyAnnotation) AppendStringString(int32, string, string)              {}
func (a *proxyAnnotation) AppendIntStringString(int32, int32, string, string)    {}
func (a *proxyAnnotation) AppendBytesStringString(int32, []byte, string, string) {}

func Test_setProxyHeader(t *testing.T) {
	tests := []struct {
		name   string
		header string
		value  string
		want   proxyValues
	}{
		{name: "no proxy header"},

		{name: "apache", header: "Pinpoint-ProxyApache", value: "t=1500968753503 D=125 i=51 b=48",
			want: proxyValues{called: true, code: 3, receivedTime: 1500968753, duration: 125, idle: 51, busy: 48}},
		{name: "apache bare token", header: "Pinpoint-ProxyApache", value: "t",
			want: proxyValues{called: true, code: 3}},
		{name: "apache bare token before valid ones", header: "Pinpoint-ProxyApache", value: "t D=125 junk i=51 b",
			want: proxyValues{called: true, code: 3, duration: 125, idle: 51}},
		{name: "apache empty values", header: "Pinpoint-ProxyApache", value: "t= D= i= b=",
			want: proxyValues{called: true, code: 3}},
		{name: "apache extra spaces", header: "Pinpoint-ProxyApache", value: "  t=1500968753503   D=125  ",
			want: proxyValues{called: true, code: 3, receivedTime: 1500968753, duration: 125}},

		{name: "nginx", header: "Pinpoint-ProxyNginx", value: "t=1504164327.484 D=0.000",
			want: proxyValues{called: true, code: 2, receivedTime: 1504164327484}},
		{name: "nginx bare token", header: "Pinpoint-ProxyNginx", value: "t",
			want: proxyValues{called: true, code: 2}},
		{name: "nginx bare token before valid one", header: "Pinpoint-ProxyNginx", value: "t D=7",
			want: proxyValues{called: true, code: 2, duration: 7}},
		{name: "nginx empty values", header: "Pinpoint-ProxyNginx", value: "t= D=",
			want: proxyValues{called: true, code: 2}},
		{name: "nginx NaN", header: "Pinpoint-ProxyNginx", value: "t=NaN",
			want: proxyValues{called: true, code: 2}},
		{name: "nginx +Inf", header: "Pinpoint-ProxyNginx", value: "t=Inf",
			want: proxyValues{called: true, code: 2}},
		{name: "nginx -Inf", header: "Pinpoint-ProxyNginx", value: "t=-Inf",
			want: proxyValues{called: true, code: 2}},
		{name: "nginx overflow", header: "Pinpoint-ProxyNginx", value: "t=1e400",
			want: proxyValues{called: true, code: 2}},
		{name: "nginx negative overflow", header: "Pinpoint-ProxyNginx", value: "t=-1e400",
			want: proxyValues{called: true, code: 2}},
		{name: "nginx out of int64 range", header: "Pinpoint-ProxyNginx", value: "t=1e300",
			want: proxyValues{called: true, code: 2}},
		{name: "nginx unparsable", header: "Pinpoint-ProxyNginx", value: "t=abc",
			want: proxyValues{called: true, code: 2}},

		{name: "app", header: "Pinpoint-ProxyApp", value: "t=1500968753503 app=foo-bar",
			want: proxyValues{called: true, code: 1, receivedTime: 1500968753503, app: "foo-bar"}},
		{name: "app bare token", header: "Pinpoint-ProxyApp", value: "app",
			want: proxyValues{called: true, code: 1}},
		{name: "app bare token before valid one", header: "Pinpoint-ProxyApp", value: "app t=1500968753503",
			want: proxyValues{called: true, code: 1, receivedTime: 1500968753503}},
		{name: "app empty values", header: "Pinpoint-ProxyApp", value: "t= app=",
			want: proxyValues{called: true, code: 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.header != "" {
				req.Header.Set(tt.header, tt.value)
			}

			want := tt.want
			if want.called {
				want.key = pinpoint.AnnotationHttpProxyHeader
			}

			a := &proxyAnnotation{}
			setProxyHeader(a, req)

			if a.got != want {
				t.Errorf("%s: %q\n got: %+v\nwant: %+v", tt.header, tt.value, a.got, want)
			}
		})
	}
}
