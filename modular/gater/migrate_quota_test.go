package gater

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasSufficientMigrationQuotaDoesNotUnderflow(t *testing.T) {
	assert.False(t, hasSufficientMigrationQuota(1, 1, 3, 0, 1))
}

func TestHasSufficientMigrationQuotaSaturatesAddition(t *testing.T) {
	assert.True(t, hasSufficientMigrationQuota(math.MaxUint64, 1, 0, 0, math.MaxUint64-1))
}
