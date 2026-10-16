package script

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const containerName = "moca-sp-test-mysql"

// mysqlGreeting is the start of a MySQL handshake: a MySQL server speaks first.
var mysqlGreeting = []byte("J\x00\x00\x00\n8.0.43\x00")

// fakeBin holds the one copy of testdata/fake-docker every test puts first on PATH:
// macOS assesses each freshly written executable on its first exec, which can take
// seconds under load, so the copy is made and warmed once per test binary.
var fakeBin string

// nextPort hands each test a loopback port of its own below the OS ephemeral range
// (32768 and up on Linux, 49152 and up on macOS), so neither Listen(":0") in a parallel
// test nor the script's outgoing probes can take it; the pid keeps test binaries apart.
var nextPort atomic.Int32

func TestMain(m *testing.M) {
	nextPort.Store(int32(20000 + os.Getpid()%10000))
	src, err := os.ReadFile(filepath.Join("testdata", "fake-docker"))
	if err != nil {
		panic(err)
	}
	dir, err := os.MkdirTemp("", "fake-docker")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docker"), src, 0o755); err != nil {
		panic(err)
	}
	fakeBin = dir
	warm := exec.Command(filepath.Join(dir, "docker"), "ps", "-a", "--filter", "name=^/"+containerName+"$", "--format", "{{.State}}")
	warm.Env = []string{"PATH=" + os.Getenv("PATH"), "FAKE_DOCKER_STATE=" + dir}
	_ = warm.Run() // no container yet: prints nothing
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// fakeDocker runs test-mysql.sh against the recording docker double with its own state dir.
type fakeDocker struct {
	state string
}

func newFakeDocker(t *testing.T) *fakeDocker {
	t.Helper()
	f := &fakeDocker{state: filepath.Join(t.TempDir(), "state")}
	if err := os.MkdirAll(filepath.Join(f.state, "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

// seed makes the fake know a container left behind by an earlier run.
func (f *fakeDocker) seed(t *testing.T, state, binding string) {
	t.Helper()
	dir := filepath.Join(f.state, "c", containerName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"state": state, "binding": binding} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// lines counts the recorded lines of file (calls.log or events.log) starting with prefix.
func (f *fakeDocker) lines(t *testing.T, file, prefix string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.state, file))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" && strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}

// up runs "test-mysql.sh up addr" with the fake docker first on PATH and a fast poll, and
// returns its exit code and combined output (-1 when it could not run at all, reported
// with t.Errorf since up is also called from goroutines); env holds extra KEY=VALUE knobs.
func (f *fakeDocker) up(t *testing.T, addr string, timeout int, env ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(2*timeout+10)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "test-mysql.sh", "up", addr)
	cmd.WaitDelay = time.Second
	cmd.Env = append([]string{
		"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_DOCKER_STATE=" + f.state,
		"TEST_MYSQL_POLL=0.05",
		fmt.Sprintf("TEST_MYSQL_TIMEOUT=%d", timeout),
	}, env...)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &exit):
		return exit.ExitCode(), string(out)
	default:
		t.Errorf("running test-mysql.sh: %v\n%s", err, out)
		return -1, string(out)
	}
}

// listenFree binds the next port of this test binary's range, skipping ports in use.
func listenFree(t *testing.T) net.Listener {
	t.Helper()
	for i := 0; i < 1000; i++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", nextPort.Add(1)))
		if err == nil {
			return ln
		}
	}
	t.Fatal("no free loopback port in the test range")
	return nil
}

// freeAddr returns a loopback host:port nothing listens on. A wildcard listener on the
// same port is invisible to the loopback bind on macOS, so the port is also dialled.
func freeAddr(t *testing.T) string {
	t.Helper()
	for i := 0; i < 1000; i++ {
		ln := listenFree(t)
		addr := ln.Addr().String()
		_ = ln.Close()
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return addr
		}
		_ = conn.Close()
	}
	t.Fatal("no unused loopback port in the test range")
	return ""
}

// listen serves greeting (or silence when nil) on a port of its own and returns host:port.
func listen(t *testing.T, greeting []byte) string {
	t.Helper()
	ln := listenFree(t)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				if greeting != nil {
					_, _ = conn.Write(greeting)
				}
				_, _ = io.Copy(io.Discard, conn) // hold the connection until the client goes away
			}()
		}
	}()
	return ln.Addr().String()
}

func expect(t *testing.T, got int, want int, out, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %d, want %d\n%s", what, got, want, out)
	}
}

func expectOutput(t *testing.T, out, want string) {
	t.Helper()
	if !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
}

