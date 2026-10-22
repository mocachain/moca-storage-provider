package signer

import (
	"bytes"
	"context"
	stderrors "errors"
	"fmt"
	"math/big"
	"os/user"
	"strings"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum"
	ethcmn "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"gorm.io/gorm"

	evmostypes "github.com/mocachain/moca/v2/types"
	virtualgrouptypes "github.com/mocachain/moca/v2/x/virtualgroup/types"
)

// Manual disposition of the EVM state (SOL-486 plan §3.4, §4.2): the
// signer-evm CLI resolves a pending row, or settles an active outcome, from
// on-chain evidence; release and dispose-outcome are operator decisions. The
// CLI connects to the SP DB directly and holds no lease. Every write is one
// transaction with its audit row, conditioned on the row the decision was
// made on, so a concurrent signer or a second run cannot be overwritten.

// Exit codes of the disposition commands.
const (
	EvmDisposeExitOK = 0
	// EvmDisposeExitError: usage, RPC or DB error.
	EvmDisposeExitError = 1
	// EvmDisposeExitNotProven: the evidence does not decide the case
	// (including a dry run that could not probe).
	EvmDisposeExitNotProven = 2
	// EvmDisposeExitPrecondition: the row is gone or not in the expected
	// state (a stale id, a second run).
	EvmDisposeExitPrecondition = 3
	// EvmDisposeExitEvidenceInvalid: the signed transaction evidence is
	// missing or fails V(e).
	EvmDisposeExitEvidenceInvalid = 4
)

// The disposition commands.
const (
	EvmDisposeResolve        = "resolve"
	EvmDisposeResolveOutcome = "resolve-outcome"
	EvmDisposeRelease        = "release"
	EvmDisposeDisposeOutcome = "dispose-outcome"
)

// EvmDisposeMaxScanBlocks bounds --scan-blocks.
const EvmDisposeMaxScanBlocks = 10000

// Audit events written by the CLI.
const (
	evmAuditManualResolveSuccess        = "manual_resolve_success"
	evmAuditManualResolveFailed         = "manual_resolve_failed"
	evmAuditManualResolveOtherTxFailed  = "manual_resolve_other_tx_failed"
	evmAuditManualResolveOtherUnrelated = "manual_resolve_other_tx_unrelated"
	evmAuditManualResolveSameCall       = "manual_resolve_other_tx_same_call"
	evmAuditManualResolveElsewhere      = "manual_resolve_executed_elsewhere"
	evmAuditManualResolveUnknown        = "manual_resolve_unknown"
	evmAuditManualResolveNonceGap       = "manual_resolve_nonce_gap"
	evmAuditManualOutcomeResolved       = "manual_outcome_resolved"
	evmAuditManualReleaseUnknown        = "manual_release_unknown"
	evmAuditManualDisposeOutcome        = "manual_dispose_outcome"
	evmAuditManualRejected              = "manual_rejected"

	evmOutcomeSourceManual = "manual"

	evmReasonReceiptMissing    = "receipt_missing"
	evmReasonOtherTxUnverified = "other_tx_unverified"
	evmReasonNonceGap          = "nonce_gap"

	evmEvidenceKindEVM    = "evm"
	evmEvidenceKindCosmos = "cosmos"
)

// evmFailedTxRollsBack is V1 (plan Q5): a failed transaction leaves no state
// behind, so the transaction that took nonce N did not execute the
// operation. Verified for the node dependencies in go.mod
// (docs/signer-evm-disposition.md, "V1"). Tests switch it off to cover the
// conservative path, where a failed occupier is undetermined (branch e).
var evmFailedTxRollsBack = true

// Branches of plan §4.2. Only the original receipt, a, b and c prove the
// operation did not run elsewhere; d and d' prove it did.
const (
	EvmBranchOriginalSucceeded = "original_succeeded"
	EvmBranchOriginalFailed    = "original_failed"
	// EvmBranchNonceUnused: the chain nonce is still N, so the original
	// bytes may be included at any time.
	EvmBranchNonceUnused = "nonce_unused"
	// EvmBranchNonceNotReached: the chain nonce is below N (resolve-outcome
	// waits for N to be used).
	EvmBranchNonceNotReached = "nonce_not_reached"
	EvmBranchOtherTxFailed   = "a_other_tx_failed"
	EvmBranchOwnOtherOp      = "b_own_other_operation"
	EvmBranchPlainTransfer   = "c_plain_transfer"
	EvmBranchSameCall        = "d_same_call"
	EvmBranchCosmosSameOp    = "d_prime_cosmos_same_operation"
	EvmBranchUndetermined    = "e_undetermined"
	EvmBranchOccupierMissing = "f_occupier_not_found"
	EvmBranchNonceGap        = "g_nonce_gap"
	// EvmBranchGapUnproven: chain nonce below N, but a §3.4 condition failed.
	EvmBranchGapUnproven = "gap_unproven"
	// EvmBranchGapNotProbed: chain nonce below N in a dry run, which never
	// re-sends the bytes.
	EvmBranchGapNotProbed = "gap_not_probed"
)

// EvmBlockTx is a transaction as a block lists it.
type EvmBlockTx struct {
	Hash  ethcmn.Hash
	From  ethcmn.Address
	Nonce uint64
}

// EvmDisposeChain is the EVM JSON-RPC the disposition reads. NotFound
// errors follow go-ethereum (ethereum.NotFound).
type EvmDisposeChain interface {
	ChainID(ctx context.Context) (*big.Int, error)
	BlockNumber(ctx context.Context) (uint64, error)
	TransactionReceipt(ctx context.Context, hash ethcmn.Hash) (*types.Receipt, error)
	TransactionByHash(ctx context.Context, hash ethcmn.Hash) (*types.Transaction, bool, error)
	NonceAt(ctx context.Context, account ethcmn.Address, blockNumber *big.Int) (uint64, error)
	PendingNonceAt(ctx context.Context, account ethcmn.Address) (uint64, error)
	CodeAt(ctx context.Context, account ethcmn.Address, blockNumber *big.Int) ([]byte, error)
	SendTransaction(ctx context.Context, tx *types.Transaction) error
	// BlockTransactions lists the transactions of a block; a block the node
	// does not have (pruned) is errEvmBlockUnavailable.
	BlockTransactions(ctx context.Context, height uint64) ([]EvmBlockTx, error)
}

