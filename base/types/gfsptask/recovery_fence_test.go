package gfsptask

import (
	"bytes"
	"testing"

	sdkmath "cosmossdk.io/math"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
	"github.com/stretchr/testify/require"
)

func TestRecoveryFenceRoundTripPreservesSourceSignature(t *testing.T) {
	task := &GfSpRecoverPieceTask{Task: &GfSpTask{CreateTime: 1, UpdateTime: 2}, ObjectInfo: &storagetypes.ObjectInfo{Id: sdkmath.NewUint(7)}, StorageParams: &storagetypes.Params{}}
	// Captured from the pre-fence source request serializer at d5c805bf.
	const legacy = `{"object_info":{"id":"7"},"storage_params":{"versioned_params":{}},"task":{"create_time":"1","update_time":"2"}}`
	task.SetRecoveryFence(bytes.Repeat([]byte{0xab}, 32))
	wire, err := task.Marshal()
	require.NoError(t, err)
	var report GfSpRecoverPieceTask
	require.NoError(t, report.Unmarshal(wire))
	require.Equal(t, task.GetRecoveryFence(), report.GetRecoveryFence())
	require.Equal(t, legacy, string(report.GetSignBytes()))
}
