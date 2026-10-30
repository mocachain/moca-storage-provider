package manager

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	sdkmath "cosmossdk.io/math"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
	virtualgrouptypes "github.com/mocachain/moca/v2/x/virtualgroup/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"

	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/base/gfsppieceop"
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
	t.Setenv("RECOVERY_STAGING_LEDGER_DIR", t.TempDir())
	t.Setenv("RECOVERY_STAGING_CAPACITY_BYTES", "100000")
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
		store := &recoveryMemoryStore{pieces: make(map[string][]byte)}
		m.baseApp.SetPieceStore(store)
		m.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
		params := &storagetypes.Params{VersionedParams: storagetypes.VersionedParams{MaxSegmentSize: 10, RedundantDataChunkNum: 1}}
		con.EXPECT().QueryStorageParamsByTimestamp(gomock.Any(), int64(0)).Return(params, nil).AnyTimes()
		con.EXPECT().QueryObjectInfoByID(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, id string) (*storagetypes.ObjectInfo, error) {
			objectID, err := strconv.ParseUint(id, 10, 64)
			return schedulerRecoveryObject(objectID, 1), err
		}).AnyTimes()
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
		expectedUpdates := 2
		if tc.cursorUpdateError {
			expectedUpdates++
		}
		db.EXPECT().UpdateRecoverGVGStats(gomock.Any()).DoAndReturn(func(stats *spdb.RecoverGVGStats) error {
			if tc.cursorUpdateError && !cursorUpdateFailed {
				cursorUpdateFailed = true
				return errors.New("cursor write failed")
			}
			persisted = *stats
			return nil
		}).Times(expectedUpdates)
		var cursors []uint64
		client.EXPECT().ListObjectsInGVG(gomock.Any(), uint32(1), gomock.Any(), uint32(10)).DoAndReturn(func(_ context.Context, _ uint32, cursor uint64, _ uint32, _ ...grpc.DialOption) ([]*metadatatypes.ObjectDetails, error) {
			cursors = append(cursors, cursor)
			if cursor == 100 {
				return nil, nil
			}
			objects := []*metadatatypes.ObjectDetails{{Object: &metadatatypes.Object{ObjectInfo: &storagetypes.ObjectInfo{Id: sdkmath.NewUint(100), Version: 2, PayloadSize: 1, ObjectStatus: storagetypes.OBJECT_STATUS_SEALED, Checksums: schedulerRecoveryObject(100, 1).Checksums}}, Gvg: &virtualgrouptypes.GlobalVirtualGroup{Id: 1}}}
			if tc.mixed {
				objects = append([]*metadatatypes.ObjectDetails{{Object: &metadatatypes.Object{ObjectInfo: &storagetypes.ObjectInfo{Id: sdkmath.NewUint(99), Version: 2, PayloadSize: 1, ObjectStatus: storagetypes.OBJECT_STATUS_SEALED, Checksums: schedulerRecoveryObject(100, 1).Checksums}}, Gvg: &virtualgrouptypes.GlobalVirtualGroup{Id: 1}}}, objects...)
			}
			return objects, nil
		}).AnyTimes()
		inserts := 0
		var insertEvents []mixedInsertEvent
		inserted := make(map[uint64]bool)
		insertCall := db.EXPECT().InsertRecoverFailedObject(gomock.Any()).DoAndReturn(func(o *spdb.RecoverFailedObject) error {
			assert.Equal(t, uint32(1), o.VirtualGroupID)
			assert.Equal(t, int32(0), o.RedundancyIndex)
			if o.ObjectID == 100 {
				inserts++
			}
			success := !(o.ObjectID == 100 && (inserts <= tc.failures || tc.late))
			insertEvents = append(insertEvents, mixedInsertEvent{elapsed: time.Since(started), objectID: o.ObjectID, success: success})
			t.Logf("failed insert: object=%d elapsed=%s success=%t", o.ObjectID, time.Since(started), success)
			if !success {
				return errors.New("temporary database failure")
			}
			inserted[o.ObjectID] = true
			return nil
		})
		if tc.mixed {
			// Map order permits different totals for 99; validate every event
			// against the exact per-tick contract after the scheduler finishes.
			insertCall.AnyTimes()
		} else {
			expectedInserts := tc.failures + 1
			if tc.normal {
				expectedInserts = 0
			} else if tc.late || tc.cursorGetError || tc.cursorUpdateError {
				expectedInserts = 2
			}
			insertCall.Times(expectedInserts)
		}
		scheduler := &RecoverGVGScheduler{manager: m, gvgID: 1, currentBatchObjectIDs: make(map[objectVersion]struct{})}
		done := make(chan struct{})
		go func() { scheduler.Start(); close(done) }()
		synctest.Wait()
		publications := 0
		if tc.normal || tc.late {
			var published *spdb.IntegrityMeta
			db.EXPECT().PublishRecoveryIntegrity(gomock.Any()).DoAndReturn(func(meta *spdb.IntegrityMeta) error {
				publications++
				published = meta
				return nil
			}).Times(1)
			db.EXPECT().GetObjectIntegrity(uint64(100), int32(0)).DoAndReturn(func(uint64, int32) (*spdb.IntegrityMeta, error) { return published, nil }).Times(1)
			db.EXPECT().DeleteRecoverFailedObject(uint64(100)).Return(nil).Times(1)
		}
		var dispatched *gfsptask.GfSpRecoverPieceTask
		complete := func(version int64) {
			require.NotNil(t, dispatched)
			report := copyRecovery(dispatched)
			report.ObjectInfo.Version = version
			report.SetRecoverDone()
			if version != dispatched.ObjectInfo.Version {
				snapshotStats := func() map[objectVersion]ObjectSegmentsStats {
					m.recoverObjectStats.mux.RLock()
					defer m.recoverObjectStats.mux.RUnlock()
					result := make(map[objectVersion]ObjectSegmentsStats)
					for key, value := range m.recoverObjectStats.stats {
						copy := *value
						copy.SucceedSegments = maps.Clone(value.SucceedSegments)
						copy.FailedSegments = maps.Clone(value.FailedSegments)
						result[key] = copy
					}
					return result
				}
				before := snapshotStats()
				assert.ErrorIs(t, m.HandleRecoverPieceTask(context.Background(), report), ErrCanceledTask)
				assert.Equal(t, before, snapshotStats(), "rejected old-version report must not alter stats")
				assert.False(t, m.recoverObjectStats.isObjectProcessed(100, 2, 0))
				assert.Zero(t, publications)
				return
			}
			key, err := gfsptask.RecoveryStageKey(dispatched)
			require.NoError(t, err)
			require.NoError(t, store.PutPiece(context.Background(), key, []byte("x")))
			assert.NoError(t, m.HandleRecoverPieceTask(context.Background(), report))
			synctest.Wait()
			assert.Equal(t, 1, publications)
		}

		// Listing occurs at 10s; exactly 120s of monitoring must not time out.
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if tc.normal || tc.late {
			var queued coretask.Task
			m.recoveryQueue.ScanTask(func(task coretask.Task) { queued = task })
			require.NotNil(t, queued)
			var err error
			dispatched, err = m.dispatchRecovery(context.Background(), queued.(coretask.RecoveryPieceTask))
			require.NoError(t, err)
			require.Len(t, dispatched.GetRecoveryFence(), gfsptask.RecoveryFenceSize)
		}
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
		if tc.mixed {
			assert.NoError(t, checkMixedInsertTrajectory(insertEvents))
		}
	})
}

