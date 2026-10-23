package manager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/mocachain/moca-common/go/hash"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	"github.com/mocachain/moca-storage-provider/core/spdb"
	"github.com/mocachain/moca-storage-provider/pkg/log"
	"github.com/mocachain/moca-storage-provider/store/piecestore/storage"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// Deletion is classified only at the chain-query adapter, never from store/index errors.
var errRecoveryObjectDeleted = errors.New("recovery object deleted on chain")
var errRecoverySnapshotChanged = errors.New("recovery object snapshot changed or unstable")

func (m *ManageModular) loadRecoverySnapshot(ctx context.Context, index *storagetypes.ObjectInfo, r int32) (*storagetypes.ObjectInfo, *storagetypes.Params, error) {
	if index == nil || index.Id.IsNil() || r < -1 {
		return nil, nil, fmt.Errorf("invalid recovery object")
	}
	object, err := m.queryRecoveryObject(ctx, index.Id.String())
	if err != nil {
		return nil, nil, err
	}
	if !gfsptask.RecoverySnapshotEqual(index, object) {
		return nil, nil, errRecoverySnapshotChanged
	}
	params, err := m.recoveryParams(ctx, object, r)
	return object, params, err
}

func (m *ManageModular) queryRecoveryObject(ctx context.Context, id string) (*storagetypes.ObjectInfo, error) {
	child, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	object, err := m.baseApp.Consensus().QueryObjectInfoByID(child, id)
	if err != nil {
		// Preserve the repository's chain-query contract until target-chain RPC
		// evidence supports a typed mapping. Transport failures are never deletion.
		code := status.Code(err)
		transport := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || code == codes.Canceled || code == codes.DeadlineExceeded || code == codes.Unavailable
		if child.Err() == nil && !transport && (errors.Is(err, storagetypes.ErrNoSuchObject) || strings.Contains(err.Error(), "No such object")) {
			return nil, errRecoveryObjectDeleted
		}
		return nil, err
	}
	if child.Err() != nil {
		return nil, child.Err()
	}
	if object == nil || object.Id.IsNil() || object.Id.String() != id {
		return nil, fmt.Errorf("invalid chain object response")
	}
	return object, nil
}
func (m *ManageModular) currentRecoverySnapshot(ctx context.Context, id string, r int32) (*storagetypes.ObjectInfo, *storagetypes.Params, error) {
	object, err := m.queryRecoveryObject(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	params, err := m.recoveryParams(ctx, object, r)
	return object, params, err
}
func (m *ManageModular) recoveryParams(ctx context.Context, object *storagetypes.ObjectInfo, r int32) (*storagetypes.Params, error) {
	timestamp := object.GetLatestUpdatedTime()
	m.recoveryStateMu.Lock()
	cached := m.recoveryParamsCache[timestamp]
	m.recoveryStateMu.Unlock()
	if cached != nil {
		if _, err := gfsptask.RecoverySegmentCount(object, cached, r); err != nil {
			return nil, err
		}
		return copyRecoveryParams(cached), ctx.Err()
	}
	child, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	params, err := m.baseApp.Consensus().QueryStorageParamsByTimestamp(child, timestamp)
	if err != nil {
		return nil, err
	}
	if _, err = gfsptask.RecoverySegmentCount(object, params, r); err != nil {
		return nil, err
	}
	if err = child.Err(); err != nil {
		return nil, err
	}
	m.recoveryStateMu.Lock()
	if len(m.recoveryParamsCache) >= 128 || m.recoveryParamsCache == nil {
		m.recoveryParamsCache = make(map[int64]*storagetypes.Params)
	}
	m.recoveryParamsCache[timestamp] = copyRecoveryParams(params)
	m.recoveryStateMu.Unlock()
	return params, nil
}
func copyRecoveryParams(params *storagetypes.Params) *storagetypes.Params {
	wire, err := params.Marshal()
	if err != nil {
		panic(err)
	}
	result := new(storagetypes.Params)
	if err = result.Unmarshal(wire); err != nil {
		panic(err)
	}
	return result
}

func (m *ManageModular) checkRecoveryChain(ctx context.Context, object *storagetypes.ObjectInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	child, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	current, err := m.queryRecoveryObject(child, object.Id.String())
	if err != nil {
		return err
	}
	if !gfsptask.RecoverySnapshotEqual(object, current) {
		return errRecoverySnapshotChanged
	}
	return ctx.Err()
}
func (m *ManageModular) recoveryPieceSize(object *storagetypes.ObjectInfo, params *storagetypes.Params, r int32, i uint32) (int64, error) {
	size := m.baseApp.PieceOp().SegmentPieceSize(object.PayloadSize, i, params.GetMaxSegmentSize())
	if r >= 0 {
		size = m.baseApp.PieceOp().ECPieceSize(object.PayloadSize, i, params.GetMaxSegmentSize(), params.GetRedundantDataChunkNum())
	}
	if size <= 0 || size >= math.MaxInt64 {
		return 0, fmt.Errorf("invalid recovery piece size")
	}
	return size, nil
}
func (m *ManageModular) readRecoveryPiece(ctx context.Context, key string, size int64) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	child, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, err := m.baseApp.PieceStore().GetPiece(child, key, 0, size+1)
	if child.Err() != nil {
		return nil, false, child.Err()
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, storage.ErrNoSuchObject) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if int64(len(data)) != size {
		return nil, false, nil
	}
	return data, true, nil
}

