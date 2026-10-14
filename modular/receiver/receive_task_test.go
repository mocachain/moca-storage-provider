package receiver

import (
	"context"
	"fmt"
	"testing"

	sdkmath "cosmossdk.io/math"
	"github.com/mocachain/moca-common/go/hash"
	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/base/gfsppieceop"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	"github.com/mocachain/moca-storage-provider/core/piecestore"
	"github.com/mocachain/moca-storage-provider/core/spdb"
	"github.com/mocachain/moca-storage-provider/core/taskqueue"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
	virtualgrouptypes "github.com/mocachain/moca/v2/x/virtualgroup/types"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
	"gorm.io/gorm"
)

func TestErrPieceStoreWithDetail(t *testing.T) {
	mock := "mockDetail"
	result := ErrPieceStoreWithDetail(mock)
	assert.Equal(t, mock, result.Description)
}

func TestErrGfSpDBWithDetail(t *testing.T) {
	mock := "mockDetail"
	result := ErrGfSpDBWithDetail(mock)
	assert.Equal(t, mock, result.Description)
}

func TestHandleReceivePieceTask_RepeatedTask(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	q.EXPECT().Has(gomock.Any()).Return(true).Times(1)
	mockTask := &gfsptask.GfSpReceivePieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
			PayloadSize:  100,
		},
	}
	err := r.HandleReceivePieceTask(context.TODO(), mockTask, nil)
	assert.NotNil(t, err)
}

func TestHandleReceivePieceTask_PushTaskFailed(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	q.EXPECT().Has(gomock.Any()).Return(false).Times(1)
	q.EXPECT().Push(gomock.Any()).Return(fmt.Errorf("failed to push")).Times(1)
	mockTask := &gfsptask.GfSpReceivePieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
			PayloadSize:  100,
		},
	}
	err := r.HandleReceivePieceTask(context.TODO(), mockTask, nil)
	assert.NotNil(t, err)
}

func TestHandleReceivePieceTask_CheckChecksumFailed(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	q.EXPECT().Has(gomock.Any()).Return(false).Times(1)
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)
	mockTask := &gfsptask.GfSpReceivePieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
			PayloadSize:  100,
		},
	}
	err := r.HandleReceivePieceTask(context.TODO(), mockTask, nil)
	assert.NotNil(t, err)
}

func TestHandleReceivePieceTask_SetPieceChecksumFailed(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	r.receiveQueue = q
	q.EXPECT().Has(gomock.Any()).Return(false).Times(1)
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)
	data := []byte{'a'}
	checksum := hash.GenerateChecksum(data)
	mockTask := &gfsptask.GfSpReceivePieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
			PayloadSize:  100,
		},
		PieceChecksum: checksum,
	}
	mockSPDB := spdb.NewMockSPDB(ctrl)
	r.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().SetReplicatePieceChecksum(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(fmt.Errorf("failed to set piece checksum")).Times(1)
	err := r.HandleReceivePieceTask(context.TODO(), mockTask, data)
	assert.NotNil(t, err)
}

func TestHandleReceivePieceTask_PutPieceFailed(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	r.receiveQueue = q
	q.EXPECT().Has(gomock.Any()).Return(false).Times(1)
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)
	data := []byte{'a'}
	checksum := hash.GenerateChecksum(data)
	mockTask := &gfsptask.GfSpReceivePieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
			PayloadSize:  100,
		},
		PieceChecksum: checksum,
	}
	mockSPDB := spdb.NewMockSPDB(ctrl)
	r.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().SetReplicatePieceChecksum(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)
	mockPieceStoreAPI := piecestore.NewMockPieceStore(ctrl)
	r.baseApp.SetPieceStore(mockPieceStoreAPI)
	mockPieceStoreAPI.EXPECT().PutPiece(gomock.Any(), gomock.Any(), gomock.Any()).Return(fmt.Errorf("failed to put piece")).Times(1)
	err := r.HandleReceivePieceTask(context.TODO(), mockTask, data)
	assert.NotNil(t, err)
}

