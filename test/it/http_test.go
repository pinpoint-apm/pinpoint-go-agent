package it

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	pphttp "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The http plugin publishes its own config snapshot once per process (see
// pphttp.httpCfg), so every test in this package must share the HTTP settings
// from defaultAgentConfig. Changing them per test would silently have no
// effect after the first HTTP request of the run.

func serverRequest(t *testing.T, method, target string, headers map[string]string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Host = "frontend.example.test:443"
	req.RemoteAddr = "192.0.2.20:8443"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func TestHttpHelpersPopulateServerAndClientWireData(t *testing.T) {
	mc, _ := startStack(t, defaultAgentConfig())

	// A real downstream that echoes back the trace headers it received.
	var downstreamHeaders http.Header
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downstreamHeaders = r.Header.Clone()
		w.Header().Set("x-client-response", "client-response-5")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, "downstream")
	}))
	defer downstream.Close()

	req := serverRequest(t, http.MethodGet, "/http-helper", map[string]string{
		"X-Forwarded-For":     "203.0.113.7, 10.0.0.1",
		"Pinpoint-ProxyNginx": "t=1710000000.125 D=37",
		"x-request-id":        "server-request-1",
		"Cookie":              "session_id=server-session-2",
	})
	tracer := pphttp.NewHttpServerTracer(req, "http.helper.server")
	require.True(t, tracer.IsSampled())
	spanID := tracer.SpanId()

	clientReq, err := http.NewRequest(http.MethodGet, downstream.URL+"/items/42", nil)
	require.NoError(t, err)
	clientReq.Header.Set("x-client-request", "client-request-3")
	clientReq.AddCookie(&http.Cookie{Name: "client_session", Value: "client-session-4"})

	resp, err := pphttp.DoClient(func(r *http.Request) (*http.Response, error) {
		return http.DefaultClient.Do(r)
	}, pinpoint.RequestWithTracerContext(clientReq, tracer))
	require.NoError(t, err)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// The client hop injected a complete trace context downstream.
	assert.Equal(t, tracer.TransactionId().String(), downstreamHeaders.Get(pinpoint.HeaderTraceId))
	assert.Equal(t, strconv.FormatInt(spanID, 10), downstreamHeaders.Get(pinpoint.HeaderParentSpanId))
	assert.NotEmpty(t, downstreamHeaders.Get(pinpoint.HeaderSpanId))
	assert.Equal(t, itAppName, downstreamHeaders.Get(pinpoint.HeaderParentApplicationName))

	responseHeader := http.Header{}
	responseHeader.Set("x-response-id", "server-response-6")
	pphttp.RecordHttpServerResponse(tracer, http.StatusServiceUnavailable, responseHeader)
	pphttp.CollectUrlStat(tracer, "/http-helper/{id}", http.MethodPost, http.StatusServiceUnavailable)
	tracer.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/http-helper") != nil && len(eventsForSpan(s, spanID)) >= 1
	}, waitTimeout))

	s := mc.Snapshot()
	wire := findSpanByRpc(s, "/http-helper")
	require.NotNil(t, wire)
	// X-Forwarded-For's first entry wins over the socket address.
	assert.Equal(t, "203.0.113.7", wire.GetAcceptEvent().GetRemoteAddr())
	assert.Equal(t, "frontend.example.test:443", wire.GetAcceptEvent().GetEndPoint())
	assert.Equal(t, int32(1), wire.GetErr(), "a 5xx response is a failed request")

	proxy := findAnnotation(wire.GetAnnotation(), pinpoint.AnnotationHttpProxyHeader)
	require.NotNil(t, proxy)
	proxyValue := proxy.GetValue().GetLongIntIntByteByteStringValue()
	assert.Equal(t, int64(1710000000125), proxyValue.GetLongValue())
	assert.Equal(t, int32(2), proxyValue.GetIntValue1())
	assert.Equal(t, int32(37), proxyValue.GetIntValue2())

	assert.True(t, hasStringPairAnnotation(wire.GetAnnotation(),
		pinpoint.AnnotationHttpRequestHeader, "x-request-id", "server-request-1"))
	assert.True(t, hasStringPairAnnotation(wire.GetAnnotation(),
		pinpoint.AnnotationHttpCookie, "session_id", "server-session-2"))
	assert.True(t, hasStringPairAnnotation(wire.GetAnnotation(),
		pinpoint.AnnotationHttpResponseHeader, "x-response-id", "server-response-6"))
	assert.Equal(t, int32(http.StatusServiceUnavailable),
		findAnnotation(wire.GetAnnotation(), pinpoint.AnnotationHttpStatusCode).GetValue().GetIntValue())

	clientEvent := findEventByServiceType(eventsForSpan(s, spanID), pinpoint.ServiceTypeGoHttpClient)
	require.NotNil(t, clientEvent)
	message := clientEvent.GetNextEvent().GetMessageEvent()
	require.NotNil(t, message)
	assert.Equal(t, clientReq.Host, message.GetEndPoint())
	assert.Equal(t, clientReq.Host, message.GetDestinationId())

	url := findAnnotation(clientEvent.GetAnnotation(), pinpoint.AnnotationHttpUrl)
	require.NotNil(t, url)
	assert.Contains(t, url.GetValue().GetStringValue(), "/items/42")
	assert.Equal(t, int32(http.StatusTooManyRequests),
		findAnnotation(clientEvent.GetAnnotation(), pinpoint.AnnotationHttpStatusCode).GetValue().GetIntValue())
	assert.True(t, hasStringPairAnnotation(clientEvent.GetAnnotation(),
		pinpoint.AnnotationHttpRequestHeader, "x-client-request", "client-request-3"))
	assert.True(t, hasStringPairAnnotation(clientEvent.GetAnnotation(),
		pinpoint.AnnotationHttpCookie, "client_session", "client-session-4"))
	assert.True(t, hasStringPairAnnotation(clientEvent.GetAnnotation(),
		pinpoint.AnnotationHttpResponseHeader, "x-client-response", "client-response-5"))
}