// collectRecoveryPieces has one piece buffer at a time. It can bootstrap metadata
// from a complete staged candidate without trusting old integrity or piece_hash rows.
func (m *ManageModular) collectRecoveryPieces(ctx context.Context, object *storagetypes.ObjectInfo, params *storagetypes.Params, r int32, staged map[uint32]string, expected [][]byte) (*spdb.IntegrityMeta, bool, error) {
	count, err := gfsptask.RecoverySegmentCount(object, params, r)
	if err != nil {
		return nil, false, err
	}
	if expected != nil && uint64(len(expected)) != uint64(count) {
		return nil, false, nil
	}
	meta := &spdb.IntegrityMeta{ObjectID: object.Id.Uint64(), RedundancyIndex: r, PieceChecksumList: make([][]byte, 0, count)}
	for i := uint32(0); i < count; i++ {
		size, err := m.recoveryPieceSize(object, params, r, i)
		if err != nil {
			return nil, false, err
		}
		key := staged[i]
		if key == "" {
			key = m.baseApp.PieceOp().ChallengePieceKey(object.Id.Uint64(), i, r, object.Version)
		}
		data, present, err := m.readRecoveryPiece(ctx, key, size)
		if err != nil || !present {
			return nil, false, err
		}
		checksum := hash.GenerateChecksum(data)
		if expected != nil && !bytes.Equal(checksum, expected[i]) {
			return nil, false, nil
		}
		meta.PieceChecksumList = append(meta.PieceChecksumList, checksum)
		if math.MaxUint64-meta.ObjectSize < uint64(size) {
			return nil, false, fmt.Errorf("recovery object size overflow")
		}
		meta.ObjectSize += uint64(size)
	}
	meta.IntegrityChecksum = hash.GenerateIntegrityHash(meta.PieceChecksumList)
	if !bytes.Equal(meta.IntegrityChecksum, object.Checksums[r+1]) {
		return nil, false, nil
	}
	if err = m.checkRecoveryChain(ctx, object); err != nil {
		return nil, false, err
	}
	return meta, true, nil
}
func (m *ManageModular) verifyRecoveryIntegrity(ctx context.Context, index *storagetypes.ObjectInfo, r int32) (bool, error) {
	object, params, err := m.loadRecoverySnapshot(ctx, index, r)
	if err != nil {
		return false, err
	}
	meta, err := m.baseApp.GfSpDB().GetObjectIntegrity(object.Id.Uint64(), r)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if meta == nil {
		return false, fmt.Errorf("nil recovery integrity row")
	}
	// An empty non-nil list is needed to distinguish zero-piece metadata from bootstrap mode.
	expected := meta.PieceChecksumList
	if expected == nil {
		expected = [][]byte{}
	}
	_, ok, err := m.collectRecoveryPieces(ctx, object, params, r, nil, expected)
	return ok, err
}

func (m *ManageModular) finalizeRecovery(a *recoveryAttempt) {
	m.finalizeRecoveryContext(context.Background(), a)
}

