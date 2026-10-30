package signer

import (
	"context"
	"strings"
	"testing"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// Automatic disposition of EVM state (evm_state_auto.go). Each scenario
// leaves a pending deposit A that the node never confirmed, then asks for
// another deposit B, which the pending row of A used to block for ever.

// autoEnv is a disposeEnv whose signer reads the chain for the automatic checks.
func newAutoEnv(t *testing.T) *disposeEnv {
	t.Helper()
	env := newDisposeEnv(t)
	env.client.autoChain = &evmAutoChain{evm: env.chain, cosmos: fakeCosmosChain{env.chain}}
	return env
}

// restartAuto models a restart of a signer with the automatic checks on:
// recovery, which runs inline, settles what the chain decides.
func (env *disposeEnv) restartAuto() {
	env.t.Helper()
	env.store.expireLease(fakeEvmChainID)
	_, _, nonce := env.node.snapshot()
	c := newFakeEvmSignClientNoLease(env.t, env.node, nonce, env.store)
	c.autoChain = &evmAutoChain{evm: env.chain, cosmos: fakeCosmosChain{env.chain}}
	if err := c.checkEvmStateOnStart(context.Background()); err != nil {
		env.t.Fatalf("startup check: %v", err)
	}
	acquireEvmLeaseForTest(env.t, c)
	env.client = c
}

// refuseBytes makes the node refuse tx's bytes with msg, and handle every
// other transaction normally.
func (env *disposeEnv) refuseBytes(tx *ethtypes.Transaction, msg string) {
	env.node.script = func(_ int, sent *ethtypes.Transaction) fakeEvmAction {
		if sent.Hash() == tx.Hash() {
			return fakeEvmAction{kind: fakeEvmRPCError, code: -32000, message: msg}
		}
		return fakeEvmAction{kind: fakeEvmDefault}
	}
}

func (env *disposeEnv) depositOK(amount int64) string {
	env.t.Helper()
	hash, err := deposit(scenarioCtx(env.t), env.client, amount)
	if err != nil || hash == "" {
		env.t.Fatalf("deposit %d: hash=%q err=%v; %s", amount, hash, err, env.node.describe())
	}
	return hash
}

func (env *disposeEnv) depositBlocked(amount int64, want string) {
	env.t.Helper()
	_, err := deposit(scenarioCtx(env.t), env.client, amount)
	if err == nil || !strings.Contains(err.Error(), want) {
		env.t.Fatalf("deposit %d: want an error containing %q, got %v", amount, want, err)
	}
}

func (env *disposeEnv) requireAudit(event string, n int) {
	env.t.Helper()
	if got := env.auditCount(event); got != n {
		env.t.Fatalf("audit %s: want %d rows, got %d", event, n, got)
	}
}

func (env *disposeEnv) committedNonce(hash string) uint64 {
	env.t.Helper()
	accepted, _, _ := env.node.snapshot()
	for _, tx := range accepted {
		if tx.Hash().String() == hash {
			return tx.Nonce()
		}
	}
	env.t.Fatalf("%s was not committed; %s", hash, env.node.describe())
	return 0
}

// The original was included and failed without an EVM receipt (a node that
// aborts the execution records only the Cosmos result). The pending row ends
// at once, the next deposit takes the next nonce, and A may be signed again.
func TestEvmAutoFailedWithoutReceiptFreesScope(t *testing.T) {
	env := newAutoEnv(t)
	pendingA, txA := env.stuckDeposit(1000)
	view := env.chain.addFailedNoReceipt(txA, 111222)

	hashB := env.depositOK(2000)
	if got := env.committedNonce(hashB); got != pendingA.Nonce+1 {
		t.Fatalf("B took nonce %d, want %d", got, pendingA.Nonce+1)
	}
	env.requireAudit(evmAuditAutoResolvedFailed, 1)
	if audit := env.store.auditEvents(evmAuditAutoResolvedFailed)[0]; audit.EvidenceTxHash != view.Hash {
		t.Fatalf("audit evidence %q, want the cosmos tx %s", audit.EvidenceTxHash, view.Hash)
	}
	if _, ok := env.store.pendingOf(SignOperator); ok || len(env.store.outcomeRows()) != 0 {
		t.Fatalf("state left behind: pending=%v outcomes=%v", env.store.pendingRows(), env.store.outcomeRows())
	}

	// A failed and left no state: it is signed again, once.
	hashA := env.depositOK(1000)
	if hashA == pendingA.TxHash.String() {
		t.Fatal("A returned the failed original instead of being signed again")
	}
	if n := env.node.executionsOf(txA); n != 1 {
		t.Fatalf("A executed %d times; %s", n, env.node.describe())
	}
}

// Another transaction used A's nonce and nothing carries A's hash. Once a
// second, higher height agrees, A becomes blocked_unknown and B is signed;
// A stays blocked because whether it ran cannot be proven.
func TestEvmAutoNonceUsedNothingFoundBlocksOnlyTheOperation(t *testing.T) {
	env := newAutoEnv(t)
	pendingA, txA := env.stuckDeposit(1000)
	env.chain.addCosmosTx("EXTERNAL", 0, nil, nil) // uses N, carries no Ethereum hash

	env.depositBlocked(2000, "unconfirmed") // first sighting
	env.requireAudit(evmAuditAutoBlockedMissing, 0)
	hashB := env.depositOK(2000) // second sighting at a higher height
	if got := env.committedNonce(hashB); got != pendingA.Nonce+1 {
		t.Fatalf("B took nonce %d, want %d", got, pendingA.Nonce+1)
	}
	env.requireAudit(evmAuditAutoBlockedMissing, 1)
	outcomes := env.store.outcomeRows()
	if len(outcomes) != 1 || outcomes[0].State != evmOutcomeBlockedUnknown || outcomes[0].Reason != evmReasonReceiptMissing ||
		outcomes[0].Operation != pendingA.Operation || outcomes[0].Source != evmOutcomeSourceAuto {
		t.Fatalf("want A blocked_unknown(receipt_missing), got %+v", outcomes)
	}
	env.depositBlocked(1000, "is blocked")
	if n := env.node.executionsOf(txA); n != 0 {
		t.Fatalf("A executed %d times", n)
	}
}

// A is not in the node's mempool and the node refuses its bytes. While a
// transaction of the account waits in the mempool nothing changes; once the
// mempool is empty, A becomes blocked_unknown(dropped) and B takes A's nonce.
// B's receipt then proves A never ran, so A is signed again.
func TestEvmAutoDroppedIsSupersededThenSignedAgain(t *testing.T) {
	env := newAutoEnv(t)
	pendingA, txA := env.stuckDeposit(1000)
	env.refuseBytes(txA, "insufficient funds for gas * price + value")

	env.chain.mempool = 1
	env.depositBlocked(2000, "unconfirmed")
	env.depositBlocked(2000, "unconfirmed")
	env.requireAudit(evmAuditAutoBlockedDropped, 0)

	env.chain.mempool = 0
	env.depositBlocked(2000, "unconfirmed") // first sighting
	hashB := env.depositOK(2000)
	if got := env.committedNonce(hashB); got != pendingA.Nonce {
		t.Fatalf("B took nonce %d, want A's nonce %d", got, pendingA.Nonce)
	}
	env.requireAudit(evmAuditAutoBlockedDropped, 1)

	hashA := env.depositOK(1000)
	if got := env.committedNonce(hashA); got != pendingA.Nonce+1 {
		t.Fatalf("A was signed again at nonce %d, want %d", got, pendingA.Nonce+1)
	}
	env.requireAudit(evmAuditAutoOutcomeSuperseded, 1)
	if n := env.node.executionsOf(txA); n != 1 {
		t.Fatalf("A executed %d times; %s", n, env.node.describe())
	}
	if len(env.store.outcomeRows()) != 0 {
		t.Fatalf("outcomes left: %+v", env.store.outcomeRows())
	}
}

// The original lands after it was superseded (another node still held it):
// B is refused for its nonce and signed at the next one, and the request for
// A gets A's original hash. Nothing runs twice.
func TestEvmAutoSupersededOriginalLandsLate(t *testing.T) {
	env := newAutoEnv(t)
	pendingA, txA := env.stuckDeposit(1000)
	env.refuseBytes(txA, "insufficient funds for gas * price + value")
	env.depositBlocked(2000, "unconfirmed") // first sighting

	env.store.hookOnce("auto_resolve", func(*memEvmStateStore) {
		if err := env.node.admit(txA); err != nil {
			t.Errorf("admit A: %v", err)
		}
	})
	hashB := env.depositOK(2000)
	if got := env.committedNonce(hashB); got != pendingA.Nonce+1 {
		t.Fatalf("B took nonce %d, want %d", got, pendingA.Nonce+1)
	}
	hashA := env.depositOK(1000)
	if hashA != pendingA.TxHash.String() {
		t.Fatalf("A returned %s, want the original %s", hashA, pendingA.TxHash.Hex())
	}
	env.requireAudit(evmAuditAutoOutcomeSucceeded, 1)
	if n := env.node.executionsOf(txA); n != 1 {
		t.Fatalf("A executed %d times; %s", n, env.node.describe())
	}
}

// A was recorded at a nonce above the account's (the refusal of its first
// send was lost). With the mempool empty and the bytes refused as nonce too
// high, A becomes blocked_unknown(nonce_gap) and the scope signs from the
// chain nonce; once a transaction of the signer uses A's nonce, A is settled
// and signed again.
func TestEvmAutoNonceGapFillsFromChainNonce(t *testing.T) {
	env := newAutoEnv(t)
	gapped := scenarioNonce + 2
	env.client.operatorAccNonce = gapped
	pendingA, txA := env.stuckDeposit(1000)
	if pendingA.Nonce != gapped {
		t.Fatalf("A recorded at nonce %d, want %d", pendingA.Nonce, gapped)
	}

	env.depositBlocked(2000, "unconfirmed") // first sighting
	hashB := env.depositOK(2000)
	if got := env.committedNonce(hashB); got != scenarioNonce {
		t.Fatalf("B took nonce %d, want the chain nonce %d", got, scenarioNonce)
	}
	env.requireAudit(evmAuditAutoBlockedNonceGap, 1)
	env.depositOK(3000) // scenarioNonce+1
	env.depositOK(4000) // scenarioNonce+2: A's nonce

	hashA := env.depositOK(1000)
	if got := env.committedNonce(hashA); got != gapped+1 {
		t.Fatalf("A was signed again at nonce %d, want %d", got, gapped+1)
	}
	env.requireAudit(evmAuditAutoOutcomeSuperseded, 1)
	if n := env.node.executionsOf(txA); n != 1 {
		t.Fatalf("A executed %d times; %s", n, env.node.describe())
	}
}

// After a restart the signer settles the outcomes the chain decides without
// waiting for their operation to be requested.
func TestEvmAutoRestartSettlesOutcomes(t *testing.T) {
	env := newAutoEnv(t)
	_, txA := env.stuckDeposit(1000)
	env.refuseBytes(txA, "insufficient funds for gas * price + value")
	env.depositBlocked(2000, "unconfirmed")
	env.depositOK(2000) // supersedes A, takes its nonce
	if len(env.store.outcomeRows()) != 1 {
		t.Fatalf("want A's outcome kept, got %+v", env.store.outcomeRows())
	}

	env.restartAuto()
	if len(env.store.outcomeRows()) != 0 {
		t.Fatalf("restart left outcomes: %+v", env.store.outcomeRows())
	}
	env.requireAudit(evmAuditAutoOutcomeSuperseded, 1)
}

// The manual resolve recognises an original included as a failed Cosmos
// transaction without a receipt (branch original_failed).
func TestDisposeOriginalFailedWithoutReceipt(t *testing.T) {
	env := newDisposeEnv(t)
	pending, txA := env.stuckDeposit(1000)
	env.chain.addFailedNoReceipt(txA, 111222)

	report := env.resolve(SignOperator, pending.TxHash, EvmDisposeRequest{})
	expectExit(t, report, EvmDisposeExitOK, EvmBranchOriginalFailed)
	if _, ok := env.store.pendingOf(SignOperator); ok {
		t.Fatal("pending row kept")
	}
}