func TestHandleReceivePieceTask_HandleReceivePieceTaskSucceed(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	r.receiveQueue = q
	q.EXPECT().Has(gomock.Any()).Return(false).Times(1)
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)
	data := []byte{'a'}
	checksum := hash.GenerateChecksum(data)
	mockTask := &gfsptask.GfSpReceivePieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
			PayloadSize:  100,
		},
		PieceChecksum: checksum,
	}
	mockSPDB := spdb.NewMockSPDB(ctrl)
	r.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().SetReplicatePieceChecksum(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)
	mockPieceStoreAPI := piecestore.NewMockPieceStore(ctrl)
	r.baseApp.SetPieceStore(mockPieceStoreAPI)
	mockPieceStoreAPI.EXPECT().PutPiece(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)
	err := r.HandleReceivePieceTask(context.TODO(), mockTask, data)
	assert.Nil(t, err)
}

func TestHandleDoneReceivePieceTask_PushTaskFailed(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	q.EXPECT().Push(gomock.Any()).Return(fmt.Errorf("failed to push")).Times(1)
	mockTask := &gfsptask.GfSpReceivePieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
			PayloadSize:  100,
		},
	}
	_, err := r.HandleDoneReceivePieceTask(context.TODO(), mockTask)
	assert.NotNil(t, err)
}

func TestHandleDoneReceivePieceTask_GetAllPieceChecksumFailed(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	r.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)
	mockTask := &gfsptask.GfSpReceivePieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
			PayloadSize:  100,
		},
		StorageParams: &storagetypes.Params{
			VersionedParams: storagetypes.VersionedParams{
				MaxSegmentSize: 16 * 1024 * 1024,
			},
		},
	}
	mockSPDB := spdb.NewMockSPDB(ctrl)
	r.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("failed to get all piece checksum")).Times(1)
	_, err := r.HandleDoneReceivePieceTask(context.TODO(), mockTask)
	assert.NotNil(t, err)
}

func TestHandleDoneReceivePieceTask_PieceCountMismatch(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	r.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)
	mockTask := &gfsptask.GfSpReceivePieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
			PayloadSize:  100,
		},
		StorageParams: &storagetypes.Params{
			VersionedParams: storagetypes.VersionedParams{
				MaxSegmentSize: 16 * 1024 * 1024,
			},
		},
	}
	mockSPDB := spdb.NewMockSPDB(ctrl)
	r.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1)
	mockSPDB.EXPECT().GetObjectIntegrity(gomock.Any(), gomock.Any()).Return(nil, gorm.ErrRecordNotFound).AnyTimes()
	_, err := r.HandleDoneReceivePieceTask(context.TODO(), mockTask)
	assert.NotNil(t, err)
}

func TestHandleDoneReceivePieceTask_Integarty_Mismatch(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	r.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)
	mockTask := &gfsptask.GfSpReceivePieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
			PayloadSize:  100,
			Checksums:    [][]byte{{1, 2, 3}, {1, 2, 3}},
		},
		StorageParams: &storagetypes.Params{
			VersionedParams: storagetypes.VersionedParams{
				MaxSegmentSize: 16 * 1024 * 1024,
			},
		},
	}
	mockSPDB := spdb.NewMockSPDB(ctrl)
	r.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any(), gomock.Any()).Return([][]byte{{1, 2, 3}}, nil).Times(1)
	mockConsensus := consensus.NewMockConsensus(ctrl)
	r.baseApp.SetConsensus(mockConsensus)
	mockConsensus.EXPECT().QueryObjectInfoByID(gomock.Any(), "100").Return(mockTask.GetObjectInfo(), nil).Times(1)
	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	r.baseApp.SetGfSpClient(mockGRPCAPI)
	_, err := r.HandleDoneReceivePieceTask(context.TODO(), mockTask)
	assert.NotNil(t, err)
}

