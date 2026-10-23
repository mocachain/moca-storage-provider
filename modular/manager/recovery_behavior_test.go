package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"
	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/base/gfsppieceop"
	"github.com/mocachain/moca-storage-provider/base/gfsptqueue"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	"github.com/mocachain/moca-storage-provider/core/spdb"
	metatypes "github.com/mocachain/moca-storage-provider/modular/metadata/types"
	st "github.com/mocachain/moca/v2/x/storage/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"gorm.io/gorm"
)

type recoveryMemoryStore struct {
	mu     sync.Mutex
	pieces map[string][]byte
	err    error
}

func (s *recoveryMemoryStore) GetPiece(ctx context.Context, key string, offset, limit int64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	data, ok := s.pieces[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	if limit > 0 && int64(len(data)) > limit {
		data = data[:limit]
	}
	return append([]byte(nil), data...), ctx.Err()
}
func (s *recoveryMemoryStore) PutPiece(ctx context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.pieces[key] = append([]byte(nil), data...)
	return ctx.Err()
}
func (s *recoveryMemoryStore) DeletePiece(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pieces, key)
	return ctx.Err()
}
func (s *recoveryMemoryStore) DeletePiecesByPrefix(context.Context, string) (uint64, error) {
	return 0, errors.New("prefix deletion forbidden")
}