func TestRecoverFailedObjectSchedulerStart_DoesNotBurnRetryOnEnqueueFailure(t *testing.T) {
	for _, tc := range []struct {
		name                                              string
		enqueueErr                                        error
		verificationError, preregister, recovered         bool
		firstRetry, secondRetry, firstPushes, totalPushes int
	}{
		{name: "ordinary_error", enqueueErr: errors.New("queue unavailable"), firstRetry: 2, secondRetry: 3, firstPushes: 2, totalPushes: 3},
		{name: "queue_full", enqueueErr: gfsptqueue.ErrTaskQueueExceed, firstRetry: 2, secondRetry: 3, firstPushes: 2, totalPushes: 3},
		{name: "checksum_error", verificationError: true, firstRetry: 2, secondRetry: 3, firstPushes: 0, totalPushes: 2},
		{name: "duplicate", preregister: true, firstRetry: 3, secondRetry: 3, firstPushes: 2, totalPushes: 2},
		{name: "checksum_rows_without_integrity", firstRetry: 3, secondRetry: 3, firstPushes: 2, totalPushes: 2},
		{name: "already_recovered", recovered: true, firstRetry: 2, secondRetry: 2, firstPushes: 0, totalPushes: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RECOVERY_STAGING_LEDGER_DIR", t.TempDir())
			t.Setenv("RECOVERY_STAGING_CAPACITY_BYTES", "100000")
			synctest.Test(t, func(t *testing.T) {
				m := setup(t)
				ctrl := gomock.NewController(t)
				db := spdb.NewMockSPDB(ctrl)
				con := consensus.NewMockConsensus(ctrl)
				queue := taskqueue.NewMockTQueueOnStrategyWithLimit(ctrl)
				m.baseApp.SetGfSpDB(db)
				m.baseApp.SetConsensus(con)
				m.recoveryQueue = queue
				m.recoverObjectStats = NewObjectsSegmentsStats()
				m.verifyTerminationSignal.Store(1)
				params := &storagetypes.Params{VersionedParams: storagetypes.VersionedParams{MaxSegmentSize: 10, RedundantDataChunkNum: 1}}
				object := schedulerRecoveryObject(100, 11)
				con.EXPECT().QueryStorageParamsByTimestamp(gomock.Any(), int64(0)).Return(params, nil).AnyTimes()
				con.EXPECT().QueryObjectInfoByID(gomock.Any(), "100").Return(object, nil).AnyTimes()
				queue.EXPECT().Cap().Return(10).AnyTimes()
				con.EXPECT().QuerySwapInInfo(gomock.Any(), uint32(0), uint32(1)).Return(nil, errors.New("swap in info not exist"))
				persisted := spdb.RecoverFailedObject{ObjectID: 100, VirtualGroupID: 1, RetryTime: 2}
				round, pushes, updates, deletes := 0, 0, 0, 0
				rowPresent := true
				db.EXPECT().GetRecoverFailedObjects(uint32(maxRecoveryRetry), uint32(recoverBatchSize)).DoAndReturn(func(uint32, uint32) ([]*spdb.RecoverFailedObject, error) {
					round++
					if !rowPresent {
						return nil, nil
					}
					copy := persisted
					return []*spdb.RecoverFailedObject{&copy}, nil
				}).Times(2)
				if tc.recovered {
					store := &recoveryMemoryStore{pieces: make(map[string][]byte)}
					m.baseApp.SetPieceStore(store)
					m.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
					pieces := [][]byte{[]byte("abcdefghij"), []byte("k")}
					var list [][]byte
					for i, data := range pieces {
						sum := sha256.Sum256(data)
						list = append(list, sum[:])
						key := m.baseApp.PieceOp().ChallengePieceKey(100, uint32(i), 0, 2)
						require.NoError(t, store.PutPiece(context.Background(), key, data))
					}
					meta := &spdb.IntegrityMeta{ObjectID: 100, RedundancyIndex: 0, PieceChecksumList: list, IntegrityChecksum: object.Checksums[1], ObjectSize: 11}
					db.EXPECT().GetObjectIntegrity(uint64(100), int32(0)).Return(meta, nil).Times(1)
					db.EXPECT().DeleteRecoverFailedObject(uint64(100)).DoAndReturn(func(uint64) error { deletes++; rowPresent = false; return nil }).Times(1)
				} else {
					db.EXPECT().GetObjectIntegrity(uint64(100), int32(0)).DoAndReturn(func(uint64, int32) (*spdb.IntegrityMeta, error) {
						if tc.verificationError && round == 1 {
							return nil, errors.New("checksum query failed")
						}
						return nil, gorm.ErrRecordNotFound
					}).Times(2)
					db.EXPECT().DeleteRecoverFailedObject(gomock.Any()).Times(0)
				}
				var keysMu sync.Mutex
				keys := make(map[uint32]coretask.TKey)
				queue.EXPECT().Push(gomock.Any()).DoAndReturn(func(task coretask.Task) error {
					pushes++
					recovery := task.(coretask.RecoveryPieceTask)
					require.Len(t, recovery.GetRecoveryFence(), gfsptask.RecoveryFenceSize)
					if recovery.GetSegmentIdx() == 1 && round == 1 && tc.enqueueErr != nil {
						return tc.enqueueErr
					}
					keysMu.Lock()
					keys[recovery.GetSegmentIdx()] = recovery.Key()
					keysMu.Unlock()
					return nil
				}).Times(tc.totalPushes)
				expectedUpdates := 1
				if tc.recovered {
					expectedUpdates = 0
				}
				db.EXPECT().UpdateRecoverFailedObject(gomock.Any()).DoAndReturn(func(o *spdb.RecoverFailedObject) error { updates++; persisted = *o; return nil }).Times(expectedUpdates)
				if tc.preregister {
					task := &gfsptask.GfSpRecoverPieceTask{}
					task.InitRecoverPieceTask(object, params, coretask.DefaultSmallerPriority, 0, 0, 10, MaxRecoveryTime, maxRecoveryRetry)
					task.SetBySuccessorSP(true)
					task.SetGVGID(1)
					require.NoError(t, m.HandleRecoverPieceTask(context.Background(), task))
				}
				done := make(chan struct{})
				go func() { (&RecoverFailedObjectScheduler{manager: m, gvgID: 1}).Start(); close(done) }()
				synctest.Wait()
				time.Sleep(20 * time.Second)
				synctest.Wait()
				assert.Equal(t, tc.firstRetry, persisted.RetryTime)
				assert.Equal(t, tc.firstPushes, pushes)
				if tc.firstRetry == 3 {
					assert.Equal(t, 1, updates)
					assert.Equal(t, 3, persisted.RetryTime)
				} else if !tc.recovered {
					assert.Zero(t, updates, "partial enqueue or checksum failure must not consume an object retry")
					assert.Equal(t, 2, persisted.RetryTime)
				}
				keysMu.Lock()
				firstKeys := maps.Clone(keys)
				keysMu.Unlock()
				time.Sleep(20 * time.Second)
				synctest.Wait()
				assert.Equal(t, tc.secondRetry, persisted.RetryTime)
				assert.Equal(t, tc.totalPushes, pushes)
				if tc.recovered {
					assert.Equal(t, 1, deletes)
					assert.False(t, rowPresent)
					assert.Zero(t, updates)
					assert.Equal(t, 2, persisted.RetryTime)
				} else {
					assert.Equal(t, 3, persisted.RetryTime)
					assert.Equal(t, 1, updates)
					keysMu.Lock()
					for segment, key := range firstKeys {
						assert.Equal(t, key, keys[segment], "queued segments retain their fence across ticks")
					}
					keysMu.Unlock()
				}
				m.verifyTerminationSignal.Store(0)
				time.Sleep(20 * time.Second)
				synctest.Wait()
				<-done
			})
		})
	}
}