func TestUpConcurrentColdStartSharesOneContainer(t *testing.T) {
	t.Parallel()
	f := newFakeDocker(t)
	addr := freeAddr(t)
	const n = 3
	var wg sync.WaitGroup
	codes := make([]int, n)
	outs := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// the barrier parks every invocation inside "docker run" before any create,
			// so all of them have passed the "no container, nothing listening" checks
			codes[i], outs[i] = f.up(t, addr, 30, fmt.Sprintf("FAKE_DOCKER_RUN_BARRIER=%d", n))
		}(i)
	}
	wg.Wait()
	for i := range codes {
		expect(t, codes[i], 0, outs[i], fmt.Sprintf("invocation %d exit code", i))
		expectOutput(t, outs[i], "mysql is ready on "+addr)
	}
	expect(t, f.lines(t, "calls.log", "run "), n, "", "docker run attempts")
	expect(t, f.lines(t, "events.log", "created "), 1, "", "containers created")
	expect(t, f.lines(t, "events.log", "conflict "), n-1, "", "name conflicts")
	expect(t, f.lines(t, "calls.log", "rm "), 0, "", "docker rm calls")
}

func TestUpAdoptsContainerStillCreated(t *testing.T) {
	t.Parallel()
	f := newFakeDocker(t)
	addr := freeAddr(t)
	f.seed(t, "created", addr) // another invocation's docker run has created but not yet started it
	code, out := f.up(t, addr, 30)
	expect(t, code, 0, out, "exit code")
	expectOutput(t, out, "mysql is ready on "+addr)
	expect(t, f.lines(t, "calls.log", "run "), 0, out, "docker run calls")
	expect(t, f.lines(t, "calls.log", "rm "), 0, out, "docker rm calls")
}

func TestUpStartsStoppedContainer(t *testing.T) {
	t.Parallel()
	f := newFakeDocker(t)
	addr := freeAddr(t)
	f.seed(t, "exited", addr)
	code, out := f.up(t, addr, 30, "FAKE_DOCKER_READY_AFTER=1")
	expect(t, code, 0, out, "exit code")
	expect(t, f.lines(t, "calls.log", "start "+containerName), 1, out, "docker start calls")
	expect(t, f.lines(t, "calls.log", "run "), 0, out, "docker run calls")
	expect(t, f.lines(t, "calls.log", "rm "), 0, out, "docker rm calls")
}

func TestUpReuseWaitsForReadiness(t *testing.T) {
	t.Parallel()
	f := newFakeDocker(t)
	addr := freeAddr(t)
	f.seed(t, "running", addr) // started by somebody else, mysqld still initializing
	code, out := f.up(t, addr, 30, "FAKE_DOCKER_READY_AFTER=3")
	expect(t, code, 0, out, "exit code")
	expectOutput(t, out, "mysql is ready on "+addr)
	expect(t, f.lines(t, "calls.log", "exec "+containerName), 3, out, "readiness checks")
	for _, call := range []string{"run ", "start ", "rm "} {
		expect(t, f.lines(t, "calls.log", call), 0, out, "docker "+call+"calls")
	}
}

func TestUpForeignSilentListenerFailsClearly(t *testing.T) {
	t.Parallel()
	f := newFakeDocker(t)
	addr := listen(t, nil)
	start := time.Now()
	code, out := f.up(t, addr, 3)
	expect(t, code, 1, out, "exit code")
	expectOutput(t, out, "something listens on "+addr+" but does not answer like MySQL")
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("waited %s for a silent listener", d)
	}
	expect(t, f.lines(t, "calls.log", "run "), 0, out, "docker run calls")
}

func TestUpUsesForeignServerThatGreets(t *testing.T) {
	t.Parallel()
	f := newFakeDocker(t)
	addr := listen(t, mysqlGreeting)
	start := time.Now()
	code, out := f.up(t, addr, 30)
	expect(t, code, 0, out, "exit code")
	expectOutput(t, out, "already answering on "+addr)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %s to accept a greeting server", d)
	}
	expect(t, f.lines(t, "calls.log", "run "), 0, out, "docker run calls")
}

func TestUpExternalAddressSkipsDocker(t *testing.T) {
	t.Parallel()
	f := newFakeDocker(t)
	code, out := f.up(t, freeAddr(t), 30, "BLOCKSYNCER_TEST_DB_ADDRESS=db.example:3306")
	expect(t, code, 0, out, "exit code")
	expect(t, f.lines(t, "calls.log", ""), 0, out, "docker calls")
}
