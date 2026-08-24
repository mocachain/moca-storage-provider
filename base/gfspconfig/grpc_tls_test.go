package gfspconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadGRPCTLSCredentialsRequiresEveryFile(t *testing.T) {
	tests := []struct {
		name    string
		config  GRPCTLSConfig
		missing string
	}{
		{name: "CA certificate", config: GRPCTLSConfig{CertFile: "cert.pem", KeyFile: "key.pem"}, missing: "CACertFile"},
		{name: "certificate", config: GRPCTLSConfig{CACertFile: "ca.pem", KeyFile: "key.pem"}, missing: "CertFile"},
		{name: "private key", config: GRPCTLSConfig{CACertFile: "ca.pem", CertFile: "cert.pem"}, missing: "KeyFile"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := LoadGRPCTLSCredentials(test.config)
			require.ErrorContains(t, err, test.missing)
		})
	}
}

func TestLoadGRPCTLSCredentialsRejectsInvalidCA(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	require.NoError(t, os.WriteFile(caFile, []byte("not a certificate"), 0o600))

	_, _, err := LoadGRPCTLSCredentials(GRPCTLSConfig{
		CACertFile: caFile,
		CertFile:   filepath.Join(dir, "cert.pem"),
		KeyFile:    filepath.Join(dir, "key.pem"),
	})
	require.ErrorContains(t, err, "CA certificate")
}