// CosmosTxView is a Cosmos transaction as the disposition needs it.
type CosmosTxView struct {
	Hash   string
	Height uint64
	Code   uint32
	// AccSeqs are the tx.acc_seq attributes: "<EIP-55 address>/<sequence>"
	// per signer, emitted by the ante handler.
	AccSeqs []string
	Msgs    []sdk.Msg
	// DecodeError is set when the transaction bytes did not decode.
	DecodeError string
}

// CosmosDisposeChain is the CometBFT RPC the disposition reads.
type CosmosDisposeChain interface {
	Network(ctx context.Context) (string, error)
	// Tx returns nil, nil when the node does not know the hash, and
	// errCosmosIndexDisabled when it does not index transactions.
	Tx(ctx context.Context, hash string) (*CosmosTxView, error)
	// SearchAccSeq finds transactions by tx.acc_seq up to maxHeight;
	// errCosmosIndexDisabled when the node does not index transactions.
	SearchAccSeq(ctx context.Context, accSeq string, maxHeight uint64) ([]*CosmosTxView, error)
}

var (
	errEvmBlockUnavailable = stderrors.New("block not available on the node")
	errCosmosIndexDisabled = stderrors.New("the node does not index transactions")
)

// EvmDisposeRequest is one disposition command.
type EvmDisposeRequest struct {
	Command   string
	ChainID   string
	Scope     string
	TxHash    ethcmn.Hash
	OutcomeID uint64

	Operator         string
	Reason           string
	BusinessEvidence string
	DecisionRef      string

	EvidenceTx       ethcmn.Hash
	EvidenceCosmosTx string
	ScanBlocks       uint64
	AcceptUnknown    bool
	AcceptGap        bool
	DryRun           bool

	// BlockWait bounds the wait for the second height; Poll paces it.
	BlockWait time.Duration
	Poll      time.Duration
}

// EvmDisposeOccupier is the transaction that used nonce N.
type EvmDisposeOccupier struct {
	Kind      string `json:"kind"`
	Hash      string `json:"hash"`
	Block     uint64 `json:"block"`
	Succeeded bool   `json:"succeeded"`
	// Operation is the operation of a transaction this signer signed (b, d).
	Operation string `json:"operation,omitempty"`
	Source    string `json:"source"`

	tx     *types.Transaction
	cosmos *CosmosTxView
}

// EvmDisposeSample is the assessment at one height.
type EvmDisposeSample struct {
	Height       uint64              `json:"height"`
	ChainNonce   uint64              `json:"chain_nonce"`
	PendingNonce *uint64             `json:"pending_nonce,omitempty"`
	Branch       string              `json:"branch"`
	ReceiptBlock uint64              `json:"receipt_block,omitempty"`
	Occupier     *EvmDisposeOccupier `json:"occupier,omitempty"`
	Notes        []string            `json:"notes,omitempty"`
}

func (s *EvmDisposeSample) note(format string, args ...interface{}) {
	s.Notes = append(s.Notes, fmt.Sprintf(format, args...))
}

// key is what two samples must agree on. A nonce gap also needs the same
// account nonce at both heights.
func (s *EvmDisposeSample) key() string {
	occupier := ""
	if s.Occupier != nil {
		occupier = s.Occupier.Kind + ":" + s.Occupier.Hash
	}
	switch s.Branch {
	case EvmBranchNonceGap, EvmBranchGapUnproven, EvmBranchGapNotProbed:
		occupier = fmt.Sprintf("nonce %d", s.ChainNonce)
	}
	return s.Branch + "|" + occupier
}

// EvmDisposeReport is the result of one command.
type EvmDisposeReport struct {
	Command   string             `json:"command"`
	DryRun    bool               `json:"dry_run"`
	Scope     string             `json:"scope,omitempty"`
	OutcomeID uint64             `json:"outcome_id,omitempty"`
	State     string             `json:"state,omitempty"`
	Reason    string             `json:"reason,omitempty"`
	From      string             `json:"from_address,omitempty"`
	Nonce     uint64             `json:"nonce"`
	TxHash    string             `json:"tx_hash,omitempty"`
	Operation string             `json:"operation,omitempty"`
	Samples   []EvmDisposeSample `json:"samples,omitempty"`
	Branch    string             `json:"branch,omitempty"`
	// Action is what was done, or in a dry run would be done.
	Action  string `json:"action"`
	Applied bool   `json:"applied"`
	// Event is the audit event written (none in a dry run).
	Event    string   `json:"audit_event,omitempty"`
	Notes    []string `json:"notes,omitempty"`
	Error    string   `json:"error,omitempty"`
	ExitCode int      `json:"exit_code"`
}

// DisposeEvmState runs one disposition command against the SP DB.
func DisposeEvmState(ctx context.Context, db *gorm.DB, req EvmDisposeRequest, evm EvmDisposeChain,
	cosmos CosmosDisposeChain,
) *EvmDisposeReport {
	return disposeEvmState(ctx, newSQLEvmStateStore(db), req, evm, cosmos)
}

// evmDisposer carries one command.
type evmDisposer struct {
	store  evmStateStore
	st     *evmState
	req    EvmDisposeRequest
	evm    EvmDisposeChain
	cosmos CosmosDisposeChain
	report *EvmDisposeReport
	actor  string

	cosmosChecked bool
	blocks        map[uint64][]EvmBlockTx
}

func disposeEvmState(ctx context.Context, store evmStateStore, req EvmDisposeRequest, evm EvmDisposeChain,
	cosmos CosmosDisposeChain,
) *EvmDisposeReport {
	report := &EvmDisposeReport{Command: req.Command, DryRun: req.DryRun, Scope: req.Scope, OutcomeID: req.OutcomeID}
	if err := checkEvmDisposeRequest(req); err != nil {
		report.Error, report.ExitCode = err.Error(), EvmDisposeExitError
		return report
	}
	evmChainID, err := evmostypes.ParseChainID(req.ChainID)
	if err != nil || !evmChainID.IsUint64() {
		report.Error, report.ExitCode = fmt.Sprintf("cannot derive the EVM chain id from %q: %v", req.ChainID, err), EvmDisposeExitError
		return report
	}
	if req.BlockWait <= 0 {
		req.BlockWait = 2 * time.Minute
	}
	if req.Poll <= 0 {
		req.Poll = time.Second
	}
	d := &evmDisposer{
		store: store, st: newEvmState(store, req.ChainID, evmChainID.Uint64(), nil), req: req, evm: evm, cosmos: cosmos,
		report: report, actor: evmManualActor(req.Operator), blocks: map[uint64][]EvmBlockTx{},
	}
	switch req.Command {
	case EvmDisposeResolve:
		d.resolve(ctx)
	case EvmDisposeResolveOutcome:
		d.resolveOutcome(ctx)
	case EvmDisposeRelease, EvmDisposeDisposeOutcome:
		d.settleByDecision(ctx)
	}
	return report
}