func TestHandleDoneReceivePieceTask_RejectsTaskChecksumThatDiffersFromChain(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	r.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)

	pieceChecksums := [][]byte{{1, 2, 3}}
	taskChecksums := [][]byte{{4, 5, 6}, hash.GenerateIntegrityHash(pieceChecksums)}
	mockTask := &gfsptask.GfSpReceivePieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
			PayloadSize:  100,
			Checksums:    taskChecksums,
		},
		StorageParams: &storagetypes.Params{
			VersionedParams: storagetypes.VersionedParams{MaxSegmentSize: 16 * 1024 * 1024},
		},
	}
	mockSPDB := spdb.NewMockSPDB(ctrl)
	r.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any(), gomock.Any()).Return(pieceChecksums, nil).Times(1)

	chainChecksums := [][]byte{{4, 5, 6}, []byte("different-chain-integrity")}
	mockConsensus := consensus.NewMockConsensus(ctrl)
	r.baseApp.SetConsensus(mockConsensus)
	mockConsensus.EXPECT().QueryObjectInfoByID(gomock.Any(), "100").Return(&storagetypes.ObjectInfo{
		Id:        sdkmath.NewUint(100),
		Checksums: chainChecksums,
	}, nil).Times(1)

	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	r.baseApp.SetGfSpClient(mockGRPCAPI)

	_, err := r.HandleDoneReceivePieceTask(context.TODO(), mockTask)
	assert.ErrorIs(t, err, ErrInvalidDataChecksum)
}

func TestHandleDoneReceivePieceTask_SignSecondarySealBlsFailed(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	r.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)
	mockTask := &gfsptask.GfSpReceivePieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
			PayloadSize:  100,
			Checksums: [][]byte{
				{3, 144, 88, 198, 242, 192, 203, 73, 44, 83, 59, 10, 77, 20, 239, 119, 204, 15, 120, 171, 204, 206, 213, 40, 125, 132, 161, 162, 1, 28, 251, 129},
				{3, 144, 88, 198, 242, 192, 203, 73, 44, 83, 59, 10, 77, 20, 239, 119, 204, 15, 120, 171, 204, 206, 213, 40, 125, 132, 161, 162, 1, 28, 251, 129},
			},
		},
		StorageParams: &storagetypes.Params{
			VersionedParams: storagetypes.VersionedParams{
				MaxSegmentSize: 16 * 1024 * 1024,
			},
		},
	}
	mockSPDB := spdb.NewMockSPDB(ctrl)
	r.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any(), gomock.Any()).Return([][]byte{{1, 2, 3}}, nil).Times(1)
	mockConsensus := consensus.NewMockConsensus(ctrl)
	r.baseApp.SetConsensus(mockConsensus)
	mockConsensus.EXPECT().QueryObjectInfoByID(gomock.Any(), "100").Return(mockTask.GetObjectInfo(), nil).Times(1)
	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	r.baseApp.SetGfSpClient(mockGRPCAPI)
	mockGRPCAPI.EXPECT().SignSecondarySealBls(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("failed to sign bls")).Times(1)
	_, err := r.HandleDoneReceivePieceTask(context.TODO(), mockTask)
	assert.NotNil(t, err)
}

func TestHandleDoneReceivePieceTask_SetObjectIntegrityFailed(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	r.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)
	mockTask := &gfsptask.GfSpReceivePieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
			PayloadSize:  100,
			Checksums: [][]byte{
				{3, 144, 88, 198, 242, 192, 203, 73, 44, 83, 59, 10, 77, 20, 239, 119, 204, 15, 120, 171, 204, 206, 213, 40, 125, 132, 161, 162, 1, 28, 251, 129},
				{3, 144, 88, 198, 242, 192, 203, 73, 44, 83, 59, 10, 77, 20, 239, 119, 204, 15, 120, 171, 204, 206, 213, 40, 125, 132, 161, 162, 1, 28, 251, 129},
			},
		},
		StorageParams: &storagetypes.Params{
			VersionedParams: storagetypes.VersionedParams{
				MaxSegmentSize: 16 * 1024 * 1024,
			},
		},
	}
	mockSPDB := spdb.NewMockSPDB(ctrl)
	r.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any(), gomock.Any()).Return([][]byte{{1, 2, 3}}, nil).Times(1)
	mockSPDB.EXPECT().SetObjectIntegrity(gomock.Any()).Return(fmt.Errorf("failed to set integrity")).Times(1)
	mockConsensus := consensus.NewMockConsensus(ctrl)
	r.baseApp.SetConsensus(mockConsensus)
	mockConsensus.EXPECT().QueryObjectInfoByID(gomock.Any(), "100").Return(mockTask.GetObjectInfo(), nil).Times(1)
	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	r.baseApp.SetGfSpClient(mockGRPCAPI)
	mockGRPCAPI.EXPECT().SignSecondarySealBls(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1)
	_, err := r.HandleDoneReceivePieceTask(context.TODO(), mockTask)
	assert.NotNil(t, err)
}

