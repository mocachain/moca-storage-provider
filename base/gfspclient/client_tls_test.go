package gfspclient_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/base/gfspconfig"
)

func TestDefaultClientRejectsPlaintextServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	fixture := newTLSFixture(t)
	clientCredentials, _, err := gfspconfig.LoadGRPCTLSCredentials(fixture.configFor(t, "client"))
	require.NoError(t, err)
	client := newTestClient(clientCredentials)
	target, stop := startPlaintextHealthServer(t)
	defer stop()
	conn, err := client.Connection(ctx, target)
	require.NoError(t, err)
	defer conn.Close()

	err = conn.Invoke(ctx, "/grpc.health.v1.Health/Check", &emptypb.Empty{}, &emptypb.Empty{})
	require.Error(t, err)
	require.Equal(t, codes.Unavailable, status.Code(err), "plaintext RPC reached the server")
}

func TestMutualTLSUsesTLS13AndDerivedServerName(t *testing.T) {
	fixture := newTLSFixture(t)
	_, serverCredentials, err := gfspconfig.LoadGRPCTLSCredentials(
		fixture.configFor(t, "localhost"),
	)
	require.NoError(t, err)
	clientCredentials, _, err := gfspconfig.LoadGRPCTLSCredentials(fixture.configFor(t, "client"))
	require.NoError(t, err)

	target, stop := startHealthServer(t, serverCredentials)
	defer stop()

	conn := dialTestClient(t, target, clientCredentials)
	defer conn.Close()

	var serverPeer peer.Peer
	_, err = healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{}, grpc.Peer(&serverPeer))
	require.NoError(t, err)
	tlsInfo, ok := serverPeer.AuthInfo.(credentials.TLSInfo)
	require.True(t, ok)
	require.Equal(t, uint16(tls.VersionTLS13), tlsInfo.State.Version)
}

func TestMutualTLSRejectsInvalidPeers(t *testing.T) {
	fixture := newTLSFixture(t)
	_, serverCredentials, err := gfspconfig.LoadGRPCTLSCredentials(fixture.configFor(t, "localhost"))
	require.NoError(t, err)
	clientConfig := fixture.configFor(t, "client")
	clientCredentials, _, err := gfspconfig.LoadGRPCTLSCredentials(clientConfig)
	require.NoError(t, err)
	target, stop := startHealthServer(t, serverCredentials)
	defer stop()
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(fixture.caPEM))

	t.Run("plaintext client", func(t *testing.T) {
		assertHealthCheckFails(t, target, insecure.NewCredentials())
	})

	t.Run("client without certificate", func(t *testing.T) {
		assertHealthCheckFails(t, target, credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS13,
			RootCAs:    roots,
		}))
	})

	t.Run("TLS 1.2 client", func(t *testing.T) {
		certificate, err := tls.LoadX509KeyPair(clientConfig.CertFile, clientConfig.KeyFile)
		require.NoError(t, err)
		assertHealthCheckFails(t, target, credentials.NewTLS(&tls.Config{
			MinVersion:   tls.VersionTLS12,
			MaxVersion:   tls.VersionTLS12,
			RootCAs:      roots,
			Certificates: []tls.Certificate{certificate},
		}))
	})

	t.Run("wrong derived server name", func(t *testing.T) {
		_, port, err := net.SplitHostPort(target)
		require.NoError(t, err)
		assertHealthCheckFails(t, net.JoinHostPort("127.0.0.1", port), clientCredentials)
	})
}

type tlsFixture struct {
	caCertificate *x509.Certificate
	caKey         ed25519.PrivateKey
	caPEM         []byte
}

func newTLSFixture(t *testing.T) *tlsFixture {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          randomSerial(t),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	require.NoError(t, err)
	certificate, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &tlsFixture{
		caCertificate: certificate,
		caKey:         privateKey,
		caPEM:         pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

func (f *tlsFixture) configFor(t *testing.T, serverName string) gfspconfig.GRPCTLSConfig {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: randomSerial(t),
		Subject:      pkix.Name{CommonName: serverName},
		DNSNames:     []string{serverName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, f.caCertificate, publicKey, f.caKey)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)

	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(caFile, f.caPEM, 0o600))
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600))
	return gfspconfig.GRPCTLSConfig{CACertFile: caFile, CertFile: certFile, KeyFile: keyFile}
}

func randomSerial(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)
	return serial
}

func newTestClient(transportCredentials credentials.TransportCredentials) *gfspclient.GfSpClient {
	const target = "localhost:0"
	return gfspclient.NewGfSpClient(target, target, target, target, target, target, target, target, target,
		transportCredentials, false)
}

func startHealthServer(t *testing.T, transportCredentials credentials.TransportCredentials) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer(grpc.Creds(transportCredentials))
	healthpb.RegisterHealthServer(server, health.NewServer())
	go server.Serve(listener)
	_, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	return net.JoinHostPort("localhost", port), func() {
		server.Stop()
		_ = listener.Close()
	}
}

func startPlaintextHealthServer(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	healthpb.RegisterHealthServer(server, health.NewServer())
	go server.Serve(listener)
	_, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	return net.JoinHostPort("localhost", port), func() {
		server.Stop()
		_ = listener.Close()
	}
}

func dialTestClient(t *testing.T, target string, transportCredentials credentials.TransportCredentials) *grpc.ClientConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client := gfspclient.NewGfSpClient(target, target, target, target, target, target, target, target, target,
		transportCredentials, false)
	conn, err := client.Connection(ctx, target, grpc.WithBlock())
	require.NoError(t, err)
	return conn
}

func assertHealthCheckFails(t *testing.T, target string, transportCredentials credentials.TransportCredentials) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client := gfspclient.NewGfSpClient(target, target, target, target, target, target, target, target, target,
		transportCredentials, false)
	conn, err := client.Connection(ctx, target)
	require.NoError(t, err)
	defer conn.Close()
	_, err = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	require.Error(t, err)
}
