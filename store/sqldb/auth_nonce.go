package sqldb

import (
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ClaimAuthNonce atomically claims a normalized account/nonce pair until expiry.
func (s *SpDBImpl) ClaimAuthNonce(account, nonce string, expiresAt time.Time) (claimed bool, err error) {
	key := strings.ToLower(strings.TrimSpace(account)) + "|" + strings.ToLower(strings.TrimSpace(nonce))
	result := s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "nonce_key"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"expires_at": gorm.Expr("IF(expires_at < ?, VALUES(expires_at), expires_at)", time.Now().Unix()),
		}),
	}).Create(&AuthNonceClaimTable{
		NonceKey:  key,
		ExpiresAt: expiresAt.Unix(),
	})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// DeleteExpiredAuthNonces removes nonce claims that can no longer be replayed.
func (s *SpDBImpl) DeleteExpiredAuthNonces(expiredBefore time.Time) error {
	return s.db.Where("expires_at < ?", expiredBefore.Unix()).Delete(&AuthNonceClaimTable{}).Error
}
