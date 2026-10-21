package signer

import (
	"context"
	stderrors "errors"
	"fmt"
	"time"

	ethcmn "github.com/ethereum/go-ethereum/common"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mocachain/moca-storage-provider/store/sqldb"
)

const mysqlErrDuplicateEntry = 1062

// sqlEvmStateStore implements evmStateStore on the SP DB with gorm.
type sqlEvmStateStore struct {
	db *gorm.DB
}

var _ evmStateStore = (*sqlEvmStateStore)(nil)

func newSQLEvmStateStore(db *gorm.DB) *sqlEvmStateStore {
	return &sqlEvmStateStore{db: db}
}

func isDuplicateKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	return stderrors.As(err, &mysqlErr) && mysqlErr.Number == mysqlErrDuplicateEntry
}

// txn runs fn in one transaction. An error before COMMIT rolls back; an error
// from COMMIT itself is reported as evmCommitUnknownError.
func (s *sqlEvmStateStore) txn(ctx context.Context, fn func(tx *gorm.DB) error) (err error) {
	tx := s.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	committed := false
	defer func() {
		// Also on panic: never leave the lease row locked.
		if !committed {
			tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	committed = true
	if err = tx.Commit().Error; err != nil {
		return &evmCommitUnknownError{err: err}
	}
	return nil
}

// fenceCheck locks the lease row and requires it to be held by fence.
func fenceCheck(tx *gorm.DB, fence evmFence) error {
	var rows []struct {
		Owner string
		Epoch uint64
		Live  bool
	}
	if err := tx.Raw(`SELECT owner, epoch, expires_at > NOW(6) AS live FROM `+sqldb.SignerEvmLeaseTableName+
		` WHERE chain_id = ? FOR UPDATE`, fence.ChainID).Scan(&rows).Error; err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].Owner != fence.Owner || rows[0].Epoch != fence.Epoch || !rows[0].Live {
		return errEvmLeaseNotHeld
	}
	return nil
}

func insertAudit(tx *gorm.DB, chainID string, epoch uint64, event evmAuditEvent) error {
	row := sqldb.SignerEvmAuditTable{
		ChainID:        chainID,
		Scope:          string(event.Scope),
		Nonce:          event.Nonce,
		SignedTxID:     event.SignedTxID,
		Event:          event.Event,
		ReceiptStatus:  event.ReceiptStatus,
		ReceiptBlock:   event.ReceiptBlock,
		ChainNonce:     event.ChainNonce,
		OutcomeID:      event.OutcomeID,
		OutcomeState:   string(event.OutcomeState),
		OutcomeReason:  event.OutcomeReason,
		Actor:          evmAuditActor(),
		Detail:         event.Detail,
		EvidenceTxHash: event.EvidenceTxHash,
		EvidenceKind:   event.EvidenceKind,
		ToolVersion:    evmToolVersion(),
		LeaseEpoch:     epoch,
		CreatedAt:      time.Now().Unix(),
	}
	if event.From != (ethcmn.Address{}) {
		row.FromAddress = event.From.Hex()
	}
	if event.TxHash != (ethcmn.Hash{}) {
		row.TxHash = event.TxHash.Hex()
	}
	if event.Operation != (ethcmn.Hash{}) {
		row.Operation = event.Operation.Hex()
	}
	return tx.Create(&row).Error
}

func evidenceFromRow(row sqldb.SignerEvmSignedTxTable) signedTxEvidence {
	return signedTxEvidence{
		ID: row.ID, ChainID: row.ChainID, EvmChainID: row.EvmChainID, Scope: SignType(row.Scope),
		Operation: ethcmn.HexToHash(row.Operation), From: ethcmn.HexToAddress(row.FromAddress), Nonce: row.Nonce,
		TxHash: ethcmn.HexToHash(row.TxHash), Raw: row.Raw, RecordedBlock: row.RecordedBlock, LeaseEpoch: row.LeaseEpoch,
	}
}

func pendingFromRow(row sqldb.SignerEvmStateTable) pendingRecord {
	return pendingRecord{
		ID: row.ID, ChainID: row.ChainID, SignedTxID: row.SignedTxID, Scope: SignType(row.Scope),
		From: ethcmn.HexToAddress(row.FromAddress), Nonce: row.Nonce, TxHash: ethcmn.HexToHash(row.TxHash),
		Operation: ethcmn.HexToHash(row.Operation), Ambiguous: row.Ambiguous, LeaseEpoch: row.LeaseEpoch,
	}
}

func outcomeFromRow(row sqldb.SignerEvmStateTable) outcomeRecord {
	out := outcomeRecord{
		ID: row.ID, SignedTxID: row.SignedTxID, ChainID: row.ChainID, Scope: SignType(row.Scope),
		Operation: ethcmn.HexToHash(row.Operation), From: ethcmn.HexToAddress(row.FromAddress), Nonce: row.Nonce,
		TxHash: ethcmn.HexToHash(row.TxHash), State: evmOutcomeState(row.State), EvidenceTxHash: row.EvidenceTxHash,
		EvidenceKind: row.EvidenceKind, Reason: row.Reason, ReceiptBlock: row.ReceiptBlock, Source: row.Source,
	}
	if row.ResultTxHash != "" {
		out.ResultTxHash = ethcmn.HexToHash(row.ResultTxHash)
	}
	return out
}

func (s *sqlEvmStateStore) AcquireLease(ctx context.Context, chainID, owner string, ttl time.Duration) (uint64, bool, error) {
	var epoch uint64
	acquired := false
	err := s.txn(ctx, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT IGNORE INTO `+sqldb.SignerEvmLeaseTableName+
			` (chain_id, owner, epoch, expires_at) VALUES (?, '', 0, '1970-01-01 00:00:01')`, chainID).Error; err != nil {
			return err
		}
		var rows []struct {
			Owner   string
			Epoch   uint64
			Expired bool
		}
		if err := tx.Raw(`SELECT owner, epoch, expires_at < NOW(6) AS expired FROM `+sqldb.SignerEvmLeaseTableName+
			` WHERE chain_id = ? FOR UPDATE`, chainID).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) != 1 {
			return fmt.Errorf("lease row for %s is missing", chainID)
		}
		if !rows[0].Expired && rows[0].Owner != owner {
			return nil
		}
		epoch = rows[0].Epoch + 1
		if err := tx.Exec(`UPDATE `+sqldb.SignerEvmLeaseTableName+
			` SET owner = ?, epoch = ?, expires_at = NOW(6) + INTERVAL ? MICROSECOND WHERE chain_id = ?`,
			owner, epoch, ttl.Microseconds(), chainID).Error; err != nil {
			return err
		}
		acquired = true
		return insertAudit(tx, chainID, epoch, evmAuditEvent{
			Event: evmAuditLeaseAcquired, Detail: fmt.Sprintf("owner=%s previous_owner=%s", owner, rows[0].Owner),
		})
	})
	if err != nil {
		return 0, false, err
	}
	return epoch, acquired, nil
}

func (s *sqlEvmStateStore) RenewLease(ctx context.Context, fence evmFence, ttl time.Duration) (bool, error) {
	res := s.db.WithContext(ctx).Exec(`UPDATE `+sqldb.SignerEvmLeaseTableName+
		` SET expires_at = NOW(6) + INTERVAL ? MICROSECOND WHERE chain_id = ? AND owner = ? AND epoch = ? AND expires_at > NOW(6)`,
		ttl.Microseconds(), fence.ChainID, fence.Owner, fence.Epoch)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

func (s *sqlEvmStateStore) ReleaseLease(ctx context.Context, fence evmFence) error {
	return s.db.WithContext(ctx).Exec(`UPDATE `+sqldb.SignerEvmLeaseTableName+
		` SET expires_at = '1970-01-01 00:00:01' WHERE chain_id = ? AND owner = ? AND epoch = ?`,
		fence.ChainID, fence.Owner, fence.Epoch).Error
}

func (s *sqlEvmStateStore) GetPending(ctx context.Context, chainID string, scope SignType) (*pendingRecord, error) {
	var rows []sqldb.SignerEvmStateTable
	if err := s.db.WithContext(ctx).Where("state = 'pending' AND chain_id = ? AND scope = ?", chainID, string(scope)).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	record := pendingFromRow(rows[0])
	return &record, nil
}

func (s *sqlEvmStateStore) ListPending(ctx context.Context, chainID string) ([]pendingRecord, error) {
	var rows []sqldb.SignerEvmStateTable
	if err := s.db.WithContext(ctx).Where("state = 'pending' AND chain_id = ?", chainID).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]pendingRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, pendingFromRow(row))
	}
	return records, nil
}

func (s *sqlEvmStateStore) GetOutcome(ctx context.Context, chainID string, scope SignType, operation ethcmn.Hash) (*outcomeRecord, error) {
	var rows []sqldb.SignerEvmStateTable
	if err := s.db.WithContext(ctx).Where("state <> 'pending' AND chain_id = ? AND scope = ? AND operation = ?", chainID, string(scope), operation.Hex()).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	record := outcomeFromRow(rows[0])
	return &record, nil
}

func (s *sqlEvmStateStore) ListOutcomes(ctx context.Context, chainID string) ([]outcomeRecord, error) {
	var rows []sqldb.SignerEvmStateTable
	if err := s.db.WithContext(ctx).Where("state <> 'pending' AND chain_id = ?", chainID).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]outcomeRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, outcomeFromRow(row))
	}
	return records, nil
}

func (s *sqlEvmStateStore) GetSignedTx(ctx context.Context, id uint64) (*signedTxEvidence, error) {
	var rows []sqldb.SignerEvmSignedTxTable
	if err := s.db.WithContext(ctx).Where("id = ?", id).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	evidence := evidenceFromRow(rows[0])
	return &evidence, nil
}

func (s *sqlEvmStateStore) HasAudit(ctx context.Context, chainID, event string, txHash ethcmn.Hash, outcomeID uint64) (bool, error) {
	query := s.db.WithContext(ctx).Model(&sqldb.SignerEvmAuditTable{}).Where("chain_id = ? AND event = ?", chainID, event)
	if txHash != (ethcmn.Hash{}) {
		query = query.Where("tx_hash = ?", txHash.Hex())
	}
	if outcomeID != 0 {
		query = query.Where("outcome_id = ?", outcomeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *sqlEvmStateStore) ForeignChainIDs(ctx context.Context, chainID string) ([]string, error) {
	var ids []string
	err := s.db.WithContext(ctx).Raw(`SELECT chain_id FROM `+sqldb.SignerEvmStateTableName+` WHERE chain_id <> ?
UNION SELECT chain_id FROM `+sqldb.SignerEvmLeaseTableName+` WHERE chain_id <> ?
UNION SELECT chain_id FROM `+sqldb.SignerEvmSignedTxTableName+` WHERE chain_id <> ?
UNION SELECT chain_id FROM `+sqldb.SignerEvmAuditTableName+` WHERE chain_id <> ?`,
		chainID, chainID, chainID, chainID).Scan(&ids).Error
	return ids, err
}

func (s *sqlEvmStateStore) Record(ctx context.Context, fence evmFence, evidence signedTxEvidence, pending pendingRecord) (pendingRecord, error) {
	now := time.Now().Unix()
	err := s.txn(ctx, func(tx *gorm.DB) error {
		if err := fenceCheck(tx, fence); err != nil {
			return err
		}
		var active []uint64
		if err := tx.Raw(`SELECT id FROM `+sqldb.SignerEvmStateTableName+
			` WHERE state <> 'pending' AND chain_id = ? AND scope = ? AND operation = ? FOR UPDATE`,
			pending.ChainID, string(pending.Scope), pending.Operation.Hex()).Scan(&active).Error; err != nil {
			return err
		}
		if len(active) != 0 {
			return fmt.Errorf("%w: operation %s has active outcome %d", errEvmStateConflict, pending.Operation.Hex(), active[0])
		}
		var existing []sqldb.SignerEvmSignedTxTable
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).
			Where("chain_id = ? AND tx_hash = ?", evidence.ChainID, evidence.TxHash.Hex()).Find(&existing).Error; err != nil {
			return err
		}
		if len(existing) != 0 {
			if !sameEvidence(evidenceFromRow(existing[0]), evidence) {
				return fmt.Errorf("%w: evidence row %d for %s differs", errEvmStateConflict, existing[0].ID, evidence.TxHash.Hex())
			}
			pending.SignedTxID = existing[0].ID
		} else {
			row := sqldb.SignerEvmSignedTxTable{
				ChainID: evidence.ChainID, EvmChainID: evidence.EvmChainID, Scope: string(evidence.Scope),
				Operation: evidence.Operation.Hex(), FromAddress: evidence.From.Hex(), Nonce: evidence.Nonce,
				TxHash: evidence.TxHash.Hex(), Raw: evidence.Raw, RecordedBlock: evidence.RecordedBlock,
				LeaseEpoch: fence.Epoch, CreatedAt: now,
			}
			if err := tx.Create(&row).Error; err != nil {
				if isDuplicateKey(err) {
					return fmt.Errorf("%w: %v", errEvmStateConflict, err)
				}
				return err
			}
			pending.SignedTxID = row.ID
		}
		row := sqldb.SignerEvmStateTable{
			ChainID: pending.ChainID, SignedTxID: pending.SignedTxID, Scope: string(pending.Scope),
			FromAddress: pending.From.Hex(), Nonce: pending.Nonce, TxHash: pending.TxHash.Hex(),
			Operation: pending.Operation.Hex(), State: "pending", LeaseEpoch: fence.Epoch, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&row).Error; err != nil {
			if isDuplicateKey(err) {
				return fmt.Errorf("%w: scope %s already has a pending transaction", errEvmStateConflict, pending.Scope)
			}
			return err
		}
		pending.ID = row.ID
		pending.LeaseEpoch = fence.Epoch
		return insertAudit(tx, pending.ChainID, fence.Epoch, pending.audit(evmAuditRecorded))
	})
	if err != nil {
		return pendingRecord{}, err
	}
	return pending, nil
}

// lockPending locks the scope's pending row and requires it to be pending.
func lockPending(tx *gorm.DB, pending pendingRecord) error {
	var rows []sqldb.SignerEvmStateTable
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("state = 'pending' AND chain_id = ? AND scope = ?", pending.ChainID, string(pending.Scope)).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return errEvmRowGone
	}
	if rows[0].ID != pending.ID || rows[0].TxHash != pending.TxHash.Hex() {
		return fmt.Errorf("%w: scope %s pending is %s (id %d), expected %s (id %d)", errEvmStateConflict,
			pending.Scope, rows[0].TxHash, rows[0].ID, pending.TxHash.Hex(), pending.ID)
	}
	return nil
}

func (s *sqlEvmStateStore) MarkAmbiguous(ctx context.Context, fence evmFence, pending pendingRecord) error {
	return s.txn(ctx, func(tx *gorm.DB) error {
		if err := fenceCheck(tx, fence); err != nil {
			return err
		}
		if err := lockPending(tx, pending); err != nil {
			return err
		}
		if err := tx.Model(&sqldb.SignerEvmStateTable{}).Where("id = ? AND tx_hash = ?", pending.ID, pending.TxHash.Hex()).
			Updates(map[string]interface{}{"ambiguous": true, "updated_at": time.Now().Unix()}).Error; err != nil {
			return err
		}
		return insertAudit(tx, pending.ChainID, fence.Epoch, pending.audit(evmAuditMarkedAmbiguous))
	})
}

func (s *sqlEvmStateStore) ResolvePending(ctx context.Context, fence evmFence, pending pendingRecord, terminal pendingTerminal) (*outcomeRecord, error) {
	var outcome *outcomeRecord
	err := s.txn(ctx, func(tx *gorm.DB) error {
		if err := fenceCheck(tx, fence); err != nil {
			return err
		}
		if err := lockPending(tx, pending); err != nil {
			return err
		}
		query := tx.Model(&sqldb.SignerEvmStateTable{}).Where("id = ? AND tx_hash = ?", pending.ID, pending.TxHash.Hex())
		var res *gorm.DB
		if terminal.Kind == terminalSuccessKeep {
			res = query.Updates(map[string]interface{}{
				"state": string(evmOutcomeSucceeded), "result_tx_hash": pending.TxHash.Hex(),
				"receipt_block": terminal.ReceiptBlock, "source": terminal.Source, "updated_at": time.Now().Unix(),
			})
			record := outcomeFromRow(sqldb.SignerEvmStateTable{
				ID: pending.ID, SignedTxID: pending.SignedTxID, ChainID: pending.ChainID, Scope: string(pending.Scope),
				Operation: pending.Operation.Hex(), FromAddress: pending.From.Hex(), Nonce: pending.Nonce,
				TxHash: pending.TxHash.Hex(), ResultTxHash: pending.TxHash.Hex(), State: string(evmOutcomeSucceeded),
				ReceiptBlock: terminal.ReceiptBlock, Source: terminal.Source,
			})
			outcome = &record
		} else {
			res = query.Delete(&sqldb.SignerEvmStateTable{})
		}
		if res.Error != nil {
			if isDuplicateKey(res.Error) {
				return fmt.Errorf("%w: operation %s already has an active outcome", errEvmStateConflict, pending.Operation.Hex())
			}
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("%w: resolving pending %d affected %d rows", errEvmStateConflict, pending.ID, res.RowsAffected)
		}
		for _, name := range terminal.Kind.auditEvents() {
			event := pending.audit(name)
			event.ReceiptStatus = terminal.ReceiptStatus
			if terminal.ReceiptBlock != 0 {
				event.ReceiptBlock = uint64Ptr(terminal.ReceiptBlock)
			}
			event.Detail = terminal.Detail
			if outcome != nil && name == evmAuditOutcomeCreated {
				event.OutcomeID = uint64Ptr(outcome.ID)
				event.OutcomeState = outcome.State
			}
			if err := insertAudit(tx, pending.ChainID, fence.Epoch, event); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return outcome, nil
}

func (s *sqlEvmStateStore) ConsumeOutcome(ctx context.Context, fence evmFence, outcome outcomeRecord) error {
	return s.txn(ctx, func(tx *gorm.DB) error {
		if err := fenceCheck(tx, fence); err != nil {
			return err
		}
		var rows []sqldb.SignerEvmStateTable
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", outcome.ID).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return errEvmRowGone
		}
		if rows[0].State != string(evmOutcomeSucceeded) || rows[0].ChainID != outcome.ChainID ||
			rows[0].Scope != string(outcome.Scope) || rows[0].Operation != outcome.Operation.Hex() {
			return fmt.Errorf("%w: outcome %d is %s for %s", errEvmStateConflict, outcome.ID, rows[0].State, rows[0].Operation)
		}
		res := tx.Where("id = ? AND state = ?", outcome.ID, string(evmOutcomeSucceeded)).Delete(&sqldb.SignerEvmStateTable{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("%w: consuming outcome %d affected %d rows", errEvmStateConflict, outcome.ID, res.RowsAffected)
		}
		return insertAudit(tx, outcome.ChainID, fence.Epoch, outcome.audit(evmAuditOutcomeConsumed))
	})
}

func (s *sqlEvmStateStore) AppendAudit(ctx context.Context, fence *evmFence, chainID string, event evmAuditEvent) error {
	return s.txn(ctx, func(tx *gorm.DB) error {
		epoch := uint64(0)
		if fence != nil {
			if err := fenceCheck(tx, *fence); err != nil {
				return err
			}
			epoch = fence.Epoch
		}
		return insertAudit(tx, chainID, epoch, event)
	})
}