func TestHandleDoneReceivePieceTask_HandleDoneReceivePieceTaskSucceed(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	r.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)
	mockTask := &gfsptask.GfSpReceivePieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
			PayloadSize:  100,
			Checksums: [][]byte{
				{3, 144, 88, 198, 242, 192, 203, 73, 44, 83, 59, 10, 77, 20, 239, 119, 204, 15, 120, 171, 204, 206, 213, 40, 125, 132, 161, 162, 1, 28, 251, 129},
				{3, 144, 88, 198, 242, 192, 203, 73, 44, 83, 59, 10, 77, 20, 239, 119, 204, 15, 120, 171, 204, 206, 213, 40, 125, 132, 161, 162, 1, 28, 251, 129},
			},
		},
		StorageParams: &storagetypes.Params{
			VersionedParams: storagetypes.VersionedParams{
				MaxSegmentSize: 16 * 1024 * 1024,
			},
		},
	}
	mockSPDB := spdb.NewMockSPDB(ctrl)
	r.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any(), gomock.Any()).Return([][]byte{{1, 2, 3}}, nil).Times(1)
	mockSPDB.EXPECT().SetObjectIntegrity(gomock.Any()).Return(nil).Times(1)
	mockSPDB.EXPECT().DeleteAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any()).Return(fmt.Errorf("failed to delete all piece checksum")).Times(1)
	mockConsensus := consensus.NewMockConsensus(ctrl)
	r.baseApp.SetConsensus(mockConsensus)
	mockConsensus.EXPECT().QueryObjectInfoByID(gomock.Any(), "100").Return(mockTask.GetObjectInfo(), nil).Times(1)
	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	r.baseApp.SetGfSpClient(mockGRPCAPI)
	mockGRPCAPI.EXPECT().SignSecondarySealBls(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1)
	mockGRPCAPI.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	_, err := r.HandleDoneReceivePieceTask(context.TODO(), mockTask)
	assert.Nil(t, err)
}

// newDoneReceiveTask builds a finished receive task for object 100 in GVG 7 that carries the given checksums.
func newDoneReceiveTask(checksums [][]byte, isAgentUpload, isUpdating bool) *gfsptask.GfSpReceivePieceTask {
	return &gfsptask.GfSpReceivePieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_CREATED,
			PayloadSize:  100,
			Checksums:    checksums,
			IsUpdating:   isUpdating,
		},
		StorageParams: &storagetypes.Params{
			VersionedParams: storagetypes.VersionedParams{MaxSegmentSize: 16 * 1024 * 1024},
		},
		GlobalVirtualGroupId: 7,
		IsAgentUploadTask:    isAgentUpload,
	}
}

func TestHandleDoneReceivePieceTask_AgentUploadUsesTaskChecksumsWhenChainHasNone(t *testing.T) {
	r := setup(t)
	r.spID = 2
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	r.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)

	pieceChecksums := [][]byte{{1, 2, 3}}
	taskChecksums := [][]byte{{4, 5, 6}, hash.GenerateIntegrityHash(pieceChecksums)}
	mockTask := newDoneReceiveTask(taskChecksums, true, false)
	mockSPDB := spdb.NewMockSPDB(ctrl)
	r.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any(), gomock.Any()).Return(pieceChecksums, nil).Times(1)
	mockSPDB.EXPECT().SetObjectIntegrity(gomock.Any()).Return(nil).Times(1)
	mockSPDB.EXPECT().DeleteAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any()).Return(nil).Times(1)

	mockConsensus := consensus.NewMockConsensus(ctrl)
	r.baseApp.SetConsensus(mockConsensus)
	// a delegated object is created on chain without checksums; SealObjectV2 sets them later.
	mockConsensus.EXPECT().QueryObjectInfoByID(gomock.Any(), "100").Return(&storagetypes.ObjectInfo{
		Id:           sdkmath.NewUint(100),
		ObjectStatus: storagetypes.OBJECT_STATUS_CREATED,
	}, nil).Times(1)
	mockConsensus.EXPECT().QueryGlobalVirtualGroup(gomock.Any(), uint32(7)).Return(&virtualgrouptypes.GlobalVirtualGroup{PrimarySpId: 1}, nil).Times(1)

	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	r.baseApp.SetGfSpClient(mockGRPCAPI)
	mockGRPCAPI.EXPECT().SignSecondarySealBls(gomock.Any(), uint64(100), uint32(7), taskChecksums).Return([]byte("signature"), nil).Times(1)
	mockGRPCAPI.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	signature, err := r.HandleDoneReceivePieceTask(context.TODO(), mockTask)
	assert.Nil(t, err)
	assert.Equal(t, []byte("signature"), signature)
}

