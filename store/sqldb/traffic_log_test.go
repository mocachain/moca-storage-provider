package sqldb

import (
	"bytes"
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

func TestGetUpdatedConsumedQuotaV2_DoesNotLogQuotaStateAtInfo(t *testing.T) {
	capture := &logCapture{}
	log.SetWriter(capture)
	log.SetLevel(log.InfoLevel)
	defer log.SetLevel(log.DebugLevel)

	_, _, _, _, _, err := getUpdatedConsumedQuotaV2(10, 100, 0, 0, 1000, 100, 0)
	require.NoError(t, err)

	assert.NotContains(t, capture.String(), "quota info")
}
