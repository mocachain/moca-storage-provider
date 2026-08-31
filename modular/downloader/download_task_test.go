package downloader

import (
	"context"
	"fmt"
	"testing"

	sdkmath "cosmossdk.io/math"
	lru "github.com/hashicorp/golang-lru"
	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/base/gfsppieceop"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	"github.com/mocachain/moca-storage-provider/core/piecestore"
	"github.com/mocachain/moca-storage-provider/core/spdb"
	"github.com/mocachain/moca-storage-provider/store/sqldb"
	payment_types "github.com/mocachain/moca/v2/x/payment/types"
	sptypes "github.com/mocachain/moca/v2/x/sp/types"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
	virtualgrouptypes "github.com/mocachain/moca/v2/x/virtualgroup/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
)

func TestSplitToSegmentPieceInfos(t *testing.T) {
	var (
		task          = &gfsptask.GfSpDownloadObjectTask{}
		objectInfo    = &storagetypes.ObjectInfo{}
		storageParams = &storagetypes.Params{}
	)
	storageParams.VersionedParams.MaxSegmentSize = 16 * 1024 * 1024

	testCases := []struct {
		name             string
		objectID         uint64
		objectSize       uint64
		startOffset      uint64
		endOffset        uint64
		isErr            bool
		wantedPieceCount int
	}{
		{
			"invalid params",
			1,
			1,
			1,
			0,
			true,
			0,
		},
		{
			"1 byte object full read",
			1,
			1,
			0,
			0,
			false,
			1,
		},
		{
			"16MB byte object full read",
			1,
			16 * 1024 * 1024,
			0,
			16*1024*1024 - 1,
			false,
			1,
		},
		{
			"16MB byte object range read",
			1,
			16 * 1024 * 1024,
			0,
			11,
			false,
			1,
		},
		{
			"17MB byte object range read, in first piece",
			1,
			17 * 1024 * 1024,
			0,
			11,
			false,
			1,
		},
		{
			"17MB byte object range read, in second piece",
			1,
			17 * 1024 * 1024,
			16 * 1024 * 1024,
			16*1024*1024 + 11,
			false,
			1,
		},
		{
			"17MB byte object range read, in two piece",
			1,
			17 * 1024 * 1024,
			16*1024*1024 - 11,
			16*1024*1024 + 11,
			false,
			2,
		},
		{
			"17MB byte object full read",
			1,
			17 * 1024 * 1024,
			0,
			17*1024*1024 - 1,
			false,
			2,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			objectInfo.PayloadSize = testCase.objectSize
			objectInfo.Id = sdkmath.NewUint(testCase.objectID)
			task.InitDownloadObjectTask(
				objectInfo, nil, storageParams, 1,
				"", int64(testCase.startOffset), int64(testCase.endOffset), 0, 0)
			pieceInfos, err := SplitToSegmentPieceInfos(task, &gfsppieceop.GfSpPieceOp{})
			if !testCase.isErr {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				return
			}
			assert.Equal(t, testCase.wantedPieceCount, len(pieceInfos))
			realLength := uint64(0)
			for _, p := range pieceInfos {
				realLength += p.Length
			}
			assert.Equal(t, testCase.endOffset-testCase.startOffset+1, realLength)
		})
	}
}

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

func TestErrConsensusWithDetail(t *testing.T) {
	mock := "mockDetail"
	result := ErrConsensusWithDetail(mock)
	assert.Equal(t, mock, result.Description)
}

