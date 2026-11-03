package gfspapp

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/mocachain/moca-storage-provider/core/module"
	"github.com/mocachain/moca-storage-provider/pkg/log"
)

// An open server stream (peers and clients keep some) must not keep the gRPC
// server, and so the process, from stopping.
func TestStopRPCServer_ClosesOpenStreams(t *testing.T) {
	prev := grpcGracefulStopTimeout
	grpcGracefulStopTimeout = 200 * time.Millisecond
	t.Cleanup(func() { grpcGracefulStopTimeout = prev })

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	g := &GfSpBaseApp{server: grpc.NewServer()}
	healthpb.RegisterHealthServer(g.server, health.NewServer())
	go func() { _ = g.server.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	stream, err := healthpb.NewHealthClient(conn).Watch(context.Background(), &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- g.StopRPCServer(context.Background()) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("StopRPCServer did not return while a stream was open")
	}
	_, err = stream.Recv()
	require.Error(t, err, "the open stream is closed")
}

// A handler that ignores the cancellation of its context (for example one blocked
// on a backend call without a deadline) must not keep the gRPC server, and so the
// process, from stopping.
func TestStopRPCServer_ReturnsWhileHandlerIgnoresCancellation(t *testing.T) {
	prevGraceful, prevForce := grpcGracefulStopTimeout, grpcForceStopTimeout
	grpcGracefulStopTimeout, grpcForceStopTimeout = 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { grpcGracefulStopTimeout, grpcForceStopTimeout = prevGraceful, prevForce })

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	g := &GfSpBaseApp{server: grpc.NewServer()}
	g.server.RegisterService(&grpc.ServiceDesc{
		ServiceName: "test.Blocking",
		HandlerType: (*interface{})(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Block",
			Handler: func(_ interface{}, _ context.Context, dec func(interface{}) error, _ grpc.UnaryServerInterceptor) (interface{}, error) {
				in := new(emptypb.Empty)
				if err := dec(in); err != nil {
					return nil, err
				}
				entered <- struct{}{}
				<-release // ignores the cancellation of its context
				return in, nil
			},
		}},
	}, struct{}{})
	go func() { _ = g.server.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		_ = conn.Invoke(context.Background(), "/test.Blocking/Block", &emptypb.Empty{}, new(emptypb.Empty))
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the RPC did not reach its handler")
	}

	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- g.StopRPCServer(context.Background()) }()
	select {
	case err := <-done:
		require.NoError(t, err)
		require.Less(t, time.Since(start), time.Second, "bounded by the graceful and force stop budgets")
	case <-time.After(5 * time.Second):
		t.Fatal("StopRPCServer did not return while a handler ignored its cancellation")
	}
}

// A service whose Stop never returns must not block the shutdown.
func TestStopServices_ReturnsAfterTimeout(t *testing.T) {
	prev := stopTimeout
	stopTimeout = 100 * time.Millisecond
	t.Cleanup(func() { stopTimeout = prev })

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	ctrl := gomock.NewController(t)
	m := module.NewMockModular(ctrl)
	m.EXPECT().Stop(gomock.Any()).DoAndReturn(func(context.Context) error {
		<-release
		return nil
	}).AnyTimes()
	m.EXPECT().Name().Return("stuck").AnyTimes()
	g := &GfSpBaseApp{}
	g.RegisterServices(m)

	done := make(chan struct{})
	go func() {
		g.StopServices(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StopServices did not return after stopTimeout")
	}
}

// A stop that finishes in time is not reported as a timeout. The services' stop
// context is only cancelled by its deadline, so a successful stop cannot race
// with it.
func TestStopServices_SuccessIsNotReportedAsTimeout(t *testing.T) {
	w := &captureWriter{}
	log.SetWriter(w)
	t.Cleanup(func() { log.SetWriter(log.AddSync(os.Stderr)) })

	ctrl := gomock.NewController(t)
	m := module.NewMockModular(ctrl)
	m.EXPECT().Stop(gomock.Any()).Return(nil).AnyTimes()
	m.EXPECT().Name().Return("quick").AnyTimes()
	g := &GfSpBaseApp{}
	g.RegisterServices(m)
	for i := 0; i < 200; i++ {
		g.StopServices(context.Background())
	}
	require.Contains(t, w.String(), "all services stopped")
	require.NotContains(t, w.String(), "did not stop in time")
}

// The first signal starts the shutdown; a second one ends the process at once.
func TestSignals_SecondSignalExits(t *testing.T) {
	exited := make(chan int, 1)
	prev := exit
	exit = func(code int, _ string, _ ...interface{}) { exited <- code }
	t.Cleanup(func() { exit = prev })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	g := &GfSpBaseApp{appCtx: ctx, appCancel: cancel}
	g.Signals(syscall.SIGUSR1)
	time.Sleep(100 * time.Millisecond) // let signal.Notify register

	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGUSR1))
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the first signal did not start the shutdown")
	}
	select {
	case code := <-exited:
		t.Fatalf("exited with %d after the first signal", code)
	case <-time.After(200 * time.Millisecond):
	}

	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGUSR1))
	select {
	case code := <-exited:
		require.Equal(t, 1, code)
	case <-time.After(5 * time.Second):
		t.Fatal("the second signal did not end the process")
	}
}