func checkEvmDisposeRequest(req EvmDisposeRequest) error {
	if strings.TrimSpace(req.Operator) == "" || strings.TrimSpace(req.Reason) == "" {
		return fmt.Errorf("--operator and --reason are required")
	}
	switch req.Command {
	case EvmDisposeResolve:
		if !isEvmScope(req.Scope) {
			return fmt.Errorf("--scope must be one of operator, seal, gc")
		}
		if req.TxHash == (ethcmn.Hash{}) {
			return fmt.Errorf("--tx-hash is required")
		}
	case EvmDisposeResolveOutcome, EvmDisposeRelease, EvmDisposeDisposeOutcome:
		if req.OutcomeID == 0 {
			return fmt.Errorf("--outcome-id is required")
		}
		if req.AcceptUnknown || req.AcceptGap {
			return fmt.Errorf("--accept-unknown and --accept-gap apply to resolve only")
		}
	default:
		return fmt.Errorf("unknown command %q", req.Command)
	}
	if req.Command == EvmDisposeRelease {
		if strings.TrimSpace(req.DecisionRef) == "" || strings.TrimSpace(req.BusinessEvidence) == "" {
			return fmt.Errorf("release needs --decision-ref (the Board decision on this transaction) and --business-evidence")
		}
	}
	if req.Command == EvmDisposeRelease || req.Command == EvmDisposeDisposeOutcome {
		if req.EvidenceTx != (ethcmn.Hash{}) || req.EvidenceCosmosTx != "" || req.ScanBlocks != 0 {
			return fmt.Errorf("%s reads no chain evidence", req.Command)
		}
	}
	if req.ScanBlocks > EvmDisposeMaxScanBlocks {
		return fmt.Errorf("--scan-blocks is at most %d", EvmDisposeMaxScanBlocks)
	}
	return nil
}

func isEvmScope(scope string) bool {
	switch SignType(scope) {
	case SignOperator, SignSeal, SignGc:
		return true
	}
	return false
}

// evmManualActor is the operator name plus the OS user.
func evmManualActor(operator string) string {
	name := "unknown"
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	return fmt.Sprintf("%s (os user %s)", strings.TrimSpace(operator), name)
}

// manualEvent fills the fields every manual audit row carries.
func (d *evmDisposer) manualEvent(base evmAuditEvent, event string) evmAuditEvent {
	base.Event = event
	base.Actor = d.actor
	base.Reason = d.req.Reason
	if d.req.DecisionRef != "" {
		base.DecisionRef = d.req.DecisionRef
	}
	if last := d.lastSample(); last != nil {
		base.ChainNonce = uint64Ptr(last.ChainNonce)
		base.ChainHeight = uint64Ptr(last.Height)
	}
	return base
}

func (d *evmDisposer) lastSample() *EvmDisposeSample {
	if len(d.report.Samples) == 0 {
		return nil
	}
	return &d.report.Samples[len(d.report.Samples)-1]
}

// fail ends the command with an error that changes nothing and writes no audit.
func (d *evmDisposer) fail(code int, format string, args ...interface{}) {
	d.report.Error = fmt.Sprintf(format, args...)
	d.report.ExitCode = code
}

// reject ends the command without a change and audits manual_rejected,
// except in a dry run.
func (d *evmDisposer) reject(ctx context.Context, base evmAuditEvent, code int, format string, args ...interface{}) {
	d.fail(code, format, args...)
	d.report.Action = "none"
	if d.req.DryRun {
		return
	}
	event := d.manualEvent(base, evmAuditManualRejected)
	event.Detail = fmt.Sprintf("%s: %s", d.req.Command, d.report.Error)
	if d.report.Branch != "" {
		event.Detail += " (branch " + d.report.Branch + ")"
	}
	wctx, cancel := evmStateCtx(ctx)
	defer cancel()
	if err := d.store.AppendAudit(wctx, nil, d.req.ChainID, event); err != nil {
		d.report.Notes = append(d.report.Notes, "failed to audit the rejection: "+err.Error())
		return
	}
	d.report.Event = evmAuditManualRejected
}

// checkEvidence reads and verifies the evidence a row references. It
// returns the decoded transaction, or ends the command: exit 1 on a read
// error, exit 4 with an evidence_invalid audit when it is missing or invalid.
func (d *evmDisposer) checkEvidence(ctx context.Context, ref evidenceRef, base evmAuditEvent) (*signedTxEvidence, *types.Transaction, bool) {
	evidence, err := d.store.GetSignedTx(ctx, ref.SignedTxID)
	if err != nil {
		d.fail(EvmDisposeExitError, "failed to read the signed transaction evidence %d: %v", ref.SignedTxID, err)
		return nil, nil, false
	}
	tx, err := d.st.verify(evidence, ref)
	if err == nil {
		return evidence, tx, true
	}
	d.fail(EvmDisposeExitEvidenceInvalid, "%v; the row stays blocked", err)
	if !d.req.DryRun {
		event := d.manualEvent(base, evmAuditEvidenceInvalid)
		event.Detail = err.Error()
		if auditErr := d.store.AppendAudit(ctx, nil, d.req.ChainID, event); auditErr != nil {
			d.report.Notes = append(d.report.Notes, "failed to audit the invalid evidence: "+auditErr.Error())
		} else {
			d.report.Event = evmAuditEvidenceInvalid
		}
	}
	return nil, nil, false
}

// evmDisposeTarget is the original transaction, all from the evidence row.
type evmDisposeTarget struct {
	evidence  *signedTxEvidence
	tx        *types.Transaction
	operation ethcmn.Hash
	// probeGap: re-send the bytes when the chain nonce is below N (resolve,
	// not in a dry run).
	probeGap bool
	// gapIsBranch: a nonce below N is a gap candidate (resolve) rather than
	// "not reached yet" (resolve-outcome).
	gapIsBranch bool
}

