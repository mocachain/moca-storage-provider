package signer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	ethcmn "github.com/ethereum/go-ethereum/common"
)

// memFault is an injected failure for one store call.
type memFault int

const (
	memFaultNone memFault = iota
	// memFaultFail rolls the transaction back (or fails the read).
	memFaultFail
	// memFaultCommitUnknownApplied commits, then reports the commit as unknown.
	memFaultCommitUnknownApplied
	// memFaultCommitUnknownLost does not commit and reports it as unknown.
	memFaultCommitUnknownLost
)

var errMemInjected = errors.New("injected store failure")

type memLease struct {
	owner   string
	epoch   uint64
	expires time.Time
}

type memAudit struct {
	chainID string
	epoch   uint64
	event   evmAuditEvent
}

// memEvmStateStore is the in-memory evmStateStore for tests. It keeps the
// semantics the SQL store has: fencing, 0-row and conflict errors, unique
// keys, the append-only evidence table with RESTRICT references, and
// injectable failures per call ("record", "mark", "resolve", "consume",
// "audit" for any audit insert inside a transition, "append_audit",
// "acquire", "renew", and reads "get_pending", "get_outcome",
// "get_signed_tx", "list_pending", "list_outcomes", "has_audit").
type memEvmStateStore struct {
	mu       sync.Mutex
	now      func() time.Time
	nextID   uint64
	signed   map[uint64]signedTxEvidence
	pending  map[uint64]pendingRecord
	outcomes map[uint64]outcomeRecord
	leases   map[string]*memLease
	audits   []memAudit
	faults   map[string][]memFault
	calls    map[string]int
	// hooks run once, under the store lock, when op is next called; they
	// change the maps directly, as a concurrent writer would.
	hooks map[string]func(m *memEvmStateStore)
}

var _ evmStateStore = (*memEvmStateStore)(nil)

func newMemEvmStateStore() *memEvmStateStore {
	return &memEvmStateStore{
		now:      time.Now,
		signed:   map[uint64]signedTxEvidence{},
		pending:  map[uint64]pendingRecord{},
		outcomes: map[uint64]outcomeRecord{},
		leases:   map[string]*memLease{},
		faults:   map[string][]memFault{},
		calls:    map[string]int{},
		hooks:    map[string]func(m *memEvmStateStore){},
	}
}

// hookOnce runs fn the next time op is called.

// inject queues faults for the next calls of op.
func (m *memEvmStateStore) inject(op string, faults ...memFault) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.faults[op] = append(m.faults[op], faults...)
}

func (m *memEvmStateStore) pop(op string) memFault {
	m.calls[op]++
	if hook, ok := m.hooks[op]; ok {
		delete(m.hooks, op)
		hook(m)
	}
	queue := m.faults[op]
	if len(queue) == 0 {
		return memFaultNone
	}
	m.faults[op] = queue[1:]
	return queue[0]
}

func (m *memEvmStateStore) id() uint64 {
	m.nextID++
	return m.nextID
}

func (m *memEvmStateStore) read(op string) error {
	if m.pop(op) != memFaultNone {
		return fmt.Errorf("%s: %w", op, errMemInjected)
	}
	return nil
}

func (m *memEvmStateStore) fence(f evmFence) error {
	lease, ok := m.leases[f.ChainID]
	if !ok || lease.owner != f.Owner || lease.epoch != f.Epoch || !lease.expires.After(m.now()) {
		return errEvmLeaseNotHeld
	}
	return nil
}

// commit applies a transaction body according to the fault injected for op.
// body must check everything first and only mutate through apply.
func (m *memEvmStateStore) commit(op string, check func() error, apply func()) error {
	fault := m.pop(op)
	if err := check(); err != nil {
		return err
	}
	switch fault {
	case memFaultFail:
		return fmt.Errorf("%s: %w", op, errMemInjected)
	case memFaultCommitUnknownLost:
		return &evmCommitUnknownError{err: fmt.Errorf("%s: %w", op, errMemInjected)}
	}
	apply()
	if fault == memFaultCommitUnknownApplied {
		return &evmCommitUnknownError{err: fmt.Errorf("%s: %w", op, errMemInjected)}
	}
	return nil
}