func TestPreDownloadObject(t *testing.T) {
	d := setup(t)
	// failed due to pointer dangling
	err := d.PreDownloadObject(context.TODO(), nil)
	assert.NotNil(t, err)

	// failed due to object unsealed
	mockTask1 := &gfsptask.GfSpDownloadObjectTask{
		ObjectInfo: &storagetypes.ObjectInfo{
			Id: sdkmath.NewUint(100),
		},
		StorageParams: &storagetypes.Params{},
	}
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockSPDB := spdb.NewMockSPDB(ctrl)
	d.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetObjectIntegrity(gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("fail to get hash")).Times(1)
	err = d.PreDownloadObject(context.TODO(), mockTask1)
	assert.NotNil(t, err)

	// failed due to query spdb traffic failed
	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	d.baseApp.SetGfSpClient(mockGRPCAPI)
	mockGRPCAPI.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	mockGRPCAPI.EXPECT().GetPaymentByBucketName(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, bucketName string, includePrivate bool, opts ...grpc.DialOption) (*payment_types.StreamRecord, error) {
			return &payment_types.StreamRecord{}, nil
		}).AnyTimes()
	mockSPDB.EXPECT().GetBucketTraffic(gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("failed to get bucket traffic")).Times(1)

	mockTask2 := &gfsptask.GfSpDownloadObjectTask{
		BucketInfo: &storagetypes.BucketInfo{
			Id:         sdkmath.NewUint(100),
			BucketName: "mock_task2_bucket",
		},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
		},
		StorageParams: &storagetypes.Params{},
	}
	err = d.PreDownloadObject(context.TODO(), mockTask2)
	assert.NotNil(t, err)

	// succeed
	mockGRPCAPI.EXPECT().GetPaymentByBucketName(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, bucketName string, includePrivate bool, opts ...grpc.DialOption) (*payment_types.StreamRecord, error) {
			return &payment_types.StreamRecord{}, nil
		}).AnyTimes()
	mockSPDB.EXPECT().GetBucketTraffic(gomock.Any(), gomock.Any()).Return(nil, nil)
	mockConsensusAPI := consensus.NewMockConsensus(ctrl)
	d.baseApp.SetConsensus(mockConsensusAPI)
	mockConsensusAPI.EXPECT().QuerySPFreeQuota(gomock.Any(), gomock.Any()).Return(uint64(100), nil)
	mockSPDB.EXPECT().InitBucketTraffic(gomock.Any(), gomock.Any()).Return(nil)
	mockSPDB.EXPECT().CheckQuotaAndAddReadRecord(gomock.Any(), gomock.Any()).Return(nil)
	err = d.PreDownloadObject(context.TODO(), mockTask2)
	assert.Nil(t, err)
}

func TestPreDownloadObject_DBErrorDoesNotBypassQuota(t *testing.T) {
	d := setup(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	client := gfspclient.NewMockGfSpClientAPI(ctrl)
	d.baseApp.SetGfSpClient(client)
	client.EXPECT().GetPaymentByBucketName(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&payment_types.StreamRecord{Status: payment_types.STREAM_ACCOUNT_STATUS_ACTIVE}, nil)
	client.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	db := spdb.NewMockSPDB(ctrl)
	d.baseApp.SetGfSpDB(db)
	db.EXPECT().GetBucketTraffic(gomock.Any(), gomock.Any()).Return(&spdb.BucketTraffic{}, nil)
	db.EXPECT().CheckQuotaAndAddReadRecord(gomock.Any(), gomock.Any()).Return(fmt.Errorf("database unavailable"))

	task := &gfsptask.GfSpDownloadObjectTask{
		Task:          &gfsptask.GfSpTask{UserAddress: "user"},
		BucketInfo:    &storagetypes.BucketInfo{Id: sdkmath.NewUint(1), BucketName: "bucket"},
		ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1), ObjectStatus: storagetypes.OBJECT_STATUS_SEALED},
		StorageParams: &storagetypes.Params{},
	}

	assert.NotNil(t, d.PreDownloadObject(context.Background(), task))
}

