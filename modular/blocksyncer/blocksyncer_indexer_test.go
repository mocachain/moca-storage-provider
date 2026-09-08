package blocksyncer

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	junodatabase "github.com/forbole/juno/v4/database"
	junomysql "github.com/forbole/juno/v4/database/mysql"
	"github.com/forbole/juno/v4/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	blocksyncerdb "github.com/mocachain/moca-storage-provider/modular/blocksyncer/database"
)

func TestProcessedTreatsCurrentEpochHeightAsProcessed(t *testing.T) {
	previousBlockMap, previousEventMap := blockMap, eventMap
	previousTxMap, previousTxHashMap := txMap, txHashMap
	blockMap, eventMap = new(sync.Map), new(sync.Map)
	txMap, txHashMap = new(sync.Map), new(sync.Map)
	t.Cleanup(func() {
		blockMap, eventMap = previousBlockMap, previousEventMap
		txMap, txHashMap = previousTxMap, previousTxHashMap
	})

	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	gormDB, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      sqlDB,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{})
	require.NoError(t, err)

	mock.ExpectQuery("SELECT * FROM `epoch`").WillReturnRows(
		sqlmock.NewRows([]string{"one_row_id", "block_height", "block_hash", "update_time"}).
			AddRow(true, 42, make([]byte, 32), 0),
	)
	indexer := &Impl{
		DB: &blocksyncerdb.DB{Database: &junomysql.Database{Impl: junodatabase.Impl{Db: gormDB}}},
	}

	processed, err := indexer.Processed(context.Background(), 42)
	require.NoError(t, err)
	require.True(t, processed)
	require.NoError(t, mock.ExpectationsWereMet())
}

type epochReaderStub struct {
	epoch *models.Epoch
	err   error
}

func (s epochReaderStub) GetEpoch(context.Context) (*models.Epoch, error) {
	return s.epoch, s.err
}

func TestLastBlockRecordHeight(t *testing.T) {
	t.Run("returns the stored height when the epoch query succeeds", func(t *testing.T) {
		height, err := lastBlockRecordHeight(context.Background(), epochReaderStub{
			epoch: &models.Epoch{BlockHeight: 42},
		})

		require.NoError(t, err)
		require.Equal(t, uint64(42), height)
	})

	t.Run("returns zero and preserves the epoch query error", func(t *testing.T) {
		expectedErr := errors.New("database unavailable")
		height, err := lastBlockRecordHeight(context.Background(), epochReaderStub{err: expectedErr})

		require.ErrorIs(t, err, expectedErr)
		require.Zero(t, height)
	})
}

func TestFlattenSQLIncludesEveryStatementInOneBatch(t *testing.T) {
	sql, values := flattenSQL([]map[string][]interface{}{
		{"UPDATE buckets SET storage_size = storage_size + ?": {uint64(1)}},
		{"UPDATE objects SET sealed = ?": {true}},
	})

	assert.Contains(t, sql, "UPDATE buckets")
	assert.Contains(t, sql, "UPDATE objects")
	assert.Len(t, values, 2)
}

func TestChunkSQLUsesCommitNumber(t *testing.T) {
	statements := []map[string][]interface{}{
		{"UPDATE buckets SET storage_size = storage_size + ?": {uint64(1)}},
		{"UPDATE objects SET sealed = ?": {true}},
		{"UPDATE epochs SET block_height = ?": {uint64(2)}},
	}

	batches := chunkSQL(statements, 2)

	assert.Len(t, batches, 2)
	assert.Len(t, batches[0], 2)
	assert.Len(t, batches[1], 1)
}
