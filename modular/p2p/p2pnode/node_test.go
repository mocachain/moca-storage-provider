package p2pnode

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mocachain/moca-storage-provider/pkg/log"
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

func captureLog(t *testing.T) *logCapture {
	t.Helper()
	capture := &logCapture{}
	log.SetWriter(capture)
	t.Cleanup(func() { log.SetWriter(stderrWriter{}) })
	return capture
}

func TestNewNode_HexDecodeFailureDoesNotLogPrivateKey(t *testing.T) {
	capture := captureLog(t)
	// odd length hex string, hex.DecodeString fails
	privateKey := "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcde"

	node, err := NewNode(nil, privateKey, "127.0.0.1:9633", nil, PingPeriodMin, MinSecondaryApprovalExpiredHeight, "")

	assert.Error(t, err)
	assert.Nil(t, node)
	assert.NotContains(t, capture.String(), privateKey)
	assert.False(t, strings.Contains(capture.String(), "priv_key"), "log must not carry a private key field")
}

func TestNewNode_UnmarshalFailureDoesNotLogPrivateKey(t *testing.T) {
	capture := captureLog(t)
	// valid hex, but 31 bytes, so the secp256k1 unmarshal rejects it
	privateKey := "5f2a91c4b7e63d80a1f45c9b28e7d306af5b1c9e42d780b3a6c5e19f4d2b70"

	node, err := NewNode(nil, privateKey, "127.0.0.1:9633", nil, PingPeriodMin, MinSecondaryApprovalExpiredHeight, "")

	assert.Error(t, err)
	assert.Nil(t, node)
	assert.NotContains(t, capture.String(), privateKey)
	assert.False(t, strings.Contains(capture.String(), "priv_key"), "log must not carry a private key field")
}
