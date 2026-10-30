package gfspapp

import (
	"context"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/mocachain/moca-storage-provider/core/module"
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

// The first signal starts the shutdown; a second one ends the process at once.
func TestSignals_SecondSignalExits(t *testing.T) {
	exited := make(chan int, 1)
	prev := exit
	exit = func(code int) { exited <- code }
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