func (d *evmDisposer) describe(evidence *signedTxEvidence) {
	d.report.From = evidence.From.Hex()
	d.report.Nonce = evidence.Nonce
	d.report.TxHash = evidence.TxHash.Hex()
	d.report.Operation = evidence.Operation.Hex()
}

func (d *evmDisposer) resolve(ctx context.Context) {
	scope := SignType(d.req.Scope)
	pending, err := d.store.GetPending(ctx, d.req.ChainID, scope)
	if err != nil {
		d.fail(EvmDisposeExitError, "failed to read the pending row: %v", err)
		return
	}
	if pending == nil {
		d.fail(EvmDisposeExitPrecondition, "scope %s has no pending transaction", scope)
		return
	}
	if pending.TxHash != d.req.TxHash {
		d.fail(EvmDisposeExitPrecondition, "scope %s pending transaction is %s, not %s", scope, pending.TxHash.Hex(), d.req.TxHash.Hex())
		return
	}
	base := pending.audit("")
	evidence, tx, ok := d.checkEvidence(ctx, pending.ref(), base)
	if !ok {
		return
	}
	d.describe(evidence)
	target := evmDisposeTarget{evidence: evidence, tx: tx, operation: pending.Operation, probeGap: !d.req.DryRun, gapIsBranch: true}
	sample, ok := d.assessTwice(ctx, target, base)
	if !ok {
		return
	}
	base = withOccupier(base, sample)

	var result *outcomeRecord
	event := ""
	keep := func(state evmOutcomeState, reason string, resultHash ethcmn.Hash) {
		result = &outcomeRecord{State: state, Reason: reason, ResultTxHash: resultHash, ReceiptBlock: sample.ReceiptBlock,
			Source: evmOutcomeSourceManual}
		if sample.Occupier != nil {
			result.EvidenceTxHash, result.EvidenceKind = sample.Occupier.Hash, sample.Occupier.Kind
			if sample.Occupier.Kind == evmEvidenceKindEVM {
				result.ReceiptBlock = sample.Occupier.Block
			}
		}
	}
	switch sample.Branch {
	case EvmBranchOriginalSucceeded:
		keep(evmOutcomeSucceeded, "", pending.TxHash)
		event = evmAuditManualResolveSuccess
	case EvmBranchOriginalFailed:
		event = evmAuditManualResolveFailed
	case EvmBranchOtherTxFailed:
		event = evmAuditManualResolveOtherTxFailed
	case EvmBranchOwnOtherOp, EvmBranchPlainTransfer:
		event = evmAuditManualResolveOtherUnrelated
	case EvmBranchSameCall:
		keep(evmOutcomeSucceeded, "", ethcmn.HexToHash(sample.Occupier.Hash))
		event = evmAuditManualResolveSameCall
	case EvmBranchCosmosSameOp:
		keep(evmOutcomeExecutedElsewhere, "", ethcmn.Hash{})
		event = evmAuditManualResolveElsewhere
	case EvmBranchUndetermined, EvmBranchOccupierMissing:
		if !d.req.AcceptUnknown {
			d.reject(ctx, base, EvmDisposeExitNotProven,
				"whether the operation ran cannot be proven; --accept-unknown turns the pending row into blocked_unknown")
			return
		}
		reason := evmReasonOtherTxUnverified
		if sample.Branch == EvmBranchOccupierMissing {
			reason = evmReasonReceiptMissing
		}
		keep(evmOutcomeBlockedUnknown, reason, ethcmn.Hash{})
		event = evmAuditManualResolveUnknown
	case EvmBranchNonceGap:
		if !d.req.AcceptGap {
			d.reject(ctx, base, EvmDisposeExitNotProven,
				"nonce gap confirmed; --accept-gap turns the pending row into blocked_unknown(nonce_gap)")
			return
		}
		keep(evmOutcomeBlockedUnknown, evmReasonNonceGap, ethcmn.Hash{})
		event = evmAuditManualResolveNonceGap
	case EvmBranchGapNotProbed:
		d.reject(ctx, base, EvmDisposeExitNotProven, "not probed: a dry run never re-sends the bytes, so the nonce gap (branch g) cannot be decided")
		return
	default:
		d.reject(ctx, base, EvmDisposeExitNotProven, "the chain does not decide the pending transaction (%s); it stays pending", sample.Branch)
		return
	}

	auditEvent := d.manualEvent(base, event)
	if result != nil {
		auditEvent.OutcomeID = uint64Ptr(pending.ID)
		auditEvent.OutcomeState = result.State
		auditEvent.OutcomeReason = result.Reason
		d.report.Action = fmt.Sprintf("turn pending %d into outcome %d (%s", pending.ID, pending.ID, result.State)
		if result.Reason != "" {
			d.report.Action += ", " + result.Reason
		}
		if result.ResultTxHash != (ethcmn.Hash{}) {
			d.report.Action += ", result " + result.ResultTxHash.Hex()
		}
		d.report.Action += ")"
		d.report.OutcomeID = pending.ID
		d.report.State = string(result.State)
		d.report.Reason = result.Reason
	} else {
		d.report.Action = fmt.Sprintf("delete pending %d; the operation may be signed again", pending.ID)
	}
	if d.req.DryRun {
		return
	}
	// The write, its read-back and the audit of a conflict outlive a
	// cancelled CLI, like every signer state write.
	wctx, cancel := evmStateCtx(ctx)
	defer cancel()
	err = d.store.ManualResolvePending(wctx, *pending, result, auditEvent)
	if isEvmCommitUnknown(err) {
		current, readErr := d.store.GetPending(wctx, d.req.ChainID, scope)
		if readErr == nil && (current == nil || current.ID != pending.ID) {
			if done, readErr := d.store.HasAudit(wctx, d.req.ChainID, event, pending.TxHash, 0); readErr == nil && done {
				err = nil
			}
		}
	}
	d.finishWrite(wctx, base, event, err)
}