func TestParsesApacheProxyHeaderAndRealIpFallback(t *testing.T) {
	mc, _ := startStack(t, defaultAgentConfig())

	req := serverRequest(t, http.MethodGet, "/proxy-apache", map[string]string{
		"X-Real-Ip":            "203.0.113.99",
		"Pinpoint-ProxyApache": "t=1710000001000000 D=250 i=7 b=12",
	})
	tracer := pphttp.NewHttpServerTracer(req, "http.proxy.apache")
	require.True(t, tracer.IsSampled())
	tracer.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/proxy-apache") != nil
	}, waitTimeout))

	wire := findSpanByRpc(mc.Snapshot(), "/proxy-apache")
	require.NotNil(t, wire)
	// X-Real-Ip wins over the socket address when no X-Forwarded-For exists.
	assert.Equal(t, "203.0.113.99", wire.GetAcceptEvent().GetRemoteAddr())

	proxy := findAnnotation(wire.GetAnnotation(), pinpoint.AnnotationHttpProxyHeader)
	require.NotNil(t, proxy)
	value := proxy.GetValue().GetLongIntIntByteByteStringValue()
	// Apache reports microseconds; the agent converts to milliseconds and tags
	// the annotation with code 3 plus duration/idle/busy.
	assert.Equal(t, int64(1710000001000), value.GetLongValue())
	assert.Equal(t, int32(3), value.GetIntValue1())
	assert.Equal(t, int32(250), value.GetIntValue2())
	assert.Equal(t, int32(7), value.GetByteValue1())
	assert.Equal(t, int32(12), value.GetByteValue2())
}

