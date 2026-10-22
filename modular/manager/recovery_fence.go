package manager

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	"github.com/mocachain/moca-storage-provider/core/spdb"
	coretask "github.com/mocachain/moca-storage-provider/core/task"
)

// recoveryAttempt owns all dispatches and publication for one object/redundancy.
// Queue callbacks never acquire its lock: lock order is attempt -> queue.
// Invalidated entries remain inactive even if a taskCh consumer retains a pointer.
type recoveryAttempt struct {
	mu         sync.Mutex
	active     bool
	full       bool
	finalizing bool
	canonical  *gfsptask.GfSpRecoverPieceTask
	segments   map[uint32]*recoveryDispatch
	count      uint32
	started    time.Time
}
type recoveryDispatch struct {
	task       *gfsptask.GfSpRecoverPieceTask
	dispatched bool
	accepted   bool
	failed     bool
}

func recoverySlot(t coretask.RecoveryPieceTask) string {
	return fmt.Sprintf("%s/%d", t.GetObjectInfo().Id.String(), t.GetEcIdx())
}
func cloneRecovery(t coretask.RecoveryPieceTask) (*gfsptask.GfSpRecoverPieceTask, error) {
	// Serialize the interface, rather than retaining mutable inbound task pointers.
	source, ok := t.(*gfsptask.GfSpRecoverPieceTask)
	if !ok {
		return nil, ErrDanglingTask
	}
	wire, err := source.Marshal()
	if err != nil {
		return nil, err
	}
	result := new(gfsptask.GfSpRecoverPieceTask)
	if err = result.Unmarshal(wire); err != nil {
		return nil, err
	}
	return result, nil
}
func (m *ManageModular) findRecovery(t coretask.RecoveryPieceTask) *recoveryAttempt {
	m.recoveryStateMu.Lock()
	defer m.recoveryStateMu.Unlock()
	return m.recoveryAttempts[recoverySlot(t)]
}
func (m *ManageModular) registerRecovery(ctx context.Context, t coretask.RecoveryPieceTask) error {
	snapshot, err := cloneRecovery(t)
	if err != nil {
		return err
	}
	count, err := gfsptask.RecoverySegmentCount(snapshot.ObjectInfo, snapshot.StorageParams, snapshot.EcIdx)
	if err != nil || (snapshot.SegmentIdx >= count && !(count == 0 && snapshot.BySuccessorSP() && snapshot.SegmentIdx == 0)) {
		return fmt.Errorf("invalid recovery task: %v", err)
	}
	// Admission uses the same authoritative snapshot and historical parameters as execution.
	previous := m.findRecovery(t)
	replace := false
	if previous != nil {
		previous.mu.Lock()
		replace = previous.active && (time.Since(previous.started) > 2*time.Minute || !gfsptask.RecoverySnapshotEqual(previous.canonical.ObjectInfo, snapshot.ObjectInfo))
		previous.mu.Unlock()
	}
	if previous == nil || replace {
		object, params, err := m.loadRecoverySnapshot(ctx, snapshot.ObjectInfo, snapshot.EcIdx)
		if err != nil {
			return err
		}
		if !gfsptask.RecoverySnapshotEqual(object, snapshot.ObjectInfo) || !proto.Equal(params, snapshot.StorageParams) {
			return ErrCanceledTask
		}
		if replace {
			previous.mu.Lock()
			m.retireRecoveryLocked(previous)
			previous.mu.Unlock()
		}
	}

	slot := recoverySlot(t)
	m.recoveryStateMu.Lock()
	if m.recoveryAttempts == nil {
		m.recoveryAttempts = make(map[string]*recoveryAttempt)
	}
	a := m.recoveryAttempts[slot]
	created := a == nil
	if a == nil {
		if len(m.recoveryAttempts) >= m.recoveryQueue.Cap() {
			m.recoveryStateMu.Unlock()
			return ErrExceedTask
		}
		budget := snapshot.ObjectInfo.PayloadSize
		if snapshot.EcIdx >= 0 && count > 0 {
			shardCount := uint64(snapshot.StorageParams.GetRedundantDataChunkNum())
			segmentSize := snapshot.StorageParams.GetMaxSegmentSize()
			tail := snapshot.ObjectInfo.PayloadSize % segmentSize
			if tail == 0 {
				tail = segmentSize
			}
			shardSize := func(size uint64) uint64 { return size/shardCount + boolRecoveryRemainder(size%shardCount != 0) }
			budget = uint64(count-1)*shardSize(segmentSize) + shardSize(tail)
		}

		if err = m.reserveRecoveryCapacity(budget); err != nil {
			m.recoveryStateMu.Unlock()
			return err
		}
		a = &recoveryAttempt{active: true, full: snapshot.BySuccessorSP(), canonical: snapshot, segments: make(map[uint32]*recoveryDispatch), count: count, started: time.Now()}
		m.recoveryAttempts[slot] = a
	}
	m.recoveryStateMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.active || a.finalizing || !gfsptask.RecoverySnapshotEqual(a.canonical.ObjectInfo, snapshot.ObjectInfo) || !proto.Equal(a.canonical.StorageParams, snapshot.StorageParams) || a.canonical.GetGVGID() != snapshot.GetGVGID() || a.full != snapshot.BySuccessorSP() {
		return ErrCanceledTask
	}
	if _, ok := a.segments[snapshot.SegmentIdx]; ok {
		return ErrRepeatedTask
	}
	if count == 0 {
		if created {
			m.recoverObjectStats.put(snapshot.ObjectInfo.Id.Uint64(), snapshot.ObjectInfo.Version, 0, snapshot.EcIdx)
		}
		if !a.finalizing {
			a.finalizing = true
			go m.finalizeRecovery(a)
		}
		return nil
	}
	token := make([]byte, gfsptask.RecoveryFenceSize)
	if _, err = rand.Read(token); err != nil {
		return err
	}
	snapshot.SetRecoveryFence(token)
	d := &recoveryDispatch{task: snapshot}
	a.segments[snapshot.SegmentIdx] = d
	// A new attempt clears all terminal evidence of its predecessor.
	if snapshot.BySuccessorSP() && created {
		m.recoverObjectStats.put(snapshot.ObjectInfo.Id.Uint64(), snapshot.ObjectInfo.Version, count, snapshot.EcIdx)
	}

	if err = m.recoveryQueue.Push(copyRecovery(snapshot)); err != nil {
		delete(a.segments, snapshot.SegmentIdx)
		if len(a.segments) == 0 {
			if snapshot.BySuccessorSP() {
				m.recoverObjectStats.remove(snapshot.ObjectInfo.Id.Uint64(), snapshot.ObjectInfo.Version, snapshot.EcIdx)
			}
			m.retireRecoveryLocked(a)
		}
		return err
	}
	// Existing callers retain the admitted key, but never share the registered snapshot.
	t.SetRecoveryFence(token)
	m.recoverMtx.Lock()
	if m.recoveryTaskMap == nil {
		m.recoveryTaskMap = make(map[string]string)
	}
	m.recoveryTaskMap[snapshot.Key().String()] = snapshot.Key().String()
	m.recoverMtx.Unlock()
	return nil
}
func recoveryMatches(d *recoveryDispatch, t coretask.RecoveryPieceTask) bool {
	return d != nil && len(t.GetRecoveryFence()) == gfsptask.RecoveryFenceSize && bytes.Equal(d.task.GetRecoveryFence(), t.GetRecoveryFence()) && gfsptask.RecoverySnapshotEqual(d.task.ObjectInfo, t.GetObjectInfo()) && proto.Equal(d.task.StorageParams, t.GetStorageParams()) && d.task.GetEcIdx() == t.GetEcIdx() && d.task.GetSegmentIdx() == t.GetSegmentIdx() && d.task.GetGVGID() == t.GetGVGID() && d.task.BySuccessorSP() == t.BySuccessorSP()
}