// Fixture hashes use the standard-library SHA-256 on known content, independently
// of the recovery implementation's hash helpers and candidate traversal.
func recoveryFixture(t *testing.T, r int32) (*ManageModular, *st.ObjectInfo, *st.Params, *spdb.MockSPDB, *consensus.MockConsensus, *recoveryMemoryStore, *spdb.IntegrityMeta) {
	t.Helper()
	t.Setenv("RECOVERY_STAGING_LEDGER_DIR", t.TempDir())
	t.Setenv("RECOVERY_STAGING_CAPACITY_BYTES", "100000")
	m := setup(t)
	ctrl := gomock.NewController(t)
	pieces := [][]byte{[]byte("abcd"), []byte("efgh"), []byte("i")}
	var list [][]byte
	var joined []byte
	for _, piece := range pieces {
		sum := sha256.Sum256(piece)
		list = append(list, sum[:])
		joined = append(joined, sum[:]...)
	}
	root := sha256.Sum256(joined)
	object := &st.ObjectInfo{Id: sdkmath.NewUint(91), PayloadSize: 9, Version: 2, CreateAt: 12, UpdatedAt: 123, ObjectStatus: st.OBJECT_STATUS_SEALED, Checksums: [][]byte{root[:], root[:]}}
	params := &st.Params{VersionedParams: st.VersionedParams{MaxSegmentSize: 4, RedundantDataChunkNum: 1}}
	chain := consensus.NewMockConsensus(ctrl)
	chain.EXPECT().QueryObjectInfoByID(gomock.Any(), "91").Return(object, nil).AnyTimes()
	chain.EXPECT().QueryStorageParamsByTimestamp(gomock.Any(), int64(123)).Return(params, nil).AnyTimes()
	m.baseApp.SetConsensus(chain)
	m.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	store := &recoveryMemoryStore{pieces: make(map[string][]byte)}
	for i, piece := range pieces {
		key := m.baseApp.PieceOp().ChallengePieceKey(91, uint32(i), r, 2)
		store.pieces[key] = piece
	}
	m.baseApp.SetPieceStore(store)
	db := spdb.NewMockSPDB(ctrl)
	m.baseApp.SetGfSpDB(db)
	meta := &spdb.IntegrityMeta{ObjectID: 91, RedundancyIndex: r, PieceChecksumList: list, IntegrityChecksum: root[:], ObjectSize: 9}
	m.recoveryQueue = gfsptqueue.NewGfSpTQueueWithLimit("fence-test", 20)
	m.recoverObjectStats = NewObjectsSegmentsStats()
	return m, object, params, db, chain, store, meta
}
func TestVerifyIntegrityReadsAllHistoricalVersionPieces(t *testing.T) {
	for _, r := range []int32{-1, 0} {
		t.Run(fmt.Sprint(r), func(t *testing.T) {
			m, object, _, db, _, _, meta := recoveryFixture(t, r)
			db.EXPECT().GetObjectIntegrity(uint64(91), r).Return(meta, nil)
			ok, err := verifyIntegrity(m, object, r)
			require.NoError(t, err)
			require.True(t, ok)
		})
	}
}
func TestVerifyIntegrityRejectsLostOrCorruptPieces(t *testing.T) {
	for _, kind := range []string{"first missing", "middle missing", "tail missing", "truncated", "long", "same length corrupt", "empty", "short list", "long list", "reordered"} {
		t.Run(kind, func(t *testing.T) {
			m, object, _, db, _, store, meta := recoveryFixture(t, -1)
			key := m.baseApp.PieceOp().ChallengePieceKey(91, 1, -1, 2)
			switch kind {
			case "first missing":
				delete(store.pieces, m.baseApp.PieceOp().ChallengePieceKey(91, 0, -1, 2))
			case "middle missing":
				delete(store.pieces, key)
			case "tail missing":
				delete(store.pieces, m.baseApp.PieceOp().ChallengePieceKey(91, 2, -1, 2))
			case "truncated":
				store.pieces[key] = []byte("efg")
			case "long":
				store.pieces[key] = []byte("efghx")
			case "same length corrupt":
				store.pieces[key] = []byte("xxxx")
			case "empty":
				store.pieces[key] = nil
			case "short list":
				meta.PieceChecksumList = meta.PieceChecksumList[:2]
			case "long list":
				meta.PieceChecksumList = append(meta.PieceChecksumList, []byte("extra"))
			case "reordered":
				meta.PieceChecksumList[0], meta.PieceChecksumList[1] = meta.PieceChecksumList[1], meta.PieceChecksumList[0]
			}
			db.EXPECT().GetObjectIntegrity(uint64(91), int32(-1)).Return(meta, nil)
			ok, err := verifyIntegrity(m, object, -1)
			require.NoError(t, err)
			require.False(t, ok)
		})
	}
}
func TestVerifyIntegrityStoreUnavailableRemainsUnknown(t *testing.T) {
	m, object, _, db, _, store, meta := recoveryFixture(t, -1)
	store.err = errors.New("access denied")
	db.EXPECT().GetObjectIntegrity(uint64(91), int32(-1)).Return(meta, nil)
	ok, err := verifyIntegrity(m, object, -1)
	require.ErrorIs(t, err, store.err)
	require.False(t, ok)
}
func TestRecoveryFenceOldDispatchCannotRemoveNewDispatch(t *testing.T) {
	m, object, params, _, _, _, _ := recoveryFixture(t, -1)
	task := &gfsptask.GfSpRecoverPieceTask{}
	task.InitRecoverPieceTask(object, params, 85, 0, -1, 4, 50, 5)
	task.SetBySuccessorSP(true)
	require.NoError(t, m.HandleRecoverPieceTask(context.Background(), task))
	first, err := m.dispatchRecovery(context.Background(), task)
	require.NoError(t, err)
	second, err := m.dispatchRecovery(context.Background(), first)
	require.NoError(t, err)
	first.SetRecoverDone()
	require.ErrorIs(t, m.HandleRecoverPieceTask(context.Background(), first), ErrCanceledTask)
	require.True(t, m.recoveryQueue.Has(second.Key()))
	first.SetError(errors.New("late error"))
	require.ErrorIs(t, m.HandleRecoverPieceTask(context.Background(), first), ErrCanceledTask)
	require.True(t, m.recoveryQueue.Has(second.Key()))
}

