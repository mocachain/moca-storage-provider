package manager

import (
	"errors"
	"testing"

	sdkmath "cosmossdk.io/math"
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
	m, object, params, _, _, _, _ := recoveryFixture(t, 0)
	ctrl := gomock.NewController(t)
	queue := taskqueue.NewMockTQueueOnStrategyWithLimit(ctrl)
	queue.EXPECT().Cap().Return(10).AnyTimes()
	queue.EXPECT().Push(gomock.Any()).Return(errors.New("queue unavailable"))
	m.recoveryQueue = queue
	scheduler := &RecoverGVGScheduler{manager: m, currentBatchObjectIDs: make(map[objectVersion]struct{})}
	assert.False(t, scheduler.queueRecoveryObject(object, params, 4, 3))
	assert.False(t, m.recoverObjectStats.has(91, 2, 0))
	assert.Empty(t, scheduler.currentBatchObjectIDs)
}
func TestRecoverFailedObjectSchedulerQueueFailedObjectRecovery_QueuesEverySegment(t *testing.T) {
	m, object, params, _, _, _, _ := recoveryFixture(t, 0)
	// The DB mock rejects any checksum-only lookup: residual rows are not evidence of possession.
	scheduler := &RecoverFailedObjectScheduler{manager: m}
	assert.True(t, scheduler.queueFailedObjectRecovery(object, params, &spdb.RecoverFailedObject{ObjectID: 91}, 4, 3))
	assert.Equal(t, 3, m.recoveryQueue.Len())
}

func TestRecoverFailedObjectSchedulerSeedFailedObjectStats_RegistersCurrentVersion(t *testing.T) {
	m := setup(t)
	m.recoverObjectStats = NewObjectsSegmentsStats()
	scheduler := &RecoverFailedObjectScheduler{manager: m}
	objectInfo := &types0.ObjectInfo{Id: sdkmath.NewUint(300), Version: 2, PayloadSize: 1}

	scheduler.seedFailedObjectStats(objectInfo, 3, 0)

	// Without seeding, isRecoverFailed's absent-key branch always reports true
	// regardless of what actually happened. A real entry starts as "not
	// failed" until a segment actually fails.
	assert.True(t, m.recoverObjectStats.has(300, 2, 0))
	assert.False(t, m.recoverObjectStats.isRecoverFailed(300, 2, 0))

	// A failure reported for this object now resolves against its own version
	// instead of being invisible to it.
	m.recoverObjectStats.addSegmentRecord(300, 2, false, 0, 0)
	m.recoverObjectStats.addSegmentRecord(300, 2, true, 1, 0)
	m.recoverObjectStats.addSegmentRecord(300, 2, true, 2, 0)
	assert.True(t, m.recoverObjectStats.isRecoverFailed(300, 2, 0))

	// A report for a different version of the same object must not resolve
	// against this entry.
	assert.False(t, m.recoverObjectStats.has(300, 3, 0))
}

func TestRecoverFailedObjectSchedulerSeedFailedObjectStats_DoesNotResetInProgressEntry(t *testing.T) {
	m := setup(t)
	m.recoverObjectStats = NewObjectsSegmentsStats()
	scheduler := &RecoverFailedObjectScheduler{manager: m}
	objectInfo := &types0.ObjectInfo{Id: sdkmath.NewUint(301), Version: 1, PayloadSize: 1}

	scheduler.seedFailedObjectStats(objectInfo, 2, 0)
	m.recoverObjectStats.addSegmentRecord(301, 1, true, 0, 0)

	// A later pass over the same still-pending row (e.g. the next scheduler
	// tick, before this object's segments have all reported back) must not
	// wipe out progress already recorded for it.
	scheduler.seedFailedObjectStats(objectInfo, 2, 0)

	assert.False(t, m.recoverObjectStats.isObjectProcessed(301, 1, 0))
	m.recoverObjectStats.addSegmentRecord(301, 1, true, 1, 0)
	assert.True(t, m.recoverObjectStats.isObjectProcessed(301, 1, 0))
	assert.False(t, m.recoverObjectStats.isRecoverFailed(301, 1, 0))
}