func (m *memEvmStateStore) auditCheck(n int) error {
	for i := 0; i < n; i++ {
		if m.pop("audit") != memFaultNone {
			return fmt.Errorf("audit insert: %w", errMemInjected)
		}
	}
	return nil
}

func (m *memEvmStateStore) addAudit(chainID string, epoch uint64, event evmAuditEvent) {
	m.audits = append(m.audits, memAudit{chainID: chainID, epoch: epoch, event: event})
}

func (m *memEvmStateStore) AcquireLease(_ context.Context, chainID, owner string, ttl time.Duration) (uint64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var epoch uint64
	acquired := false
	err := m.commit("acquire", func() error { return nil }, func() {
		lease, ok := m.leases[chainID]
		if !ok {
			lease = &memLease{}
			m.leases[chainID] = lease
		}
		if lease.expires.After(m.now()) && lease.owner != owner {
			return
		}
		previous := lease.owner
		lease.owner, lease.epoch, lease.expires = owner, lease.epoch+1, m.now().Add(ttl)
		epoch, acquired = lease.epoch, true
		m.addAudit(chainID, epoch, evmAuditEvent{Event: evmAuditLeaseAcquired, Detail: "owner=" + owner + " previous_owner=" + previous})
	})
	if err != nil {
		return 0, false, err
	}
	return epoch, acquired, nil
}

func (m *memEvmStateStore) RenewLease(_ context.Context, fence evmFence, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pop("renew") != memFaultNone {
		return false, errMemInjected
	}
	if m.fence(fence) != nil {
		return false, nil
	}
	m.leases[fence.ChainID].expires = m.now().Add(ttl)
	return true, nil
}

func (m *memEvmStateStore) ReleaseLease(_ context.Context, fence evmFence) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lease, ok := m.leases[fence.ChainID]; ok && lease.owner == fence.Owner && lease.epoch == fence.Epoch {
		lease.expires = time.Unix(1, 0)
	}
	return nil
}

func (m *memEvmStateStore) GetPending(_ context.Context, chainID string, scope SignType) (*pendingRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.read("get_pending"); err != nil {
		return nil, err
	}
	for _, p := range m.pending {
		if p.ChainID == chainID && p.Scope == scope {
			copied := p
			return &copied, nil
		}
	}
	return nil, nil
}

