package gfspconfig

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"google.golang.org/grpc/credentials"
)

// LoadGRPCTLSCredentials loads the mandatory mutual TLS credentials shared by an app's gRPC client and server.
func LoadGRPCTLSCredentials(cfg GRPCTLSConfig) (
	credentials.TransportCredentials, credentials.TransportCredentials, error,
) {
	if cfg.CACertFile == "" {
		return nil, nil, errors.New("GRPCTLS.CACertFile is required")
	}
	if cfg.CertFile == "" {
		return nil, nil, errors.New("GRPCTLS.CertFile is required")
	}
	if cfg.KeyFile == "" {
		return nil, nil, errors.New("GRPCTLS.KeyFile is required")
	}

	caPEM, err := os.ReadFile(cfg.CACertFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read gRPC TLS CA certificate %q: %w", cfg.CACertFile, err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, nil, fmt.Errorf("parse gRPC TLS CA certificate %q: no certificates found", cfg.CACertFile)
	}
	certificate, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("load gRPC TLS certificate and private key: %w", err)
	}

	clientCredentials := credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      caPool,
		Certificates: []tls.Certificate{certificate},
	})
	serverCredentials := credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caPool,
		Certificates: []tls.Certificate{certificate},
	})
	return clientCredentials, serverCredentials, nil
}
