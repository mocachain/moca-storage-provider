package pprof

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

func startProfilerServer(t *testing.T, writeTimeout time.Duration) string {
	t.Helper()
	p := NewPProf("127.0.0.1:0")
	router := mux.NewRouter()
	p.registerProfiler(router)
	srv := &http.Server{
		Handler:           router,
		ReadHeaderTimeout: time.Second,
		WriteTimeout:      writeTimeout,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func getOK(t *testing.T, url string) {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err, "request must not be cut off by the write timeout")
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotEmpty(t, body, "body must arrive after the collection window")
}

// TestFgprofLongerThanWriteTimeout collects an fgprof profile whose window is
// at/above the server's WriteTimeout. fgprof writes only after collecting and,
// unlike net/http/pprof, does not extend the write deadline itself, so without
// durationAware the server terminates the connection before the response.
func TestFgprofLongerThanWriteTimeout(t *testing.T) {
	addr := startProfilerServer(t, 500*time.Millisecond)
	getOK(t, "http://"+addr+"/debug/fgprof?seconds=1")
}

// TestCPUProfileLongerThanWriteTimeout pins the equivalent behavior for the
// stock CPU-profile route, which net/http/pprof protects itself.
func TestCPUProfileLongerThanWriteTimeout(t *testing.T) {
	addr := startProfilerServer(t, 500*time.Millisecond)
	getOK(t, "http://"+addr+"/debug/pprof/profile?seconds=1")
}
