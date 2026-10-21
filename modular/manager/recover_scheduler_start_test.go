package manager

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	sdkmath "cosmossdk.io/math"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
	virtualgrouptypes "github.com/mocachain/moca/v2/x/virtualgroup/types"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"

	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/base/gfsptqueue"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	"github.com/mocachain/moca-storage-provider/core/spdb"
	coretask "github.com/mocachain/moca-storage-provider/core/task"
	"github.com/mocachain/moca-storage-provider/core/taskqueue"
	metadatatypes "github.com/mocachain/moca-storage-provider/modular/metadata/types"
	"gorm.io/gorm"
)

// All scheduler goroutines and their clock live inside the synctest bubble.
// DB reads and writes copy values to model persistence rather than pointer aliasing.
func TestRecoverGVGSchedulerStart_RetainsBatchAfterFailedRecordInsert(t *testing.T) {
	runRecoveryBatch(t, recoveryBatchCase{failures: 1})
}

type recoveryBatchCase struct {
	failures                                               int
	mixed, late, normal, cursorGetError, cursorUpdateError bool
}

func TestRecoverGVGSchedulerStart_BatchProgress(t *testing.T) {
	for name, tc := range map[string]recoveryBatchCase{
		"timeout_success":        {},
		"repeated_insert_errors": {failures: 2},
		"mixed_batch":            {failures: 2, mixed: true},
		"late_completion":        {failures: 1, late: true},
		"normal_completion":      {normal: true},
		"cursor_read_error":      {cursorGetError: true},
		"cursor_write_error":     {cursorUpdateError: true},
	} {
		t.Run(name, func(t *testing.T) { runRecoveryBatch(t, tc) })
	}
}

