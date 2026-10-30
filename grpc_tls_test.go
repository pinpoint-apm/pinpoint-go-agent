package pinpoint

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/testapp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// genTestCert writes a self-signed cert/key pair for 127.0.0.1 into dir.
func genTestCert(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()
	certFile, keyFile, err := testapp.WriteSelfSignedCert(dir, "pinpoint-test")
	require.NoError(t, err)
	return certFile, keyFile
}

// startTLSCollector serves gRPC over TLS on 127.0.0.1 with a self-signed cert
// and returns the port and the cert to trust.
func startTLSCollector(t *testing.T) (port int, certFile string) {
	t.Helper()

	certFile, keyFile := genTestCert(t, t.TempDir())
	creds, err := credentials.NewServerTLSFromFile(certFile, keyFile)
	assert.NoError(t, err)

	return startTestServer(t, grpc.Creds(creds)).Addr().(*net.TCPAddr).Port, certFile
}

func tlsTestConfig(t *testing.T, port int, opts ...ConfigOption) *Config {
	t.Helper()
	c, err := NewConfig(append([]ConfigOption{
		WithAppName("test"),
		WithCollectorHost("127.0.0.1"),
		WithCollectorAgentPort(port),
	}, opts...)...)
	assert.NoError(t, err)
	return c
}

func Test_connectCollector_tlsHandshake(t *testing.T) {
	port, certFile := startTLSCollector(t)
	c := tlsTestConfig(t, port,
		WithCollectorGrpcSslEnable(true),
		WithCollectorGrpcTrustCertFilePath(certFile))

	conn, err := connectCollector(c, CfgCollectorAgentPort)
	assert.NoError(t, err)
	defer conn.Close()

	assert.True(t, waitUntilReady(context.Background(), conn, 5*time.Second, "tls-test"))
}

func Test_connectCollector_tlsRejectsUntrustedServer(t *testing.T) {
	port, _ := startTLSCollector(t)
	// Trust a cert the server does not use: verification must fail, so the
	// channel never becomes ready.
	otherCert, _ := genTestCert(t, t.TempDir())
	c := tlsTestConfig(t, port,
		WithCollectorGrpcSslEnable(true),
		WithCollectorGrpcTrustCertFilePath(otherCert))

	conn, err := connectCollector(c, CfgCollectorAgentPort)
	assert.NoError(t, err)
	defer conn.Close()

	assert.False(t, waitUntilReady(context.Background(), conn, 2*time.Second, "tls-test"))
}

func Test_connectCollector_badTrustCertFailsLoud(t *testing.T) {
	garbage := filepath.Join(t.TempDir(), "garbage.pem")
	assert.NoError(t, os.WriteFile(garbage, []byte("not a certificate"), 0600))

	for _, certPath := range []string{"/nonexistent/ca.pem", garbage} {
		c := tlsTestConfig(t, 9991,
			WithCollectorGrpcSslEnable(true),
			WithCollectorGrpcTrustCertFilePath(certPath))

		conn, err := connectCollector(c, CfgCollectorAgentPort)
		assert.Error(t, err, certPath)
		assert.Nil(t, conn, certPath)
	}
}

func Test_connectCollector_defaultInsecure(t *testing.T) {
	c := tlsTestConfig(t, startTestServer(t).Addr().(*net.TCPAddr).Port)
	assert.False(t, c.Bool(CfgCollectorGrpcSslEnable))

	conn, err := connectCollector(c, CfgCollectorAgentPort)
	assert.NoError(t, err)
	defer conn.Close()

	assert.True(t, waitUntilReady(context.Background(), conn, 5*time.Second, "plaintext-test"))
}