func TestShutdownDeadline_BelowSystemdDefault(t *testing.T) {
	require.Less(t, shutdownDeadline(), 90*time.Second, "systemd's default TimeoutStopSec")
}

// The forced exit (watchdog, second signal) and the end of a normal stop both
// write and flush logs; a log writer that blocks must delay neither by more than
// logFlushTimeout. Each case runs in a child process because the stalled writer
// and the real os.Exit cannot be undone in this one.
func TestShutdown_DoesNotWaitForStalledLogWriter(t *testing.T) {
	for _, tc := range []struct {
		mode string
		code int
	}{
		{"exit-write", 1}, // exit(1), the write of its log line blocks
		{"exit-stop", 1},  // exit(1), flushing the buffered lines blocks
		{"finish-write", 0},
		{"finish-stop", 0},
		{"signals-write", 1}, // the first signal's log must not prevent either signal from being handled
	} {
		t.Run(tc.mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestShutdownHelperProcess$")
			cmd.Env = append(os.Environ(), "GFSPAPP_SHUTDOWN_HELPER="+tc.mode)
			start := time.Now()
			require.NoError(t, cmd.Start())
			waited := make(chan error, 1)
			go func() { waited <- cmd.Wait() }()
			select {
			case err := <-waited:
				code := 0
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					code = exitErr.ExitCode()
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, tc.code, code)
				require.Less(t, time.Since(start), 5*time.Second)
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatal("the process was still running after 10s")
			}
		})
	}
}

// TestShutdownHelperProcess is the child process of
// TestShutdown_DoesNotWaitForStalledLogWriter; it does nothing on its own.
func TestShutdownHelperProcess(t *testing.T) {
	mode := os.Getenv("GFSPAPP_SHUTDOWN_HELPER")
	if mode == "" {
		return
	}
	logFlushTimeout = 100 * time.Millisecond
	block := make(chan struct{})
	switch mode {
	case "exit-write", "finish-write", "signals-write":
		log.SetWriter(&stalledWriter{write: block, stop: block})
	default:
		log.SetWriter(&stalledWriter{stop: block})
	}
	switch mode {
	case "exit-write", "exit-stop":
		exit(1, "exiting")
	case "signals-write":
		ctx, cancel := context.WithCancel(context.Background())
		g := &GfSpBaseApp{appCtx: ctx, appCancel: cancel}
		g.Signals(syscall.SIGUSR1)
		time.Sleep(100 * time.Millisecond) // let signal.Notify register
		if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
			os.Exit(5)
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			os.Exit(4) // logging prevented the first signal from starting shutdown
		}
		if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
			os.Exit(6)
		}
		select {} // the real second-signal exit must end the process
	default:
		if finishLogs(func() { log.Infow("stopped") }) {
			os.Exit(3) // the stalled writer cannot have finished
		}
		os.Exit(0)
	}
}

// stalledWriter blocks in Write while write is open (nil: never) and in Stop
// while stop is open.
type stalledWriter struct {
	write, stop chan struct{}
}

func (w *stalledWriter) Write(p []byte) (int, error) {
	if w.write != nil {
		<-w.write
	}
	return len(p), nil
}

func (w *stalledWriter) Sync() error { return nil }

func (w *stalledWriter) Stop() error {
	<-w.stop
	return nil
}

type captureWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *captureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *captureWriter) Sync() error { return nil }

func (w *captureWriter) Stop() error { return nil }

func (w *captureWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