func TestRecoveryFinalizationBootstrapsCompleteIntegrity(t *testing.T) {
	for _, failPublish := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "database unavailable"}[failPublish], func(t *testing.T) {
			m, object, params, db, _, store, meta := recoveryFixture(t, -1)
			delete(store.pieces, "s91_s0_v2")
			delete(store.pieces, "s91_s1_v2")
			delete(store.pieces, "s91_s2_v2")
			published := make(chan struct{})
			var attempt *recoveryAttempt
			for i, data := range [][]byte{[]byte("abcd"), []byte("efgh"), []byte("i")} {
				task := &gfsptask.GfSpRecoverPieceTask{}
				task.InitRecoverPieceTask(object, params, 85, uint32(i), -1, 4, 50, 5)
				task.SetBySuccessorSP(true)
				require.NoError(t, m.HandleRecoverPieceTask(context.Background(), task))
				dispatched, err := m.dispatchRecovery(context.Background(), task)
				require.NoError(t, err)
				key, err := gfsptask.RecoveryStageKey(dispatched)
				require.NoError(t, err)
				require.NoError(t, store.PutPiece(context.Background(), key, data))
				if i == 2 {
					attempt = m.findRecovery(dispatched)
					db.EXPECT().PublishRecoveryIntegrity(gomock.Any()).DoAndReturn(func(actual *spdb.IntegrityMeta) error {
						require.Equal(t, meta, actual)
						close(published)
						if failPublish {
							return errors.New("database unavailable")
						}
						return nil
					})
					if !failPublish {
						db.EXPECT().GetObjectIntegrity(uint64(91), int32(-1)).Return(meta, nil)
						db.EXPECT().DeleteRecoverFailedObject(uint64(91)).Return(nil)
					}
				}
				dispatched.SetRecoverDone()
				require.NoError(t, m.HandleRecoverPieceTask(context.Background(), dispatched))
			}
			select {
			case <-published:
			case <-time.After(5 * time.Second):
				t.Fatal("publisher did not reach metadata publication")
			}
			attempt.mu.Lock()
			require.False(t, attempt.finalizing)
			require.Equal(t, failPublish, attempt.active)
			attempt.mu.Unlock()

			require.Equal(t, []byte("abcd"), store.pieces["s91_s0_v2"])
			if failPublish {
				require.False(t, m.recoverObjectStats.isObjectProcessed(91, 2, -1))
			} else {
				require.True(t, m.recoverObjectStats.isObjectProcessed(91, 2, -1))
			}
		})
	}
}

func TestRecoveryCapacityReservationsSurviveManagerRestart(t *testing.T) {
	t.Setenv("RECOVERY_STAGING_LEDGER_DIR", t.TempDir())
	t.Setenv("RECOVERY_STAGING_CAPACITY_BYTES", "9")
	m := new(ManageModular)
	_, err := m.reserveRecoveryCapacity(9)
	require.NoError(t, err)
	restarted := new(ManageModular)
	_, err = restarted.reserveRecoveryCapacity(1)
	require.Error(t, err)
}