func TestVerifyIntegrityAcceptsMatchingChainChecksum(t *testing.T) {
	m, object, _, db, _, _, meta := recoveryFixture(t, 0)
	db.EXPECT().GetObjectIntegrity(uint64(91), int32(0)).Return(meta, nil)
	ok, err := verifyIntegrity(m, object, 0)
	assert.NoError(t, err)
	assert.True(t, ok)
}
func TestVerifyIntegrityRejectsMismatchedChainChecksum(t *testing.T) {
	m, object, _, db, _, _, meta := recoveryFixture(t, 0)
	object.Checksums[1] = []byte("different chain root")
	db.EXPECT().GetObjectIntegrity(uint64(91), int32(0)).Return(meta, nil)
	ok, err := verifyIntegrity(m, object, 0)
	assert.NoError(t, err)
	assert.False(t, ok)
}
func TestVerifyIntegrityRejectsMissingIntegrityMetadata(t *testing.T) {
	m, object, _, db, _, _, _ := recoveryFixture(t, 0)
	db.EXPECT().GetObjectIntegrity(uint64(91), int32(0)).Return(nil, gorm.ErrRecordNotFound)
	ok, err := verifyIntegrity(m, object, 0)
	assert.NoError(t, err)
	assert.False(t, ok)
}
func TestVerifyIntegrityPropagatesDatabaseErrors(t *testing.T) {
	m, object, _, db, _, _, _ := recoveryFixture(t, 0)
	dbErr := errors.New("database unavailable")
	db.EXPECT().GetObjectIntegrity(uint64(91), int32(0)).Return(nil, dbErr)
	ok, err := verifyIntegrity(m, object, 0)
	assert.ErrorIs(t, err, dbErr)
	assert.False(t, ok)
}
func TestVerifyIntegrityRejectsInvalidChecksumIndex(t *testing.T) {
	for _, r := range []int32{-2, 5} {
		m, object, _, _, _, _, _ := recoveryFixture(t, 0)
		ok, err := verifyIntegrity(m, object, r)
		assert.Error(t, err)
		assert.False(t, ok)
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
	stats.put(1, 1, 1, 0)
	has := stats.has(1, 1, 0)
	assert.Equal(t, true, has)
	stats.addSegmentRecord(1, 1, true, 1, 0)
	stats.addSegmentRecord(1, 1, false, 1, 0)
	l := stats.isObjectProcessed(1, 1, 0)
	assert.Equal(t, true, l)
	isFailed := stats.isRecoverFailed(1, 1, 0)
	assert.Equal(t, false, isFailed)
	stats.remove(1, 1, 0)
}

func TestObjectsSegmentsStats_IgnoresLateReportFromEarlierVersion(t *testing.T) {
	stats := NewObjectsSegmentsStats()
	stats.put(1, 1, 1, 0)
	stats.put(1, 2, 1, 0)

	stats.addSegmentRecord(1, 1, true, 0, 0)

	assert.False(t, stats.isObjectProcessed(1, 2, 0))
}

func TestVerifyGVGScheduler_NilChainResponseRemainsUnresolved(t *testing.T) {
	m := setup(t)
	ctrl := gomock.NewController(t)
	client := gfspclient.NewMockGfSpClientAPI(ctrl)
	m.baseApp.SetGfSpClient(client)
	client.EXPECT().ListObjectsInGVG(gomock.Any(), uint32(1), uint64(0), verifyGVGQueryLimit).Return(nil, nil)
	db := spdb.NewMockSPDB(ctrl)
	m.baseApp.SetGfSpDB(db)
	chain := consensus.NewMockConsensus(ctrl)
	m.baseApp.SetConsensus(chain)
	chain.EXPECT().QueryObjectInfoByID(gomock.Any(), "1").Return(nil, nil)
	v, _ := NewVerifyGVGScheduler(m, 1, 1)
	v.verifyFailedObjects[1] = struct{}{}
	stats := &spdb.RecoverGVGStats{VirtualGroupID: 1, Status: spdb.Processed}
	completed, err := v.verifyPass(stats)
	assert.Error(t, err)
	assert.False(t, completed)
	assert.Equal(t, spdb.Processed, stats.Status)
	assert.Contains(t, v.verifyFailedObjects, uint64(1))
}
