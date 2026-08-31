package blocksyncer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
