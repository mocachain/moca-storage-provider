package manager

import (
	"errors"
	"testing"

	sdkmath "cosmossdk.io/math"
	"github.com/mocachain/moca-common/go/hash"
	types0 "github.com/mocachain/moca/v2/x/storage/types"
	types1 "github.com/mocachain/moca/v2/x/virtualgroup/types"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/base/gfsptqueue"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	"github.com/mocachain/moca-storage-provider/core/spdb"
	"github.com/mocachain/moca-storage-provider/core/taskqueue"
	"github.com/mocachain/moca-storage-provider/modular/metadata/types"
	"gorm.io/gorm"
)

func TestRecoverGVGSchedulerQueueRecoveryObject_DoesNotTrackFailedEnqueue(t *testing.T) {
	m := setup(t)
	ctrl := gomock.NewController(t)
	queue := taskqueue.NewMockTQueueOnStrategyWithLimit(ctrl)
	m.recoveryQueue = queue
	m.recoverObjectStats = NewObjectsSegmentsStats()
	scheduler := &RecoverGVGScheduler{
		manager:               m,
		currentBatchObjectIDs: make(map[objectVersion]struct{}),
		gvgID:                 1,
		redundancyIndex:       0,
	}
	objectInfo := &types0.ObjectInfo{Id: sdkmath.NewUint(100), PayloadSize: 1}
	storageParams := &types0.Params{VersionedParams: types0.VersionedParams{MaxSegmentSize: 10}}
	queue.EXPECT().Push(gomock.Any()).Return(errors.New("queue unavailable")).Times(1)

	queued := scheduler.queueRecoveryObject(objectInfo, storageParams, 10, 1)

	assert.False(t, queued)
	assert.False(t, m.recoverObjectStats.has(100, objectInfo.Version))
	_, exists := scheduler.currentBatchObjectIDs[objectVersion{objectID: 100, version: objectInfo.Version}]
	assert.False(t, exists)
}

func TestRecoverFailedObjectSchedulerSeedFailedObjectStats_RegistersCurrentVersion(t *testing.T) {
	m := setup(t)
	m.recoverObjectStats = NewObjectsSegmentsStats()
	scheduler := &RecoverFailedObjectScheduler{manager: m}
	objectInfo := &types0.ObjectInfo{Id: sdkmath.NewUint(300), Version: 2, PayloadSize: 1}

	scheduler.seedFailedObjectStats(objectInfo, 3)

	// Without seeding, isRecoverFailed's absent-key branch always reports true
	// regardless of what actually happened. A real entry starts as "not
	// failed" until a segment actually fails.
	assert.True(t, m.recoverObjectStats.has(300, 2))
	assert.False(t, m.recoverObjectStats.isRecoverFailed(300, 2))

	// A failure reported for this object now resolves against its own version
	// instead of being invisible to it.
	m.recoverObjectStats.addSegmentRecord(300, 2, false, 0)
	m.recoverObjectStats.addSegmentRecord(300, 2, true, 1)
	m.recoverObjectStats.addSegmentRecord(300, 2, true, 2)
	assert.True(t, m.recoverObjectStats.isRecoverFailed(300, 2))

	// A report for a different version of the same object must not resolve
	// against this entry.
	assert.False(t, m.recoverObjectStats.has(300, 3))
}

func TestRecoverFailedObjectSchedulerSeedFailedObjectStats_DoesNotResetInProgressEntry(t *testing.T) {
	m := setup(t)
	m.recoverObjectStats = NewObjectsSegmentsStats()
	scheduler := &RecoverFailedObjectScheduler{manager: m}
	objectInfo := &types0.ObjectInfo{Id: sdkmath.NewUint(301), Version: 1, PayloadSize: 1}

	scheduler.seedFailedObjectStats(objectInfo, 2)
	m.recoverObjectStats.addSegmentRecord(301, 1, true, 0)

	// A later pass over the same still-pending row (e.g. the next scheduler
	// tick, before this object's segments have all reported back) must not
	// wipe out progress already recorded for it.
	scheduler.seedFailedObjectStats(objectInfo, 2)

	assert.False(t, m.recoverObjectStats.isObjectProcessed(301, 1))
	m.recoverObjectStats.addSegmentRecord(301, 1, true, 1)
	assert.True(t, m.recoverObjectStats.isObjectProcessed(301, 1))
	assert.False(t, m.recoverObjectStats.isRecoverFailed(301, 1))
}

func TestVerifyIntegrityAcceptsMatchingChainChecksum(t *testing.T) {
	m := setup(t)
	ctrl := gomock.NewController(t)
	pieceChecksums := [][]byte{[]byte("piece-checksum")}

	db := spdb.NewMockSPDB(ctrl)
	db.EXPECT().GetObjectIntegrity(uint64(1), int32(0)).Return(&spdb.IntegrityMeta{
		PieceChecksumList: pieceChecksums,
	}, nil).Times(1)
	m.baseApp.SetGfSpDB(db)

	verified, err := verifyIntegrity(m, &types0.ObjectInfo{
		Id:        sdkmath.NewUint(1),
		Checksums: [][]byte{nil, hash.GenerateIntegrityHash(pieceChecksums)},
	}, 0)

	assert.NoError(t, err)
	assert.True(t, verified)
}

