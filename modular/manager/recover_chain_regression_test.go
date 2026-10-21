package manager

import (
	"context"
	"testing"

	sdkmath "cosmossdk.io/math"
	"github.com/mocachain/moca-common/go/hash"
	"github.com/mocachain/moca-storage-provider/base/gfsptqueue"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	"github.com/mocachain/moca-storage-provider/core/spdb"
	coretask "github.com/mocachain/moca-storage-provider/core/task"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestVerifyIntegrityRejectsMatchingIndexWhenChainDiffers(t *testing.T) {
	m := setup(t)
	ctrl := gomock.NewController(t)
	checksums := [][]byte{hash.GenerateChecksum([]byte("indexed data"))}
	indexed := &storagetypes.ObjectInfo{
		Id: sdkmath.NewUint(701), PayloadSize: 12,
		Checksums: [][]byte{hash.GenerateIntegrityHash(checksums)},
	}
	db := spdb.NewMockSPDB(ctrl)
	db.EXPECT().GetObjectIntegrity(uint64(701), int32(-1)).Return(&spdb.IntegrityMeta{
		PieceChecksumList: checksums,
	}, nil).AnyTimes()
	m.baseApp.SetGfSpDB(db)
	chain := consensus.NewMockConsensus(ctrl)
	chain.EXPECT().QueryObjectInfoByID(gomock.Any(), "701").Return(&storagetypes.ObjectInfo{
		Id: sdkmath.NewUint(701), PayloadSize: 12,
		Checksums: [][]byte{[]byte("different sealed checksum")},
	}, nil).AnyTimes()
	m.baseApp.SetConsensus(chain)

	verified, _ := verifyIntegrity(m, indexed, -1)
	require.False(t, verified, "matching index and SPDB rows must not override the chain checksum")
}

func TestRecoverPieceReportFromOldVersionPreservesCurrentRecovery(t *testing.T) {
	m := setup(t)
	m.recoveryQueue = gfsptqueue.NewGfSpTQueueWithLimit("version-regression", 10)
	m.recoveryTaskMap = make(map[string]string)
	m.recoverObjectStats = NewObjectsSegmentsStats()
	params := &storagetypes.Params{VersionedParams: storagetypes.VersionedParams{MaxSegmentSize: 4}}
	makeTask := func(version int64) *gfsptask.GfSpRecoverPieceTask {
		task := &gfsptask.GfSpRecoverPieceTask{}
		task.InitRecoverPieceTask(&storagetypes.ObjectInfo{
			Id: sdkmath.NewUint(702), BucketName: "bucket", ObjectName: "object",
			PayloadSize: 4, Version: version,
		}, params, coretask.DefaultSmallerPriority, 0, -1, 4, MaxRecoveryTime, maxRecoveryRetry)
		task.SetBySuccessorSP(true)
		return task
	}
	current := makeTask(2)
	m.recoverObjectStats.put(702, 2, 1)
	require.NoError(t, m.HandleRecoverPieceTask(context.Background(), current))

	late := makeTask(1)
	late.SetRecoverDone()
	// A delayed report can arrive after an object update or manager restart.
	// It must not remove a queued task belonging to the current chain version.
	_ = m.HandleRecoverPieceTask(context.Background(), late)
	require.True(t, m.recoveryQueue.Has(current.Key()), "a version-1 report removed version-2 recovery work")
}
