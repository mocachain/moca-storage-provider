package sqldb

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/mocachain/moca-storage-provider/store/config"
)

// signerEvmMaxOpenConns bounds the signer's pool: EVM submissions are rare and
// serialized per scope, so a handful of connections is plenty.
const signerEvmMaxOpenConns = 8

// signerEvmTableDDL creates the four signer EVM tables. Order matters: the
// evidence table must exist before the tables that reference it.
var signerEvmTableDDL = []string{
	`CREATE TABLE IF NOT EXISTS ` + SignerEvmSignedTxTableName + ` (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  chain_id VARCHAR(64) NOT NULL,
  evm_chain_id BIGINT UNSIGNED NOT NULL,
  scope VARCHAR(16) NOT NULL,
  operation CHAR(66) NOT NULL,
  from_address CHAR(42) NOT NULL,
  nonce BIGINT UNSIGNED NOT NULL,
  tx_hash CHAR(66) NOT NULL,
  raw MEDIUMBLOB NOT NULL,
  recorded_block BIGINT UNSIGNED NOT NULL,
  lease_epoch BIGINT UNSIGNED NOT NULL,
  created_at BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uniq_hash (chain_id, tx_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS ` + SignerEvmStateTableName + ` (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  signed_tx_id BIGINT UNSIGNED NOT NULL,
  chain_id VARCHAR(64) NOT NULL,
  scope VARCHAR(16) NOT NULL,
  operation CHAR(66) NOT NULL,
  from_address CHAR(42) NOT NULL,
  nonce BIGINT UNSIGNED NOT NULL,
  tx_hash CHAR(66) NOT NULL,
  result_tx_hash VARCHAR(66) NOT NULL DEFAULT '',
  state VARCHAR(32) NOT NULL DEFAULT 'pending',
  evidence_tx_hash VARCHAR(128) NOT NULL DEFAULT '',
  evidence_kind VARCHAR(16) NOT NULL DEFAULT '',
  reason VARCHAR(32) NOT NULL DEFAULT '',
  receipt_block BIGINT UNSIGNED NOT NULL DEFAULT 0,
  source VARCHAR(16) NOT NULL DEFAULT '',
  ambiguous TINYINT(1) NOT NULL DEFAULT 0,
  lease_epoch BIGINT UNSIGNED NOT NULL DEFAULT 0,
  updated_at BIGINT NOT NULL DEFAULT 0,
  pending_scope VARCHAR(16) GENERATED ALWAYS AS (CASE WHEN state = 'pending' THEN scope ELSE NULL END) STORED,
  outcome_operation CHAR(66) GENERATED ALWAYS AS (CASE WHEN state <> 'pending' THEN operation ELSE NULL END) STORED,
  created_at BIGINT NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uniq_pending (chain_id, pending_scope),
  UNIQUE KEY uniq_outcome (chain_id, scope, outcome_operation),
  KEY idx_signed_tx (signed_tx_id),
  CONSTRAINT fk_signer_evm_state_signed_tx FOREIGN KEY (signed_tx_id)
    REFERENCES ` + SignerEvmSignedTxTableName + ` (id) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS ` + SignerEvmLeaseTableName + ` (
  chain_id VARCHAR(64) NOT NULL,
  owner VARCHAR(255) NOT NULL DEFAULT '',
  epoch BIGINT UNSIGNED NOT NULL DEFAULT 0,
  expires_at DATETIME(6) NOT NULL,
  PRIMARY KEY (chain_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS ` + SignerEvmAuditTableName + ` (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  chain_id VARCHAR(64) NOT NULL,
  scope VARCHAR(16) NOT NULL DEFAULT '',
  from_address VARCHAR(42) NOT NULL DEFAULT '',
  nonce BIGINT UNSIGNED NULL,
  tx_hash VARCHAR(66) NOT NULL DEFAULT '',
  operation VARCHAR(66) NOT NULL DEFAULT '',
  signed_tx_id BIGINT UNSIGNED NULL,
  event VARCHAR(64) NOT NULL,
  receipt_status BIGINT UNSIGNED NULL,
  receipt_block BIGINT UNSIGNED NULL,
  chain_nonce BIGINT UNSIGNED NULL,
  chain_height BIGINT UNSIGNED NULL,
  outcome_id BIGINT UNSIGNED NULL,
  outcome_state VARCHAR(32) NOT NULL DEFAULT '',
  outcome_reason VARCHAR(32) NOT NULL DEFAULT '',
  decision_ref VARCHAR(512) NOT NULL DEFAULT '',
  actor VARCHAR(255) NOT NULL DEFAULT '',
  reason TEXT NULL,
  detail TEXT NULL,
  evidence_tx_hash VARCHAR(128) NOT NULL DEFAULT '',
  evidence_kind VARCHAR(16) NOT NULL DEFAULT '',
  tool_version VARCHAR(128) NOT NULL DEFAULT '',
  lease_epoch BIGINT UNSIGNED NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL,
  PRIMARY KEY (id),
  KEY idx_tx_hash (chain_id, tx_hash),
  KEY idx_outcome (chain_id, outcome_id),
  KEY idx_signed_tx (signed_tx_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
}

// signerEvmRequiredColumns lists, per table, the columns the signer reads or
// writes. The startup check refuses to run against a table missing any.
var signerEvmRequiredColumns = map[string][]string{
	SignerEvmSignedTxTableName: {"id", "chain_id", "evm_chain_id", "scope", "operation", "from_address", "nonce",
		"tx_hash", "raw", "recorded_block", "lease_epoch", "created_at"},
	SignerEvmStateTableName: {"id", "signed_tx_id", "chain_id", "scope", "operation", "from_address", "nonce",
		"tx_hash", "result_tx_hash", "state", "evidence_tx_hash", "evidence_kind", "reason", "receipt_block",
		"source", "created_at", "updated_at", "ambiguous", "lease_epoch", "pending_scope", "outcome_operation"},
	SignerEvmLeaseTableName: {"chain_id", "owner", "epoch", "expires_at"},
	SignerEvmAuditTableName: {"id", "chain_id", "scope", "from_address", "nonce", "tx_hash", "operation",
		"signed_tx_id", "event", "receipt_status", "receipt_block", "chain_nonce", "chain_height", "outcome_id",
		"outcome_state", "outcome_reason", "decision_ref", "actor", "reason", "detail", "evidence_tx_hash",
		"evidence_kind", "tool_version", "lease_epoch", "created_at"},
}

// signerEvmRequiredUniqueKeys lists the unique keys whose columns carry the
// one-active-row invariants.
var signerEvmRequiredUniqueKeys = map[string][]string{
	SignerEvmSignedTxTableName: {"chain_id,tx_hash"},
	SignerEvmStateTableName:    {"chain_id,pending_scope", "chain_id,scope,outcome_operation"},
}

// SignerEvmTableNames returns the four signer EVM tables.
func SignerEvmTableNames() []string {
	return []string{SignerEvmSignedTxTableName, SignerEvmStateTableName,
		SignerEvmLeaseTableName, SignerEvmAuditTableName}
}

// SignerEvmStore is the signer's own connection to the SP DB. Unlike NewSpDB
// it neither migrates nor touches any other table.
type SignerEvmStore struct {
	db *gorm.DB
}

// NewSignerEvmStore opens the SP DB for the signer EVM tables only. Env vars
// override the configured credentials, as for NewSpDB.
func NewSignerEvmStore(cfg *config.SQLDBConfig) (*SignerEvmStore, error) {
	LoadDBConfigFromEnv(cfg)
	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?charset=utf8mb4&parseTime=True&loc=UTC",
		cfg.User, cfg.Passwd, cfg.Address, cfg.Database)
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("failed to open signer EVM state DB: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 || maxOpen > signerEvmMaxOpenConns {
		maxOpen = signerEvmMaxOpenConns
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxOpen)
	sqlDB.SetConnMaxLifetime(time.Hour)
	return &SignerEvmStore{db: db}, nil
}

// DB returns the underlying connection.
func (s *SignerEvmStore) DB() *gorm.DB {
	return s.db
}

// Close closes the connection pool.
func (s *SignerEvmStore) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Migrate creates the four tables if they do not exist. It needs the
// migration account (CREATE and REFERENCES); the signer never calls it.
func (s *SignerEvmStore) Migrate(ctx context.Context) error {
	return MigrateSignerEvmTables(s.db.WithContext(ctx))
}

// MigrateSignerEvmTables creates the four signer EVM tables on db.
func MigrateSignerEvmTables(db *gorm.DB) error {
	if err := rejectLegacySignerEvmTables(db); err != nil {
		return err
	}
	for _, ddl := range signerEvmTableDDL {
		if err := db.Exec(ddl).Error; err != nil {
			return fmt.Errorf("failed to create signer EVM table: %w", err)
		}
	}
	return nil
}

// CheckSchema verifies that the four tables exist on InnoDB with every
// required column, the conditional unique keys, and the evidence foreign key.
func (s *SignerEvmStore) CheckSchema(ctx context.Context) error {
	db := s.db.WithContext(ctx)
	if err := rejectLegacySignerEvmTables(db); err != nil {
		return err
	}
	var problems []string
	for _, table := range SignerEvmTableNames() {
		var engine string
		if err := db.Raw(`SELECT ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`,
			table).Scan(&engine).Error; err != nil {
			return fmt.Errorf("failed to inspect table %s: %w", table, err)
		}
		if engine == "" {
			problems = append(problems, fmt.Sprintf("table %s is missing", table))
			continue
		}
		if !strings.EqualFold(engine, "InnoDB") {
			problems = append(problems, fmt.Sprintf("table %s uses engine %s, want InnoDB", table, engine))
		}
		var columns []string
		if err := db.Raw(`SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`,
			table).Scan(&columns).Error; err != nil {
			return fmt.Errorf("failed to inspect columns of %s: %w", table, err)
		}
		have := make(map[string]bool, len(columns))
		for _, column := range columns {
			have[strings.ToLower(column)] = true
		}
		for _, column := range signerEvmRequiredColumns[table] {
			if !have[column] {
				problems = append(problems, fmt.Sprintf("table %s lacks column %s", table, column))
			}
		}
		for _, want := range signerEvmRequiredUniqueKeys[table] {
			found, err := hasUniqueKey(db, table, want)
			if err != nil {
				return err
			}
			if !found {
				problems = append(problems, fmt.Sprintf("table %s lacks unique key (%s)", table, want))
			}
		}
	}
	for column, expression := range map[string]string{
		"pending_scope":     "casewhenstate='pending'thenscopeelsenullend",
		"outcome_operation": "casewhenstate<>'pending'thenoperationelsenullend",
	} {
		var rows []struct {
			Extra                string
			GenerationExpression string
		}
		if err := db.Raw(`SELECT EXTRA AS extra, GENERATION_EXPRESSION AS generation_expression FROM information_schema.COLUMNS
            WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`, SignerEvmStateTableName, column).Scan(&rows).Error; err != nil {
			return err
		}
		normalize := strings.NewReplacer("\\", "", "`", "", " ", "", "(", "", ")", "", "_utf8mb4", "", "_utf8mb3", "")
		if len(rows) != 1 || rows[0].Extra != "STORED GENERATED" || normalize.Replace(strings.ToLower(rows[0].GenerationExpression)) != expression {
			problems = append(problems, fmt.Sprintf("table %s lacks generated constraint column %s (found %+v)", SignerEvmStateTableName, column, rows))
		}
	}
	for _, table := range []string{SignerEvmStateTableName} {
		var count int64
		if err := db.Raw(`SELECT COUNT(*) FROM information_schema.REFERENTIAL_CONSTRAINTS rc
  JOIN information_schema.KEY_COLUMN_USAGE k
    ON k.CONSTRAINT_SCHEMA = rc.CONSTRAINT_SCHEMA AND k.CONSTRAINT_NAME = rc.CONSTRAINT_NAME AND k.TABLE_NAME = rc.TABLE_NAME
 WHERE rc.CONSTRAINT_SCHEMA = DATABASE() AND rc.TABLE_NAME = ? AND rc.REFERENCED_TABLE_NAME = ?
   AND rc.DELETE_RULE IN ('RESTRICT', 'NO ACTION') AND k.COLUMN_NAME = 'signed_tx_id' AND k.REFERENCED_COLUMN_NAME = 'id'`,
			table, SignerEvmSignedTxTableName).Scan(&count).Error; err != nil {
			return fmt.Errorf("failed to inspect foreign keys of %s: %w", table, err)
		}
		if count == 0 {
			problems = append(problems, fmt.Sprintf("table %s lacks the RESTRICT foreign key signed_tx_id -> %s.id",
				table, SignerEvmSignedTxTableName))
		}
	}
	if len(problems) != 0 {
		sort.Strings(problems)
		return fmt.Errorf("signer EVM tables are not ready, run 'signer-evm migrate' with the migration account: %s",
			strings.Join(problems, "; "))
	}
	return nil
}

func hasUniqueKey(db *gorm.DB, table, columns string) (bool, error) {
	var keys []string
	if err := db.Raw(`SELECT GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX) FROM information_schema.STATISTICS
 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND NON_UNIQUE = 0 GROUP BY INDEX_NAME`, table).Scan(&keys).Error; err != nil {
		return false, fmt.Errorf("failed to inspect indexes of %s: %w", table, err)
	}
	for _, key := range keys {
		if strings.EqualFold(key, columns) {
			return true, nil
		}
	}
	return false, nil
}

// Never hide active submissions from the earlier draft schema behind empty tables.
func rejectLegacySignerEvmTables(db *gorm.DB) error {
	var count int64
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN ('signer_evm_pending', 'signer_evm_outcome')`).Scan(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("legacy signer EVM pending/outcome tables exist; stop the signer and reconcile their state before migrating (see signer EVM upgrade runbook)")
	}
	return nil
}
