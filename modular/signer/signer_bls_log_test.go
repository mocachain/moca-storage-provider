package signer

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mocachain/moca-storage-provider/base/gfspapp"
	"github.com/mocachain/moca-storage-provider/pkg/log"
	"github.com/mocachain/moca/v2/sdk/keys"
)

// logCapture collects log entries so tests can assert on what is written.
type logCapture struct {
	mux sync.Mutex
	buf bytes.Buffer
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mux.Lock()
	defer c.mux.Unlock()
	return c.buf.Write(p)
}

func (c *logCapture) Sync() error { return nil }

func (c *logCapture) Stop() error { return nil }

func (c *logCapture) String() string {
	c.mux.Lock()
	defer c.mux.Unlock()
	return c.buf.String()
}

// stderrWriter restores the default log destination after a test.
type stderrWriter struct{}

func (stderrWriter) Write(p []byte) (int, error) { return os.Stderr.Write(p) }

func (stderrWriter) Sync() error { return nil }

func (stderrWriter) Stop() error { return nil }

func TestSignSecondarySealBls_DoesNotLogKeyOrSignature(t *testing.T) {
	// test-only bls private key material, 32 bytes
	blsKm, err := keys.NewBlsPrivateKeyManager("2f3a1b6c9d4e5f60718293a4b5c6d7e8f9012a3b4c5d6e7f8091a2b3c4d5e6f7")
	require.NoError(t, err)

	capture := &logCapture{}
	log.SetWriter(capture)
	log.SetLevel(log.DebugLevel)
	t.Cleanup(func() { log.SetWriter(stderrWriter{}) })

	signer := &SignModular{
		baseApp: &gfspapp.GfSpBaseApp{},
		client:  &MocaChainSignClient{blsKm: blsKm},
	}

	sig, err := signer.SignSecondarySealBls(context.Background(), 100, 2, [][]byte{[]byte("checksum")})
	require.NoError(t, err)
	require.NotEmpty(t, sig)

	logged := capture.String()
	assert.NotContains(t, logged, hex.EncodeToString(sig))
	assert.NotContains(t, logged, hex.EncodeToString(blsKm.PubKey().Bytes()))
	assert.NotContains(t, logged, "pub_key")
	assert.NotContains(t, logged, "sign_doc")
}