func (m *memEvmStateStore) ListPending(_ context.Context, chainID string) ([]pendingRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.read("list_pending"); err != nil {
		return nil, err
	}
	var out []pendingRecord
	for _, p := range m.pending {
		if p.ChainID == chainID {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *memEvmStateStore) GetOutcome(_ context.Context, chainID string, scope SignType, operation ethcmn.Hash) (*outcomeRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.read("get_outcome"); err != nil {
		return nil, err
	}
	for _, o := range m.outcomes {
		if o.ChainID == chainID && o.Scope == scope && o.Operation == operation {
			copied := o
			return &copied, nil
		}
	}
	return nil, nil
}

func (m *memEvmStateStore) ListOutcomes(_ context.Context, chainID string) ([]outcomeRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.read("list_outcomes"); err != nil {
		return nil, err
	}
	var out []outcomeRecord
	for _, o := range m.outcomes {
		if o.ChainID == chainID {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *memEvmStateStore) GetSignedTx(_ context.Context, id uint64) (*signedTxEvidence, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.read("get_signed_tx"); err != nil {
		return nil, err
	}
	evidence, ok := m.signed[id]
	if !ok {
		return nil, nil
	}
	evidence.Raw = append([]byte(nil), evidence.Raw...)
	return &evidence, nil
}

func (m *memEvmStateStore) HasAudit(_ context.Context, chainID, event string, txHash ethcmn.Hash, outcomeID uint64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.read("has_audit"); err != nil {
		return false, err
	}
	for _, a := range m.audits {
		if a.chainID != chainID || a.event.Event != event {
			continue
		}
		if txHash != (ethcmn.Hash{}) && a.event.TxHash != txHash {
			continue
		}
		if outcomeID != 0 && (a.event.OutcomeID == nil || *a.event.OutcomeID != outcomeID) {
			continue
		}
		return true, nil
	}
	return false, nil
}

func (m *memEvmStateStore) ForeignChainIDs(_ context.Context, chainID string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	for _, p := range m.pending {
		if p.ChainID != chainID {
			seen[p.ChainID] = true
		}
	}
	for _, o := range m.outcomes {
		if o.ChainID != chainID {
			seen[o.ChainID] = true
		}
	}
	for id := range m.leases {
		if id != chainID {
			seen[id] = true
		}
	}
	for _, e := range m.signed {
		if e.ChainID != chainID {
			seen[e.ChainID] = true
		}
	}
	for _, a := range m.audits {
		if a.chainID != chainID {
			seen[a.chainID] = true
		}
	}
	var out []string
	for id := range seen {
		out = append(out, id)
	}
	return out, nil
}

func (m *memEvmStateStore) Record(_ context.Context, fence evmFence, evidence signedTxEvidence, pending pendingRecord) (pendingRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reuse := uint64(0)
	err := m.commit("record", func() error {
		if err := m.fence(fence); err != nil {
			return err
		}
		for _, o := range m.outcomes {
			if o.ChainID == pending.ChainID && o.Scope == pending.Scope && o.Operation == pending.Operation {
				return fmt.Errorf("%w: operation has active outcome %d", errEvmStateConflict, o.ID)
			}
		}
		for _, e := range m.signed {
			if e.ChainID == evidence.ChainID && e.TxHash == evidence.TxHash {
				if !sameEvidence(e, evidence) {
					return fmt.Errorf("%w: evidence differs", errEvmStateConflict)
				}
				reuse = e.ID
			}
		}
		for _, p := range m.pending {
			if p.ChainID == pending.ChainID && p.Scope == pending.Scope {
				return fmt.Errorf("%w: scope already has a pending transaction", errEvmStateConflict)
			}
		}
		return m.auditCheck(1)
	}, func() {
		if reuse == 0 {
			evidence.ID = m.id()
			evidence.LeaseEpoch = fence.Epoch
			evidence.Raw = append([]byte(nil), evidence.Raw...)
			m.signed[evidence.ID] = evidence
			reuse = evidence.ID
		}
		pending.SignedTxID = reuse
		pending.ID = m.id()
		pending.LeaseEpoch = fence.Epoch
		m.pending[pending.ID] = pending
		m.addAudit(pending.ChainID, fence.Epoch, pending.audit(evmAuditRecorded))
	})
	if err != nil {
		return pendingRecord{}, err
	}
	return pending, nil
}

func (m *memEvmStateStore) lockPending(pending pendingRecord) error {
	for _, p := range m.pending {
		if p.ChainID == pending.ChainID && p.Scope == pending.Scope {
			if p.ID != pending.ID || p.TxHash != pending.TxHash {
				return fmt.Errorf("%w: scope pending is %s", errEvmStateConflict, p.TxHash.Hex())
			}
			return nil
		}
	}
	return errEvmRowGone
}

func (m *memEvmStateStore) MarkAmbiguous(_ context.Context, fence evmFence, pending pendingRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.commit("mark", func() error {
		if err := m.fence(fence); err != nil {
			return err
		}
		if err := m.lockPending(pending); err != nil {
			return err
		}
		return m.auditCheck(1)
	}, func() {
		p := m.pending[pending.ID]
		p.Ambiguous = true
		m.pending[pending.ID] = p
		m.addAudit(pending.ChainID, fence.Epoch, pending.audit(evmAuditMarkedAmbiguous))
	})
}

func (m *memEvmStateStore) ResolvePending(_ context.Context, fence evmFence, pending pendingRecord, terminal pendingTerminal) (*outcomeRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var outcome *outcomeRecord
	err := m.commit("resolve", func() error {
		if err := m.fence(fence); err != nil {
			return err
		}
		if err := m.lockPending(pending); err != nil {
			return err
		}
		if terminal.Kind == terminalSuccessKeep {
			if _, ok := m.signed[pending.SignedTxID]; !ok {
				return fmt.Errorf("%w: evidence row missing", errEvmStateConflict)
			}
			for _, o := range m.outcomes {
				if o.ChainID == pending.ChainID && o.Scope == pending.Scope && o.Operation == pending.Operation {
					return fmt.Errorf("%w: operation already has an active outcome", errEvmStateConflict)
				}
			}
		}
		return m.auditCheck(len(terminal.Kind.auditEvents()))
	}, func() {
		delete(m.pending, pending.ID)
		if terminal.Kind == terminalSuccessKeep {
			o := outcomeRecord{
				ID: pending.ID, SignedTxID: pending.SignedTxID, ChainID: pending.ChainID, Scope: pending.Scope,
				Operation: pending.Operation, From: pending.From, Nonce: pending.Nonce, TxHash: pending.TxHash,
				ResultTxHash: pending.TxHash, State: evmOutcomeSucceeded, ReceiptBlock: terminal.ReceiptBlock, Source: terminal.Source,
			}
			m.outcomes[o.ID] = o
			outcome = &o
		}
		for _, name := range terminal.Kind.auditEvents() {
			event := pending.audit(name)
			event.ReceiptStatus = terminal.ReceiptStatus
			event.Detail = terminal.Detail
			if outcome != nil && name == evmAuditOutcomeCreated {
				event.OutcomeID = uint64Ptr(outcome.ID)
				event.OutcomeState = outcome.State
			}
			m.addAudit(pending.ChainID, fence.Epoch, event)
		}
	})
	if err != nil && !isEvmCommitUnknown(err) {
		return nil, err
	}
	return outcome, err
}

func (m *memEvmStateStore) ConsumeOutcome(_ context.Context, fence evmFence, outcome outcomeRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.commit("consume", func() error {
		if err := m.fence(fence); err != nil {
			return err
		}
		current, ok := m.outcomes[outcome.ID]
		if !ok {
			return errEvmRowGone
		}
		if current.State != evmOutcomeSucceeded || current.Operation != outcome.Operation || current.Scope != outcome.Scope {
			return fmt.Errorf("%w: outcome changed", errEvmStateConflict)
		}
		return m.auditCheck(1)
	}, func() {
		delete(m.outcomes, outcome.ID)
		m.addAudit(outcome.ChainID, fence.Epoch, outcome.audit(evmAuditOutcomeConsumed))
	})
}

func (m *memEvmStateStore) AppendAudit(_ context.Context, fence *evmFence, chainID string, event evmAuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	epoch := uint64(0)
	return m.commit("append_audit", func() error {
		if fence != nil {
			if err := m.fence(*fence); err != nil {
				return err
			}
			epoch = fence.Epoch
		}
		return nil
	}, func() { m.addAudit(chainID, epoch, event) })
}

// Test helpers that act on the tables directly, as the CLI or an operator would.

func (m *memEvmStateStore) auditEvents(event string) []evmAuditEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []evmAuditEvent
	for _, a := range m.audits {
		if a.event.Event == event {
			out = append(out, a.event)
		}
	}
	return out
}

// putOutcome inserts an outcome row directly, bypassing the invariants.

// tamper changes an evidence row in place, as a corrupted DB would.

// tamperOutcome changes an outcome row in place.

// deleteSignedTx models the RESTRICT foreign keys.

// expireLease makes the chain's lease expire now.
