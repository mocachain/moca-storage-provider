package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	coretask "github.com/mocachain/moca-storage-provider/core/task"
)

func (e *ExecuteModular) validateRecoveryDispatch(ctx context.Context, t coretask.RecoveryPieceTask) error {
	if _, err := gfsptask.RecoveryStageKey(t); err != nil {
		return err
	}
	count, err := gfsptask.RecoverySegmentCount(t.GetObjectInfo(), t.GetStorageParams(), t.GetEcIdx())
	if err != nil {
		return err
	}
	if t.GetSegmentIdx() >= count {
		return fmt.Errorf("recovery segment out of bounds")
	}
	child, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	current, err := e.baseApp.Consensus().QueryObjectInfoByID(child, t.GetObjectInfo().Id.String())
	if err != nil {
		return err
	}
	if !gfsptask.RecoverySnapshotEqual(t.GetObjectInfo(), current) {
		return fmt.Errorf("recovery object changed")
	}
	params, err := e.baseApp.Consensus().QueryStorageParamsByTimestamp(child, current.GetLatestUpdatedTime())
	if err != nil {
		return err
	}
	if !proto.Equal(params, t.GetStorageParams()) {
		return fmt.Errorf("recovery historical parameters differ")
	}
	return child.Err()
}