func TestVerifyIntegrityEmptyObjectUsesUploadProtocolVector(t *testing.T) {
	m, object, _, db, _, _, _ := recoveryFixture(t, -1)
	// Independent vector confirmed with the SDK CreateFolder upload hash path:
	// ComputeIntegrityHash(empty reader,4,1,1,true), N=0, all roots SHA256(empty).
	root, err := hex.DecodeString("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	require.NoError(t, err)
	object.PayloadSize = 0
	object.Checksums = [][]byte{root, root}
	db.EXPECT().GetObjectIntegrity(uint64(91), int32(-1)).Return(&spdb.IntegrityMeta{PieceChecksumList: [][]byte{}}, nil)
	ok, err := verifyIntegrity(m, object, -1)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestVerifyGVGUnknownReadDoesNotAdvanceOrComplete(t *testing.T) {
	m, object, _, db, _, store, meta := recoveryFixture(t, -1)
	ctrl := gomock.NewController(t)
	client := gfspclient.NewMockGfSpClientAPI(ctrl)
	m.baseApp.SetGfSpClient(client)
	objects := []*metatypes.ObjectDetails{{Object: &metatypes.Object{ObjectInfo: object}}}
	client.EXPECT().ListObjectsInGVG(gomock.Any(), uint32(7), uint64(0), verifyGVGQueryLimit).Return(objects, nil).Times(2)
	client.EXPECT().ListObjectsInGVG(gomock.Any(), uint32(7), uint64(91), verifyGVGQueryLimit).Return(nil, nil)
	db.EXPECT().GetObjectIntegrity(uint64(91), int32(-1)).Return(meta, nil).Times(2)
	db.EXPECT().UpdateRecoverGVGStats(gomock.Any()).Return(nil)
	scheduler, _ := NewVerifyGVGScheduler(m, 7, -1)
	stats := &spdb.RecoverGVGStats{VirtualGroupID: 7, Status: spdb.Processed}
	store.err = errors.New("backend unavailable")
	complete, err := scheduler.verifyPass(stats)
	require.Error(t, err)
	require.False(t, complete)
	require.Zero(t, scheduler.curStartAfter)
	require.Equal(t, spdb.Processed, stats.Status)
	store.err = nil
	complete, err = scheduler.verifyPass(stats)
	require.NoError(t, err)
	require.False(t, complete)
	require.Equal(t, uint64(91), scheduler.curStartAfter)
	complete, err = scheduler.verifyPass(stats)
	require.NoError(t, err)
	require.True(t, complete)
	require.Equal(t, spdb.Completed, stats.Status)
}
func TestVerifyGVGMissingFailedRowCannotProvePossession(t *testing.T) {
	m, _, _, db, _, _, _ := recoveryFixture(t, -1)
	ctrl := gomock.NewController(t)
	client := gfspclient.NewMockGfSpClientAPI(ctrl)
	m.baseApp.SetGfSpClient(client)
	client.EXPECT().ListObjectsInGVG(gomock.Any(), uint32(7), uint64(0), verifyGVGQueryLimit).Return(nil, nil)
	// There is no failure-row lookup or completion update: actual data still needs verification.
	db.EXPECT().GetObjectIntegrity(uint64(91), int32(-1)).Return(nil, gorm.ErrRecordNotFound)
	scheduler, _ := NewVerifyGVGScheduler(m, 7, -1)
	scheduler.verifyFailedObjects[91] = struct{}{}
	stats := &spdb.RecoverGVGStats{VirtualGroupID: 7, Status: spdb.Processed}
	complete, err := scheduler.verifyPass(stats)
	require.NoError(t, err)
	require.False(t, complete)
	require.Contains(t, scheduler.verifyFailedObjects, uint64(91))
	require.Equal(t, spdb.Processed, stats.Status)
}

func TestRecoveryFenceExhaustedOrdinaryReportReleasesRecovery(t *testing.T) {
	m, object, params, _, _, _, _ := recoveryFixture(t, -1)
	task := &gfsptask.GfSpRecoverPieceTask{}
	task.InitRecoverPieceTask(object, params, 85, 0, -1, 4, 50, 1)
	require.NoError(t, m.HandleRecoverPieceTask(context.Background(), task))
	dispatched, err := m.dispatchRecovery(context.Background(), task)
	require.NoError(t, err)
	dispatched.SetError(errors.New("source unavailable"))
	require.NoError(t, m.HandleRecoverPieceTask(context.Background(), dispatched))
	require.Zero(t, m.recoveryQueue.Len(), "exhausted ordinary task must release queue capacity")
	require.Empty(t, m.recoveryTaskMap, "exhausted ordinary task must release its unfinished marker")
	fresh := &gfsptask.GfSpRecoverPieceTask{}
	fresh.InitRecoverPieceTask(object, params, 85, 0, -1, 4, 50, 1)
	require.NoError(t, m.HandleRecoverPieceTask(context.Background(), fresh), "later user-triggered recovery must be admissible")
}

func TestRecoveryFenceNewVersionReplacesOldAttempt(t *testing.T) {
	m, object, params, _, _, _, _ := recoveryFixture(t, -1)
	task := &gfsptask.GfSpRecoverPieceTask{}
	task.InitRecoverPieceTask(object, params, 85, 0, -1, 4, 50, 2)
	task.SetBySuccessorSP(true)
	require.NoError(t, m.HandleRecoverPieceTask(context.Background(), task))
	old, err := m.dispatchRecovery(context.Background(), task)
	require.NoError(t, err)
	next := copyRecovery(task)
	next.SetRecoveryFence(nil)
	next.ObjectInfo.Version++
	chain := consensus.NewMockConsensus(gomock.NewController(t))
	chain.EXPECT().QueryObjectInfoByID(gomock.Any(), "91").Return(next.ObjectInfo, nil).AnyTimes()
	chain.EXPECT().QueryStorageParamsByTimestamp(gomock.Any(), int64(123)).Return(params, nil).AnyTimes()
	m.baseApp.SetConsensus(chain)
	require.NoError(t, m.HandleRecoverPieceTask(context.Background(), next))
	require.False(t, m.recoveryQueue.Has(old.Key()))
	require.True(t, m.recoveryQueue.Has(next.Key()))
	old.SetError(errors.New("late failure"))
	require.ErrorIs(t, m.HandleRecoverPieceTask(context.Background(), old), ErrCanceledTask)
	require.True(t, m.recoveryQueue.Has(next.Key()))
}

type recoveryFaultStore struct {
	*recoveryMemoryStore
	mode    string
	entered chan struct{}
	once    sync.Once
	reads   map[string]int
}

func (s *recoveryFaultStore) GetPiece(ctx context.Context, key string, offset, limit int64) ([]byte, error) {
	s.once.Do(func() {
		if s.entered != nil {
			close(s.entered)
		}
	})
	if s.reads != nil {
		s.reads[key]++
	}
	if s.mode == "canonical reread" && key == "s91_s1_v2" {
		return []byte("bad!"), nil
	}
	data, err := s.recoveryMemoryStore.GetPiece(ctx, key, offset, limit)
	if s.mode == "changed stage" && len(key) > 7 && key[:7] == "rstage-" && s.reads[key] > 1 {
		return []byte("bad!"), nil
	}
	return data, err
}
func (s *recoveryFaultStore) PutPiece(ctx context.Context, key string, data []byte) error {
	if s.mode == "canonical put" && key == "s91_s1_v2" {
		return errors.New("write unavailable")
	}
	return s.recoveryMemoryStore.PutPiece(ctx, key, data)
}

func TestRecoveryFinalizationFailureDoesNotExposeSuccess(t *testing.T) {
	for _, mode := range []string{"missing stage", "corrupt stage", "long stage", "changed stage", "canonical put", "canonical reread", "metadata readback", "failed cleanup"} {
		t.Run(mode, func(t *testing.T) {
			m, object, params, db, _, memory, meta := recoveryFixture(t, -1)
			task := &gfsptask.GfSpRecoverPieceTask{}
			task.InitRecoverPieceTask(object, params, 85, 1, -1, 4, 50, 5)
			require.NoError(t, m.HandleRecoverPieceTask(context.Background(), task))
			dispatched, err := m.dispatchRecovery(context.Background(), task)
			require.NoError(t, err)
			key, err := gfsptask.RecoveryStageKey(dispatched)
			require.NoError(t, err)
			data := []byte("efgh")
			if mode == "corrupt stage" {
				data = []byte("bad!")
			}
			if mode == "long stage" {
				data = []byte("efgh!")
			}
			if mode != "missing stage" {
				require.NoError(t, memory.PutPiece(context.Background(), key, data))
			}
			store := &recoveryFaultStore{recoveryMemoryStore: memory, mode: mode, entered: make(chan struct{}), reads: make(map[string]int)}
			m.baseApp.SetPieceStore(store)
			if mode == "metadata readback" || mode == "failed cleanup" {
				db.EXPECT().PublishRecoveryIntegrity(gomock.Any()).Return(nil)
				if mode == "metadata readback" {
					db.EXPECT().GetObjectIntegrity(uint64(91), int32(-1)).Return(nil, errors.New("readback failed"))
				} else {
					db.EXPECT().GetObjectIntegrity(uint64(91), int32(-1)).Return(meta, nil)
					db.EXPECT().DeleteRecoverFailedObject(uint64(91)).Return(errors.New("failed cleanup unavailable"))
				}
			}
			attempt := m.findRecovery(dispatched)
			dispatched.SetRecoverDone()
			require.NoError(t, m.HandleRecoverPieceTask(context.Background(), dispatched))
			select {
			case <-store.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("finalizer did not read candidate")
			}
			attempt.mu.Lock()
			require.True(t, attempt.active, "publication failure must preserve the unresolved attempt")
			require.False(t, attempt.finalizing)
			attempt.mu.Unlock()
			require.NotEmpty(t, m.recoveryTaskMap, "ordinary task must stay unfinished through publication failure")
			// A duplicate success cannot retry publication or clear the marker.
			require.ErrorIs(t, m.HandleRecoverPieceTask(context.Background(), dispatched), ErrCanceledTask)
		})
	}
}

func TestRecoveryFinalizationRetriesAcceptedStagingWithoutRedispatch(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			m, object, params, db, _, store, meta := recoveryFixture(t, -1)
			initial := &gfsptask.GfSpRecoverPieceTask{}
			initial.InitRecoverPieceTask(object, params, 85, 1, -1, 4, 50, 5)
			require.NoError(t, m.HandleRecoverPieceTask(context.Background(), initial))
			dispatched, err := m.dispatchRecovery(context.Background(), initial)
			require.NoError(t, err)
			key, err := gfsptask.RecoveryStageKey(dispatched)
			require.NoError(t, err)
			require.NoError(t, store.PutPiece(context.Background(), key, []byte("efgh")))
			attempted := make(chan struct{})
			db.EXPECT().PublishRecoveryIntegrity(gomock.Any()).DoAndReturn(func(*spdb.IntegrityMeta) error { close(attempted); return errors.New("temporary database outage") })
			attempt := m.findRecovery(dispatched)
			dispatched.SetRecoverDone()
			require.NoError(t, m.HandleRecoverPieceTask(context.Background(), dispatched))
			<-attempted
			attempt.mu.Lock()
			require.True(t, attempt.active)
			require.False(t, attempt.finalizing)
			attempt.started = time.Now().Add(-3 * time.Minute)
			attempt.mu.Unlock()
			require.True(t, m.recoveryFullyQueued(object, -1), "publication unknown must not consume a fresh download attempt")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db.EXPECT().PublishRecoveryIntegrity(gomock.Any()).Return(nil)
			db.EXPECT().GetObjectIntegrity(uint64(91), int32(-1)).Return(meta, nil)
			db.EXPECT().DeleteRecoverFailedObject(uint64(91)).DoAndReturn(func(uint64) error {
				if expired {
					cancel()
				}
				return nil
			})
			m.retryRecoveryFinalizations(ctx)
			if expired {
				require.NotEmpty(t, m.recoveryTaskMap, "cleanup returning after the context expires must not expose completion")
				attempt.mu.Lock()
				require.True(t, attempt.active)
				attempt.mu.Unlock()
			} else {
				require.Empty(t, m.recoveryTaskMap, "the retry worker must complete ordinary recovery after the outage")
				require.Nil(t, m.findRecovery(dispatched))
			}
			require.Equal(t, int64(1), dispatched.GetRetry(), "publication retry must reuse the original dispatch")
		})
	}
}

