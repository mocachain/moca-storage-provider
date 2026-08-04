package bsdb

import (
	"testing"

	permtypes "github.com/mocachain/moca/v2/x/permission/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPossibleValuesForAction pins that the bitmap values are selected by the bit the
// action occupies in ActionTypeMap, not by the action's enum value. The two numbers
// coincide for the fourteen ordinary actions but not for ACTION_TYPE_ALL, which is
// enum 99 in bit 0.
func TestPossibleValuesForAction(t *testing.T) {
	maxVal := 0
	for _, bit := range ActionTypeMap {
		maxVal |= 1 << bit
	}

	tests := []struct {
		name   string
		action permtypes.ActionType
	}{
		{"ACTION_TYPE_ALL", permtypes.ACTION_TYPE_ALL},
		{"ACTION_GET_OBJECT", permtypes.ACTION_GET_OBJECT},
		{"ACTION_UPDATE_OBJECT_CONTENT", permtypes.ACTION_UPDATE_OBJECT_CONTENT},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bit, ok := ActionTypeMap[tt.action]
			require.True(t, ok, "action is not in ActionTypeMap")

			got := PossibleValuesForAction(tt.action)
			require.NotEmpty(t, got)

			// exactly the bitmaps that have this action's bit set, and no others
			want := 0
			for i := 0; i <= maxVal; i++ {
				if i&(1<<bit) == 1<<bit {
					want++
				}
			}
			assert.Len(t, got, want)

			missingBit := 0
			firstBad := -1
			for _, v := range got {
				if v&(1<<bit) != 1<<bit {
					missingBit++
					if firstBad < 0 {
						firstBad = v
					}
				}
			}
			assert.Zero(t, missingBit, "%d of %d values do not carry bit %d, e.g. %d",
				missingBit, len(got), bit, firstBad)
		})
	}
}

// TestPossibleValuesForActionUnknownAction pins that an action with no bit assigned
// yields no values, so the caller returns nothing rather than falling back to bit 0.
func TestPossibleValuesForActionUnknownAction(t *testing.T) {
	_, ok := ActionTypeMap[permtypes.ACTION_UNSPECIFIED]
	require.False(t, ok, "ACTION_UNSPECIFIED is expected to have no bit")

	assert.Empty(t, PossibleValuesForAction(permtypes.ACTION_UNSPECIFIED))
}
