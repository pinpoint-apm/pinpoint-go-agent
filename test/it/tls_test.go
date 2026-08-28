package it

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// selfSignedCert writes a self-signed certificate/key pair for 127.0.0.1 into a
// temporary directory and returns their paths.
func selfSignedCert(t *testing.T, commonName string) (certFile, keyFile string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	keyDer, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certFile,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyFile,
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDer}), 0o600))
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

	cfg := defaultAgentConfig()
	cfg.grpcSslEnable = true
	cfg.grpcTrustCertFilePath = certFile
	agent := startAgent(t, mc, cfg)

	require.True(t, mc.WaitFor(func(s Snapshot) bool { return len(s.AgentInfos) > 0 }, waitTimeout))
	require.True(t, waitUntil(func() bool { return agent.Enable() }, waitTimeout))

	tracer := agent.NewSpanTracer("tls.request", "/tls-traced")
	require.True(t, tracer.IsSampled())
	tracer.EndSpan()

	mc.SendEchoCommand(801, "tls-echo")
	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/tls-traced") != nil &&
			len(s.Stats) > 0 && len(s.Pings) > 0 && hasEchoResponse(s, 801)
	}, waitTimeout))

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

	cfg := defaultAgentConfig()
	cfg.grpcSslEnable = true
	cfg.grpcTrustCertFilePath = otherCert
	agent := startAgent(t, mc, cfg)

	assert.False(t, waitUntil(func() bool { return agent.Enable() }, 2*time.Second),
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

	cfg := defaultAgentConfig()
	cfg.grpcSslEnable = true
	cfg.grpcTrustCertFilePath = certFile
	agent := startAgent(t, mc, cfg)

	assert.False(t, waitUntil(func() bool { return agent.Enable() }, 2*time.Second))
	assert.Empty(t, mc.Snapshot().AgentInfos)
}

// An unreadable trust certificate is a configuration error, not a silent
// downgrade: the agent must refuse to come online.
func TestRefusesUnreadableTrustCertificate(t *testing.T) {
	mc := startCollector(t)

	cfg := defaultAgentConfig()
	cfg.grpcSslEnable = true
	cfg.grpcTrustCertFilePath = filepath.Join(t.TempDir(), "missing.pem")
	agent := startAgent(t, mc, cfg)

	assert.False(t, waitUntil(func() bool { return agent.Enable() }, 2*time.Second))
	assert.Empty(t, mc.Snapshot().AgentInfos)

	// The agent that never got a channel is still the installed global one, and
	// hands out inert tracers; shutting it down has to release it so the host
	// can build another once the configuration is fixed.
	requireNoopTracer(t, agent.NewSpanTracer("tls.misconfigured", "/tls-misconfigured"))
	agent.Shutdown()
	assert.Equal(t, pinpoint.NoopAgent(), pinpoint.GetAgent())
}