func TestRecoveryFinalizationExpiredSnapshotReleasesMonitor(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprint(deleted), func(t *testing.T) {
			m, object, params, _, _, store, _ := recoveryFixture(t, -1)
			var current *gfsptask.GfSpRecoverPieceTask
			for i, data := range [][]byte{[]byte("abcd"), []byte("efgh"), []byte("i")} {
				task := &gfsptask.GfSpRecoverPieceTask{}
				task.InitRecoverPieceTask(object, params, 85, uint32(i), -1, 4, 50, 5)
				task.SetBySuccessorSP(true)
				require.NoError(t, m.HandleRecoverPieceTask(context.Background(), task))
				dispatched, err := m.dispatchRecovery(context.Background(), task)
				require.NoError(t, err)
				key, err := gfsptask.RecoveryStageKey(dispatched)
				require.NoError(t, err)
				require.NoError(t, store.PutPiece(context.Background(), key, data))
				current = dispatched
				if i < 2 {
					dispatched.SetRecoverDone()
					require.NoError(t, m.HandleRecoverPieceTask(context.Background(), dispatched))
				}
			}
			entered := make(chan struct{})
			chain := consensus.NewMockConsensus(gomock.NewController(t))
			chain.EXPECT().QueryObjectInfoByID(gomock.Any(), "91").DoAndReturn(func(context.Context, string) (*st.ObjectInfo, error) {
				close(entered)
				if deleted {
					return nil, st.ErrNoSuchObject
				}
				changed := *object
				changed.Version++
				return &changed, nil
			})
			m.baseApp.SetConsensus(chain)
			attempt := m.findRecovery(current)
			current.SetRecoverDone()
			require.NoError(t, m.HandleRecoverPieceTask(context.Background(), current))
			<-entered
			attempt.mu.Lock()
			require.False(t, attempt.finalizing)
			attempt.mu.Unlock()
			require.False(t, m.recoveryPublicationPending(91, 2, -1), "a deleted or superseded chain snapshot must not hold the initial monitor forever")
		})
	}
}