func TestHandleDoneReceivePieceTask_AgentUploadRejectsTaskChecksumThatDoesNotMatchData(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	r.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)

	pieceChecksums := [][]byte{{1, 2, 3}}
	taskChecksums := [][]byte{{4, 5, 6}, []byte("does-not-match-received-data")}
	mockTask := newDoneReceiveTask(taskChecksums, true, false)
	mockSPDB := spdb.NewMockSPDB(ctrl)
	r.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any(), gomock.Any()).Return(pieceChecksums, nil).Times(1)

	mockConsensus := consensus.NewMockConsensus(ctrl)
	r.baseApp.SetConsensus(mockConsensus)
	mockConsensus.EXPECT().QueryObjectInfoByID(gomock.Any(), "100").Return(&storagetypes.ObjectInfo{
		Id:           sdkmath.NewUint(100),
		ObjectStatus: storagetypes.OBJECT_STATUS_CREATED,
	}, nil).Times(1)

	// no SignSecondarySealBls expectation: signing a mismatching list fails the test.
	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	r.baseApp.SetGfSpClient(mockGRPCAPI)

	_, err := r.HandleDoneReceivePieceTask(context.TODO(), mockTask)
	assert.ErrorIs(t, err, ErrInvalidDataChecksum)
}

func TestHandleDoneReceivePieceTask_RejectsClientUploadWhenChainHasNoChecksums(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	r.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)

	pieceChecksums := [][]byte{{1, 2, 3}}
	taskChecksums := [][]byte{{4, 5, 6}, hash.GenerateIntegrityHash(pieceChecksums)}
	mockTask := newDoneReceiveTask(taskChecksums, false, false)
	mockSPDB := spdb.NewMockSPDB(ctrl)
	r.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any(), gomock.Any()).Return(pieceChecksums, nil).Times(1)

	mockConsensus := consensus.NewMockConsensus(ctrl)
	r.baseApp.SetConsensus(mockConsensus)
	mockConsensus.EXPECT().QueryObjectInfoByID(gomock.Any(), "100").Return(&storagetypes.ObjectInfo{
		Id:           sdkmath.NewUint(100),
		ObjectStatus: storagetypes.OBJECT_STATUS_CREATED,
	}, nil).Times(1)

	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	r.baseApp.SetGfSpClient(mockGRPCAPI)

	_, err := r.HandleDoneReceivePieceTask(context.TODO(), mockTask)
	assert.ErrorIs(t, err, ErrInvalidDataChecksum)
}

func TestHandleDoneReceivePieceTask_RejectsAgentUploadWhenSealedObjectHasNoChecksums(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	r.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)

	pieceChecksums := [][]byte{{1, 2, 3}}
	taskChecksums := [][]byte{{4, 5, 6}, hash.GenerateIntegrityHash(pieceChecksums)}
	mockTask := newDoneReceiveTask(taskChecksums, true, false)
	mockSPDB := spdb.NewMockSPDB(ctrl)
	r.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any(), gomock.Any()).Return(pieceChecksums, nil).Times(1)

	mockConsensus := consensus.NewMockConsensus(ctrl)
	r.baseApp.SetConsensus(mockConsensus)
	mockConsensus.EXPECT().QueryObjectInfoByID(gomock.Any(), "100").Return(&storagetypes.ObjectInfo{
		Id:           sdkmath.NewUint(100),
		ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
	}, nil).Times(1)

	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	r.baseApp.SetGfSpClient(mockGRPCAPI)

	_, err := r.HandleDoneReceivePieceTask(context.TODO(), mockTask)
	assert.ErrorIs(t, err, ErrInvalidDataChecksum)
}