// Match the stable chain snapshot required by fenced recovery admission.
func schedulerRecoveryObject(id, payload uint64) *storagetypes.ObjectInfo {
	data := []byte("x")
	if payload == 11 {
		data = []byte("abcdefghijk")
	}
	var joined []byte
	for i := 0; i < len(data); i += 10 {
		end := i + 10
		if end > len(data) {
			end = len(data)
		}
		sum := sha256.Sum256(data[i:end])
		joined = append(joined, sum[:]...)
	}
	root := sha256.Sum256(joined)
	return &storagetypes.ObjectInfo{Id: sdkmath.NewUint(id), Version: 2, PayloadSize: payload, ObjectStatus: storagetypes.OBJECT_STATUS_SEALED, Checksums: [][]byte{root[:], root[:]}}
}

// mixedInsertEvent records the observable persistence side effect, including its
// virtual timestamp and returned result, without imposing production map order.
type mixedInsertEvent struct {
	elapsed  time.Duration
	objectID uint64
	success  bool
}

func checkMixedInsertTrajectory(events []mixedInsertEvent) error {
	groups := make(map[time.Duration][]mixedInsertEvent)
	for _, event := range events {
		if event.elapsed != 140*time.Second && event.elapsed != 150*time.Second && event.elapsed != 160*time.Second {
			return fmt.Errorf("insert outside allowed tick: %s", event.elapsed)
		}
		groups[event.elapsed] = append(groups[event.elapsed], event)
	}
	for _, tick := range []time.Duration{140 * time.Second, 150 * time.Second} {
		group := groups[tick]
		if len(group) == 2 && group[0].objectID == 99 && group[0].success {
			group = group[1:]
		}
		if len(group) != 1 || group[0].objectID != 100 || group[0].success {
			return fmt.Errorf("invalid failed tick %s: %+v", tick, groups[tick])
		}
	}
	group := groups[160*time.Second]
	if len(group) != 2 || !group[0].success || !group[1].success ||
		!((group[0].objectID == 99 && group[1].objectID == 100) || (group[0].objectID == 100 && group[1].objectID == 99)) {
		return fmt.Errorf("invalid final tick: %+v", group)
	}
	return nil
}