func TestHandleDownloadObjectTask(t *testing.T) {
	d := setup(t)
	mockTask1 := &gfsptask.GfSpDownloadObjectTask{
		Task: &gfsptask.GfSpTask{},
		BucketInfo: &storagetypes.BucketInfo{
			Id:         sdkmath.NewUint(100),
			BucketName: "mock_task2_bucket",
		},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
		},
		StorageParams: &storagetypes.Params{},
	}

	// failed due to exceed max download concurrent
	_, err := d.HandleDownloadObjectTask(context.TODO(), mockTask1)
	assert.NotNil(t, err)

	// failed due to object param wrong
	d.downloading = 1
	d.downloadParallel = 100
	_, err = d.HandleDownloadObjectTask(context.TODO(), mockTask1)
	assert.NotNil(t, err)

	// succeed
	d.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	d.pieceCache, _ = lru.New(100)
	mockTask2 := &gfsptask.GfSpDownloadObjectTask{
		Task: &gfsptask.GfSpTask{},
		BucketInfo: &storagetypes.BucketInfo{
			Id:         sdkmath.NewUint(100),
			BucketName: "mock_task2_bucket",
		},
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
	ctrl := gomock.NewController(t)
	mockPieceStoreAPI := piecestore.NewMockPieceStore(ctrl)
	d.baseApp.SetPieceStore(mockPieceStoreAPI)
	mockPieceStoreAPI.EXPECT().GetPiece(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return([]byte{'1'}, nil)
	_, err = d.HandleDownloadObjectTask(context.TODO(), mockTask2)
	assert.Nil(t, err)
}

func TestPostDownloadObject(t *testing.T) {
	d := setup(t)
	d.PostDownloadObject(context.TODO(), nil)
}

func TestPreDownloadPiece(t *testing.T) {
	d := setup(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	d.baseApp.SetGfSpClient(mockGRPCAPI)
	mockGRPCAPI.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	// failed due to pointer dangling
	err := d.PreDownloadPiece(context.TODO(), nil)
	assert.NotNil(t, err)

	mockSPDB := spdb.NewMockSPDB(ctrl)
	d.baseApp.SetGfSpDB(mockSPDB)

	// failed due to get hash failed
	mockSPDB.EXPECT().GetObjectIntegrity(gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("fail to get hash")).Times(1)
	mockTask1 := &gfsptask.GfSpDownloadPieceTask{
		ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(100)},
		StorageParams: &storagetypes.Params{},
	}
	err = d.PreDownloadPiece(context.TODO(), mockTask1)
	assert.NotNil(t, err)

	// failed due to query spdb traffic failed
	mockGRPCAPI.EXPECT().GetPaymentByBucketName(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, bucketName string, includePrivate bool, opts ...grpc.DialOption) (*payment_types.StreamRecord, error) {
			return &payment_types.StreamRecord{}, nil
		}).AnyTimes()
	mockSPDB.EXPECT().GetBucketTraffic(gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("failed to get bucket traffic")).Times(1)

	mockConsensusAPI := consensus.NewMockConsensus(ctrl)
	d.baseApp.SetConsensus(mockConsensusAPI)
	mockConsensusAPI.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&virtualgrouptypes.GlobalVirtualGroupFamily{}, nil).Times(1)
	mockConsensusAPI.EXPECT().QuerySPByID(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{}, nil).Times(1)
	mockTask2 := &gfsptask.GfSpDownloadPieceTask{
		Task: &gfsptask.GfSpTask{
			UserAddress: "123",
		},
		BucketInfo: &storagetypes.BucketInfo{
			Id:         sdkmath.NewUint(100),
			BucketName: "mock_task2_bucket",
		},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
		},
		StorageParams: &storagetypes.Params{},
		EnableCheck:   true,
	}
	err = d.PreDownloadPiece(context.TODO(), mockTask2)
	assert.NotNil(t, err)

	// succeed
	mockGRPCAPI.EXPECT().GetPaymentByBucketName(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, bucketName string, includePrivate bool, opts ...grpc.DialOption) (*payment_types.StreamRecord, error) {
			return &payment_types.StreamRecord{}, nil
		}).AnyTimes()

	mockSPDB.EXPECT().GetBucketTraffic(gomock.Any(), gomock.Any()).Return(nil, nil)

	mockConsensusAPI.EXPECT().QuerySPFreeQuota(gomock.Any(), gomock.Any()).Return(uint64(100), nil)
	mockConsensusAPI.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&virtualgrouptypes.GlobalVirtualGroupFamily{}, nil)
	mockConsensusAPI.EXPECT().QuerySPByID(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{}, nil)
	mockSPDB.EXPECT().InitBucketTraffic(gomock.Any(), gomock.Any()).Return(nil)
	mockSPDB.EXPECT().CheckQuotaAndAddReadRecord(gomock.Any(), gomock.Any()).Return(nil)
	err = d.PreDownloadPiece(context.TODO(), mockTask2)
	assert.Nil(t, err)
}