func TestHandleDoneReceivePieceTask_ChainChecksumsWinOverTaskChecksumsForAgentUpload(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	r.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)

	pieceChecksums := [][]byte{{1, 2, 3}}
	taskChecksums := [][]byte{{4, 5, 6}, hash.GenerateIntegrityHash(pieceChecksums)}
	mockTask := newDoneReceiveTask(taskChecksums, true, false)
	mockSPDB := spdb.NewMockSPDB(ctrl)
	r.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any(), gomock.Any()).Return(pieceChecksums, nil).Times(1)

	mockConsensus := consensus.NewMockConsensus(ctrl)
	r.baseApp.SetConsensus(mockConsensus)
	mockConsensus.EXPECT().QueryObjectInfoByID(gomock.Any(), "100").Return(&storagetypes.ObjectInfo{
		Id:           sdkmath.NewUint(100),
		ObjectStatus: storagetypes.OBJECT_STATUS_CREATED,
		Checksums:    [][]byte{{4, 5, 6}, []byte("different-chain-integrity")},
	}, nil).Times(1)

	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	r.baseApp.SetGfSpClient(mockGRPCAPI)

	_, err := r.HandleDoneReceivePieceTask(context.TODO(), mockTask)
	assert.ErrorIs(t, err, ErrInvalidDataChecksum)
}

func TestHandleDoneReceivePieceTask_UpdatingObjectUsesShadowChecksums(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	r.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)

	pieceChecksums := [][]byte{{1, 2, 3}}
	shadowChecksums := [][]byte{{4, 5, 6}, hash.GenerateIntegrityHash(pieceChecksums)}
	mockTask := newDoneReceiveTask(shadowChecksums, false, true)
	mockSPDB := spdb.NewMockSPDB(ctrl)
	r.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any(), gomock.Any()).Return(pieceChecksums, nil).Times(1)
	mockSPDB.EXPECT().SetShadowObjectIntegrity(gomock.Any()).Return(nil).Times(1)
	mockSPDB.EXPECT().DeleteAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any()).Return(nil).Times(1)

	mockConsensus := consensus.NewMockConsensus(ctrl)
	r.baseApp.SetConsensus(mockConsensus)
	// the sealed object keeps the previous content's checksums while the update is in flight;
	// the new content's checksums live in the shadow object.
	mockConsensus.EXPECT().QueryObjectInfoByID(gomock.Any(), "100").Return(&storagetypes.ObjectInfo{
		Id:           sdkmath.NewUint(100),
		BucketName:   "bucket",
		ObjectName:   "object",
		ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
		IsUpdating:   true,
		Checksums:    [][]byte{{7, 8, 9}, []byte("previous-content-integrity")},
	}, nil).Times(1)
	mockConsensus.EXPECT().QueryShadowObjectInfo(gomock.Any(), "bucket", "object").Return(&storagetypes.ShadowObjectInfo{
		Id:        sdkmath.NewUint(100),
		Checksums: shadowChecksums,
	}, nil).Times(1)

	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	r.baseApp.SetGfSpClient(mockGRPCAPI)
	mockGRPCAPI.EXPECT().SignSecondarySealBls(gomock.Any(), uint64(100), uint32(7), shadowChecksums).Return([]byte("signature"), nil).Times(1)
	mockGRPCAPI.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	signature, err := r.HandleDoneReceivePieceTask(context.TODO(), mockTask)
	assert.Nil(t, err)
	assert.Equal(t, []byte("signature"), signature)
}