func runRecoveryBatch(t *testing.T, tc recoveryBatchCase) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		m := setup(t)
		ctrl := gomock.NewController(t)
		db := spdb.NewMockSPDB(ctrl)
		con := consensus.NewMockConsensus(ctrl)
		client := gfspclient.NewMockGfSpClientAPI(ctrl)
		m.baseApp.SetGfSpDB(db)
		m.baseApp.SetConsensus(con)
		m.baseApp.SetGfSpClient(client)
		m.recoveryQueue = gfsptqueue.NewGfSpTQueueWithLimit("recovery", 10)
		m.recoverObjectStats = NewObjectsSegmentsStats()
		params := &storagetypes.Params{VersionedParams: storagetypes.VersionedParams{MaxSegmentSize: 10}}
		con.EXPECT().QueryStorageParams(gomock.Any()).Return(params, nil)
		persisted := spdb.RecoverGVGStats{VirtualGroupID: 1, StartAfter: 10, NextStartAfter: 100, Limit: 10, Status: spdb.Processing}
		cursorGetFailed, cursorUpdateFailed := false, false
		started := time.Now()
		db.EXPECT().GetRecoverGVGStats(uint32(1)).DoAndReturn(func(uint32) (*spdb.RecoverGVGStats, error) {
			if tc.cursorGetError && !cursorGetFailed && time.Since(started) >= 140*time.Second {
				cursorGetFailed = true
				return nil, errors.New("cursor read failed")
			}
			copy := persisted
			return &copy, nil
		}).AnyTimes()
		db.EXPECT().UpdateRecoverGVGStats(gomock.Any()).DoAndReturn(func(stats *spdb.RecoverGVGStats) error {
			if tc.cursorUpdateError && !cursorUpdateFailed {
				cursorUpdateFailed = true
				return errors.New("cursor write failed")
			}
			persisted = *stats
			return nil
		}).AnyTimes()
		var cursors []uint64
		client.EXPECT().ListObjectsInGVG(gomock.Any(), uint32(1), gomock.Any(), uint32(10)).DoAndReturn(func(_ context.Context, _ uint32, cursor uint64, _ uint32, _ ...grpc.DialOption) ([]*metadatatypes.ObjectDetails, error) {
			cursors = append(cursors, cursor)
			if cursor == 100 {
				return nil, nil
			}
			objects := []*metadatatypes.ObjectDetails{{Object: &metadatatypes.Object{ObjectInfo: &storagetypes.ObjectInfo{Id: sdkmath.NewUint(100), Version: 2, PayloadSize: 1}}, Gvg: &virtualgrouptypes.GlobalVirtualGroup{Id: 1}}}
			if tc.mixed {
				objects = append([]*metadatatypes.ObjectDetails{{Object: &metadatatypes.Object{ObjectInfo: &storagetypes.ObjectInfo{Id: sdkmath.NewUint(99), Version: 2, PayloadSize: 1}}, Gvg: &virtualgrouptypes.GlobalVirtualGroup{Id: 1}}}, objects...)
			}
			return objects, nil
		}).AnyTimes()
		inserts := 0
		inserted := make(map[uint64]bool)
		db.EXPECT().InsertRecoverFailedObject(gomock.Any()).DoAndReturn(func(o *spdb.RecoverFailedObject) error {
			assert.Equal(t, uint32(1), o.VirtualGroupID)
			assert.Equal(t, int32(0), o.RedundancyIndex)
			if o.ObjectID == 100 {
				inserts++
			}
			if o.ObjectID == 100 && (inserts <= tc.failures || tc.late) {
				return errors.New("temporary database failure")
			}
			inserted[o.ObjectID] = true
			return nil
		}).AnyTimes()
		scheduler := &RecoverGVGScheduler{manager: m, gvgID: 1, currentBatchObjectIDs: make(map[objectVersion]struct{})}
		done := make(chan struct{})
		go func() { scheduler.Start(); close(done) }()
		synctest.Wait()
		complete := func(version int64) {
			task := &gfsptask.GfSpRecoverPieceTask{}
			info := &storagetypes.ObjectInfo{Id: sdkmath.NewUint(100), Version: version, PayloadSize: 1}
			task.InitRecoverPieceTask(info, params, coretask.DefaultSmallerPriority, 0, 0, 10, MaxRecoveryTime, maxRecoveryRetry)
			task.SetBySuccessorSP(true)
			task.SetRecoverDone()
			assert.NoError(t, m.HandleRecoverPieceTask(context.Background(), task))
		}
		// Listing occurs at 10s; exactly 120s of monitoring must not time out.
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if tc.normal {
			complete(2)
		}
		time.Sleep(120 * time.Second)
		synctest.Wait()
		assert.Zero(t, inserts)
		if !tc.normal {
			assert.Equal(t, uint64(10), persisted.StartAfter)
			assert.Equal(t, []uint64{10}, cursors)
		}
		for i := 0; i < tc.failures; i++ {
			time.Sleep(10 * time.Second)
			synctest.Wait()
			assert.Equal(t, i+1, inserts)
			assert.Equal(t, uint64(10), persisted.StartAfter, "insert failure must retain the persisted cursor")
			assert.Equal(t, []uint64{10}, cursors, "must not list a subsequent batch after an insert failure")
		}
		if tc.late {
			complete(3) // An old/different version must not complete the current batch.
			time.Sleep(10 * time.Second)
			synctest.Wait()
			assert.Equal(t, uint64(10), persisted.StartAfter)
			// Keep the next timeout insert failing to observe version isolation.
			complete(2)
		}
		if tc.cursorGetError || tc.cursorUpdateError {
			time.Sleep(10 * time.Second)
			synctest.Wait()
			assert.Equal(t, uint64(10), persisted.StartAfter)
			assert.Equal(t, []uint64{10}, cursors)
		}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		assert.Equal(t, uint64(100), persisted.StartAfter)
		assert.Equal(t, []uint64{10, 100}, cursors)
		if tc.late {
			assert.Equal(t, 2, inserts)
		}
		if tc.normal {
			assert.Empty(t, inserted)
		} else if !tc.late {
			assert.True(t, inserted[100])
			if !tc.cursorGetError && !tc.cursorUpdateError {
				assert.Equal(t, tc.failures+1, inserts)
			}
			if tc.mixed {
				assert.True(t, inserted[99])
			}
		}
		<-done
	})
}

