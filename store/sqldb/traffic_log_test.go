package sqldb

import (
	"bytes"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestGetUpdatedConsumedQuotaV2_DoesNotLogQuotaStateAtInfo(t *testing.T) {
	capture := &logCapture{}
	log.SetWriter(capture)
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() {
		log.SetLevel(log.DebugLevel)
		log.SetWriter(stderrWriter{})
	})

	_, _, _, _, _, err := getUpdatedConsumedQuotaV2(10, 100, 0, 0, 1000, 100, 0)
	require.NoError(t, err)

	assert.NotContains(t, capture.String(), "quota info")
}
