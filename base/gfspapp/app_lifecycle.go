package gfspapp

import (
	"context"
	"os"
	"os/signal"
	"time"

	corelifecycle "github.com/mocachain/moca-storage-provider/core/lifecycle"
	"github.com/mocachain/moca-storage-provider/pkg/log"
)

const (
	// DefaultStopTime bounds how long stopping the registered services may take.
	DefaultStopTime = 30 * time.Second
	// shutdownGrace is the margin on top of the service and gRPC stop budgets
	// before the process exits on its own.
	shutdownGrace = 5 * time.Second
)

var (
	// stopTimeout and exit are variables so tests can shorten and observe them.
	stopTimeout = DefaultStopTime
	exit        = os.Exit
)

// shutdownDeadline is the longest a stop may take from the first signal until the
// process exits: the service stop budget, the gRPC stop budget and a margin. It
// stays below systemd's default TimeoutStopSec (90s), so a supervisor never has
// to escalate to SIGKILL, which cannot be handled.
func shutdownDeadline() time.Duration {
	return stopTimeout + grpcGracefulStopTimeout + shutdownGrace
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
		log.Infow("received signal, stopping", "signal", sig.String())
		g.appCancel()
	}
	// Any signal received while stopping ends the process at once, so an
	// operator (or a supervisor that resends SIGTERM) is never ignored.
	sig := <-sigCh
	log.Errorw("received signal while stopping, exiting now", "signal", sig.String())
	exit(1)
}

// Wait blocks until context is done.
func (g *GfSpBaseApp) Wait(ctx context.Context) {
	<-g.appCtx.Done()
	g.StopServices(ctx)
}

// StopServices stops the registered services and returns once they have all
// stopped or stopTimeout has passed, whichever comes first.
func (g *GfSpBaseApp) StopServices(ctx context.Context) {
	stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		g.stopServices(stopCtx, cancel)
		close(done)
	}()
	select {
	case <-done:
		log.Infow("all services stopped")
	case <-stopCtx.Done():
		select {
		case <-done:
			log.Infow("all services stopped")
		default:
			log.Errorw("services did not stop in time, continuing shutdown", "timeout", stopTimeout)
		}
	}
}

func (g *GfSpBaseApp) stopServices(ctx context.Context, cancel context.CancelFunc) {
	for _, service := range g.services {
		if err := service.Stop(ctx); err != nil {
			log.Errorw("failed to stop service", "service_name", service.Name(), "error", err)
		} else {
			log.Infow("succeed to stop service", "service_name", service.Name())
		}
	}
	cancel()
}

// Done check context is done.
func (g *GfSpBaseApp) Done() <-chan struct{} {
	return g.appCtx.Done()
}
