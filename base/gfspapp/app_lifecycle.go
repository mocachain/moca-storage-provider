package gfspapp

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"time"

	corelifecycle "github.com/mocachain/moca-storage-provider/core/lifecycle"
	"github.com/mocachain/moca-storage-provider/pkg/log"
)

const (
	// DefaultStopTime bounds how long stopping the registered services may take.
	DefaultStopTime = 30 * time.Second
	// shutdownGrace is the margin on top of the service and gRPC stop budgets
	// before the process exits on its own.
	shutdownGrace = 3 * time.Second
)

var (
	// stopTimeout, logFlushTimeout and exit are variables so tests can shorten
	// and observe them.
	stopTimeout = DefaultStopTime
	// logFlushTimeout bounds writing the last log line and flushing the
	// buffered ones, so a stalled log writer cannot keep the process alive.
	logFlushTimeout = 2 * time.Second
	// exit logs msg as an error, flushes the logs for at most logFlushTimeout
	// and ends the process with code.
	exit = func(code int, msg string, keysAndValues ...interface{}) {
		finishLogs(func() { log.Errorw(msg, keysAndValues...) })
		os.Exit(code)
	}

	stopLogsOnce sync.Once
)

// shutdownDeadline is the longest a stop may take from the first signal until the
// process exits: the service stop budget, the gRPC stop budgets and a margin. It
// stays below systemd's default TimeoutStopSec (90s), so a supervisor never has
// to escalate to SIGKILL, which cannot be handled.
func shutdownDeadline() time.Duration {
	return stopTimeout + grpcGracefulStopTimeout + grpcForceStopTimeout + shutdownGrace
}

// finishLogs runs last, which writes the last log lines, then stops the logger,
// which flushes the buffered ones. It waits for both at most logFlushTimeout and
// reports whether they finished: a log writer that blocks (a full stdout pipe, a
// stalled disk) is left behind so the process can still exit.
func finishLogs(last func()) bool {
	done := make(chan struct{})
	go func() {
		last()
		// Once also makes a concurrent caller wait for the first stop to finish.
		stopLogsOnce.Do(log.Stop)
		close(done)
	}()
	timer := time.NewTimer(logFlushTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

var _ corelifecycle.Lifecycle = &GfSpBaseApp{}

// RegisterServices register services of an application.
func (g *GfSpBaseApp) RegisterServices(services ...corelifecycle.Service) {
	g.services = append(g.services, services...)
}

// StartServices starts running services.
func (g *GfSpBaseApp) StartServices(ctx context.Context) corelifecycle.Lifecycle {
	g.appCtx, g.appCancel = context.WithCancel(ctx)
	g.startServices(ctx)
	return g
}

func (g *GfSpBaseApp) startServices(ctx context.Context) {
	for i, service := range g.services {
		if err := service.Start(ctx); err != nil {
			g.GetProbe().Unready(err)
			log.Errorw("failed to start service", "service_name", service.Name(), "error", err)
			g.services = g.services[:i]
			g.appCancel()
			break
		} else {
			g.GetProbe().Ready()
			log.Infow("succeed to start service", "service_name", service.Name())
		}
	}
}

// Signals registers monitor signals.
func (g *GfSpBaseApp) Signals(sigs ...os.Signal) corelifecycle.Lifecycle {
	go g.signals(sigs...)
	return g
}

func (g *GfSpBaseApp) signals(sigs ...os.Signal) {
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, sigs...)
	select {
	case <-g.appCtx.Done():
	case sig := <-sigCh:
		g.appCancel()
		// A stalled log writer must not delay cancellation or the next signal.
		go log.Infow("received signal, stopping", "signal", sig.String())
	}
	// Any signal received while stopping ends the process at once, so an
	// operator (or a supervisor that resends SIGTERM) is never ignored.
	sig := <-sigCh
	exit(1, "received signal while stopping, exiting now", "signal", sig.String())
}

// Wait blocks until context is done.
func (g *GfSpBaseApp) Wait(ctx context.Context) {
	<-g.appCtx.Done()
	g.StopServices(ctx)
}

// StopServices stops the registered services and returns once they have all
// stopped or stopTimeout has passed, whichever comes first.
func (g *GfSpBaseApp) StopServices(ctx context.Context) {
	// Only the deadline cancels stopCtx before this function returns, so its
	// Done channel means the services ran out of time.
	stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		g.stopServices(stopCtx)
		close(done)
	}()
	select {
	case <-done:
		log.Infow("all services stopped")
	case <-stopCtx.Done():
		select {
		case <-done: // finished just as the deadline passed
			log.Infow("all services stopped")
		default:
			log.Errorw("services did not stop in time, continuing shutdown", "timeout", stopTimeout)
		}
	}
}

func (g *GfSpBaseApp) stopServices(ctx context.Context) {
	for _, service := range g.services {
		if err := service.Stop(ctx); err != nil {
			log.Errorw("failed to stop service", "service_name", service.Name(), "error", err)
		} else {
			log.Infow("succeed to stop service", "service_name", service.Name())
		}
	}
}

// Done check context is done.
func (g *GfSpBaseApp) Done() <-chan struct{} {
	return g.appCtx.Done()
}
