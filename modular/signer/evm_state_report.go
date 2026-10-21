package signer

import (
	"context"
	"fmt"
	"time"

	ethcmn "github.com/ethereum/go-ethereum/common"
	"gorm.io/gorm"

	"github.com/mocachain/moca-storage-provider/store/sqldb"
	evmostypes "github.com/mocachain/moca/v2/types"
)

// EvmStateRow is one pending or active outcome row as 'signer-evm list'
// shows it, with the result of the evidence check V(e).
type EvmStateRow struct {
	Kind           string `json:"kind"`
	ID             uint64 `json:"id"`
	Scope          string `json:"scope"`
	State          string `json:"state,omitempty"`
	Reason         string `json:"reason,omitempty"`
	From           string `json:"from_address"`
	Nonce          uint64 `json:"nonce"`
	TxHash         string `json:"tx_hash"`
	ResultTxHash   string `json:"result_tx_hash,omitempty"`
	EvidenceTxHash string `json:"evidence_tx_hash,omitempty"`
	Operation      string `json:"operation"`
	Ambiguous      bool   `json:"ambiguous,omitempty"`
	SignedTxID     uint64 `json:"signed_tx_id"`
	RecordedBlock  uint64 `json:"recorded_block,omitempty"`
	EvidenceValid  bool   `json:"evidence_valid"`
	EvidenceError  string `json:"evidence_error,omitempty"`
}

// EvmLeaseView is the lease row.
type EvmLeaseView struct {
	Owner     string    `json:"owner"`
	Epoch     uint64    `json:"epoch"`
	ExpiresAt time.Time `json:"expires_at"`
	Live      bool      `json:"live"`
}

// EvmStateReport is the read-only view of the signer EVM state of one chain.
type EvmStateReport struct {
	ChainID         string        `json:"chain_id"`
	EvmChainID      uint64        `json:"evm_chain_id"`
	Pending         []EvmStateRow `json:"pending"`
	Outcomes        []EvmStateRow `json:"outcomes"`
	Lease           *EvmLeaseView `json:"lease,omitempty"`
	ForeignChainIDs []string      `json:"foreign_chain_ids,omitempty"`
}

// InspectEvmState reads the pending rows, the active outcomes and the lease
// of chainID and runs V(e) on every row. It writes nothing.
func InspectEvmState(ctx context.Context, db *gorm.DB, chainID string) (*EvmStateReport, error) {
	evmChainID, err := evmostypes.ParseChainID(chainID)
	if err != nil {
		return nil, fmt.Errorf("failed to derive the EVM chain id from %q: %w", chainID, err)
	}
	store := newSQLEvmStateStore(db)

	report := &EvmStateReport{ChainID: chainID, EvmChainID: evmChainID.Uint64()}

	if report.ForeignChainIDs, err = store.ForeignChainIDs(ctx, chainID); err != nil {
		return nil, err
	}
	pendings, err := store.ListPending(ctx, chainID)
	if err != nil {
		return nil, err
	}
	for _, p := range pendings {
		row := EvmStateRow{Kind: "pending", ID: p.ID, Scope: string(p.Scope), From: p.From.Hex(), Nonce: p.Nonce,
			TxHash: p.TxHash.Hex(), Operation: p.Operation.Hex(), Ambiguous: p.Ambiguous, SignedTxID: p.SignedTxID}
		if err := inspectEvidence(ctx, store, evmChainID.Uint64(), p.ref(), &row); err != nil {
			return nil, err
		}
		report.Pending = append(report.Pending, row)
	}
	outcomes, err := store.ListOutcomes(ctx, chainID)
	if err != nil {
		return nil, err
	}
	for _, o := range outcomes {
		row := EvmStateRow{Kind: "outcome", ID: o.ID, Scope: string(o.Scope), State: string(o.State), Reason: o.Reason,
			From: o.From.Hex(), Nonce: o.Nonce, TxHash: o.TxHash.Hex(), EvidenceTxHash: o.EvidenceTxHash,
			Operation: o.Operation.Hex(), SignedTxID: o.SignedTxID}
		if o.ResultTxHash != (ethcmn.Hash{}) {
			row.ResultTxHash = o.ResultTxHash.Hex()
		}
		if err := inspectEvidence(ctx, store, evmChainID.Uint64(), o.ref(), &row); err != nil {
			return nil, err
		}
		report.Outcomes = append(report.Outcomes, row)
	}

	var leases []struct {
		Owner     string
		Epoch     uint64
		ExpiresAt time.Time
		Live      bool
	}
	if err := db.WithContext(ctx).Raw(`SELECT owner, epoch, expires_at, expires_at > NOW(6) AS live FROM `+
		sqldb.SignerEvmLeaseTableName+` WHERE chain_id = ?`, chainID).Scan(&leases).Error; err != nil {
		return nil, err
	}
	if len(leases) == 1 {
		report.Lease = &EvmLeaseView{Owner: leases[0].Owner, Epoch: leases[0].Epoch, ExpiresAt: leases[0].ExpiresAt, Live: leases[0].Live}
	}
	return report, nil
}

func inspectEvidence(ctx context.Context, store evmStateStore, chainID uint64, ref evidenceRef, row *EvmStateRow) error {
	evidence, err := store.GetSignedTx(ctx, ref.SignedTxID)
	if err != nil {
		return fmt.Errorf("failed to read evidence %d: %w", ref.SignedTxID, err)
	}
	if evidence != nil {
		row.RecordedBlock = evidence.RecordedBlock
	}
	if _, err := verifySignedTxEvidence(evidence, ref); err != nil {
		row.EvidenceError = err.Error()
		return nil
	}
	if evidence.EvmChainID != chainID {
		row.EvidenceError = fmt.Sprintf("%v: row %d evm_chain_id=%d, signer EVM chain id is %d", errEvmEvidenceInvalid, evidence.ID, evidence.EvmChainID, chainID)
		return nil
	}
	row.EvidenceValid = true
	return nil
}
