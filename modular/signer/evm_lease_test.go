package signer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newClockedLease(store *memEvmStateStore, clock *fakeClock, ttl time.Duration) *evmLease {
	lease := newEvmLease(store, fakeEvmChainID, ttl)
	lease.now = clock.Now
	return lease
}

func TestEvmLeaseSingleHolderAndFencing(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	store := newMemEvmStateStore()
	store.now = clock.Now
	ttl := 30 * time.Second
	a, b := newClockedLease(store, clock, ttl), newClockedLease(store, clock, ttl)
	ctx := context.Background()

	if _, ok := a.fence(); ok {
		t.Fatal("no fence before the lease is acquired")
	}
	a.tick(ctx)
	fenceA, ok := a.fence()
	if !ok || fenceA.Epoch != 1 {
		t.Fatalf("a must hold epoch 1, got %+v ok=%t", fenceA, ok)
	}
	b.tick(ctx)
	if _, ok := b.fence(); ok {
		t.Fatal("b must not acquire a live lease")
	}

	// a stops renewing; past the TTL b takes over with a new epoch
	clock.advance(ttl + time.Second)
	if _, ok := a.fence(); ok {
		t.Fatal("a must stop signing once renewals are older than TTL/2")
	}
	b.tick(ctx)
	fenceB, ok := b.fence()
	if !ok || fenceB.Epoch != 2 {
		t.Fatalf("b must hold epoch 2, got %+v ok=%t", fenceB, ok)
	}
	if err := store.AppendAudit(ctx, &fenceA, fakeEvmChainID, evmAuditEvent{Event: "probe"}); !errors.Is(err, errEvmLeaseNotHeld) {
		t.Fatalf("a's writes must be fenced, got %v", err)
	}
	if err := store.AppendAudit(ctx, &fenceB, fakeEvmChainID, evmAuditEvent{Event: "probe"}); err != nil {
		t.Fatalf("b's writes must pass, got %v", err)
	}

	// a's next renewal finds the lease taken
	a.tick(ctx)
	if len(store.auditEvents(evmAuditLeaseLost)) != 1 {
		t.Fatal("expected a lease_lost audit")
	}
	if len(store.auditEvents(evmAuditLeaseAcquired)) != 2 {
		t.Fatal("expected one lease_acquired audit per epoch")
	}
}

func TestEvmLeaseRenewalFailureStopsSigningEarly(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	store := newMemEvmStateStore()
	store.now = clock.Now
	ttl := 30 * time.Second
	lease := newClockedLease(store, clock, ttl)
	ctx := context.Background()
	lease.tick(ctx)

	store.inject("renew", memFaultFail, memFaultFail, memFaultFail)
	clock.advance(ttl / 3)
	lease.tick(ctx)
	if _, ok := lease.fence(); !ok {
		t.Fatal("one failed renewal within TTL/2 keeps signing")
	}
	clock.advance(ttl / 3)
	lease.tick(ctx)
	if _, ok := lease.fence(); ok {
		t.Fatal("renewals failing for more than TTL/2 must stop signing")
	}
	clock.advance(ttl / 2)
	lease.tick(ctx) // past the TTL: the lease is given up
	if len(store.auditEvents(evmAuditLeaseLost)) != 1 {
		t.Fatal("expected a lease_lost audit")
	}
	lease.tick(ctx) // and taken again with a new epoch
	fence, ok := lease.fence()
	if !ok || fence.Epoch != 2 {
		t.Fatalf("expected the lease again at epoch 2, got %+v ok=%t", fence, ok)
	}
}

func TestEvmLeaseStopReleasesForSuccessor(t *testing.T) {
	store := newMemEvmStateStore()
	a := newEvmLease(store, fakeEvmChainID, time.Hour)
	b := newEvmLease(store, fakeEvmChainID, time.Hour)
	ctx := context.Background()
	a.start(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := a.fence(); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lease loop did not acquire")
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.stop(ctx)
	if _, ok := a.fence(); ok {
		t.Fatal("a stopped lease must not sign")
	}
	b.tick(ctx)
	if _, ok := b.fence(); !ok {
		t.Fatal("a released lease must be available at once")
	}
}
