package gfsptask

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"

	coretask "github.com/mocachain/moca-storage-provider/core/task"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
)

const RecoveryFenceSize = 32

// RecoveryStageKey never accepts a path or store key supplied by a report.
func RecoveryStageKey(t coretask.RecoveryPieceTask) (string, error) {
	if t == nil || t.GetObjectInfo() == nil || t.GetObjectInfo().Id.IsNil() || len(t.GetRecoveryFence()) != RecoveryFenceSize {
		return "", fmt.Errorf("invalid recovery fence")
	}
	return fmt.Sprintf("rstage-%s-%s-%d-%d-%d", hex.EncodeToString(t.GetRecoveryFence()), t.GetObjectInfo().Id.String(), t.GetObjectInfo().Version, t.GetEcIdx(), t.GetSegmentIdx()), nil
}

// RecoverySnapshotEqual compares the chain fields that define the recovered data.
func RecoverySnapshotEqual(a, b *storagetypes.ObjectInfo) bool {
	if a == nil || b == nil || a.Id.IsNil() || b.Id.IsNil() || !a.Id.Equal(b.Id) || a.Version != b.Version || a.PayloadSize != b.PayloadSize || a.GetLatestUpdatedTime() != b.GetLatestUpdatedTime() || a.IsUpdating || b.IsUpdating || a.ObjectStatus != storagetypes.OBJECT_STATUS_SEALED || b.ObjectStatus != storagetypes.OBJECT_STATUS_SEALED || len(a.Checksums) != len(b.Checksums) {
		return false
	}
	for i := range a.Checksums {
		if !bytes.Equal(a.Checksums[i], b.Checksums[i]) {
			return false
		}
	}
	return true
}

// RecoverySegmentCount validates the historical parameters before narrowing counts.
func RecoverySegmentCount(object *storagetypes.ObjectInfo, params *storagetypes.Params, redundancy int32) (uint32, error) {
	if object == nil || object.Id.IsNil() || object.IsUpdating || object.ObjectStatus != storagetypes.OBJECT_STATUS_SEALED || object.PayloadSize > math.MaxInt64 || params == nil || params.GetMaxSegmentSize() == 0 || params.GetMaxSegmentSize() > math.MaxInt64-1 || params.GetRedundantDataChunkNum() == 0 || redundancy < -1 || int64(redundancy) >= int64(params.GetRedundantDataChunkNum())+int64(params.GetRedundantParityChunkNum()) || int64(redundancy)+1 >= int64(len(object.Checksums)) || len(object.Checksums[redundancy+1]) == 0 {
		return 0, fmt.Errorf("invalid recovery snapshot or storage parameters")
	}
	n := object.PayloadSize / params.GetMaxSegmentSize()
	if object.PayloadSize%params.GetMaxSegmentSize() != 0 {
		n++
	}
	if n > math.MaxUint32 {
		return 0, fmt.Errorf("recovery segment count overflow")
	}
	return uint32(n), nil
}