// withOccupier copies the occupying transaction into an audit event.
func withOccupier(base evmAuditEvent, sample *EvmDisposeSample) evmAuditEvent {
	if sample.Occupier != nil {
		base.EvidenceTxHash, base.EvidenceKind = sample.Occupier.Hash, sample.Occupier.Kind
		if sample.Occupier.Operation != "" {
			base.Detail = "occupier operation " + sample.Occupier.Operation
		}
	}
	if sample.ReceiptBlock != 0 {
		base.ReceiptBlock = uint64Ptr(sample.ReceiptBlock)
	}
	return base
}

// finishWrite reports a manual transition's result. A row that changed in
// between is a precondition failure, audited as manual_rejected.
func (d *evmDisposer) finishWrite(ctx context.Context, base evmAuditEvent, event string, err error) {
	switch {
	case err == nil:
		d.report.Applied = true
		d.report.Event = event
		d.report.ExitCode = EvmDisposeExitOK
	case stderrors.Is(err, errEvmRowGone), stderrors.Is(err, errEvmStateConflict):
		d.reject(ctx, base, EvmDisposeExitPrecondition, "the row was handled or changed meanwhile: %v", err)
	case isEvmCommitUnknown(err):
		d.fail(EvmDisposeExitError, "%v; the change may or may not have committed, check 'signer-evm list' and the audit before retrying", err)
	default:
		d.fail(EvmDisposeExitError, "failed to write the change: %v", err)
	}
}

// loadOutcome reads the outcome row of --outcome-id and requires one of the
// states; otherwise the command ends with exit 3 and manual_rejected.
func (d *evmDisposer) loadOutcome(ctx context.Context, states ...evmOutcomeState) (*outcomeRecord, bool) {
	outcome, err := d.store.GetOutcomeByID(ctx, d.req.ChainID, d.req.OutcomeID)
	if err != nil {
		d.fail(EvmDisposeExitError, "failed to read outcome %d: %v", d.req.OutcomeID, err)
		return nil, false
	}
	base := evmAuditEvent{OutcomeID: uint64Ptr(d.req.OutcomeID)}
	if outcome == nil {
		d.reject(ctx, base, EvmDisposeExitPrecondition, "outcome %d does not exist (already handled, or never existed)", d.req.OutcomeID)
		return nil, false
	}
	d.report.Scope, d.report.State, d.report.Reason = string(outcome.Scope), string(outcome.State), outcome.Reason
	for _, state := range states {
		if outcome.State == state {
			return outcome, true
		}
	}
	d.reject(ctx, outcome.audit(""), EvmDisposeExitPrecondition, "outcome %d is %s, %s needs %v", outcome.ID, outcome.State,
		d.req.Command, states)
	return nil, false
}

func (d *evmDisposer) resolveOutcome(ctx context.Context) {
	outcome, ok := d.loadOutcome(ctx, evmOutcomeBlockedUnknown)
	if !ok {
		return
	}
	base := outcome.audit("")
	evidence, tx, ok := d.checkEvidence(ctx, outcome.ref(), base)
	if !ok {
		return
	}
	d.describe(evidence)
	target := evmDisposeTarget{evidence: evidence, tx: tx, operation: outcome.Operation}
	sample, ok := d.assessTwice(ctx, target, base)
	if !ok {
		return
	}
	base = withOccupier(base, sample)

	var to *outcomeRecord
	switch sample.Branch {
	case EvmBranchOriginalSucceeded:
		to = &outcomeRecord{State: evmOutcomeSucceeded, ResultTxHash: outcome.TxHash, ReceiptBlock: sample.ReceiptBlock}
	case EvmBranchSameCall:
		to = &outcomeRecord{State: evmOutcomeSucceeded, ResultTxHash: ethcmn.HexToHash(sample.Occupier.Hash),
			ReceiptBlock: sample.Occupier.Block}
	case EvmBranchCosmosSameOp:
		to = &outcomeRecord{State: evmOutcomeExecutedElsewhere}
	case EvmBranchOriginalFailed, EvmBranchOtherTxFailed, EvmBranchOwnOtherOp, EvmBranchPlainTransfer:
	default:
		d.reject(ctx, base, EvmDisposeExitNotProven, "the chain still does not prove the outcome (%s); outcome %d stays blocked",
			sample.Branch, outcome.ID)
		return
	}
	if to != nil {
		to.Source = evmOutcomeSourceManual
		if sample.Occupier != nil {
			to.EvidenceTxHash, to.EvidenceKind = sample.Occupier.Hash, sample.Occupier.Kind
		}
		d.report.Action = fmt.Sprintf("update outcome %d from blocked_unknown to %s", outcome.ID, to.State)
		if to.ResultTxHash != (ethcmn.Hash{}) {
			d.report.Action += ", result " + to.ResultTxHash.Hex()
		}
	} else {
		d.report.Action = fmt.Sprintf("delete outcome %d; the operation may be signed again", outcome.ID)
	}
	event := d.manualEvent(base, evmAuditManualOutcomeResolved)
	if to != nil {
		event.OutcomeState = to.State
		event.OutcomeReason = ""
	}
	event.Detail = strings.TrimSpace(event.Detail + " branch " + sample.Branch)
	d.settle(ctx, *outcome, []evmOutcomeState{evmOutcomeBlockedUnknown}, to, base, event)
}

// settleByDecision runs release and dispose-outcome, which act on an
// operator decision rather than on chain evidence.
func (d *evmDisposer) settleByDecision(ctx context.Context) {
	states := []evmOutcomeState{evmOutcomeSucceeded, evmOutcomeExecutedElsewhere}
	eventName := evmAuditManualDisposeOutcome
	if d.req.Command == EvmDisposeRelease {
		states = []evmOutcomeState{evmOutcomeBlockedUnknown}
		eventName = evmAuditManualReleaseUnknown
	}
	outcome, ok := d.loadOutcome(ctx, states...)
	if !ok {
		return
	}
	base := outcome.audit("")
	if d.req.Command == EvmDisposeRelease {
		evidence, _, ok := d.checkEvidence(ctx, outcome.ref(), base)
		if !ok {
			return
		}
		d.describe(evidence)
	} else {
		d.report.From, d.report.Nonce, d.report.TxHash = outcome.From.Hex(), outcome.Nonce, outcome.TxHash.Hex()
		d.report.Operation = outcome.Operation.Hex()
		evidence, err := d.store.GetSignedTx(ctx, outcome.SignedTxID)
		if err == nil {
			_, err = d.st.verify(evidence, outcome.ref())
		}
		if err != nil {
			// Disposing a row only lets the operation be signed again on
			// the business owner's word; it does not rely on the evidence.
			d.report.Notes = append(d.report.Notes, "evidence check: "+err.Error())
		}
	}
	event := d.manualEvent(base, eventName)
	if d.req.Command == EvmDisposeRelease {
		event.Detail = "business evidence: " + d.req.BusinessEvidence
		d.report.Action = fmt.Sprintf("delete blocked_unknown outcome %d without on-chain proof; the operation may be signed again", outcome.ID)
	} else {
		d.report.Action = fmt.Sprintf("delete %s outcome %d; the next request for the operation is signed again", outcome.State, outcome.ID)
	}
	d.settle(ctx, *outcome, states, nil, base, event)
}