// dispatchRecovery rotates the token for every dispatch, including timeouts.
// An old backup or taskCh item can never register itself again.
func (m *ManageModular) dispatchRecovery(ctx context.Context, t coretask.RecoveryPieceTask) (*gfsptask.GfSpRecoverPieceTask, error) {
	a := m.findRecovery(t)
	if a == nil {
		return nil, ErrCanceledTask
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	d := a.segments[t.GetSegmentIdx()]
	if !a.active || !recoveryMatches(d, t) || d.accepted {
		return nil, ErrCanceledTask
	}
	if d.task.ExceedRetry() {
		d.failed = true
		if err := m.failRecoveryLocked(a); err != nil {
			return nil, err
		}
		return nil, ErrCanceledTask
	}
	if d.task.GetRetry() > 0 {
		size, err := m.recoveryPieceSize(d.task.ObjectInfo, d.task.StorageParams, d.task.EcIdx, d.task.SegmentIdx)
		if err != nil {
			return nil, err
		}
		if err = m.reserveRecoveryCapacity(uint64(size)); err != nil {
			return nil, err
		}
	}
	old := d.task
	next := copyRecovery(old)
	token := make([]byte, gfsptask.RecoveryFenceSize)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	next.SetRecoveryFence(token)
	next.IncRetry()
	next.SetError(nil)
	next.SetUpdateTime(time.Now().Unix())
	next.SetAddress(t.GetAddress())
	// No queue callback may call back into the registry while this gate is held.
	m.recoveryQueue.PopByKey(old.Key())
	if err := m.recoveryQueue.Push(copyRecovery(next)); err != nil {
		return nil, err
	}
	d.task = next
	d.dispatched = true
	m.recoverMtx.Lock()
	delete(m.recoveryTaskMap, old.Key().String())
	m.recoveryTaskMap[next.Key().String()] = next.Key().String()
	m.recoverMtx.Unlock()
	return copyRecovery(next), nil
}

func (m *ManageModular) reportRecovery(ctx context.Context, t coretask.RecoveryPieceTask) error {
	a := m.findRecovery(t)
	if a == nil {
		return ErrCanceledTask
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	d := a.segments[t.GetSegmentIdx()]
	if !a.active || !recoveryMatches(d, t) || !d.dispatched || d.accepted || d.failed {
		return ErrCanceledTask
	}
	if t.Error() != nil {
		// Keep the current queue item until timeout makes it eligible for a new dispatch.
		// No terminal report is allowed to supply a replacement task.
		d.dispatched = false
		if d.task.ExceedRetry() {
			d.failed = true
			return m.failRecoveryLocked(a)
		}
		return nil
	}
	if !t.GetRecovered() {
		return ErrCanceledTask
	}
	d.accepted = true
	m.recoveryQueue.PopByKey(d.task.Key())
	complete := !a.full || uint32(len(a.segments)) == a.count
	for _, segment := range a.segments {
		complete = complete && segment.accepted && !segment.failed
	}
	if complete && !a.finalizing {
		a.finalizing = true
		go m.finalizeRecovery(a)
	}
	return nil
}
func (m *ManageModular) repushRecovery(t coretask.RecoveryPieceTask) {
	a := m.findRecovery(t)
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	d := a.segments[t.GetSegmentIdx()]
	if a.active && recoveryMatches(d, t) && !d.accepted && !d.failed {
		_ = m.recoveryQueue.Push(copyRecovery(d.task))
	}
}

func (m *ManageModular) invalidateRecovery(objectID uint64, redundancy int32) {
	slot := fmt.Sprintf("%d/%d", objectID, redundancy)
	m.recoveryStateMu.Lock()
	a := m.recoveryAttempts[slot]
	m.recoveryStateMu.Unlock()
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	m.retireRecoveryLocked(a)
}
func (m *ManageModular) retireRecoveryLocked(a *recoveryAttempt) {
	a.active = false
	for _, d := range a.segments {
		m.recoveryQueue.PopByKey(d.task.Key())
		m.recoverMtx.Lock()
		delete(m.recoveryTaskMap, d.task.Key().String())
		m.recoverMtx.Unlock()
	}
	m.recoveryStateMu.Lock()
	slot := recoverySlot(a.canonical)
	if m.recoveryAttempts[slot] == a {
		delete(m.recoveryAttempts, slot)
	}
	m.recoveryStateMu.Unlock()
}

func copyRecovery(t *gfsptask.GfSpRecoverPieceTask) *gfsptask.GfSpRecoverPieceTask {
	wire, err := t.Marshal()
	if err != nil {
		panic(err)
	}
	result := new(gfsptask.GfSpRecoverPieceTask)
	if err = result.Unmarshal(wire); err != nil {
		panic(err)
	}
	return result
}

func (m *ManageModular) recoveryFullyQueued(id uint64, r int32) bool {
	slot := fmt.Sprintf("%d/%d", id, r)
	m.recoveryStateMu.Lock()
	a := m.recoveryAttempts[slot]
	m.recoveryStateMu.Unlock()
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.active {
		return false
	}
	if time.Since(a.started) > 2*time.Minute {
		m.retireRecoveryLocked(a)
		return false
	}
	return uint32(len(a.segments)) == a.count
}

func boolRecoveryRemainder(present bool) uint64 {
	if present {
		return 1
	}
	return 0
}

// Caller holds the attempt gate. Persist successor failure before exposing a
// terminal state to monitorBatch; a failed DB write leaves dispatch retryable.
func (m *ManageModular) failRecoveryLocked(a *recoveryAttempt) error {
	t := a.canonical
	if a.full {
		if err := m.baseApp.GfSpDB().InsertRecoverFailedObject(&spdb.RecoverFailedObject{ObjectID: t.ObjectInfo.Id.Uint64(), VirtualGroupID: t.GetGVGID(), RedundancyIndex: t.EcIdx}); err != nil {
			return err
		}
		for i := uint32(0); i < a.count; i++ {
			m.recoverObjectStats.addSegmentRecord(t.ObjectInfo.Id.Uint64(), t.ObjectInfo.Version, false, i, t.EcIdx)
		}
	}
	m.recoverMtx.Lock()
	m.recoveryFailedList = append(m.recoveryFailedList, t.ObjectInfo.ObjectName)
	m.recoverMtx.Unlock()
	m.retireRecoveryLocked(a)
	return nil
}