func (m *ManageModular) finalizeRecoveryContext(parent context.Context, a *recoveryAttempt) {
	a.mu.Lock()
	defer a.mu.Unlock()
	defer func() { a.finalizing = false }()
	if !a.active {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 120*time.Second)
	defer cancel()
	t := a.canonical
	stages := make(map[uint32]string, len(a.segments))
	for i, d := range a.segments {
		if !d.accepted || d.failed {
			return
		}
		key, err := gfsptask.RecoveryStageKey(d.task)
		if err != nil {
			return
		}
		stages[i] = key
	}
	object, params, err := m.loadRecoverySnapshot(ctx, t.ObjectInfo, t.EcIdx)
	if err == nil {
		var candidate *spdb.IntegrityMeta
		var valid bool
		candidate, valid, err = m.collectRecoveryPieces(ctx, object, params, t.EcIdx, stages, nil)
		if err == nil && !valid {
			a.ready = false
			err = fmt.Errorf("incomplete or corrupt recovery candidate")
		}
		if err == nil {
			for i := uint32(0); i < a.count; i++ {
				key, ok := stages[i]
				if !ok {
					continue
				}
				size, sizeErr := m.recoveryPieceSize(object, params, t.EcIdx, i)
				if sizeErr != nil {
					err = sizeErr
					break
				}
				data, present, readErr := m.readRecoveryPiece(ctx, key, size)
				if readErr != nil {
					err = readErr
					break
				}
				if !present || !bytes.Equal(hash.GenerateChecksum(data), candidate.PieceChecksumList[i]) {
					a.ready = false
					err = fmt.Errorf("staged recovery piece changed before publication")
					break
				}
				if err = m.checkRecoveryChain(ctx, object); err != nil {
					break
				}
				canonical := m.baseApp.PieceOp().ChallengePieceKey(object.Id.Uint64(), i, t.EcIdx, object.Version)
				if err = m.baseApp.PieceStore().PutPiece(ctx, canonical, data); err != nil {
					break
				}
			}
		}
		if err == nil {
			var published *spdb.IntegrityMeta
			published, valid, err = m.collectRecoveryPieces(ctx, object, params, t.EcIdx, nil, candidate.PieceChecksumList)
			if err == nil && !valid {
				err = fmt.Errorf("canonical recovery verification failed")
			}
			if err == nil {
				err = m.baseApp.GfSpDB().PublishRecoveryIntegrity(published)
			}
			if err == nil {
				var readback *spdb.IntegrityMeta
				readback, err = m.baseApp.GfSpDB().GetObjectIntegrity(object.Id.Uint64(), t.EcIdx)
				if err == nil && !equalRecoveryIntegrity(published, readback) {
					err = fmt.Errorf("recovery integrity readback differs")
				}
			}
			if err == nil {
				err = m.checkRecoveryChain(ctx, object)
			}
		}
	}
	if err != nil {
		if errors.Is(err, errRecoveryObjectDeleted) || errors.Is(err, errRecoverySnapshotChanged) {
			a.ready = false
		}
		log.Errorw("recovery publication remains unresolved", "object_id", t.ObjectInfo.Id.String(), "error", err)
		return
	}
	if err = m.baseApp.GfSpDB().DeleteRecoverFailedObject(t.ObjectInfo.Id.Uint64()); err != nil {
		return
	}
	if ctx.Err() != nil {
		return
	}
	if a.count == 0 {
		m.recoverObjectStats.markEmptyVerified(t.ObjectInfo.Id.Uint64(), t.ObjectInfo.Version, t.EcIdx)
	}
	for i := range a.segments {
		if t.BySuccessorSP() {
			m.recoverObjectStats.addSegmentRecord(t.ObjectInfo.Id.Uint64(), t.ObjectInfo.Version, true, i, t.EcIdx)
		}
	}
	m.retireRecoveryLocked(a)
}
func equalRecoveryIntegrity(a, b *spdb.IntegrityMeta) bool {
	if a == nil || b == nil || a.ObjectID != b.ObjectID || a.RedundancyIndex != b.RedundancyIndex || a.ObjectSize != b.ObjectSize || !bytes.Equal(a.IntegrityChecksum, b.IntegrityChecksum) || len(a.PieceChecksumList) != len(b.PieceChecksumList) {
		return false
	}
	for i := range a.PieceChecksumList {
		if !bytes.Equal(a.PieceChecksumList[i], b.PieceChecksumList[i]) {
			return false
		}
	}
	return true
}