func (d *evmDisposer) settle(ctx context.Context, outcome outcomeRecord, from []evmOutcomeState, to *outcomeRecord,
	base, event evmAuditEvent,
) {
	if d.req.DryRun {
		return
	}
	wctx, cancel := evmStateCtx(ctx)
	defer cancel()
	err := d.store.ManualSettleOutcome(wctx, outcome, from, to, event)
	if isEvmCommitUnknown(err) {
		if done, readErr := d.store.HasAudit(wctx, d.req.ChainID, event.Event, ethcmn.Hash{}, outcome.ID); readErr == nil && done {
			err = nil
		}
	}
	d.finishWrite(wctx, base, event.Event, err)
}

// assessTwice binds the decision to two heights H < H': both samples must
// reach the same conclusion. It ends the command (with manual_rejected) when
// they do not, or when the chain cannot be read.
func (d *evmDisposer) assessTwice(ctx context.Context, target evmDisposeTarget, base evmAuditEvent) (*EvmDisposeSample, bool) {
	chainID, err := d.evm.ChainID(ctx)
	if err != nil {
		d.reject(ctx, base, EvmDisposeExitError, "eth_chainId: %v", err)
		return nil, false
	}
	if !chainID.IsUint64() || chainID.Uint64() != target.evidence.EvmChainID {
		d.reject(ctx, base, EvmDisposeExitError, "the EVM RPC serves chain id %s, the evidence was signed for %d", chainID, target.evidence.EvmChainID)
		return nil, false
	}
	first, err := d.evm.BlockNumber(ctx)
	if err != nil {
		d.reject(ctx, base, EvmDisposeExitError, "eth_blockNumber: %v", err)
		return nil, false
	}
	for i, height := range []uint64{first, 0} {
		if i == 1 {
			if height, err = d.waitHeight(ctx, first+1); err != nil {
				d.reject(ctx, base, EvmDisposeExitError, "no second height: %v; retry later", err)
				return nil, false
			}
		}
		sample, err := d.assess(ctx, target, height)
		if sample != nil {
			d.report.Samples = append(d.report.Samples, *sample)
		}
		if err != nil {
			d.reject(ctx, base, EvmDisposeExitError, "reading the chain at height %d: %v", height, err)
			return nil, false
		}
	}
	a, b := &d.report.Samples[0], &d.report.Samples[1]
	d.report.Branch = b.Branch
	if a.key() != b.key() {
		d.report.Branch = ""
		d.reject(ctx, base, EvmDisposeExitNotProven, "heights %d and %d disagree (%s vs %s); retry later", a.Height, b.Height, a.key(), b.key())
		return nil, false
	}
	return b, true
}

func (d *evmDisposer) waitHeight(ctx context.Context, want uint64) (uint64, error) {
	deadline := time.Now().Add(d.req.BlockWait)
	for {
		height, err := d.evm.BlockNumber(ctx)
		if err != nil {
			return 0, err
		}
		if height >= want {
			return height, nil
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("height %d not reached within %s", want, d.req.BlockWait)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(d.req.Poll):
		}
	}
}

func heightArg(height uint64) *big.Int { return new(big.Int).SetUint64(height) }

// assess decides the case at one height. Everything about the original
// transaction comes from the evidence; the RPC is never asked for it.
func (d *evmDisposer) assess(ctx context.Context, target evmDisposeTarget, height uint64) (*EvmDisposeSample, error) {
	sample := &EvmDisposeSample{Height: height}
	evidence := target.evidence

	receipt, err := d.evm.TransactionReceipt(ctx, evidence.TxHash)
	switch {
	case stderrors.Is(err, ethereum.NotFound) || (err == nil && receipt == nil):
	case err != nil:
		return sample, fmt.Errorf("receipt of %s: %w", evidence.TxHash.Hex(), err)
	case receipt.BlockNumber == nil || receipt.BlockNumber.Uint64() > height:
		sample.note("the original receipt is above height %d", height)
	default:
		sample.ReceiptBlock = receipt.BlockNumber.Uint64()
		sample.Branch = EvmBranchOriginalFailed
		if receipt.Status == types.ReceiptStatusSuccessful {
			sample.Branch = EvmBranchOriginalSucceeded
		}
	}

	if sample.ChainNonce, err = d.evm.NonceAt(ctx, evidence.From, heightArg(height)); err != nil {
		return sample, fmt.Errorf("nonce of %s at %d: %w", evidence.From.Hex(), height, err)
	}
	if sample.Branch != "" {
		return sample, nil
	}
	switch {
	case sample.ChainNonce == evidence.Nonce:
		sample.Branch = EvmBranchNonceUnused
		return sample, nil
	case sample.ChainNonce < evidence.Nonce:
		if !target.gapIsBranch {
			sample.Branch = EvmBranchNonceNotReached
			return sample, nil
		}
		return sample, d.assessGap(ctx, target, sample)
	}

	occupier, err := d.findOccupier(ctx, evidence, sample)
	if err != nil {
		return sample, err
	}
	if occupier == nil {
		sample.Branch = EvmBranchOccupierMissing
		return sample, nil
	}
	sample.Occupier = occupier
	return sample, d.classify(ctx, target, sample)
}

