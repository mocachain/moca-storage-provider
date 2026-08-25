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
