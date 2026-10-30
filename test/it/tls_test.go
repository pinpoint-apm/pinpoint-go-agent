package it

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/testapp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// selfSignedCert writes a self-signed certificate/key pair for 127.0.0.1 into a
// temporary directory and returns their paths.
func selfSignedCert(t *testing.T, commonName string) (certFile, keyFile string) {
	t.Helper()
	certFile, keyFile, err := testapp.WriteSelfSignedCert(t.TempDir(), commonName)
	require.NoError(t, err)
	return certFile, keyFile
}

// startTLSCollector starts a collector whose three endpoints all serve TLS.
func startTLSCollector(t *testing.T, certFile, keyFile string) *MockCollector {
	t.Helper()
	mc := NewMockCollector()
	require.NoError(t, mc.UseTLS(certFile, keyFile))
	require.NoError(t, mc.Start())
	t.Cleanup(mc.Shutdown)
	return mc
}

// Every collector channel dials with the configured credentials, so enabling
// TLS has to carry the whole agent -- registration, spans, statistics and
// profiler commands -- not just the first handshake.
func TestRegistersAndTracesOverTlsCollector(t *testing.T) {
	certFile, keyFile := selfSignedCert(t, "pinpoint-it-collector")
	mc := startTLSCollector(t, certFile, keyFile)

	agent := startAgent(t, mc,
		pinpoint.WithCollectorGrpcSslEnable(true),
		pinpoint.WithCollectorGrpcTrustCertFilePath(certFile))

	mc.WaitFor(t, func(s Snapshot) bool { return len(s.AgentInfos) > 0 }, waitTimeout)
	require.Eventually(t, func() bool { return agent.Enable() }, waitTimeout, 10*time.Millisecond)

	tracer := agent.NewSpanTracer("tls.request", "/tls-traced")
	require.True(t, tracer.IsSampled())
	tracer.EndSpan()

	mc.SendEchoCommand(801, "tls-echo")
	mc.WaitFor(t, func(s Snapshot) bool {
		return findSpanByRpc(s, "/tls-traced") != nil &&
			len(s.Stats) > 0 && len(s.Pings) > 0 && hasEchoResponse(s, 801)
	}, waitTimeout)

	// The identity headers must survive the encrypted hop unchanged.
	s := mc.Snapshot()
	expectCommonMetadata(t, s.AgentInfos[0].Metadata, false)
	expectCommonMetadata(t, s.SpanBatches[0].Metadata, false)
	assert.True(t, agent.Enable())
}

// A trust root that does not sign the collector's certificate must fail the
// handshake and leave the agent disabled, never fall back to plaintext.
func TestRefusesCollectorWithUntrustedCertificate(t *testing.T) {
	certFile, keyFile := selfSignedCert(t, "pinpoint-it-collector")
	otherCert, _ := selfSignedCert(t, "pinpoint-it-other")
	mc := startTLSCollector(t, certFile, keyFile)

	agent := startAgent(t, mc,
		pinpoint.WithCollectorGrpcSslEnable(true),
		pinpoint.WithCollectorGrpcTrustCertFilePath(otherCert))

	assert.Never(t, func() bool { return agent.Enable() }, 2*time.Second, 10*time.Millisecond,
		"an unverifiable collector certificate must not enable the agent")
	s := mc.Snapshot()
	assert.Empty(t, s.AgentInfos, "no request may reach a collector the agent cannot verify")
	assert.Empty(t, s.PingStreams)
	assert.Empty(t, s.StatStreams)
}

// TLS on against a plaintext collector must also fail closed: the agent stays
// disabled rather than retrying in the clear.
func TestDoesNotFallBackToPlaintextWhenTlsIsEnabled(t *testing.T) {
	certFile, _ := selfSignedCert(t, "pinpoint-it-collector")
	mc := startCollector(t) // plaintext

	agent := startAgent(t, mc,
		pinpoint.WithCollectorGrpcSslEnable(true),
		pinpoint.WithCollectorGrpcTrustCertFilePath(certFile))

	assert.Never(t, func() bool { return agent.Enable() }, 2*time.Second, 10*time.Millisecond)
	assert.Empty(t, mc.Snapshot().AgentInfos)
}

// An unreadable trust certificate is a configuration error, not a silent
// downgrade: the agent must refuse to come online.
func TestRefusesUnreadableTrustCertificate(t *testing.T) {
	mc := startCollector(t)

	agent := startAgent(t, mc,
		pinpoint.WithCollectorGrpcSslEnable(true),
		pinpoint.WithCollectorGrpcTrustCertFilePath(filepath.Join(t.TempDir(), "missing.pem")))

	assert.Never(t, func() bool { return agent.Enable() }, 2*time.Second, 10*time.Millisecond)
	assert.Empty(t, mc.Snapshot().AgentInfos)

	// A connect that can never succeed releases the global agent itself, without
	// waiting for the application to call Shutdown: GetAgent stops handing out
	// the dead agent, and NewAgent can be retried once the path is fixed. The
	// handle the caller still holds keeps serving inert tracers, and shutting it
	// down afterwards stays safe and leaves the release in place.
	assert.Equal(t, pinpoint.NoopAgent(), pinpoint.GetAgent(),
		"a dead agent must not stay global until Shutdown")
	requireNoopTracer(t, agent.NewSpanTracer("tls.misconfigured", "/tls-misconfigured"))
	agent.Shutdown()
	assert.Equal(t, pinpoint.NoopAgent(), pinpoint.GetAgent())
}
