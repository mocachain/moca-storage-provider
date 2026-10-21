package sqldb

import "time"

// The signer EVM tables hold the state that keeps an EVM operation from being
// signed twice across restarts. They are created by explicit DDL (see
// signer_evm.go) rather than AutoMigrate, because the foreign keys and
// privileges matter: signer_evm_signed_tx and signer_evm_audit are append-only.
const (
	SignerEvmSignedTxTableName = "signer_evm_signed_tx"
	SignerEvmStateTableName    = "signer_evm_state"
	SignerEvmLeaseTableName    = "signer_evm_lease"
	SignerEvmAuditTableName    = "signer_evm_audit"
)

// SignerEvmSignedTxTable is the append-only evidence of every EVM transaction
// the signer signed for broadcast. It is the only copy of the raw bytes.
type SignerEvmSignedTxTable struct {
	ID            uint64 `gorm:"column:id;primaryKey;autoIncrement"`
	ChainID       string `gorm:"column:chain_id"`
	EvmChainID    uint64 `gorm:"column:evm_chain_id"`
	Scope         string `gorm:"column:scope"`
	Operation     string `gorm:"column:operation"`
	FromAddress   string `gorm:"column:from_address"`
	Nonce         uint64 `gorm:"column:nonce"`
	TxHash        string `gorm:"column:tx_hash"`
	Raw           []byte `gorm:"column:raw"`
	RecordedBlock uint64 `gorm:"column:recorded_block"`
	LeaseEpoch    uint64 `gorm:"column:lease_epoch"`
	CreatedAt     int64  `gorm:"column:created_at;autoCreateTime:false"`
}

// TableName is used to set SignerEvmSignedTxTable Schema's table name in database
func (SignerEvmSignedTxTable) TableName() string {
	return SignerEvmSignedTxTableName
}

// SignerEvmStateTable holds pending submissions and retained outcomes.
// Conditional unique keys enforce one pending per scope and one outcome per operation.
type SignerEvmStateTable struct {
	ID             uint64 `gorm:"column:id;primaryKey;autoIncrement"`
	SignedTxID     uint64 `gorm:"column:signed_tx_id"`
	ChainID        string `gorm:"column:chain_id"`
	Scope          string `gorm:"column:scope"`
	Operation      string `gorm:"column:operation"`
	FromAddress    string `gorm:"column:from_address"`
	Nonce          uint64 `gorm:"column:nonce"`
	TxHash         string `gorm:"column:tx_hash"`
	ResultTxHash   string `gorm:"column:result_tx_hash"`
	State          string `gorm:"column:state"`
	EvidenceTxHash string `gorm:"column:evidence_tx_hash"`
	EvidenceKind   string `gorm:"column:evidence_kind"`
	Reason         string `gorm:"column:reason"`
	ReceiptBlock   uint64 `gorm:"column:receipt_block"`
	Source         string `gorm:"column:source"`
	Ambiguous      bool   `gorm:"column:ambiguous"`
	LeaseEpoch     uint64 `gorm:"column:lease_epoch"`
	UpdatedAt      int64  `gorm:"column:updated_at;autoUpdateTime:false"`
	CreatedAt      int64  `gorm:"column:created_at;autoCreateTime:false"`
}

func (SignerEvmStateTable) TableName() string {
	return SignerEvmStateTableName
}

// SignerEvmLeaseTable has one row per chain: the signer allowed to write EVM state.
type SignerEvmLeaseTable struct {
	ChainID   string    `gorm:"column:chain_id;primaryKey"`
	Owner     string    `gorm:"column:owner"`
	Epoch     uint64    `gorm:"column:epoch"`
	ExpiresAt time.Time `gorm:"column:expires_at"`
}

// TableName is used to set SignerEvmLeaseTable Schema's table name in database
func (SignerEvmLeaseTable) TableName() string {
	return SignerEvmLeaseTableName
}

// SignerEvmAuditTable is the append-only history of every state change.
type SignerEvmAuditTable struct {
	ID             uint64  `gorm:"column:id;primaryKey;autoIncrement"`
	ChainID        string  `gorm:"column:chain_id"`
	Scope          string  `gorm:"column:scope"`
	FromAddress    string  `gorm:"column:from_address"`
	Nonce          *uint64 `gorm:"column:nonce"`
	TxHash         string  `gorm:"column:tx_hash"`
	Operation      string  `gorm:"column:operation"`
	SignedTxID     *uint64 `gorm:"column:signed_tx_id"`
	Event          string  `gorm:"column:event"`
	ReceiptStatus  *uint64 `gorm:"column:receipt_status"`
	ReceiptBlock   *uint64 `gorm:"column:receipt_block"`
	ChainNonce     *uint64 `gorm:"column:chain_nonce"`
	ChainHeight    *uint64 `gorm:"column:chain_height"`
	OutcomeID      *uint64 `gorm:"column:outcome_id"`
	OutcomeState   string  `gorm:"column:outcome_state"`
	OutcomeReason  string  `gorm:"column:outcome_reason"`
	DecisionRef    string  `gorm:"column:decision_ref"`
	Actor          string  `gorm:"column:actor"`
	Reason         string  `gorm:"column:reason"`
	Detail         string  `gorm:"column:detail"`
	EvidenceTxHash string  `gorm:"column:evidence_tx_hash"`
	EvidenceKind   string  `gorm:"column:evidence_kind"`
	ToolVersion    string  `gorm:"column:tool_version"`
	LeaseEpoch     uint64  `gorm:"column:lease_epoch"`
	CreatedAt      int64   `gorm:"column:created_at;autoCreateTime:false"`
}

// TableName is used to set SignerEvmAuditTable Schema's table name in database
func (SignerEvmAuditTable) TableName() string {
	return SignerEvmAuditTableName
}
