package signer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/mocachain/moca-storage-provider/pkg/log"
)

var (
	evmAuditActorValue  = defaultEvmAuditActor()
	evmToolVersionValue = defaultEvmToolVersion()
)

func defaultEvmAuditActor() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("signer/%s/%d", host, os.Getpid())
}

func defaultEvmToolVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	version := info.Main.Version
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			version += "@" + setting.Value
		}
	}
	return version
}

func evmAuditActor() string  { return evmAuditActorValue }
func evmToolVersion() string { return evmToolVersionValue }

// newEvmLeaseOwner returns "<hostname>/<pid>/<random>", unique per process start.
func newEvmLeaseOwner() string {
	host, _ := os.Hostname()
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	return fmt.Sprintf("%s/%d/%s", host, os.Getpid(), hex.EncodeToString(nonce))
}

// evmLease keeps the single-writer lease for one chain. Correctness comes
// from the fencing check inside every write transaction; the local view here
// only lets the signer fail early once renewals stop succeeding.
type evmLease struct {
	store   evmStateStore
	chainID string
	owner   string
	ttl     time.Duration
	now     func() time.Time
	// onAcquire runs on the lease goroutine after each new epoch, before
	// the next renewal.
	onAcquire func(ctx context.Context, fence evmFence)

	mu        sync.Mutex
	held      bool
	epoch     uint64
	lastRenew time.Time

	cancel context.CancelFunc
	done   chan struct{}
}

func newEvmLease(store evmStateStore, chainID string, ttl time.Duration) *evmLease {
	if ttl <= 0 {
		ttl = evmDefaultLeaseTTL
	}
	return &evmLease{store: store, chainID: chainID, owner: newEvmLeaseOwner(), ttl: ttl, now: time.Now}
}

// fence returns the lease to write under, or false when this process must
// not sign: the lease is not held, or the last successful renewal is older
// than TTL/2.
func (l *evmLease) fence() (evmFence, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held || l.now().Sub(l.lastRenew) > l.ttl/2 {
		return evmFence{}, false
	}
	return evmFence{ChainID: l.chainID, Owner: l.owner, Epoch: l.epoch}, true
}

// tick acquires or renews once.
func (l *evmLease) tick(ctx context.Context) {
	l.mu.Lock()
	held, epoch, lastRenew := l.held, l.epoch, l.lastRenew
	l.mu.Unlock()

	if !held {
		newEpoch, acquired, err := l.store.AcquireLease(ctx, l.chainID, l.owner, l.ttl)
		if err != nil {
			log.CtxErrorw(ctx, "failed to acquire the signer EVM lease", "chain_id", l.chainID, "error", err)
			return
		}
		if !acquired {
			log.CtxWarnw(ctx, "signer EVM lease is held by another signer; EVM requests fail until it expires", "chain_id", l.chainID)
			return
		}
		l.mu.Lock()
		l.held, l.epoch, l.lastRenew = true, newEpoch, l.now()
		l.mu.Unlock()
		log.CtxWarnw(ctx, "acquired the signer EVM lease", "chain_id", l.chainID, "owner", l.owner, "epoch", newEpoch)
		if l.onAcquire != nil {
			l.onAcquire(ctx, evmFence{ChainID: l.chainID, Owner: l.owner, Epoch: newEpoch})
		}
		return
	}

	fence := evmFence{ChainID: l.chainID, Owner: l.owner, Epoch: epoch}
	renewed, err := l.store.RenewLease(ctx, fence, l.ttl)
	switch {
	case err == nil && renewed:
		l.mu.Lock()
		l.lastRenew = l.now()
		l.mu.Unlock()
	case err == nil:
		l.lose(ctx, fence, "renewal found the lease taken or expired")
	default:
		log.CtxErrorw(ctx, "failed to renew the signer EVM lease", "chain_id", l.chainID, "epoch", epoch, "error", err)
		if l.now().Sub(lastRenew) > l.ttl {
			l.lose(ctx, fence, "renewal failed for longer than the lease TTL: "+err.Error())
		}
	}
}

func (l *evmLease) lose(ctx context.Context, fence evmFence, reason string) {
	l.mu.Lock()
	if !l.held || l.epoch != fence.Epoch {
		l.mu.Unlock()
		return
	}
	l.held = false
	l.mu.Unlock()
	log.CtxErrorw(ctx, "lost the signer EVM lease; EVM requests fail until it is acquired again",
		"chain_id", l.chainID, "epoch", fence.Epoch, "reason", reason)
	if err := l.store.AppendAudit(ctx, nil, l.chainID, evmAuditEvent{Event: evmAuditLeaseLost, Detail: reason}); err != nil {
		log.CtxErrorw(ctx, "failed to audit the lost signer EVM lease", "error", err)
	}
}

// start runs acquisition and renewal every TTL/3 until stop.
func (l *evmLease) start(ctx context.Context) {
	ctx, l.cancel = context.WithCancel(ctx)
	l.done = make(chan struct{})
	go func() {
		defer close(l.done)
		ticker := time.NewTicker(l.ttl / 3)
		defer ticker.Stop()
		for {
			l.tick(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// stop ends the loop and releases the lease so a successor need not wait
// for it to expire.
func (l *evmLease) stop(ctx context.Context) {
	if l.cancel != nil {
		l.cancel()
		<-l.done
	}
	l.mu.Lock()
	held, epoch := l.held, l.epoch
	l.held = false
	l.mu.Unlock()
	if !held {
		return
	}
	if err := l.store.ReleaseLease(ctx, evmFence{ChainID: l.chainID, Owner: l.owner, Epoch: epoch}); err != nil {
		log.CtxErrorw(ctx, "failed to release the signer EVM lease; a successor waits for it to expire", "error", err)
	}
}