func TestHandleDoneReceivePieceTask_DelegatedUpdateUsesTaskChecksumsWhenShadowHasNone(t *testing.T) {
	r := setup(t)
	r.spID = 2
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	r.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)

	pieceChecksums := [][]byte{{1, 2, 3}}
	taskChecksums := [][]byte{{4, 5, 6}, hash.GenerateIntegrityHash(pieceChecksums)}
	mockTask := newDoneReceiveTask(taskChecksums, true, true)
	mockSPDB := spdb.NewMockSPDB(ctrl)
	r.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any(), gomock.Any()).Return(pieceChecksums, nil).Times(1)
	mockSPDB.EXPECT().SetShadowObjectIntegrity(gomock.Any()).Return(nil).Times(1)
	mockSPDB.EXPECT().DeleteAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any()).Return(nil).Times(1)

	mockConsensus := consensus.NewMockConsensus(ctrl)
	r.baseApp.SetConsensus(mockConsensus)
	mockConsensus.EXPECT().QueryObjectInfoByID(gomock.Any(), "100").Return(&storagetypes.ObjectInfo{
		Id:           sdkmath.NewUint(100),
		BucketName:   "bucket",
		ObjectName:   "object",
		ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
		IsUpdating:   true,
		Checksums:    [][]byte{{7, 8, 9}, []byte("previous-content-integrity")},
	}, nil).Times(1)
	// a delegated update registers a shadow object without checksums; SealObjectV2 sets them later.
	mockConsensus.EXPECT().QueryShadowObjectInfo(gomock.Any(), "bucket", "object").Return(&storagetypes.ShadowObjectInfo{
		Id: sdkmath.NewUint(100),
	}, nil).Times(1)
	mockConsensus.EXPECT().QueryGlobalVirtualGroup(gomock.Any(), uint32(7)).Return(&virtualgrouptypes.GlobalVirtualGroup{PrimarySpId: 1}, nil).Times(1)

	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	r.baseApp.SetGfSpClient(mockGRPCAPI)
	mockGRPCAPI.EXPECT().SignSecondarySealBls(gomock.Any(), uint64(100), uint32(7), taskChecksums).Return([]byte("signature"), nil).Times(1)
	mockGRPCAPI.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	signature, err := r.HandleDoneReceivePieceTask(context.TODO(), mockTask)
	assert.Nil(t, err)
	assert.Equal(t, []byte("signature"), signature)
}

func TestQueryTasks(t *testing.T) {
	r := setup(t)
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	q.EXPECT().ScanTask(gomock.Any()).Times(1)
	r.QueryTasks(context.TODO(), "")
}

func TestHandleDoneReceivePieceTask_RejectsUpdatingObjectWhenShadowRecordMissing(t *testing.T) {
	r := setup(t)
	r.spID = 2
	ctrl := gomock.NewController(t)
	q := taskqueue.NewMockTQueueOnStrategy(ctrl)
	r.receiveQueue = q
	r.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	q.EXPECT().Push(gomock.Any()).Return(nil).Times(1)
	q.EXPECT().PopByKey(gomock.Any()).Return(nil).Times(1)

	pieceChecksums := [][]byte{{1, 2, 3}}
	taskChecksums := [][]byte{{4, 5, 6}, hash.GenerateIntegrityHash(pieceChecksums)}
	// an agent-upload task for an object under update: without a shadow record the task
	// checksums must not be trusted, even though the object is unsealed for update purposes.
	mockTask := newDoneReceiveTask(taskChecksums, true, true)
	mockSPDB := spdb.NewMockSPDB(ctrl)
	r.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetAllReplicatePieceChecksumOptimized(gomock.Any(), gomock.Any(), gomock.Any()).Return(pieceChecksums, nil).Times(1)

	mockConsensus := consensus.NewMockConsensus(ctrl)
	r.baseApp.SetConsensus(mockConsensus)
	mockConsensus.EXPECT().QueryObjectInfoByID(gomock.Any(), "100").Return(&storagetypes.ObjectInfo{
		Id:           sdkmath.NewUint(100),
		BucketName:   "bucket",
		ObjectName:   "object",
		ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
		IsUpdating:   true,
		Checksums:    [][]byte{{7, 8, 9}, []byte("previous-content-integrity")},
	}, nil).Times(1)
	// the consensus interface reports a missing shadow record as (nil, nil)
	mockConsensus.EXPECT().QueryShadowObjectInfo(gomock.Any(), "bucket", "object").Return(nil, nil).Times(1)

	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	r.baseApp.SetGfSpClient(mockGRPCAPI)

	_, err := r.HandleDoneReceivePieceTask(context.TODO(), mockTask)
	assert.ErrorIs(t, err, ErrInvalidDataChecksum)
}
