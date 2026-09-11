/*
 * Copyright 2020-present NAVER Corp.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Locked parity invariants.
//
// This is the plugin/http half of the agent's parity lock suite. The four
// proxy request parsers live in this package, which imports the agent
// package, so the group that covers them cannot sit in
// java_parity_lock_test.go (package pinpoint) without an import cycle. The
// group keeps its number and its title on both sides, and the C++ agent
// mirrors it as group 14 of test/test_java_parity_lock.cpp.
//
// Everything said in the header of java_parity_lock_test.go applies here: a
// failure means either the change is wrong, or all three implementations and
// doc/java_parity.md move together.
//
// Groups:
//  14  proxy request header pipeline
//
// The parent half of group 14 - PParentInfo is emitted only for a non-empty
// parent application name - is reachable from package pinpoint and is locked
// there, in Test_javaParityLock_ParentInfoRequiresAParentAppName.

package pphttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ===========================================================================
// Group 14 - proxy request header pipeline
// ===========================================================================

// parityProxyRequest runs the whole proxy pipeline over one request and hands
// back the proxy annotations it recorded, in order. proxyAnnotation and
// proxyValues are the recorders server_test.go already defines.
func parityProxyRequest(headers map[string]string) []proxyValues {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	a := &proxyAnnotation{}
	setProxyHeader(a, header{req.Header})
	return a.got
}

// Test_javaParityLock_ProxyParsersRunIndependently locks that all four
// parsers - apache, nginx, app and the configured user headers - run on every
// request and record independently (plugin/http/server.go:155-170), so a
// request that crossed two proxies produces two annotations rather than only
// the hop nearest the agent. Java does the same: DefaultProxyRequestRecorder
// .record (DefaultProxyRequestRecorder.java:52-53) loops over every
// registered parser and parseHeaderAndRecord records one annotation per valid
// header. A first-match if/else chain would report one hop and silently drop
// the rest of the chain.
func Test_javaParityLock_ProxyParsersRunIndependently(t *testing.T) {
	usePluginConfig(t, WithHttpServerProxyUserHeaderNames([]string{"X-Proxy-Time"}))

	got := parityProxyRequest(map[string]string{
		"Pinpoint-ProxyApache": "t=1000000000000 D=100 i=5 b=95",
		"Pinpoint-ProxyNginx":  "t=2000000.000 D=0.200",
		"Pinpoint-ProxyApp":    "t=3000000000000 app=OtherApp",
		"X-Proxy-Time":         "t=4000000000000",
	})

	codes := make([]int32, 0, len(got))
	for _, v := range got {
		assert.Equal(t, int32(pinpoint.AnnotationHttpProxyHeader), v.key)
		codes = append(codes, v.code)
	}
	assert.Equal(t, []int32{proxyTypeApache, proxyTypeNginx, proxyTypeApp, proxyTypeUser}, codes,
		"one annotation per valid proxy header, in pipeline order")
}

// Test_javaParityLock_ProxyHeaderNeedsAPositiveReceivedTime locks that every
// parser is gated on a positive received time (plugin/http/server.go:172-181):
// no t=, t=0 or a t= that does not parse records nothing at all. Java reaches
// the same place from the other side - each parser calls setValid(false) and
// DefaultProxyRequestRecorder (DefaultProxyRequestRecorder.java:71) records
// only a header whose isValid() holds.
//
// An annotation with a received time of 0 is worse than no annotation: the
// web UI charts the proxy-to-agent gap from that field, and 0 draws the hop
// at the epoch. Test_setProxyHeader (server_test.go) carries the full
// per-parser matrix; one case per parser per failure mode is locked here.
func Test_javaParityLock_ProxyHeaderNeedsAPositiveReceivedTime(t *testing.T) {
	usePluginConfig(t, WithHttpServerProxyUserHeaderNames([]string{"X-Proxy-Time"}))

	tests := []struct {
		name   string
		header string
		value  string
	}{
		{"apache without t", "Pinpoint-ProxyApache", "D=1500 i=10 b=90"},
		{"apache t=0", "Pinpoint-ProxyApache", "t=0 D=1500"},
		{"apache unparseable t", "Pinpoint-ProxyApache", "t=abc D=1500"},
		{"nginx without t", "Pinpoint-ProxyNginx", "D=0.123"},
		{"nginx t=0", "Pinpoint-ProxyNginx", "t=0.000 D=0.123"},
		{"nginx unparseable t", "Pinpoint-ProxyNginx", "t=abc D=0.123"},
		{"app without t", "Pinpoint-ProxyApp", "app=MyApp"},
		{"app t=0", "Pinpoint-ProxyApp", "t=0 app=MyApp"},
		{"app unparseable t", "Pinpoint-ProxyApp", "t=abc app=MyApp"},
		{"user without t", "X-Proxy-Time", "1500968753503"},
		{"user t=0", "X-Proxy-Time", "t=0000000000000"},
		{"user unparseable t", "X-Proxy-Time", "t=abc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Empty(t, parityProxyRequest(map[string]string{tc.header: tc.value}),
				"%s: %q must record no annotation", tc.header, tc.value)
		})
	}
}

// Test_javaParityLock_ProxyNginxTimestampsAreExactThreeDecimals locks the
// nginx time format: t= ($msec) and D= ($request_time) are seconds with
// exactly three decimals, and both are converted with integer arithmetic
// (nginxMillis, plugin/http/server.go:233-243). Java does it the same way -
// NginxRequestParser.toReceivedTimeMillis / toDurationTimeMicros
// (NginxRequestParser.java:74-110) reject a value whose
// `length - millisPosition != 4` and convert by deleting the '.' and parsing
// the result, never by scaling a double.
//
// That is what makes the value exact: 1504230492.763 has no binary
// representation, so parsing it as a float and multiplying by 1000 lands on
// 1504230492762.99 and truncates a millisecond away.
//
// (Not locked, and noted for the cross-agent review: a negative nginx D= is
// recorded here as a negative duration, where Java's
// `durationTimeMicroseconds > 0` guard leaves the duration unset. The
// received time is gated in both; the duration is not gated here.)
func Test_javaParityLock_ProxyNginxTimestampsAreExactThreeDecimals(t *testing.T) {
	usePluginConfig(t)

	got := parityProxyRequest(map[string]string{"Pinpoint-ProxyNginx": "t=1504230492.763 D=0.123"})
	require.Len(t, got, 1)
	assert.Equal(t, int64(1504230492763), got[0].receivedTime,
		"sec.mmm read as an exact integer count of milliseconds")
	assert.Equal(t, int32(123000), got[0].duration, "0.123s is exactly 123000us, not 122999")

	for _, tc := range []struct {
		value string
		want  int64
	}{
		{"1504230492.763", 1504230492763},
		{"0.000", 0},
		{"0.001", 1},
		{"1504230492.76", 0},
		{"1504230492.7634", 0},
		{"1504230492", 0},
		{"abc", 0},
		{"", 0},
	} {
		assert.Equal(t, tc.want, nginxMillis(tc.value), "nginxMillis(%q)", tc.value)
	}

	// A t= that is not sec.mmm leaves no received time, so the whole header
	// is discarded.
	for _, bad := range []string{"1504230492.76", "1504230492", "1504230492.7634"} {
		assert.Empty(t, parityProxyRequest(map[string]string{"Pinpoint-ProxyNginx": "t=" + bad + " D=0.123"}),
			"t=%s is not sec.mmm", bad)
	}

	// A D= that is not sec.mmm records no duration rather than a guess - the
	// plain microsecond integer apache sends included, which read as seconds
	// would inflate the duration a millionfold.
	for _, bad := range []string{"0.1", "0.12", "0.1234", "123", "abc"} {
		other := parityProxyRequest(map[string]string{"Pinpoint-ProxyNginx": "t=1504230492.763 D=" + bad})
		if assert.Len(t, other, 1, "D=%s must not discard the header", bad) {
			assert.Equal(t, int32(-1), other[0].duration, "D=%s is not sec.mmm: unset (-1)", bad)
		}
	}
}

// Test_javaParityLock_ProxyUserHeaderInfersItsWriter locks the user proxy
// parser. A header named in Http.Server.ProxyUserHeaderNames may have been
// written by any of the three proxies, so UserRequestParser.toReceivedTimeMillis
// infers the format from the value's shape: fewer than 13 characters is not a
// millisecond epoch and is rejected; 16 or more is apache's microseconds,
// converted by dropping the last three digits before parsing; a '.' at index
// 10 or later is nginx's sec.mmm; anything else is an app's milliseconds.
// toDurationTimeMicros reads D= the same way - a '.' means fractional
// seconds, otherwise a microsecond count - and, like every parser, applies
// it only when positive. userReceivedTimeMillis / userDurationMicros
// (plugin/http/server.go) and the C++ agent's parseProxyUserReceivedTimeMillis
// / parseProxyUserDurationMicros (src/http.cpp) are the same function.
//
// Reading t= as plain milliseconds would put an apache hop 47,000 years out
// and drop an nginx hop whole, since "1504230492.763" does not parse.
func Test_javaParityLock_ProxyUserHeaderInfersItsWriter(t *testing.T) {
	usePluginConfig(t, WithHttpServerProxyUserHeaderNames([]string{"X-Proxy-Time"}))

	for _, tc := range []struct {
		name         string
		value        string
		receivedTime int64
		duration     int32
	}{
		{"apache micros", "t=1504230492763123 D=1500", 1504230492763, 1500},
		{"nginx sec.mmm", "t=1504230492.763 D=0.123", 1504230492763, 123000},
		{"app millis", "t=1504230492763 D=42", 1504230492763, 42},
		{"D not positive is unset", "t=1504230492763 D=-5", 1504230492763, -1},
		{"nginx D not positive is unset", "t=1504230492763 D=-0.123", 1504230492763, -1},
		{"D beyond int32 is unset", "t=1504230492763 D=3000000.000", 1504230492763, -1},
		{"no D is unset", "t=1504230492763", 1504230492763, -1},
	} {
		got := parityProxyRequest(map[string]string{"X-Proxy-Time": tc.value})
		if assert.Len(t, got, 1, "%s: %q", tc.name, tc.value) {
			assert.Equal(t, tc.receivedTime, got[0].receivedTime, "%s: received time", tc.name)
			assert.Equal(t, tc.duration, got[0].duration, "%s: duration", tc.name)
			assert.Equal(t, "X-Proxy-Time", got[0].app, "the header name is the app")
		}
	}

	// A t= whose shape fits none of the three writers leaves no received
	// time, so the header is discarded whole.
	for _, bad := range []string{"150423049276", "15042304.9276", "1504230492.76", "abc"} {
		assert.Empty(t, parityProxyRequest(map[string]string{"X-Proxy-Time": "t=" + bad}),
			"t=%s fits no proxy's format", bad)
	}
}

// Test_javaParityLock_ProxyDurationAndPercentAreGated locks the value gates
// the standard parsers share with UserRequestParser: every parser applies D=
// only when positive (ApacheRequestParser / NginxRequestParser / AppRequestParser
// `durationTimeMicroseconds > 0`), the nginx product is reported as unset
// rather than a wrapped int32 (C++ parseProxyNginxDurationMicros; Java's
// parseInteger fails first on such input), and the apache i=/b= are applied
// only inside [0, 100] (ApacheRequestParser). An unset field goes on the wire
// as -1, ProxyRequestHeaderBuilder's default, which the web UI reads as "not
// reported"; the percent is a peer-controlled byte, out of range it is unset,
// not truncated.
func Test_javaParityLock_ProxyDurationAndPercentAreGated(t *testing.T) {
	usePluginConfig(t)

	nginx := parityProxyRequest(map[string]string{"Pinpoint-ProxyNginx": "t=1504230492.763 D=-0.123"})
	require.Len(t, nginx, 1)
	assert.Equal(t, int32(-1), nginx[0].duration, "negative nginx D= is unset")

	nginx = parityProxyRequest(map[string]string{"Pinpoint-ProxyNginx": "t=1504230492.763 D=3000000.000"})
	require.Len(t, nginx, 1)
	assert.Equal(t, int32(-1), nginx[0].duration, "nginx D= past int32/1000 is unset, not wrapped")

	apache := parityProxyRequest(map[string]string{"Pinpoint-ProxyApache": "t=1504230492763123 D=-7 i=101 b=-1"})
	require.Len(t, apache, 1)
	assert.Equal(t, int32(-1), apache[0].duration, "negative apache D= is unset")
	assert.Equal(t, int32(-1), apache[0].idle, "i= above 100 is unset")
	assert.Equal(t, int32(-1), apache[0].busy, "b= below 0 is unset")

	apache = parityProxyRequest(map[string]string{"Pinpoint-ProxyApache": "t=1504230492763123 D=7 i=0 b=100"})
	require.Len(t, apache, 1)
	assert.Equal(t, int32(7), apache[0].duration)
	assert.Equal(t, int32(0), apache[0].idle)
	assert.Equal(t, int32(100), apache[0].busy)
}