func TestHandleDownloadPieceTask(t *testing.T) {
	d := setup(t)
	mockTask1 := &gfsptask.GfSpDownloadPieceTask{
		Task: &gfsptask.GfSpTask{},
		BucketInfo: &storagetypes.BucketInfo{
			Id:         sdkmath.NewUint(100),
			BucketName: "mock_task2_bucket",
		},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
		},
		StorageParams: &storagetypes.Params{},
	}

	// failed due to exceed max download concurrent
	_, err := d.HandleDownloadPieceTask(context.TODO(), mockTask1)
	assert.NotNil(t, err)

	// succeed
	d.downloading = 1
	d.downloadParallel = 100
	d.pieceCache, _ = lru.New(100)
	d.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	mockTask2 := &gfsptask.GfSpDownloadPieceTask{
		Task: &gfsptask.GfSpTask{},
		BucketInfo: &storagetypes.BucketInfo{
			Id:         sdkmath.NewUint(100),
			BucketName: "mock_task2_bucket",
		},
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
	ctrl := gomock.NewController(t)
	mockPieceStoreAPI := piecestore.NewMockPieceStore(ctrl)
	d.baseApp.SetPieceStore(mockPieceStoreAPI)
	mockPieceStoreAPI.EXPECT().GetPiece(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return([]byte{'1'}, nil)
	_, err = d.HandleDownloadPieceTask(context.TODO(), mockTask2)
	assert.Nil(t, err)
}

func TestPostDownloadPiece(t *testing.T) {
	d := setup(t)
	d.PostDownloadPiece(context.TODO(), nil)
}

func TestPreChallengePiece(t *testing.T) {
	d := setup(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	d.baseApp.SetGfSpClient(mockGRPCAPI)
	mockGRPCAPI.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	// failed due to pointer dangling
	err := d.PreChallengePiece(context.TODO(), nil)
	assert.NotNil(t, err)

	// failed due to object unsealed
	mockTask1 := &gfsptask.GfSpChallengePieceTask{
		ObjectInfo:    &storagetypes.ObjectInfo{},
		StorageParams: &storagetypes.Params{},
	}
	err = d.PreChallengePiece(context.TODO(), mockTask1)
	assert.NotNil(t, err)

	// succeed
	mockTask2 := &gfsptask.GfSpChallengePieceTask{
		ObjectInfo: &storagetypes.ObjectInfo{
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
		},
		StorageParams: &storagetypes.Params{},
	}
	err = d.PreChallengePiece(context.TODO(), mockTask2)
	assert.Nil(t, err)
}

func TestHandleChallengePiece(t *testing.T) {
	d := setup(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	d.baseApp.SetGfSpClient(mockGRPCAPI)
	mockGRPCAPI.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	// failed due to exceed max download concurrent
	mockTask1 := &gfsptask.GfSpChallengePieceTask{
		Task: &gfsptask.GfSpTask{},
		BucketInfo: &storagetypes.BucketInfo{
			Id:         sdkmath.NewUint(100),
			BucketName: "mock_task2_bucket",
		},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:           sdkmath.NewUint(100),
			ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
			PayloadSize:  100,
		},
		StorageParams: &storagetypes.Params{
			VersionedParams: storagetypes.VersionedParams{
				MaxSegmentSize:          16 * 1024 * 1024,
				RedundantDataChunkNum:   4,
				RedundantParityChunkNum: 2,
			},
		},
	}
	_, _, _, err := d.HandleChallengePiece(context.TODO(), mockTask1)
	assert.NotNil(t, err)

	// succeed
	d.challenging = 1
	d.challengeParallel = 100
	d.pieceCache, _ = lru.New(100)
	d.baseApp.SetPieceOp(&gfsppieceop.GfSpPieceOp{})
	mockPieceStoreAPI := piecestore.NewMockPieceStore(ctrl)
	d.baseApp.SetPieceStore(mockPieceStoreAPI)
	mockPieceStoreAPI.EXPECT().GetPiece(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return([]byte{'1'}, nil).Times(1)
	mockSPDB := spdb.NewMockSPDB(ctrl)
	d.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetObjectIntegrity(gomock.Any(), gomock.Any()).Return(&spdb.IntegrityMeta{PieceChecksumList: [][]byte{[]byte("mock")}}, nil).Times(1)
	_, _, _, err = d.HandleChallengePiece(context.TODO(), mockTask1)
	assert.Nil(t, err)
}

func TestPostChallengePiece(t *testing.T) {
	d := setup(t)
	d.PostChallengePiece(context.TODO(), nil)
}

func TestQueryTasks(t *testing.T) {
	d := setup(t)
	d.QueryTasks(context.TODO(), "")
}

// TestPreDownloadObject_PaymentLookupFailureIsNotFailOpen covers the frozen-account
// check when the payment status cannot be established. A metadata error there used to
// be swallowed, which let a frozen account keep downloading for as long as the lookup
// kept failing.
func TestPreDownloadObject_PaymentLookupFailureIsNotFailOpen(t *testing.T) {
	d := setup(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	d.baseApp.SetGfSpClient(mockGRPCAPI)
	mockGRPCAPI.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	mockGRPCAPI.EXPECT().GetPaymentByBucketName(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, fmt.Errorf("metadata unavailable"))
	allowEverythingAfterTheFrozenCheck(ctrl, d)

	err := d.PreDownloadObject(context.TODO(), newFrozenCheckTask())
	assert.NotNil(t, err, "an unknown payment status must not be treated as active")
}

// TestPreDownloadObject_MissingStreamRecordIsNotFailOpen covers the same check when the
// lookup succeeds but returns no record: the status is just as unknown, and reading it
// off a nil record would panic.
func TestPreDownloadObject_MissingStreamRecordIsNotFailOpen(t *testing.T) {
	d := setup(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	d.baseApp.SetGfSpClient(mockGRPCAPI)
	mockGRPCAPI.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	mockGRPCAPI.EXPECT().GetPaymentByBucketName(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, nil)
	allowEverythingAfterTheFrozenCheck(ctrl, d)

	err := d.PreDownloadObject(context.TODO(), newFrozenCheckTask())
	assert.NotNil(t, err, "a missing stream record must not be treated as active")
}

// TestPreDownloadObject_FrozenAccountIsRefused pins the case the check exists for.
func TestPreDownloadObject_FrozenAccountIsRefused(t *testing.T) {
	d := setup(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	d.baseApp.SetGfSpClient(mockGRPCAPI)
	mockGRPCAPI.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	mockGRPCAPI.EXPECT().GetPaymentByBucketName(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&payment_types.StreamRecord{Status: payment_types.STREAM_ACCOUNT_STATUS_FROZEN}, nil)
	allowEverythingAfterTheFrozenCheck(ctrl, d)

	err := d.PreDownloadObject(context.TODO(), newFrozenCheckTask())
	assert.Equal(t, ErrAccountFrozen, err)
}

// allowEverythingAfterTheFrozenCheck makes every step after the frozen-account check
// succeed, so that a test reaching the end of PreDownloadObject gets a nil error and
// the assertion is about the frozen check alone.
func allowEverythingAfterTheFrozenCheck(ctrl *gomock.Controller, d *DownloadModular) {
	mockSPDB := spdb.NewMockSPDB(ctrl)
	d.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetBucketTraffic(gomock.Any(), gomock.Any()).Return(&spdb.BucketTraffic{}, nil).AnyTimes()
	mockSPDB.EXPECT().CheckQuotaAndAddReadRecord(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
}

func newFrozenCheckTask() *gfsptask.GfSpDownloadObjectTask {
	return &gfsptask.GfSpDownloadObjectTask{
		Task:          &gfsptask.GfSpTask{UserAddress: "123"},
		BucketInfo:    &storagetypes.BucketInfo{Id: sdkmath.NewUint(100), BucketName: "mock_bucket"},
		ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(100), ObjectStatus: storagetypes.OBJECT_STATUS_SEALED},
		StorageParams: &storagetypes.Params{},
	}
}

// TestPreDownloadPiece_DBErrorDoesNotBypassQuota covers the quota accounting write
// failing for a reason other than "quota exhausted". Letting the request through in
// that case serves the piece without charging for it, so a database that is down or
// rejecting writes turns into unmetered reads.
func TestPreDownloadPiece_DBErrorDoesNotBypassQuota(t *testing.T) {
	d := setup(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	d.baseApp.SetGfSpClient(mockGRPCAPI)
	mockGRPCAPI.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	mockSPDB := spdb.NewMockSPDB(ctrl)
	d.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetBucketTraffic(gomock.Any(), gomock.Any()).Return(nil, nil)
	mockSPDB.EXPECT().InitBucketTraffic(gomock.Any(), gomock.Any()).Return(nil)
	mockSPDB.EXPECT().CheckQuotaAndAddReadRecord(gomock.Any(), gomock.Any()).
		Return(fmt.Errorf("dial tcp 127.0.0.1:3306: connect: connection refused"))

	mockConsensusAPI := consensus.NewMockConsensus(ctrl)
	d.baseApp.SetConsensus(mockConsensusAPI)
	mockConsensusAPI.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&virtualgrouptypes.GlobalVirtualGroupFamily{}, nil)
	mockConsensusAPI.EXPECT().QuerySPByID(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{}, nil)
	mockConsensusAPI.EXPECT().QuerySPFreeQuota(gomock.Any(), gomock.Any()).Return(uint64(100), nil)

	task := &gfsptask.GfSpDownloadPieceTask{
		Task:          &gfsptask.GfSpTask{UserAddress: "123"},
		BucketInfo:    &storagetypes.BucketInfo{Id: sdkmath.NewUint(100), BucketName: "mock_bucket"},
		ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(100), ObjectStatus: storagetypes.OBJECT_STATUS_SEALED},
		StorageParams: &storagetypes.Params{},
		EnableCheck:   true,
	}

	err := d.PreDownloadPiece(context.TODO(), task)
	assert.NotNil(t, err, "a failed quota accounting write must refuse the read, not serve it unmetered")
}

// TestPreDownloadPiece_ExhaustedQuotaKeepsItsOwnError makes sure the change above did
// not flatten the quota-exhausted case into a generic database error.
func TestPreDownloadPiece_ExhaustedQuotaKeepsItsOwnError(t *testing.T) {
	d := setup(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockGRPCAPI := gfspclient.NewMockGfSpClientAPI(ctrl)
	d.baseApp.SetGfSpClient(mockGRPCAPI)
	mockGRPCAPI.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	mockSPDB := spdb.NewMockSPDB(ctrl)
	d.baseApp.SetGfSpDB(mockSPDB)
	mockSPDB.EXPECT().GetBucketTraffic(gomock.Any(), gomock.Any()).Return(nil, nil)
	mockSPDB.EXPECT().InitBucketTraffic(gomock.Any(), gomock.Any()).Return(nil)
	mockSPDB.EXPECT().CheckQuotaAndAddReadRecord(gomock.Any(), gomock.Any()).Return(sqldb.ErrCheckQuotaEnough)

	mockConsensusAPI := consensus.NewMockConsensus(ctrl)
	d.baseApp.SetConsensus(mockConsensusAPI)
	mockConsensusAPI.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&virtualgrouptypes.GlobalVirtualGroupFamily{}, nil)
	mockConsensusAPI.EXPECT().QuerySPByID(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{}, nil)
	mockConsensusAPI.EXPECT().QuerySPFreeQuota(gomock.Any(), gomock.Any()).Return(uint64(100), nil)

	task := &gfsptask.GfSpDownloadPieceTask{
		Task:          &gfsptask.GfSpTask{UserAddress: "123"},
		BucketInfo:    &storagetypes.BucketInfo{Id: sdkmath.NewUint(100), BucketName: "mock_bucket"},
		ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(100), ObjectStatus: storagetypes.OBJECT_STATUS_SEALED},
		StorageParams: &storagetypes.Params{},
		EnableCheck:   true,
	}

	err := d.PreDownloadPiece(context.TODO(), task)
	assert.Equal(t, ErrExceedBucketQuota, err)
}