func TestRecordsAppProxyHeaderAndGuardsNginxTimestampRange(t *testing.T) {
	mc, _ := startStack(t, defaultAgentConfig())

	trace := func(rpc, operation string, headers map[string]string) {
		tracer := pphttp.NewHttpServerTracer(serverRequest(t, http.MethodGet, rpc, headers), operation)
		require.True(t, tracer.IsSampled())
		tracer.EndSpan()
	}
	trace("/proxy-app", "http.proxy.app", map[string]string{
		"Pinpoint-ProxyApp": "t=1712345678123 app=edge-proxy",
	})
	// 1e300 * 1000 does not fit into int64: the untrusted timestamp must be
	// rejected before the cast, while the annotation itself is still recorded.
	trace("/proxy-nginx-range", "http.proxy.nginx.range", map[string]string{
		"Pinpoint-ProxyNginx": "t=1e300 D=25",
	})
	// When several proxy headers are present, Apache wins over Nginx and App.
	trace("/proxy-priority", "http.proxy.priority", map[string]string{
		"Pinpoint-ProxyApache": "t=1710000002000000 D=9",
		"Pinpoint-ProxyNginx":  "t=1710000003.5",
		"Pinpoint-ProxyApp":    "t=1710000004000 app=ignored",
	})

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/proxy-app") != nil &&
			findSpanByRpc(s, "/proxy-nginx-range") != nil &&
			findSpanByRpc(s, "/proxy-priority") != nil
	}, waitTimeout))

	s := mc.Snapshot()
	proxyOf := func(rpc string) *pb.PLongIntIntByteByteStringValue {
		wire := findSpanByRpc(s, rpc)
		require.NotNil(t, wire, rpc)
		annotation := findAnnotation(wire.GetAnnotation(), pinpoint.AnnotationHttpProxyHeader)
		require.NotNil(t, annotation, rpc)
		return annotation.GetValue().GetLongIntIntByteByteStringValue()
	}

	app := proxyOf("/proxy-app")
	assert.Equal(t, int64(1712345678123), app.GetLongValue())
	assert.Equal(t, int32(1), app.GetIntValue1())
	assert.Equal(t, "edge-proxy", app.GetStringValue().GetValue())

	nginx := proxyOf("/proxy-nginx-range")
	assert.Equal(t, int64(0), nginx.GetLongValue())
	assert.Equal(t, int32(2), nginx.GetIntValue1())
	assert.Equal(t, int32(25), nginx.GetIntValue2())

	priority := proxyOf("/proxy-priority")
	assert.Equal(t, int64(1710000002000), priority.GetLongValue())
	assert.Equal(t, int32(3), priority.GetIntValue1())
	assert.Equal(t, int32(9), priority.GetIntValue2())
}

// A filtered request gets the plain noop tracer, whose span id is 0. An
// unsampled request still carries a real id, so the id separates filtering
// from a sampling decision.
func TestExcludesFilteredUrlsAndMethods(t *testing.T) {
	mc, _ := startStack(t, defaultAgentConfig())

	excluded := pphttp.NewHttpServerTracer(
		serverRequest(t, http.MethodGet, "/excluded/deep/leaf", nil), "http.excluded")
	assert.False(t, excluded.IsSampled())
	assert.Equal(t, int64(0), excluded.SpanId())
	excluded.EndSpan()

	excludedMethod := pphttp.NewHttpServerTracer(
		serverRequest(t, http.MethodOptions, "/kept", nil), "http.excluded.method")
	assert.False(t, excludedMethod.IsSampled())
	assert.Equal(t, int64(0), excludedMethod.SpanId())
	excludedMethod.EndSpan()

	kept := pphttp.NewHttpServerTracer(serverRequest(t, http.MethodGet, "/kept", nil), "http.kept")
	require.True(t, kept.IsSampled())
	assert.NotEqual(t, int64(0), kept.SpanId())
	kept.EndSpan()

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/kept") != nil
	}, waitTimeout))

	s := mc.Snapshot()
	assert.Equal(t, 1, countSpansByRpc(s, "/kept"))
	assert.Equal(t, 0, countSpansByRpc(s, "/excluded/deep/leaf"))
}

// The wrapped handler is how applications actually use the plugin: it must
// trace the request, name the handler as a span event, and hand the tracer to
// the handler through the request context.
func TestWrappedHandlerTracesRequestEndToEnd(t *testing.T) {
	mc, _ := startStack(t, defaultAgentConfig())

	handler := pphttp.WrapHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracer := pinpoint.TracerFromRequestContext(r)
		assert.True(t, tracer.IsSampled())
		tracer.NewSpanEvent("handler.work")
		tracer.EndSpanEvent()
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, "ok")
	})
	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()

	resp, err := http.Get(server.URL + "/wrapped")
	require.NoError(t, err)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		wire := findSpanByRpc(s, "/wrapped")
		return wire != nil && len(eventsForSpan(s, wire.GetSpanId())) >= 2
	}, waitTimeout))

	s := mc.Snapshot()
	wire := findSpanByRpc(s, "/wrapped")
	require.NotNil(t, wire)
	assert.Equal(t, int32(0), wire.GetErr())
	assert.Equal(t, int32(http.StatusCreated),
		findAnnotation(wire.GetAnnotation(), pinpoint.AnnotationHttpStatusCode).GetValue().GetIntValue())
	assert.GreaterOrEqual(t, len(eventsForSpan(s, wire.GetSpanId())), 2)
}