func TestRecoveryCapacityReusedAfterTerminalStageCleanup(t *testing.T) {
	for _, mode := range []string{"clean", "stage delete failure", "redispatched"} {
		t.Run(mode, func(t *testing.T) {
			m, object, params, db, _, memory, meta := recoveryFixture(t, -1)
			t.Setenv("RECOVERY_STAGING_CAPACITY_BYTES", "13") // one attempt plus one redispatch
			store := &recoveryFaultStore{recoveryMemoryStore: memory, mode: mode}
			m.baseApp.SetPieceStore(store)
			for round := 0; round < 2; round++ {
				initial := &gfsptask.GfSpRecoverPieceTask{}
				initial.InitRecoverPieceTask(object, params, 85, 1, -1, 4, 50, 5)
				err := m.HandleRecoverPieceTask(context.Background(), initial)
				if round == 1 && mode != "clean" {
					require.ErrorContains(t, err, "capacity exhausted", "unconfirmed cleanup and orphan writers must retain capacity")
					return
				}
				require.NoError(t, err, "successful cleanup must allow repeated recovery under the same budget")
				current, err := m.dispatchRecovery(context.Background(), initial)
				require.NoError(t, err)
				if mode == "redispatched" {
					current, err = m.dispatchRecovery(context.Background(), current)
					require.NoError(t, err)
				}
				key, err := gfsptask.RecoveryStageKey(current)
				require.NoError(t, err)
				require.NoError(t, memory.PutPiece(context.Background(), key, []byte("efgh")))
				published := make(chan struct{})
				db.EXPECT().PublishRecoveryIntegrity(gomock.Any()).DoAndReturn(func(*spdb.IntegrityMeta) error { close(published); return nil })
				db.EXPECT().GetObjectIntegrity(uint64(91), int32(-1)).Return(meta, nil)
				db.EXPECT().DeleteRecoverFailedObject(uint64(91)).Return(nil)
				attempt := m.findRecovery(current)
				current.SetRecoverDone()
				require.NoError(t, m.HandleRecoverPieceTask(context.Background(), current))
				<-published
				attempt.mu.Lock()
				require.False(t, attempt.active)
				attempt.mu.Unlock()
			}
		})
	}
}