// assessGap is §3.4 branch g: the chain nonce M is below N at the height and
// in the node's pending view, and the node refuses the bytes for a nonce
// above the account's. The probe is a re-send of the same bytes, as the
// signer itself does; a dry run never sends.
func (d *evmDisposer) assessGap(ctx context.Context, target evmDisposeTarget, sample *EvmDisposeSample) error {
	pending, err := d.evm.PendingNonceAt(ctx, target.evidence.From)
	if err != nil {
		return fmt.Errorf("pending nonce of %s: %w", target.evidence.From.Hex(), err)
	}
	sample.PendingNonce = uint64Ptr(pending)
	if !target.probeGap {
		sample.Branch = EvmBranchGapNotProbed
		sample.note("not probed")
		return nil
	}
	if pending != sample.ChainNonce {
		sample.Branch = EvmBranchGapUnproven
		sample.note("pending nonce %d differs from the nonce %d at height %d", pending, sample.ChainNonce, sample.Height)
		return nil
	}
	sendErr := d.evm.SendTransaction(ctx, target.tx)
	switch {
	case sendErr == nil:
		sample.Branch = EvmBranchGapUnproven
		sample.note("the node accepted the re-sent bytes: the gap may have closed")
	case classifyEvmSendError(ctx, sendErr) == evmSendNonceRejected && isEvmNonceHighError(sendErr.Error()):
		sample.Branch = EvmBranchNonceGap
		sample.note("probe refused: %v", sendErr)
	default:
		sample.Branch = EvmBranchGapUnproven
		sample.note("probe answer is not a nonce-too-high refusal: %v", sendErr)
	}
	return nil
}

// findOccupier looks for the transaction T that used nonce N: the given EVM
// hash, the given Cosmos hash, then a bounded scan from the recorded block
// (EVM blocks and tx.acc_seq). It returns nil when none is found.
func (d *evmDisposer) findOccupier(ctx context.Context, evidence *signedTxEvidence, sample *EvmDisposeSample) (*EvmDisposeOccupier, error) {
	if d.req.EvidenceTx != (ethcmn.Hash{}) {
		occupier, err := d.evmOccupier(ctx, evidence, d.req.EvidenceTx, sample, "--evidence-tx")
		if occupier != nil || err != nil {
			return occupier, err
		}
	}
	if d.req.EvidenceCosmosTx != "" {
		if d.cosmos == nil {
			return nil, stderrors.New("--evidence-cosmos-tx needs a CometBFT RPC")
		}
		if err := d.checkCosmos(ctx, evidence, sample); err != nil {
			return nil, err
		}
		view, err := d.cosmos.Tx(ctx, d.req.EvidenceCosmosTx)
		switch {
		case stderrors.Is(err, errCosmosIndexDisabled):
			sample.note("--evidence-cosmos-tx cannot be looked up: %v", err)
		case err != nil:
			return nil, fmt.Errorf("cosmos tx %s: %w", d.req.EvidenceCosmosTx, err)
		case view == nil:
			sample.note("--evidence-cosmos-tx %s not found", d.req.EvidenceCosmosTx)
		default:
			if occupier := cosmosOccupier(evidence, view, sample, "--evidence-cosmos-tx"); occupier != nil {
				return occupier, nil
			}
		}
	}
	if d.req.ScanBlocks == 0 {
		return nil, nil
	}
	start := evidence.RecordedBlock
	end := start + d.req.ScanBlocks - 1
	if end > sample.Height {
		end = sample.Height
	} else if end < sample.Height {
		sample.note("scan stopped at block %d, below height %d", end, sample.Height)
	}
	for height := start; height <= end; height++ {
		txs, ok := d.blocks[height]
		if !ok {
			var err error
			txs, err = d.evm.BlockTransactions(ctx, height)
			if stderrors.Is(err, errEvmBlockUnavailable) {
				sample.note("block %d is not available on the node", height)
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("block %d: %w", height, err)
			}
			d.blocks[height] = txs
		}
		for _, tx := range txs {
			if tx.From != evidence.From || tx.Nonce != evidence.Nonce || tx.Hash == evidence.TxHash {
				continue
			}
			occupier, err := d.evmOccupier(ctx, evidence, tx.Hash, sample, fmt.Sprintf("scan of block %d", height))
			if occupier != nil || err != nil {
				return occupier, err
			}
		}
	}
	if err := d.checkCosmos(ctx, evidence, sample); err != nil {
		return nil, err
	}
	if d.cosmos == nil {
		return nil, nil
	}
	accSeq := fmt.Sprintf("%s/%d", evidence.From.Hex(), evidence.Nonce)
	views, err := d.cosmos.SearchAccSeq(ctx, accSeq, sample.Height)
	if stderrors.Is(err, errCosmosIndexDisabled) {
		sample.note("tx.acc_seq search unavailable: %v", err)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("tx_search tx.acc_seq='%s': %w", accSeq, err)
	}
	for _, view := range views {
		if occupier := cosmosOccupier(evidence, view, sample, "tx.acc_seq search"); occupier != nil {
			return occupier, nil
		}
	}
	return nil, nil
}

// checkCosmos verifies once that the CometBFT RPC serves the row's chain.
func (d *evmDisposer) checkCosmos(ctx context.Context, evidence *signedTxEvidence, sample *EvmDisposeSample) error {
	if d.cosmos == nil {
		sample.note("no CometBFT RPC configured: Cosmos transactions are not searched")
		return nil
	}
	if d.cosmosChecked {
		return nil
	}
	network, err := d.cosmos.Network(ctx)
	if err != nil {
		return fmt.Errorf("CometBFT status: %w", err)
	}
	if network != evidence.ChainID {
		return fmt.Errorf("the CometBFT RPC serves chain %s, the row belongs to %s", network, evidence.ChainID)
	}
	d.cosmosChecked = true
	return nil
}

