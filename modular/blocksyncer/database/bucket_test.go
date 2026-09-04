package database

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	junodatabase "github.com/forbole/juno/v4/database"
	junomysql "github.com/forbole/juno/v4/database/mysql"
	"github.com/stretchr/testify/assert"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestUpdateBucketOffChainStatusBit(t *testing.T) {
	sqlDB, _, err := sqlmock.New()
	assert.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	gormDB, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, SkipDefaultTransaction: true})
	assert.NoError(t, err)
	db := &DB{Database: &junomysql.Database{Impl: junodatabase.Impl{Db: gormDB}}}
	status := 4

	t.Run("enable", func(t *testing.T) {
		sql, values := db.UpdateBucketOffChainStatusBit(context.Background(), "bucket", status, true, 1, [32]byte{}, 2)

		assert.Contains(t, sql, "off_chain_status | ?")
		assert.Contains(t, values, status)
	})

	t.Run("disable", func(t *testing.T) {
		sql, values := db.UpdateBucketOffChainStatusBit(context.Background(), "bucket", status, false, 1, [32]byte{}, 2)

		assert.Contains(t, sql, "off_chain_status & ?")
		assert.Contains(t, values, ^status)
	})
}