func TestCheckMixedInsertTrajectory(t *testing.T) {
	event := func(second int, id uint64, success bool) mixedInsertEvent {
		return mixedInsertEvent{time.Duration(second) * time.Second, id, success}
	}
	only100 := []mixedInsertEvent{event(140, 100, false), event(150, 100, false), event(160, 99, true), event(160, 100, true)}
	cases := []struct {
		name   string
		events []mixedInsertEvent
		valid  bool
	}{
		{"observed_both_before", []mixedInsertEvent{event(140, 99, true), event(140, 100, false), event(150, 99, true), event(150, 100, false), event(160, 99, true), event(160, 100, true)}, true},
		{"observed_final_reversed", []mixedInsertEvent{event(140, 99, true), event(140, 100, false), event(150, 99, true), event(150, 100, false), event(160, 100, true), event(160, 99, true)}, true},
		{"observed_second_only100", []mixedInsertEvent{event(140, 99, true), event(140, 100, false), event(150, 100, false), event(160, 100, true), event(160, 99, true)}, true},
		{"observed_first_only100", []mixedInsertEvent{event(140, 100, false), event(150, 99, true), event(150, 100, false), event(160, 99, true), event(160, 100, true)}, true},
		{"both_failures_only100", only100, true},
		{"duplicate_99_before_failure", []mixedInsertEvent{event(140, 99, true), event(140, 99, true), event(140, 100, false), event(150, 100, false), event(160, 99, true), event(160, 100, true)}, false},
		{"duplicate_99_final", append(append([]mixedInsertEvent{}, only100...), event(160, 99, true)), false},
		{"missing_99_final", []mixedInsertEvent{event(140, 100, false), event(150, 100, false), event(160, 100, true)}, false},
		{"missing_100_final", []mixedInsertEvent{event(140, 100, false), event(150, 100, false), event(160, 99, true)}, false},
		{"after_failure", []mixedInsertEvent{event(140, 100, false), event(140, 99, true), event(150, 100, false), event(160, 99, true), event(160, 100, true)}, false},
		{"early_130", append([]mixedInsertEvent{event(130, 99, true)}, only100...), false},
		{"late_170", append(append([]mixedInsertEvent{}, only100...), event(170, 99, true)), false},
		{"between_ticks_145", append([]mixedInsertEvent{event(145, 99, true)}, only100...), false},
		{"missing_150", []mixedInsertEvent{event(140, 100, false), event(160, 99, true), event(160, 100, true)}, false},
		{"extra_100_failure", append([]mixedInsertEvent{event(140, 100, false)}, only100...), false},
		{"missing_140", only100[1:], false},
		{"early_100_success", []mixedInsertEvent{event(140, 100, true), event(150, 100, false), event(160, 99, true), event(160, 100, true)}, false},
		{"99_failure", []mixedInsertEvent{event(140, 99, false), event(140, 100, false), event(150, 100, false), event(160, 99, true), event(160, 100, true)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkMixedInsertTrajectory(tc.events)
			if tc.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