func TestVerifyIntegrityRejectsMismatchedChainChecksum(t *testing.T) {
	m := setup(t)
	ctrl := gomock.NewController(t)
	pieceChecksums := [][]byte{[]byte("piece-checksum")}

	db := spdb.NewMockSPDB(ctrl)
	db.EXPECT().GetObjectIntegrity(uint64(1), int32(0)).Return(&spdb.IntegrityMeta{
		PieceChecksumList: pieceChecksums,
	}, nil).Times(1)
	m.baseApp.SetGfSpDB(db)

	verified, err := verifyIntegrity(m, &types0.ObjectInfo{
		Id:        sdkmath.NewUint(1),
		Checksums: [][]byte{nil, hash.GenerateIntegrityHash([][]byte{[]byte("different-checksum")})},
	}, 0)

	assert.NoError(t, err)
	assert.False(t, verified)
}

func TestVerifyIntegrityRejectsMissingIntegrityMetadata(t *testing.T) {
	m := setup(t)
	ctrl := gomock.NewController(t)

	db := spdb.NewMockSPDB(ctrl)
	db.EXPECT().GetObjectIntegrity(uint64(1), int32(0)).Return(nil, gorm.ErrRecordNotFound).Times(1)
	m.baseApp.SetGfSpDB(db)

	verified, err := verifyIntegrity(m, &types0.ObjectInfo{Id: sdkmath.NewUint(1)}, 0)

	assert.NoError(t, err)
	assert.False(t, verified)
}

func TestVerifyIntegrityPropagatesDatabaseErrors(t *testing.T) {
	m := setup(t)
	ctrl := gomock.NewController(t)
	dbErr := errors.New("database unavailable")

	db := spdb.NewMockSPDB(ctrl)
	db.EXPECT().GetObjectIntegrity(uint64(1), int32(0)).Return(nil, dbErr).Times(1)
	m.baseApp.SetGfSpDB(db)

	verified, err := verifyIntegrity(m, &types0.ObjectInfo{Id: sdkmath.NewUint(1)}, 0)

	assert.ErrorIs(t, err, dbErr)
	assert.False(t, verified)
}

func TestVerifyIntegrityRejectsInvalidChecksumIndex(t *testing.T) {
	for _, tc := range []struct {
		name            string
		redundancyIndex int32
		checksums       [][]byte
	}{
		{
			name:            "lower bound",
			redundancyIndex: -2,
			checksums:       [][]byte{hash.GenerateIntegrityHash([][]byte{[]byte("piece-checksum")})},
		},
		{
			name:            "upper bound",
			redundancyIndex: 0,
			checksums:       [][]byte{hash.GenerateIntegrityHash([][]byte{[]byte("piece-checksum")})},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := setup(t)
			ctrl := gomock.NewController(t)

			db := spdb.NewMockSPDB(ctrl)
			db.EXPECT().GetObjectIntegrity(uint64(1), tc.redundancyIndex).Return(&spdb.IntegrityMeta{
				PieceChecksumList: [][]byte{[]byte("piece-checksum")},
			}, nil).Times(1)
			m.baseApp.SetGfSpDB(db)

			verified, err := verifyIntegrity(m, &types0.ObjectInfo{
				Id:        sdkmath.NewUint(1),
				Checksums: tc.checksums,
			}, tc.redundancyIndex)

			assert.NoError(t, err)
			assert.False(t, verified)
		})
	}
}