// evmOccupier checks that hash is an EVM transaction from the row's sender
// at nonce N, other than the original, with a receipt at or below the height.
func (d *evmDisposer) evmOccupier(ctx context.Context, evidence *signedTxEvidence, hash ethcmn.Hash, sample *EvmDisposeSample,
	source string,
) (*EvmDisposeOccupier, error) {
	if hash == evidence.TxHash {
		sample.note("%s: %s is the original transaction", source, hash.Hex())
		return nil, nil
	}
	tx, _, err := d.evm.TransactionByHash(ctx, hash)
	if stderrors.Is(err, ethereum.NotFound) || (err == nil && tx == nil) {
		sample.note("%s: %s not found", source, hash.Hex())
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("transaction %s: %w", hash.Hex(), err)
	}
	from, err := types.Sender(types.LatestSignerForChainID(new(big.Int).SetUint64(evidence.EvmChainID)), tx)
	if err != nil || from != evidence.From || tx.Nonce() != evidence.Nonce || tx.Hash() != hash {
		sample.note("%s: %s is not from %s at nonce %d", source, hash.Hex(), evidence.From.Hex(), evidence.Nonce)
		return nil, nil
	}
	receipt, err := d.evm.TransactionReceipt(ctx, hash)
	if stderrors.Is(err, ethereum.NotFound) || (err == nil && receipt == nil) {
		sample.note("%s: %s has no receipt", source, hash.Hex())
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("receipt of %s: %w", hash.Hex(), err)
	}
	if receipt.BlockNumber == nil || receipt.BlockNumber.Uint64() > sample.Height {
		sample.note("%s: %s is above height %d", source, hash.Hex(), sample.Height)
		return nil, nil
	}
	return &EvmDisposeOccupier{
		Kind: evmEvidenceKindEVM, Hash: hash.Hex(), Block: receipt.BlockNumber.Uint64(),
		Succeeded: receipt.Status == types.ReceiptStatusSuccessful, Source: source, tx: tx,
	}, nil
}

// cosmosOccupier checks that a Cosmos transaction has the row's sender as a
// signer at sequence N, at or below the height.
func cosmosOccupier(evidence *signedTxEvidence, view *CosmosTxView, sample *EvmDisposeSample, source string) *EvmDisposeOccupier {
	want := fmt.Sprintf("%s/%d", evidence.From.Hex(), evidence.Nonce)
	signed := false
	for _, accSeq := range view.AccSeqs {
		if strings.EqualFold(accSeq, want) {
			signed = true
		}
	}
	if !signed {
		sample.note("%s: cosmos tx %s is not signed by %s", source, view.Hash, want)
		return nil
	}
	if view.Height == 0 || view.Height > sample.Height {
		sample.note("%s: cosmos tx %s is above height %d", source, view.Hash, sample.Height)
		return nil
	}
	return &EvmDisposeOccupier{
		Kind: evmEvidenceKindCosmos, Hash: strings.ToUpper(strings.TrimPrefix(view.Hash, "0x")), Block: view.Height,
		Succeeded: view.Code == 0, Source: source, cosmos: view,
	}
}

// classify is plan §4.2 step 3 for an occupier found at the height.
func (d *evmDisposer) classify(ctx context.Context, target evmDisposeTarget, sample *EvmDisposeSample) error {
	occupier := sample.Occupier
	if !occupier.Succeeded {
		if evmFailedTxRollsBack {
			sample.Branch = EvmBranchOtherTxFailed
		} else {
			sample.Branch = EvmBranchUndetermined
			sample.note("the occupier failed, but V1 (failed transactions leave no state) is not verified")
		}
		return nil
	}
	if occupier.Kind == evmEvidenceKindCosmos {
		sample.Branch = EvmBranchUndetermined
		if occupier.cosmos.DecodeError != "" {
			sample.note("cosmos tx %s does not decode: %s", occupier.Hash, occupier.cosmos.DecodeError)
			return nil
		}
		for _, msg := range occupier.cosmos.Msgs {
			if operation, err := evmOperationOf(msg); err == nil && operation == target.operation {
				sample.Branch = EvmBranchCosmosSameOp
				return nil
			}
		}
		sample.note("no message of cosmos tx %s has the operation's fingerprint", occupier.Hash)
		return nil
	}

	tx := occupier.tx
	own, err := d.store.GetSignedTxByHash(ctx, target.evidence.ChainID, tx.Hash())
	if err != nil {
		return fmt.Errorf("evidence of %s: %w", tx.Hash().Hex(), err)
	}
	if own != nil {
		ref := evidenceRef{SignedTxID: own.ID, ChainID: own.ChainID, Scope: own.Scope, Operation: own.Operation,
			From: own.From, Nonce: own.Nonce, TxHash: own.TxHash}
		if _, err := d.st.verify(own, ref); err != nil {
			sample.Branch = EvmBranchUndetermined
			sample.note("the occupier's own evidence row is invalid: %v", err)
			return nil
		}
		occupier.Operation = own.Operation.Hex()
		sample.Branch = EvmBranchOwnOtherOp
		if own.Operation == target.operation {
			sample.Branch = EvmBranchSameCall
		}
		return nil
	}
	original := target.tx
	if tx.To() != nil && original.To() != nil && *tx.To() == *original.To() && tx.Value().Cmp(original.Value()) == 0 &&
		bytes.Equal(tx.Data(), original.Data()) {
		sample.Branch = EvmBranchSameCall
		return nil
	}
	sample.Branch = EvmBranchUndetermined
	if tx.To() == nil || len(tx.Data()) != 0 || isPossiblePrecompile(*tx.To()) {
		sample.note("the occupier calls or creates a contract or precompile")
		return nil
	}
	if occupier.Block == 0 {
		sample.note("the occupier has no earlier block to read code at")
		return nil
	}
	for _, at := range []uint64{occupier.Block - 1, occupier.Block} {
		code, err := d.evm.CodeAt(ctx, *tx.To(), heightArg(at))
		if err != nil {
			sample.note("code of %s at %d unavailable (%v): historical state is needed for branch c", tx.To().Hex(), at, err)
			return nil
		}
		if len(code) != 0 {
			sample.note("%s had code at block %d", tx.To().Hex(), at)
			return nil
		}
	}
	sample.Branch = EvmBranchPlainTransfer
	return nil
}

// isPossiblePrecompile is conservative: every Moca precompile
// (moca types/contract.go, 0x1000–0x2002) and every cosmos/evm and Ethereum
// precompile lies below 0x10000, and precompiles have no code.
func isPossiblePrecompile(address ethcmn.Address) bool {
	for _, b := range address[:18] {
		if b != 0 {
			return false
		}
	}
	return true
}

// evmOperationOf is the operation fingerprint the signer's *Evm method for
// the message's type computes.
func evmOperationOf(msg sdk.Msg) (ethcmn.Hash, error) {
	if exit, ok := msg.(*virtualgrouptypes.MsgCompleteStorageProviderExit); ok {
		return completeSPExitEvmFingerprint(exit)
	}
	return evmOperationFingerprint(msg)
}
