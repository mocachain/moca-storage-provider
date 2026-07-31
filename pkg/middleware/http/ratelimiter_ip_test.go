package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRequestFrom(t *testing.T, remoteAddr string, forwardedFor string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remoteAddr
	if forwardedFor != "" {
		r.Header.Set("X-Forwarded-For", forwardedFor)
	}
	return r
}

func TestGetIP_IgnoresForwardedForWhenNoProxyIsTrusted(t *testing.T) {
	first := GetIP(newRequestFrom(t, "203.0.113.7:44321", "1.1.1.1"))
	second := GetIP(newRequestFrom(t, "203.0.113.7:51002", "2.2.2.2"))

	assert.Equal(t, "203.0.113.7", first)
	assert.Equal(t, first, second,
		"a caller must not get a fresh rate limit bucket by changing a header it controls")
}

func TestGetIP_DropsThePortSoOneClientIsOneBucket(t *testing.T) {
	first := GetIP(newRequestFrom(t, "203.0.113.7:44321", ""))
	second := GetIP(newRequestFrom(t, "203.0.113.7:51002", ""))

	assert.Equal(t, "203.0.113.7", first)
	assert.Equal(t, first, second, "each new connection must not get its own bucket")
}

func TestClientIP_UsesForwardedForBehindATrustedProxy(t *testing.T) {
	resolver, err := newIPResolver([]string{"10.0.0.0/8"})
	require.NoError(t, err)

	assert.Equal(t, "198.51.100.9", resolver.clientIP(newRequestFrom(t, "10.1.2.3:44321", "198.51.100.9")))
}

func TestClientIP_TakesTheLastUntrustedHopOfAForwardedForChain(t *testing.T) {
	resolver, err := newIPResolver([]string{"10.0.0.0/8"})
	require.NoError(t, err)

	// the client may prepend anything it likes to the chain, only the hops the
	// trusted proxies appended can be believed
	assert.Equal(t, "198.51.100.9",
		resolver.clientIP(newRequestFrom(t, "10.1.2.3:44321", "1.1.1.1, 198.51.100.9, 10.4.5.6")))
}

func TestClientIP_FallsBackToThePeerWhenEveryHopIsTrusted(t *testing.T) {
	resolver, err := newIPResolver([]string{"10.0.0.0/8"})
	require.NoError(t, err)

	assert.Equal(t, "10.1.2.3", resolver.clientIP(newRequestFrom(t, "10.1.2.3:44321", "10.7.7.7, 10.4.5.6")))
}

func TestClientIP_IgnoresForwardedForFromAnUntrustedPeer(t *testing.T) {
	resolver, err := newIPResolver([]string{"10.0.0.0/8"})
	require.NoError(t, err)

	assert.Equal(t, "203.0.113.7", resolver.clientIP(newRequestFrom(t, "203.0.113.7:44321", "198.51.100.9")))
}

func TestClientIP_RejectsAGarbageForwardedForEntry(t *testing.T) {
	resolver, err := newIPResolver([]string{"10.0.0.0/8"})
	require.NoError(t, err)

	assert.Equal(t, "10.1.2.3", resolver.clientIP(newRequestFrom(t, "10.1.2.3:44321", "not-an-ip")))
}

func TestNewIPResolver_RejectsAnInvalidProxyEntry(t *testing.T) {
	_, err := newIPResolver([]string{"10.0.0.0/8", "nonsense"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonsense")
}
