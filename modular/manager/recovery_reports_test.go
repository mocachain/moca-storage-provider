package manager

import (
	"context"
	"errors"
	"testing"

	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/base/types/gfsplimit"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	"github.com/mocachain/moca-storage-provider/core/spdb"
	coretask "github.com/mocachain/moca-storage-provider/core/task"
	metatypes "github.com/mocachain/moca-storage-provider/modular/metadata/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRecoveryFenceReportsCannotMutateCurrentDispatch(t *testing.T) {
	for _, kind := range []string{"missing token", "unknown token", "version", "size", "updating", "segment", "redundancy", "restart", "old attempt"} {
		t.Run(kind, func(t *testing.T) {
			m, object, params, _, _, _, _ := recoveryFixture(t, -1)
			initial := &gfsptask.GfSpRecoverPieceTask{}
			initial.InitRecoverPieceTask(object, params, 85, 0, -1, 4, 50, 5)
			initial.SetBySuccessorSP(true)
			require.NoError(t, m.HandleRecoverPieceTask(context.Background(), initial))
			m.recoveryQueue.SetRetireTaskStrategy(m.GCRecoverQueue)
			m.recoveryQueue.SetFilterTaskStrategy(m.FilterRecoveryTask)
			m.taskCh = make(chan coretask.Task, 1)
			m.taskCh <- initial
			dispatched, err := m.DispatchTask(context.Background(), &gfsplimit.GfSpLimit{Memory: 100, Tasks: 1, TasksLowPriority: 1, TasksMediumPriority: 1, TasksHighPriority: 1})
			require.NoError(t, err)
			current := dispatched.(*gfsptask.GfSpRecoverPieceTask)
			report := copyRecovery(current)
			switch kind {
			case "missing token":
				report.SetRecoveryFence(nil)
			case "unknown token":
				report.RecoveryFence[0] ^= 0xff
			case "version":
				report.ObjectInfo.Version++
			case "size":
				report.ObjectInfo.PayloadSize++
			case "updating":
				report.ObjectInfo.IsUpdating = true
			case "segment":
				report.SegmentIdx++
			case "redundancy":
				report.EcIdx++
			case "restart":
				m.recoveryAttempts = nil
			case "old attempt":
				m.invalidateRecovery(91, -1)
				next := copyRecovery(initial)
				next.SetRecoveryFence(nil)
				require.NoError(t, m.HandleRecoverPieceTask(context.Background(), next))
				current = next
			}
			for _, failed := range []bool{false, true} {
				report.SetRecoverDone()
				if failed {
					report.SetError(errors.New("late error"))
				}
				require.ErrorIs(t, m.HandleRecoverPieceTask(context.Background(), report), ErrCanceledTask)
				require.True(t, m.recoveryQueue.Has(current.Key()))
				require.Contains(t, m.recoveryTaskMap, current.Key().String())
				require.False(t, m.recoverObjectStats.isObjectProcessed(91, 2, -1))
			}
		})
	}
}

func TestRecoveryFenceRetiredBackupCannotReviveAfterQueueGC(t *testing.T) {
	m, object, params, _, _, _, _ := recoveryFixture(t, -1)
	task := &gfsptask.GfSpRecoverPieceTask{}
	task.InitRecoverPieceTask(object, params, 85, 0, -1, 4, 50, 5)
	require.NoError(t, m.HandleRecoverPieceTask(context.Background(), task))
	m.recoveryQueue.SetRetireTaskStrategy(m.GCRecoverQueue)
	backup := m.recoveryQueue.PopByKey(task.Key()).(*gfsptask.GfSpRecoverPieceTask)
	start := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		<-start
		for i := 0; i < 100; i++ {
			m.repushTask(backup)
			m.recoveryQueue.Has(backup.Key())
		}
		close(finished)
	}()
	close(start)
	m.invalidateRecovery(91, -1)
	<-finished
	require.Zero(t, m.recoveryQueue.Len())
	require.Empty(t, m.recoveryTaskMap)
}

func TestVerifyGVGDeletedObjectRequiresDurableCleanup(t *testing.T) {
	m, object, _, db, _, _, _ := recoveryFixture(t, -1)
	ctrl := gomock.NewController(t)
	chain := consensus.NewMockConsensus(ctrl)
	chain.EXPECT().QueryObjectInfoByID(gomock.Any(), "91").Return(nil, status.Error(codes.Unknown, "rpc error: code = Unknown desc = No such object: key not found")).AnyTimes()
	m.baseApp.SetConsensus(chain)
	client := gfspclient.NewMockGfSpClientAPI(ctrl)
	client.EXPECT().ListObjectsInGVG(gomock.Any(), uint32(7), uint64(0), verifyGVGQueryLimit).Return([]*metatypes.ObjectDetails{{Object: &metatypes.Object{ObjectInfo: object}}}, nil).Times(2)
	client.EXPECT().ListObjectsInGVG(gomock.Any(), uint32(7), uint64(91), verifyGVGQueryLimit).Return(nil, nil)
	m.baseApp.SetGfSpClient(client)
	gomock.InOrder(db.EXPECT().DeleteRecoverFailedObject(uint64(91)).Return(errors.New("database unavailable")), db.EXPECT().DeleteRecoverFailedObject(uint64(91)).Return(nil))
	db.EXPECT().UpdateRecoverGVGStats(gomock.Any()).Return(nil)
	scheduler, _ := NewVerifyGVGScheduler(m, 7, -1)
	stats := &spdb.RecoverGVGStats{VirtualGroupID: 7, Status: spdb.Processed}
	complete, err := scheduler.verifyPass(stats)
	require.Error(t, err)
	require.False(t, complete)
	require.Zero(t, scheduler.curStartAfter)
	complete, err = scheduler.verifyPass(stats)
	require.NoError(t, err)
	require.False(t, complete)
	require.Equal(t, uint64(91), scheduler.curStartAfter)
	complete, err = scheduler.verifyPass(stats)
	require.NoError(t, err)
	require.True(t, complete)
	require.Equal(t, spdb.Completed, stats.Status)
}

func TestRecoveryDeletionClassifierRejectsTransportAndGenericNotFound(t *testing.T) {
	for _, rpcErr := range []error{status.Error(codes.NotFound, "not found"), status.Error(codes.Unavailable, "No such object"), status.Error(codes.DeadlineExceeded, "No such object"), status.Error(codes.Canceled, "No such object"), context.Canceled, context.DeadlineExceeded} {
		t.Run(rpcErr.Error(), func(t *testing.T) {
			m, object, _, _, _, _, _ := recoveryFixture(t, -1)
			chain := consensus.NewMockConsensus(gomock.NewController(t))
			chain.EXPECT().QueryObjectInfoByID(gomock.Any(), "91").Return(nil, rpcErr)
			m.baseApp.SetConsensus(chain)
			ok, err := verifyIntegrity(m, object, -1)
			require.False(t, ok)
			require.ErrorIs(t, err, rpcErr)
		})
	}
}
