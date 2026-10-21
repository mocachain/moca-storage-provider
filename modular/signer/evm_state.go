package signer

import (
	"bytes"
	"context"
	stderrors "errors"
	"fmt"
	"time"

	ethcmn "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// The EVM state store is the only source of truth for EVM transactions the
// signer has signed: pending submissions, active outcomes later requests still
// need, the append-only evidence of every signed transaction, the lease that
// fences writers, and the audit history. Every transition is one transaction
// that also writes its audit rows; a state change counts only once it commits.

type evmOutcomeState string

const (
	evmOutcomeSucceeded         evmOutcomeState = "succeeded"
	evmOutcomeExecutedElsewhere evmOutcomeState = "executed_elsewhere"
	evmOutcomeBlockedUnknown    evmOutcomeState = "blocked_unknown"
)

// Audit events written by the signer.
const (
	evmAuditRecorded              = "recorded"
	evmAuditMarkedAmbiguous       = "marked_ambiguous"
	evmAuditResolvedSuccess       = "resolved_success"
	evmAuditResolvedFailed        = "resolved_failed"
	evmAuditNotAdmittedNonceLow   = "not_admitted_nonce_low"
	evmAuditNotAdmittedNonceHigh  = "not_admitted_nonce_high"
	evmAuditNotAdmittedRejected   = "not_admitted_rejected"
	evmAuditOutcomeCreated        = "outcome_created"
	evmAuditOutcomeConsumed       = "outcome_consumed"
	evmAuditLoadedOnStart         = "loaded_on_start"
	evmAuditLeaseAcquired         = "lease_acquired"
	evmAuditLeaseLost             = "lease_lost"
	evmAuditPendingVanished       = "pending_vanished"
	evmAuditEvidenceInvalid       = "evidence_invalid"
	evmOutcomeSourceSubmit        = "submit"
	evmOutcomeSourceResolve       = "resolve"
	evmOutcomeSourceStartup       = "startup"
	evmDefaultLeaseTTL            = 30 * time.Second
	evmStateWriteTimeout          = 10 * time.Second
	evmStateBackgroundResolveWait = 30 * time.Second
)

var (
	// errEvmLeaseNotHeld means the fencing check failed: this process does
	// not hold a live lease at its epoch, so the transaction rolled back.
	errEvmLeaseNotHeld = stderrors.New("signer does not hold the EVM state lease")
	// errEvmStateConflict means a row differs from what the transition
	// expected (another hash, an existing active outcome, a unique key
	// conflict). The transaction rolled back and the scope stays blocked.
	errEvmStateConflict = stderrors.New("EVM state is inconsistent")
	// errEvmRowGone means the row the transition targets no longer exists,
	// so someone else (the CLI) already handled it. The transaction rolled
	// back; the caller decides again from the DB.
	errEvmRowGone = stderrors.New("EVM state row no longer exists")
	// errEvmEvidenceInvalid means a row's signed transaction evidence is
	// missing or fails verification. The row stays blocked.
	errEvmEvidenceInvalid = stderrors.New("EVM signed transaction evidence is invalid")
)

// evmCommitUnknownError means COMMIT was sent but its result is unknown.
// The caller reads the state back before deciding.
type evmCommitUnknownError struct {
	err error
}

func (e *evmCommitUnknownError) Error() string {
	return "EVM state commit outcome unknown: " + e.err.Error()
}

func (e *evmCommitUnknownError) Unwrap() error { return e.err }

func isEvmCommitUnknown(err error) bool {
	var target *evmCommitUnknownError
	return stderrors.As(err, &target)
}

// evmFence identifies the lease a write runs under. Every signer write
// transaction first locks the lease row and checks owner, epoch and expiry.
type evmFence struct {
	ChainID string
	Owner   string
	Epoch   uint64
}

// signedTxEvidence is a row of the append-only evidence table.
type signedTxEvidence struct {
	ID            uint64
	ChainID       string
	EvmChainID    uint64
	Scope         SignType
	Operation     ethcmn.Hash
	From          ethcmn.Address
	Nonce         uint64
	TxHash        ethcmn.Hash
	Raw           []byte
	RecordedBlock uint64
	LeaseEpoch    uint64
}

// pendingRecord is the at most one unresolved transaction of a scope.
type pendingRecord struct {
	ID         uint64
	ChainID    string
	SignedTxID uint64
	Scope      SignType
	From       ethcmn.Address
	Nonce      uint64
	TxHash     ethcmn.Hash
	Operation  ethcmn.Hash
	Ambiguous  bool
	LeaseEpoch uint64
}

// outcomeRecord is an active conclusion for an operation.
type outcomeRecord struct {
	ID             uint64
	SignedTxID     uint64
	ChainID        string
	Scope          SignType
	Operation      ethcmn.Hash
	From           ethcmn.Address
	Nonce          uint64
	TxHash         ethcmn.Hash
	ResultTxHash   ethcmn.Hash
	State          evmOutcomeState
	EvidenceTxHash string
	EvidenceKind   string
	Reason         string
	ReceiptBlock   uint64
	Source         string
}

// evmAuditEvent is one audit row. Zero values are stored as empty or NULL.
type evmAuditEvent struct {
	Event          string
	Scope          SignType
	From           ethcmn.Address
	Nonce          *uint64
	TxHash         ethcmn.Hash
	Operation      ethcmn.Hash
	SignedTxID     *uint64
	ReceiptStatus  *uint64
	ReceiptBlock   *uint64
	ChainNonce     *uint64
	OutcomeID      *uint64
	OutcomeState   evmOutcomeState
	OutcomeReason  string
	Detail         string
	EvidenceTxHash string
	EvidenceKind   string
}

func uint64Ptr(v uint64) *uint64 { return &v }

func (p pendingRecord) audit(event string) evmAuditEvent {
	return evmAuditEvent{
		Event: event, Scope: p.Scope, From: p.From, Nonce: uint64Ptr(p.Nonce), TxHash: p.TxHash,
		Operation: p.Operation, SignedTxID: uint64Ptr(p.SignedTxID),
	}
}

func (o outcomeRecord) audit(event string) evmAuditEvent {
	return evmAuditEvent{
		Event: event, Scope: o.Scope, From: o.From, Nonce: uint64Ptr(o.Nonce), TxHash: o.TxHash,
		Operation: o.Operation, SignedTxID: uint64Ptr(o.SignedTxID), OutcomeID: uint64Ptr(o.ID),
		OutcomeState: o.State, OutcomeReason: o.Reason, EvidenceTxHash: o.EvidenceTxHash, EvidenceKind: o.EvidenceKind,
	}
}

// pendingTerminalKind is how a pending row ends.
type pendingTerminalKind int

const (
	// terminalSuccessConsume: success receipt, and the request asking is
	// for the same operation, so it takes the hash and no outcome is kept.
	terminalSuccessConsume pendingTerminalKind = iota
	// terminalSuccessKeep: success receipt seen by the background resolver
	// or a request for another operation; a succeeded outcome is kept.
	terminalSuccessKeep
	terminalFailed
	terminalNotAdmittedNonceLow
	terminalNotAdmittedNonceHigh
	terminalNotAdmittedRejected
)

func (k pendingTerminalKind) auditEvents() []string {
	switch k {
	case terminalSuccessConsume:
		return []string{evmAuditResolvedSuccess, evmAuditOutcomeConsumed}
	case terminalSuccessKeep:
		return []string{evmAuditResolvedSuccess, evmAuditOutcomeCreated}
	case terminalFailed:
		return []string{evmAuditResolvedFailed}
	case terminalNotAdmittedNonceLow:
		return []string{evmAuditNotAdmittedNonceLow}
	case terminalNotAdmittedNonceHigh:
		return []string{evmAuditNotAdmittedNonceHigh}
	default:
		return []string{evmAuditNotAdmittedRejected}
	}
}

// pendingTerminal is the evidence that ends a pending row.
type pendingTerminal struct {
	Kind          pendingTerminalKind
	ReceiptStatus *uint64
	ReceiptBlock  uint64
	Detail        string
	Source        string
}

// evmStateStore is the narrow store the signer uses. Implementations must
// keep these semantics: a transition either commits with its audit rows or
// changes nothing; a commit whose result is unknown is reported as
// evmCommitUnknownError; a targeted row that does not exist gives
// errEvmRowGone; a different row, or a unique key conflict, gives
// errEvmStateConflict; a failed fencing check gives errEvmLeaseNotHeld;
// evidence rows are never updated or deleted, and cannot be deleted while a
// pending or outcome row references them.
type evmStateStore interface {
	AcquireLease(ctx context.Context, chainID, owner string, ttl time.Duration) (uint64, bool, error)
	RenewLease(ctx context.Context, fence evmFence, ttl time.Duration) (bool, error)
	ReleaseLease(ctx context.Context, fence evmFence) error

	GetPending(ctx context.Context, chainID string, scope SignType) (*pendingRecord, error)
	ListPending(ctx context.Context, chainID string) ([]pendingRecord, error)
	GetOutcome(ctx context.Context, chainID string, scope SignType, operation ethcmn.Hash) (*outcomeRecord, error)
	ListOutcomes(ctx context.Context, chainID string) ([]outcomeRecord, error)
	GetSignedTx(ctx context.Context, id uint64) (*signedTxEvidence, error)
	HasAudit(ctx context.Context, chainID, event string, txHash ethcmn.Hash, outcomeID uint64) (bool, error)
	ForeignChainIDs(ctx context.Context, chainID string) ([]string, error)

	// Record inserts the evidence (or reuses an identical evidence row), the
	// pending row and the recorded audit. It fails with errEvmStateConflict
	// when the operation has an active outcome or the scope a pending row.
	Record(ctx context.Context, fence evmFence, evidence signedTxEvidence, pending pendingRecord) (pendingRecord, error)
	MarkAmbiguous(ctx context.Context, fence evmFence, pending pendingRecord) error
	// ResolvePending deletes the pending row matching pending.ID and
	// pending.TxHash, inserts a succeeded outcome for terminalSuccessKeep,
	// and writes the audit rows.
	ResolvePending(ctx context.Context, fence evmFence, pending pendingRecord, terminal pendingTerminal) (*outcomeRecord, error)
	// ConsumeOutcome deletes a succeeded outcome by id.
	ConsumeOutcome(ctx context.Context, fence evmFence, outcome outcomeRecord) error
	// AppendAudit inserts one audit row. A nil fence skips the fencing check;
	// only lease and evidence events use that.
	AppendAudit(ctx context.Context, fence *evmFence, chainID string, event evmAuditEvent) error
}

// evidenceRef is the set of columns a referencing row (pending or outcome)
// must share with its evidence row.
type evidenceRef struct {
	SignedTxID uint64
	ChainID    string
	Scope      SignType
	Operation  ethcmn.Hash
	From       ethcmn.Address
	Nonce      uint64
	TxHash     ethcmn.Hash
}

func (p pendingRecord) ref() evidenceRef {
	return evidenceRef{SignedTxID: p.SignedTxID, ChainID: p.ChainID, Scope: p.Scope, Operation: p.Operation,
		From: p.From, Nonce: p.Nonce, TxHash: p.TxHash}
}

// verifySignedTxEvidence is the evidence check V(e): the raw bytes decode as
// an EIP-2718 transaction whose hash, recovered sender, nonce and chain id
// equal the evidence columns, and the referencing row agrees with the
// evidence row column by column. It returns the decoded transaction.
func verifySignedTxEvidence(evidence *signedTxEvidence, ref evidenceRef) (*types.Transaction, error) {
	if evidence == nil {
		return nil, fmt.Errorf("%w: evidence row %d is missing", errEvmEvidenceInvalid, ref.SignedTxID)
	}
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(evidence.Raw); err != nil {
		return nil, fmt.Errorf("%w: row %d raw does not decode: %v", errEvmEvidenceInvalid, evidence.ID, err)
	}
	if hash := crypto.Keccak256Hash(evidence.Raw); hash != evidence.TxHash || tx.Hash() != evidence.TxHash {
		return nil, fmt.Errorf("%w: row %d keccak(raw)=%s, tx_hash=%s", errEvmEvidenceInvalid, evidence.ID, hash.Hex(), evidence.TxHash.Hex())
	}
	if tx.ChainId() == nil || !tx.ChainId().IsUint64() || tx.ChainId().Uint64() != evidence.EvmChainID {
		return nil, fmt.Errorf("%w: row %d raw chain id %v, evm_chain_id=%d", errEvmEvidenceInvalid, evidence.ID, tx.ChainId(), evidence.EvmChainID)
	}
	sender, err := types.Sender(types.LatestSignerForChainID(tx.ChainId()), tx)
	if err != nil {
		return nil, fmt.Errorf("%w: row %d sender does not recover: %v", errEvmEvidenceInvalid, evidence.ID, err)
	}
	if sender != evidence.From {
		return nil, fmt.Errorf("%w: row %d recovered sender %s, from_address=%s", errEvmEvidenceInvalid, evidence.ID, sender.Hex(), evidence.From.Hex())
	}
	if tx.Nonce() != evidence.Nonce {
		return nil, fmt.Errorf("%w: row %d raw nonce %d, nonce=%d", errEvmEvidenceInvalid, evidence.ID, tx.Nonce(), evidence.Nonce)
	}
	var mismatched []string
	if ref.SignedTxID != evidence.ID {
		mismatched = append(mismatched, "signed_tx_id")
	}
	if ref.ChainID != evidence.ChainID {
		mismatched = append(mismatched, "chain_id")
	}
	if ref.Scope != evidence.Scope {
		mismatched = append(mismatched, "scope")
	}
	if ref.Operation != evidence.Operation {
		mismatched = append(mismatched, "operation")
	}
	if ref.From != evidence.From {
		mismatched = append(mismatched, "from_address")
	}
	if ref.Nonce != evidence.Nonce {
		mismatched = append(mismatched, "nonce")
	}
	if ref.TxHash != evidence.TxHash {
		mismatched = append(mismatched, "tx_hash")
	}
	if len(mismatched) != 0 {
		return nil, fmt.Errorf("%w: referencing row differs from evidence row %d in %v", errEvmEvidenceInvalid, evidence.ID, mismatched)
	}
	return tx, nil
}

// sameEvidence reports whether an existing evidence row describes exactly
// the transaction being recorded, so Record may reference it again (the same
// operation re-signed with identical bytes after a refusal).
func sameEvidence(a, b signedTxEvidence) bool {
	return a.ChainID == b.ChainID && a.EvmChainID == b.EvmChainID && a.Scope == b.Scope && a.Operation == b.Operation &&
		a.From == b.From && a.Nonce == b.Nonce && a.TxHash == b.TxHash && bytes.Equal(a.Raw, b.Raw)
}