func TestRecoverFailedObjectSchedulerStart_DoesNotBurnRetryOnEnqueueFailure(t *testing.T) {
	for name, enqueueErr := range map[string]error{
		"ordinary_error":    errors.New("queue unavailable"),
		"queue_full":        gfsptqueue.ErrTaskQueueExceed,
		"duplicate":         ErrRepeatedTask,
		"already_recovered": nil,
		"checksum_error":    errors.New("checksum query failed"),
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := setup(t)
				ctrl := gomock.NewController(t)
				db := spdb.NewMockSPDB(ctrl)
				con := consensus.NewMockConsensus(ctrl)
				client := gfspclient.NewMockGfSpClientAPI(ctrl)
				queue := taskqueue.NewMockTQueueOnStrategyWithLimit(ctrl)
				m.baseApp.SetGfSpDB(db)
				m.baseApp.SetConsensus(con)
				m.baseApp.SetGfSpClient(client)
				m.recoveryQueue = queue
				m.recoverObjectStats = NewObjectsSegmentsStats()
				m.verifyTerminationSignal.Store(1)
				con.EXPECT().QueryStorageParams(gomock.Any()).Return(&storagetypes.Params{VersionedParams: storagetypes.VersionedParams{MaxSegmentSize: 10}}, nil)
				con.EXPECT().QuerySwapInInfo(gomock.Any(), uint32(0), uint32(1)).Return(nil, errors.New("swap in info not exist"))
				persisted := spdb.RecoverFailedObject{ObjectID: 100, VirtualGroupID: 1, RetryTime: 2}
				db.EXPECT().GetRecoverFailedObjects(uint32(maxRecoveryRetry), uint32(recoverBatchSize)).DoAndReturn(func(uint32, uint32) ([]*spdb.RecoverFailedObject, error) {
					copy := persisted
					return []*spdb.RecoverFailedObject{&copy}, nil
				}).Times(2)
				client.EXPECT().GetObjectByID(gomock.Any(), uint64(100)).Return(&storagetypes.ObjectInfo{Id: sdkmath.NewUint(100), Version: 2, PayloadSize: 11}, nil).Times(2)
				db.EXPECT().GetObjectIntegrity(uint64(100), int32(0)).Return(nil, gorm.ErrRecordNotFound).Times(2)
				round := 0
				db.EXPECT().GetReplicatePieceChecksum(uint64(100), uint32(0), int32(0)).DoAndReturn(func(uint64, uint32, int32) ([]byte, error) {
					round++
					if name == "already_recovered" {
						return []byte("checksum"), nil
					}
					return nil, gorm.ErrRecordNotFound
				}).Times(2)
				db.EXPECT().GetReplicatePieceChecksum(uint64(100), uint32(1), int32(0)).DoAndReturn(func(uint64, uint32, int32) ([]byte, error) {
					if name == "checksum_error" && round == 1 {
						return nil, enqueueErr
					}
					if name == "already_recovered" {
						return []byte("checksum"), nil
					}
					return nil, gorm.ErrRecordNotFound
				}).Times(2)
				if name != "already_recovered" {
					queue.EXPECT().Push(gomock.Any()).DoAndReturn(func(task coretask.Task) error {
						if task.(coretask.RecoveryPieceTask).GetSegmentIdx() == 1 && round == 1 {
							return enqueueErr
						}
						return nil
					}).AnyTimes()
				}
				updates := 0
				db.EXPECT().UpdateRecoverFailedObject(gomock.Any()).DoAndReturn(func(o *spdb.RecoverFailedObject) error { updates++; persisted = *o; return nil }).AnyTimes()
				done := make(chan struct{})
				go func() { (&RecoverFailedObjectScheduler{manager: m, gvgID: 1}).Start(); close(done) }()
				synctest.Wait()
				time.Sleep(20 * time.Second)
				synctest.Wait()
				consumesRound := name == "duplicate" || name == "already_recovered"
				if consumesRound {
					assert.Equal(t, 1, updates)
					assert.Equal(t, 3, persisted.RetryTime)
				} else {
					assert.Zero(t, updates, "partial enqueue or checksum failure must not consume an object retry")
					assert.Equal(t, 2, persisted.RetryTime)
				}
				time.Sleep(20 * time.Second)
				synctest.Wait()
				if consumesRound {
					assert.Equal(t, 4, persisted.RetryTime)
				} else {
					assert.Equal(t, 3, persisted.RetryTime)
					assert.Equal(t, 1, updates)
				}
				m.verifyTerminationSignal.Store(0)
				time.Sleep(20 * time.Second)
				synctest.Wait()
				<-done
			})
		})
	}
}