func (s *recoveryFaultStore) DeletePiece(ctx context.Context, key string) error {
	if s.mode == "stage delete failure" {
		return errors.New("delete denied")
	}
	return s.recoveryMemoryStore.DeletePiece(ctx, key)
}

func TestRecoveryAdmissionLedgerDoesNotBlockUnrelatedDispatch(t *testing.T) {
	m, object, params, _, chain, _, _ := recoveryFixture(t, -1)
	initial := &gfsptask.GfSpRecoverPieceTask{}
	initial.InitRecoverPieceTask(object, params, 85, 1, -1, 4, 50, 5)
	require.NoError(t, m.HandleRecoverPieceTask(context.Background(), initial))
	other := *object
	other.Id = sdkmath.NewUint(92)
	entered := make(chan struct{})
	chain.EXPECT().QueryObjectInfoByID(gomock.Any(), "92").DoAndReturn(func(context.Context, string) (*st.ObjectInfo, error) { close(entered); return &other, nil })
	next := &gfsptask.GfSpRecoverPieceTask{}
	next.InitRecoverPieceTask(&other, params, 85, 1, -1, 4, 50, 5)
	m.recoveryCapacityMu.Lock() // suspend ledger IO without suspending registry access
	done := make(chan error, 1)
	go func() { done <- m.HandleRecoverPieceTask(context.Background(), next) }()
	defer func() { m.recoveryCapacityMu.Unlock(); require.NoError(t, <-done) }()
	<-entered
	// Wait for the gated admission placeholder, then exercise an existing slot.
	dispatched := make(chan error, 1)
	go func() {
		for m.findRecovery(next) == nil {
			runtime.Gosched()
		}
		_, err := m.dispatchRecovery(context.Background(), initial)
		dispatched <- err
	}()
	select {
	case err := <-dispatched:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ledger IO blocked unrelated dispatch")
	}
}

