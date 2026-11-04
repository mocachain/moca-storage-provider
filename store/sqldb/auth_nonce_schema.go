package sqldb

// AuthNonceClaimTable records a normalized account/nonce pair until its expiry.
type AuthNonceClaimTable struct {
	NonceKey  string `gorm:"primaryKey;size:128"`
	ExpiresAt int64  `gorm:"not null;index"`
}

// TableName returns the shared nonce claim table name.
func (AuthNonceClaimTable) TableName() string {
	return AuthNonceClaimTableName
}
