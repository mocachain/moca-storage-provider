package blocksyncer

import (
	"context"
	"errors"
	"testing"

	"github.com/forbole/juno/v4/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
