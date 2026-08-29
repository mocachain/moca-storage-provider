package util

import (
	"bytes"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mocachain/moca-storage-provider/pkg/log"
)

const (
	testSecretEnv   = "MOCA_TEST_SECRET"
	testEnvValue    = "1f2e3d4c5b6a798807162534435261708f9e0d1c2b3a49586776859403a2b1c0"
	testConfigValue = "0f6e0d9c8b7a695847362514f3e2d1c0b9a8978685746352413f2e1d0c9b8a79"
)

// logCapture collects everything the global logger writes during a test.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *logCapture) Sync() error { return nil }

func (c *logCapture) Stop() error { return nil }

func (c *logCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

type stderrWriter struct{}

func (stderrWriter) Write(p []byte) (int, error) { return os.Stderr.Write(p) }

func (stderrWriter) Sync() error { return nil }

func (stderrWriter) Stop() error { return nil }

// captureLog redirects the global logger for the duration of the test and puts it
// back afterwards, so the capture does not leak into other tests.
func captureLog(t *testing.T) *logCapture {
	t.Helper()
	capture := &logCapture{}
	log.SetWriter(capture)
	t.Cleanup(func() { log.SetWriter(stderrWriter{}) })
	return capture
}

func TestSecretFromEnv_UnsetKeepsTheConfiguredValue(t *testing.T) {
	capture := captureLog(t)

	secret, err := SecretFromEnv(testSecretEnv, testConfigValue)
	require.NoError(t, err)
	assert.Equal(t, testConfigValue, secret)
	assert.Empty(t, capture.String(), "an unset env var is the normal case, it is not worth a log line")
}

func TestSecretFromEnv_SetButEmptyIsRejected(t *testing.T) {
	t.Setenv(testSecretEnv, "")

	_, err := SecretFromEnv(testSecretEnv, testConfigValue)
	require.Error(t, err)
	assert.Contains(t, err.Error(), testSecretEnv)
}

func TestSecretFromEnv_SetValueWins(t *testing.T) {
	t.Setenv(testSecretEnv, testEnvValue)
	capture := captureLog(t)

	secret, err := SecretFromEnv(testSecretEnv, "")
	require.NoError(t, err)
	assert.Equal(t, testEnvValue, secret)
	assert.Contains(t, capture.String(), testSecretEnv)
}

func TestSecretFromEnv_ReplacingAConfiguredValueIsRecorded(t *testing.T) {
	t.Setenv(testSecretEnv, testEnvValue)
	capture := captureLog(t)

	secret, err := SecretFromEnv(testSecretEnv, testConfigValue)
	require.NoError(t, err)
	assert.Equal(t, testEnvValue, secret)

	logged := capture.String()
	assert.Contains(t, logged, testSecretEnv,
		"an operator must be able to tell which source the process actually started with")
	assert.Contains(t, logged, "replaced")
}

func TestSecretFromEnv_NeverLogsTheSecret(t *testing.T) {
	t.Setenv(testSecretEnv, testEnvValue)
	capture := captureLog(t)

	_, err := SecretFromEnv(testSecretEnv, testConfigValue)
	require.NoError(t, err)

	logged := capture.String()
	assert.NotContains(t, logged, testEnvValue)
	assert.NotContains(t, logged, testConfigValue)
}