func TestRecoveryPublicationExhaustionPersistsFailureAndReleasesSlot(t *testing.T) {
	for _, full := range []bool{false, true} {
		for _, mode := range []string{"canonical put", "failed cleanup"} {
			t.Run(fmt.Sprintf("successor=%t/%s", full, mode), func(t *testing.T) {
				m, object, params, db, _, memory, meta := recoveryFixture(t, -1)
				store := &recoveryFaultStore{recoveryMemoryStore: memory, mode: mode, entered: make(chan struct{}), reads: make(map[string]int)}
				m.baseApp.SetPieceStore(store)
				if mode == "failed cleanup" {
					db.EXPECT().PublishRecoveryIntegrity(gomock.Any()).Return(nil).Times(5)
					db.EXPECT().GetObjectIntegrity(uint64(91), int32(-1)).Return(meta, nil).Times(5)
					db.EXPECT().DeleteRecoverFailedObject(uint64(91)).Return(errors.New("delete failed row denied")).Times(5)
				}
				// Persistence failure must keep the slot unresolved and retry persistence,
				// without performing a sixth publication or exposing false completion.
				gomock.InOrder(
					db.EXPECT().InsertRecoverFailedObject(&spdb.RecoverFailedObject{ObjectID: 91, VirtualGroupID: 50, RedundancyIndex: -1}).Return(errors.New("database unavailable")),
					db.EXPECT().InsertRecoverFailedObject(&spdb.RecoverFailedObject{ObjectID: 91, VirtualGroupID: 50, RedundancyIndex: -1}).Return(nil),
				)
				var current *gfsptask.GfSpRecoverPieceTask
				for i, data := range [][]byte{[]byte("abcd"), []byte("efgh"), []byte("i")} {
					if !full && i != 1 {
						continue
					}
					initial := &gfsptask.GfSpRecoverPieceTask{}
					initial.InitRecoverPieceTask(object, params, 85, uint32(i), -1, 4, 50, 5)
					initial.SetBySuccessorSP(full)
					initial.SetGVGID(50)
					require.NoError(t, m.HandleRecoverPieceTask(context.Background(), initial))
					var err error
					current, err = m.dispatchRecovery(context.Background(), initial)
					require.NoError(t, err)
					key, err := gfsptask.RecoveryStageKey(current)
					require.NoError(t, err)
					require.NoError(t, memory.PutPiece(context.Background(), key, data))
					current.SetRecoverDone()
					require.NoError(t, m.HandleRecoverPieceTask(context.Background(), current))
				}
				attempt := m.findRecovery(current)
				<-store.entered
				attempt.mu.Lock()
				require.False(t, attempt.finalizing)
				attempt.mu.Unlock()
				for i := 0; i < 4; i++ {
					m.retryRecoveryFinalizations(context.Background())
				}
				require.True(t, m.recoveryPublicationPending(91, 2, -1), "failed-row persistence must finish before retirement")
				m.retryRecoveryFinalizations(context.Background())
				require.False(t, m.recoveryPublicationPending(91, 2, -1), "exhausted publication must release monitor")
				require.Nil(t, m.findRecovery(current))
				require.Empty(t, m.recoveryTaskMap)
				if full {
					stats := m.recoverObjectStats
					require.True(t, stats.isObjectProcessed(91, 2, -1))
				}
				// A successor's complete retry is now admitted; the scheduler owns RetryTime
				// and increments it once after queueFailedObjectRecovery succeeds.
				scheduler := &RecoverFailedObjectScheduler{manager: m}
				require.True(t, scheduler.queueFailedObjectRecovery(object, params, &spdb.RecoverFailedObject{ObjectID: 91, VirtualGroupID: 50, RedundancyIndex: -1, RetryTime: 4}, 4, 3))
			})
		}
	}
}

func TestRecoveryFailedReportRemainsRetryableUntilFailureIsDurable(t *testing.T) {
	m, object, params, db, _, _, _ := recoveryFixture(t, -1)
	initial := &gfsptask.GfSpRecoverPieceTask{}
	initial.InitRecoverPieceTask(object, params, 85, 0, -1, 4, 50, 1)
	initial.SetBySuccessorSP(true)
	require.NoError(t, m.HandleRecoverPieceTask(context.Background(), initial))
	current, err := m.dispatchRecovery(context.Background(), initial)
	require.NoError(t, err)
	db.EXPECT().InsertRecoverFailedObject(gomock.Any()).Return(errors.New("database unavailable"))
	current.SetError(errors.New("source unavailable"))
	require.Error(t, m.HandleRecoverPieceTask(context.Background(), current))
	require.False(t, m.recoverObjectStats.isObjectProcessed(91, 2, -1))
	queued := m.recoveryQueue.PopByKey(current.Key())
	require.NotNil(t, queued)
	m.repushRecovery(current)
	require.True(t, m.recoveryQueue.Has(current.Key()), "failed persistence must not make the dispatch ineligible for repush")
}
