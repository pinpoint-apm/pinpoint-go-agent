package pinpoint

import (
	"context"
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

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// genTestCert writes a self-signed cert/key pair for 127.0.0.1 into dir.
func genTestCert(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	assert.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "pinpoint-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	assert.NoError(t, err)
	keyDer, err := x509.MarshalECPrivateKey(key)
	assert.NoError(t, err)

	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	assert.NoError(t, os.WriteFile(certFile,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	assert.NoError(t, os.WriteFile(keyFile,
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDer}), 0600))
	return certFile, keyFile
}

// startTLSCollector serves gRPC over TLS on 127.0.0.1 with a self-signed cert
// and returns the port and the cert to trust.
func startTLSCollector(t *testing.T) (port int, certFile string) {
	t.Helper()

	certFile, keyFile := genTestCert(t, t.TempDir())
	creds, err := credentials.NewServerTLSFromFile(certFile, keyFile)
	assert.NoError(t, err)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)

	srv := grpc.NewServer(grpc.Creds(creds))
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	return lis.Addr().(*net.TCPAddr).Port, certFile
}

func tlsTestConfig(t *testing.T, port int, opts ...ConfigOption) *Config {
	t.Helper()
	c, err := NewConfig(append([]ConfigOption{
		WithAppName("test"),
		WithAgentId("testagent"),
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
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	srv := grpc.NewServer()
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	c := tlsTestConfig(t, lis.Addr().(*net.TCPAddr).Port)
	assert.False(t, c.Bool(CfgCollectorGrpcSslEnable))

	conn, err := connectCollector(c, CfgCollectorAgentPort)
	assert.NoError(t, err)
	defer conn.Close()

	assert.True(t, waitUntilReady(context.Background(), conn, 5*time.Second, "plaintext-test"))
}