func TestManageModular_RecoverVGFScheduler(t *testing.T) {
	m := setup(t)
	ctrl := gomock.NewController(t)

	con := consensus.NewMockConsensus(ctrl)
	m.baseApp.SetConsensus(con)
	con.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&types1.GlobalVirtualGroupFamily{
		Id:                    1,
		PrimarySpId:           1,
		GlobalVirtualGroupIds: []uint32{2, 3, 4, 5, 6, 7},
	}, nil).AnyTimes()

	db := spdb.NewMockSPDB(ctrl)
	m.baseApp.SetGfSpDB(db)
	db.EXPECT().SetRecoverGVGStats(gomock.Any()).Return(nil).AnyTimes()

	recoverVGF, err := NewRecoverVGFScheduler(m, 1)
	assert.Equal(t, nil, err)

	m.recoveryQueue = gfsptqueue.NewGfSpTQueueWithLimit("test", 2)
	con.EXPECT().QueryStorageParams(gomock.Any()).Return(&types0.Params{
		VersionedParams: types0.VersionedParams{
			MaxSegmentSize: 10,
		},
	}, nil).AnyTimes()
	db.EXPECT().GetRecoverGVGStats(gomock.Any()).Return(&spdb.RecoverGVGStats{
		Status:          spdb.Processing,
		RedundancyIndex: 1,
		Limit:           10,
	}, nil).AnyTimes()
	spClient := gfspclient.NewMockGfSpClientAPI(ctrl)
	m.baseApp.SetGfSpClient(spClient)
	spClient.EXPECT().ListObjectsInGVG(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return([]*types.ObjectDetails{}, nil).AnyTimes()
	db.EXPECT().UpdateRecoverGVGStats(gomock.Any()).Return(nil).AnyTimes()
	db.EXPECT().InsertRecoverFailedObject(gomock.Any()).Return(nil).AnyTimes()
	m.recoverObjectStats = NewObjectsSegmentsStats()
	db.EXPECT().GetObjectIntegrity(gomock.Any(), gomock.Any()).Return(&spdb.IntegrityMeta{}, nil).AnyTimes()
	db.EXPECT().GetRecoverFailedObject(gomock.Any()).Return(&spdb.RecoverFailedObject{
		RetryTime: 5,
	}, nil).AnyTimes()
	con.EXPECT().QueryObjectInfoByID(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	spClient.EXPECT().GetObjectByID(gomock.Any(), gomock.Any()).Return(&types0.ObjectInfo{Id: sdkmath.NewUint(1)}, nil).AnyTimes()

	recoverVGF.Start()
}

func TestManageModular_RecoverVGFScheduler1(t *testing.T) {
	m := setup(t)
	ctrl := gomock.NewController(t)

	con := consensus.NewMockConsensus(ctrl)
	m.baseApp.SetConsensus(con)
	con.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&types1.GlobalVirtualGroupFamily{
		Id:                    1,
		PrimarySpId:           1,
		GlobalVirtualGroupIds: []uint32{},
	}, nil).AnyTimes()

	spClient := gfspclient.NewMockGfSpClientAPI(ctrl)
	m.baseApp.SetGfSpClient(spClient)
	spClient.EXPECT().CompleteSwapIn(gomock.Any(), gomock.Any()).Return("", nil).AnyTimes()

	_, err := NewRecoverVGFScheduler(m, 1)
	assert.Equal(t, nil, err)
}

func TestManageModular_ObjectsSegmentsStats(t *testing.T) {
	stats := NewObjectsSegmentsStats()
	stats.put(1, 1, 1)
	has := stats.has(1, 1)
	assert.Equal(t, true, has)
	stats.addSegmentRecord(1, 1, true, 1)
	stats.addSegmentRecord(1, 1, false, 1)
	l := stats.isObjectProcessed(1, 1)
	assert.Equal(t, true, l)
	isFailed := stats.isRecoverFailed(1, 1)
	assert.Equal(t, false, isFailed)
	stats.remove(1, 1)
}

func TestObjectsSegmentsStats_IgnoresLateReportFromEarlierVersion(t *testing.T) {
	stats := NewObjectsSegmentsStats()
	stats.put(1, 1, 1)
	stats.put(1, 2, 1)

	stats.addSegmentRecord(1, 1, true, 0)

	assert.False(t, stats.isObjectProcessed(1, 2))
}

func TestVerifyGVGScheduler_Start(t *testing.T) {
	m := setup(t)
	ctrl := gomock.NewController(t)
	db := spdb.NewMockSPDB(ctrl)
	m.baseApp.SetGfSpDB(db)
	db.EXPECT().GetRecoverGVGStats(gomock.Any()).Return(&spdb.RecoverGVGStats{
		Status: spdb.Processed,
	}, nil).AnyTimes()
	spClient := gfspclient.NewMockGfSpClientAPI(ctrl)
	m.baseApp.SetGfSpClient(spClient)
	spClient.EXPECT().ListObjectsInGVG(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	v := &VerifyGVGScheduler{
		manager:              m,
		gvgID:                1,
		redundancyIndex:      1,
		curStartAfter:        0,
		verifyFailedObjects:  make(map[uint64]struct{}, 0),
		verifySuccessObjects: make(map[uint64]struct{}, 0),
	}
	v.verifyFailedObjects[1] = struct{}{}
	db.EXPECT().GetRecoverFailedObject(gomock.Any()).Return(&spdb.RecoverFailedObject{
		ObjectID:  1,
		RetryTime: maxRecoveryRetry,
	}, nil).AnyTimes()
	con := consensus.NewMockConsensus(ctrl)
	m.baseApp.SetConsensus(con)
	con.EXPECT().QueryObjectInfoByID(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	spClient.EXPECT().GetObjectByID(gomock.Any(), gomock.Any()).Return(&types0.ObjectInfo{
		Id: sdkmath.NewUint(1),
	}, nil).AnyTimes()
	db.EXPECT().GetObjectIntegrity(gomock.Any(), gomock.Any()).Return(&spdb.IntegrityMeta{}, nil).AnyTimes()
	db.EXPECT().DeleteRecoverFailedObject(gomock.Any()).Return(nil).AnyTimes()
	db.EXPECT().UpdateRecoverGVGStats(gomock.Any()).Return(nil).AnyTimes()
	v.Start()
}
