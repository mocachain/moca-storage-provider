package executor

import (
	"context"
	"io"
	"strings"
	"testing"

	sdkmath "cosmossdk.io/math"
	"github.com/0xPolygon/polygon-edge/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	sptypes "github.com/evmos/evmos/v12/x/sp/types"
	storagetypes "github.com/evmos/evmos/v12/x/storage/types"
	virtual_types "github.com/evmos/evmos/v12/x/virtualgroup/types"
	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	"github.com/mocachain/moca-storage-provider/core/piecestore"
	corercmgr "github.com/mocachain/moca-storage-provider/core/rcmgr"
	"github.com/mocachain/moca-storage-provider/core/spdb"
	corespdb "github.com/mocachain/moca-storage-provider/core/spdb"
	coretask "github.com/mocachain/moca-storage-provider/core/task"
	metadatatypes "github.com/mocachain/moca-storage-provider/modular/metadata/types"
)

const (
	mockBucketName = "mock-bucket-name"
	mockObjectName = "mock-object-name"
)

func mustMarshalBLSSignatures(t *testing.T, total int) [][]byte {
	t.Helper()

	signatures := make([][]byte, 0, total)
	for i := 0; i < total; i++ {
		key, err := bls.GenerateBlsKey()
		require.NoError(t, err)

		signature, err := key.Sign([]byte("seal-object"), []byte("test-domain"))
		require.NoError(t, err)

		raw, err := signature.Marshal()
		require.NoError(t, err)
		signatures = append(signatures, raw)
	}

	return signatures
}

func TestErrGfSpDBWithDetail(t *testing.T) {
	err := ErrGfSpDBWithDetail("test")
	assert.NotNil(t, err)
}

func TestErrPieceStoreWithDetail(t *testing.T) {
	err := ErrPieceStoreWithDetail("test")
	assert.NotNil(t, err)
}

func TestErrConsensusWithDetail(t *testing.T) {
	err := ErrConsensusWithDetail("test")
	assert.NotNil(t, err)
}

func TestExecuteModular_HandleSealObjectTask(t *testing.T) {
	cases := []struct {
		name      string
		task      coretask.SealObjectTask
		fn        func() *ExecuteModular
		wantedErr error
	}{
		{
			name:      "dangling pointer",
			task:      &gfsptask.GfSpSealObjectTask{Task: &gfsptask.GfSpTask{}},
			fn:        func() *ExecuteModular { return setup(t) },
			wantedErr: ErrDanglingPointer,
		},
		{
			name: "invalid secondary sig",
			task: &gfsptask.GfSpSealObjectTask{
				Task:                &gfsptask.GfSpTask{MaxRetry: 1},
				ObjectInfo:          &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)},
				StorageParams:       &storagetypes.Params{},
				SecondarySignatures: [][]byte{[]byte("test")},
			},
			fn:        func() *ExecuteModular { return setup(t) },
			wantedErr: ErrUnsealed,
		},
		{
			name: "object is sealed",
			task: &gfsptask.GfSpSealObjectTask{
				Task:                &gfsptask.GfSpTask{MaxRetry: 1},
				ObjectInfo:          &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)},
				StorageParams:       &storagetypes.Params{},
				SecondarySignatures: mustMarshalBLSSignatures(t, 4),
			},
			fn: func() *ExecuteModular {
				e := setup(t)
				e.maxListenSealRetry = 1
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().SealObject(gomock.Any(), gomock.Any()).Return("", mockErr).Times(2)
				e.baseApp.SetGfSpClient(m)

				m1 := consensus.NewMockConsensus(ctrl)
				m1.EXPECT().ListenObjectSeal(gomock.Any(), gomock.Any(), gomock.Any()).Return(true, nil).Times(1)
				e.baseApp.SetConsensus(m1)
				return e
			},
			wantedErr: ErrUnsealed,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tt.fn().HandleSealObjectTask(context.TODO(), tt.task)
		})
	}
}

func TestExecuteModular_sealObject(t *testing.T) {
	cases := []struct {
		name      string
		fn        func() *ExecuteModular
		wantedErr error
	}{
		{
			name: "object unsealed",
			fn: func() *ExecuteModular {
				e := setup(t)
				e.maxListenSealRetry = 1
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().SealObject(gomock.Any(), gomock.Any()).Return("", mockErr).Times(2)
				e.baseApp.SetGfSpClient(m)

				m1 := consensus.NewMockConsensus(ctrl)
				m1.EXPECT().ListenObjectSeal(gomock.Any(), gomock.Any(), gomock.Any()).Return(false, nil).Times(1)
				e.baseApp.SetConsensus(m1)
				return e
			},
			wantedErr: ErrUnsealed,
		},
		{
			name: "object is sealed",
			fn: func() *ExecuteModular {
				e := setup(t)
				e.maxListenSealRetry = 1
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().SealObject(gomock.Any(), gomock.Any()).Return("txHash", nil).Times(1)
				e.baseApp.SetGfSpClient(m)

				m1 := consensus.NewMockConsensus(ctrl)
				m1.EXPECT().ListenObjectSeal(gomock.Any(), gomock.Any(), gomock.Any()).Return(true, nil).Times(1)
				e.baseApp.SetConsensus(m1)
				return e
			},
			wantedErr: nil,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			objectInfo := &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)}
			task := &gfsptask.GfSpUploadObjectTask{
				Task:                 &gfsptask.GfSpTask{MaxRetry: 1},
				VirtualGroupFamilyId: 1,
				ObjectInfo:           objectInfo,
				StorageParams:        &storagetypes.Params{},
			}
			sealMsg := &storagetypes.MsgSealObject{ObjectName: "mockObjectName"}
			err := tt.fn().sealObject(context.TODO(), task, sealMsg)
			assert.Equal(t, tt.wantedErr, err)
		})
	}
}

func TestExecuteModular_listenSealObject(t *testing.T) {
	cases := []struct {
		name      string
		fn        func() *ExecuteModular
		wantedErr error
	}{
		{
			name: "failed to listen object seal",
			fn: func() *ExecuteModular {
				e := setup(t)
				e.maxListenSealRetry = 1
				ctrl := gomock.NewController(t)
				m := consensus.NewMockConsensus(ctrl)
				m.EXPECT().ListenObjectSeal(gomock.Any(), gomock.Any(), gomock.Any()).Return(false, mockErr).Times(1)
				e.baseApp.SetConsensus(m)
				return e
			},
			wantedErr: mockErr,
		},
		{
			name: "object unsealed",
			fn: func() *ExecuteModular {
				e := setup(t)
				e.maxListenSealRetry = 1
				ctrl := gomock.NewController(t)
				m := consensus.NewMockConsensus(ctrl)
				m.EXPECT().ListenObjectSeal(gomock.Any(), gomock.Any(), gomock.Any()).Return(false, nil).Times(1)
				e.baseApp.SetConsensus(m)
				return e
			},
			wantedErr: ErrUnsealed,
		},
		{
			name: "object is sealed",
			fn: func() *ExecuteModular {
				e := setup(t)
				e.maxListenSealRetry = 1
				ctrl := gomock.NewController(t)
				m := consensus.NewMockConsensus(ctrl)
				m.EXPECT().ListenObjectSeal(gomock.Any(), gomock.Any(), gomock.Any()).Return(true, nil).Times(1)
				e.baseApp.SetConsensus(m)
				return e
			},
			wantedErr: nil,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			objectInfo := &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)}
			task := &gfsptask.GfSpUploadObjectTask{
				Task:                 &gfsptask.GfSpTask{},
				VirtualGroupFamilyId: 1,
				ObjectInfo:           objectInfo,
				StorageParams:        &storagetypes.Params{},
			}
			err := tt.fn().listenSealObject(context.TODO(), task, objectInfo)
			assert.Equal(t, tt.wantedErr, err)
		})
	}
}

func TestExecuteModular_HandleReceivePieceTask(t *testing.T) {
	cases := []struct {
		name string
		task coretask.ReceivePieceTask
		fn   func() *ExecuteModular
	}{
		{
			name: "task pointer dangling",
			task: &gfsptask.GfSpReceivePieceTask{},
			fn:   func() *ExecuteModular { return setup(t) },
		},
		{
			name: "failed to get object info",
			task: &gfsptask.GfSpReceivePieceTask{
				Task:          &gfsptask.GfSpTask{},
				ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)},
				StorageParams: &storagetypes.Params{MaxPayloadSize: 10},
			},
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetObjectByID(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
				e.baseApp.SetGfSpClient(m)
				return e
			},
		},
		{
			name: "failed to confirm receive task, object is unsealed",
			task: &gfsptask.GfSpReceivePieceTask{
				Task:          &gfsptask.GfSpTask{},
				ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)},
				StorageParams: &storagetypes.Params{MaxPayloadSize: 10},
			},
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetObjectByID(gomock.Any(), gomock.Any()).Return(&storagetypes.ObjectInfo{
					ObjectStatus: storagetypes.OBJECT_STATUS_CREATED,
				}, nil).Times(1)
				e.baseApp.SetGfSpClient(m)
				return e
			},
		},
		{
			name: "failed to get bucket by bucket name",
			task: &gfsptask.GfSpReceivePieceTask{
				Task:          &gfsptask.GfSpTask{},
				ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)},
				StorageParams: &storagetypes.Params{MaxPayloadSize: 10},
			},
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetObjectByID(gomock.Any(), gomock.Any()).Return(&storagetypes.ObjectInfo{
					ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
				}, nil).Times(1)
				m.EXPECT().GetBucketByBucketName(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
				e.baseApp.SetGfSpClient(m)
				return e
			},
		},
		{
			name: "failed to get global virtual group",
			task: &gfsptask.GfSpReceivePieceTask{
				Task:          &gfsptask.GfSpTask{},
				ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)},
				StorageParams: &storagetypes.Params{MaxPayloadSize: 10},
			},
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetObjectByID(gomock.Any(), gomock.Any()).Return(&storagetypes.ObjectInfo{
					ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
				}, nil).Times(1)
				m.EXPECT().GetBucketByBucketName(gomock.Any(), gomock.Any(), gomock.Any()).Return(&metadatatypes.Bucket{
					BucketInfo: &storagetypes.BucketInfo{Id: sdkmath.NewUint(1)},
				}, nil).Times(1)
				m.EXPECT().GetGlobalVirtualGroup(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
				e.baseApp.SetGfSpClient(m)
				return e
			},
		},
		{
			name: "replicate idx out of bounds",
			task: &gfsptask.GfSpReceivePieceTask{
				Task:          &gfsptask.GfSpTask{},
				ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)},
				StorageParams: &storagetypes.Params{MaxPayloadSize: 10},
			},
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetObjectByID(gomock.Any(), gomock.Any()).Return(&storagetypes.ObjectInfo{
					ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
				}, nil).Times(1)
				m.EXPECT().GetBucketByBucketName(gomock.Any(), gomock.Any(), gomock.Any()).Return(&metadatatypes.Bucket{
					BucketInfo: &storagetypes.BucketInfo{Id: sdkmath.NewUint(1)},
				}, nil).Times(1)
				m.EXPECT().GetGlobalVirtualGroup(gomock.Any(), gomock.Any(), gomock.Any()).Return(&virtual_types.GlobalVirtualGroup{
					SecondarySpIds: []uint32{},
				}, nil).Times(1)
				e.baseApp.SetGfSpClient(m)
				return e
			},
		},
		{
			name: "failed to get sp id",
			task: &gfsptask.GfSpReceivePieceTask{
				Task:          &gfsptask.GfSpTask{},
				ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)},
				StorageParams: &storagetypes.Params{MaxPayloadSize: 10},
			},
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetObjectByID(gomock.Any(), gomock.Any()).Return(&storagetypes.ObjectInfo{
					ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
				}, nil).Times(1)
				m.EXPECT().GetBucketByBucketName(gomock.Any(), gomock.Any(), gomock.Any()).Return(&metadatatypes.Bucket{
					BucketInfo: &storagetypes.BucketInfo{Id: sdkmath.NewUint(1)},
				}, nil).Times(1)
				m.EXPECT().GetGlobalVirtualGroup(gomock.Any(), gomock.Any(), gomock.Any()).Return(&virtual_types.GlobalVirtualGroup{
					SecondarySpIds: []uint32{1},
				}, nil).Times(1)
				e.baseApp.SetGfSpClient(m)

				m1 := consensus.NewMockConsensus(ctrl)
				m1.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
				e.baseApp.SetConsensus(m1)
				return e
			},
		},
		{
			name: "success",
			task: &gfsptask.GfSpReceivePieceTask{
				Task:          &gfsptask.GfSpTask{},
				ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)},
				StorageParams: &storagetypes.Params{MaxPayloadSize: 10},
			},
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetObjectByID(gomock.Any(), gomock.Any()).Return(&storagetypes.ObjectInfo{
					ObjectStatus: storagetypes.OBJECT_STATUS_SEALED,
				}, nil).Times(1)
				m.EXPECT().GetBucketByBucketName(gomock.Any(), gomock.Any(), gomock.Any()).Return(&metadatatypes.Bucket{
					BucketInfo: &storagetypes.BucketInfo{Id: sdkmath.NewUint(1)},
				}, nil).Times(1)
				m.EXPECT().GetGlobalVirtualGroup(gomock.Any(), gomock.Any(), gomock.Any()).Return(&virtual_types.GlobalVirtualGroup{
					SecondarySpIds: []uint32{1},
				}, nil).Times(1)
				e.baseApp.SetGfSpClient(m)

				m1 := consensus.NewMockConsensus(ctrl)
				m1.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Id: 1}, nil).Times(1)
				e.baseApp.SetConsensus(m1)
				return e
			},
		},
		{
			name: "failed to delete integrity, REDUNDANCY_EC_TYPE and failed to delete piece data",
			task: &gfsptask.GfSpReceivePieceTask{
				Task: &gfsptask.GfSpTask{},
				ObjectInfo: &storagetypes.ObjectInfo{
					Id:             sdkmath.NewUint(1),
					RedundancyType: storagetypes.REDUNDANCY_EC_TYPE,
				},
				StorageParams: &storagetypes.Params{MaxPayloadSize: 10},
			},
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetObjectByID(gomock.Any(), gomock.Any()).Return(&storagetypes.ObjectInfo{
					ObjectStatus: storagetypes.OBJECT_STATUS_SEALED, Id: sdkmath.NewUint(1),
				}, nil).Times(1)
				m.EXPECT().GetBucketByBucketName(gomock.Any(), gomock.Any(), gomock.Any()).Return(&metadatatypes.Bucket{
					BucketInfo: &storagetypes.BucketInfo{Id: sdkmath.NewUint(1)},
				}, nil).Times(1)
				m.EXPECT().GetGlobalVirtualGroup(gomock.Any(), gomock.Any(), gomock.Any()).Return(&virtual_types.GlobalVirtualGroup{
					SecondarySpIds: []uint32{2},
				}, nil).Times(1)
				e.baseApp.SetGfSpClient(m)

				m1 := consensus.NewMockConsensus(ctrl)
				m1.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Id: 1}, nil).Times(1)
				e.baseApp.SetConsensus(m1)

				m2 := corespdb.NewMockSPDB(ctrl)
				m2.EXPECT().DeleteObjectIntegrity(gomock.Any(), gomock.Any()).Return(mockErr).Times(1)
				e.baseApp.SetGfSpDB(m2)

				m3 := piecestore.NewMockPieceOp(ctrl)
				m3.EXPECT().SegmentPieceCount(gomock.Any(), gomock.Any()).Return(uint32(1)).Times(1)
				m3.EXPECT().ECPieceKey(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("test").Times(1)
				e.baseApp.SetPieceOp(m3)

				m4 := piecestore.NewMockPieceStore(ctrl)
				m4.EXPECT().DeletePiece(gomock.Any(), gomock.Any()).Return(mockErr).Times(1)
				e.baseApp.SetPieceStore(m4)
				return e
			},
		},
		{
			name: "non REDUNDANCY_EC_TYPE and succeed to delete piece data",
			task: &gfsptask.GfSpReceivePieceTask{
				Task: &gfsptask.GfSpTask{},
				ObjectInfo: &storagetypes.ObjectInfo{
					Id:             sdkmath.NewUint(1),
					RedundancyType: storagetypes.REDUNDANCY_REPLICA_TYPE,
				},
				StorageParams: &storagetypes.Params{MaxPayloadSize: 10},
			},
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetObjectByID(gomock.Any(), gomock.Any()).Return(&storagetypes.ObjectInfo{
					ObjectStatus: storagetypes.OBJECT_STATUS_SEALED, Id: sdkmath.NewUint(1),
				}, nil).Times(1)
				m.EXPECT().GetBucketByBucketName(gomock.Any(), gomock.Any(), gomock.Any()).Return(&metadatatypes.Bucket{
					BucketInfo: &storagetypes.BucketInfo{Id: sdkmath.NewUint(1)},
				}, nil).Times(1)
				m.EXPECT().GetGlobalVirtualGroup(gomock.Any(), gomock.Any(), gomock.Any()).Return(&virtual_types.GlobalVirtualGroup{
					SecondarySpIds: []uint32{2},
				}, nil).Times(1)
				e.baseApp.SetGfSpClient(m)

				m1 := consensus.NewMockConsensus(ctrl)
				m1.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Id: 1}, nil).Times(1)
				e.baseApp.SetConsensus(m1)

				m2 := spdb.NewMockSPDB(ctrl)
				m2.EXPECT().DeleteObjectIntegrity(gomock.Any(), gomock.Any()).Return(nil).Times(1)
				e.baseApp.SetGfSpDB(m2)

				m3 := piecestore.NewMockPieceOp(ctrl)
				m3.EXPECT().SegmentPieceCount(gomock.Any(), gomock.Any()).Return(uint32(1)).Times(1)
				m3.EXPECT().SegmentPieceKey(gomock.Any(), gomock.Any(), gomock.Any()).Return("test").Times(1)
				e.baseApp.SetPieceOp(m3)

				m4 := piecestore.NewMockPieceStore(ctrl)
				m4.EXPECT().DeletePiece(gomock.Any(), gomock.Any()).Return(nil).Times(1)
				e.baseApp.SetPieceStore(m4)
				return e
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tt.fn().HandleReceivePieceTask(context.TODO(), tt.task)
		})
	}
}

func TestExecuteModular_HandleGCObjectTask(t *testing.T) {
	cases := []struct {
		name string
		task coretask.GCObjectTask
		fn   func() *ExecuteModular
	}{
		{
			name: "failed to query deleted object list",
			task: &gfsptask.GfSpGCObjectTask{
				Task: &gfsptask.GfSpTask{},
			},
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().ListDeletedObjectsByBlockNumberRange(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(nil, uint64(0), mockErr).Times(1)
				m.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).Times(1)
				e.baseApp.SetGfSpClient(m)
				return e
			},
		},
		{
			name: "metadata is not latest",
			task: &gfsptask.GfSpGCObjectTask{
				Task:             &gfsptask.GfSpTask{},
				StartBlockNumber: 1,
			},
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().ListDeletedObjectsByBlockNumberRange(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(nil, uint64(0), nil).Times(1)
				m.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).Times(1)
				e.baseApp.SetGfSpClient(m)
				return e
			},
		},
		{
			name: "no waiting gc objects",
			task: &gfsptask.GfSpGCObjectTask{
				Task: &gfsptask.GfSpTask{},
			},
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().ListDeletedObjectsByBlockNumberRange(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(nil, uint64(0), nil).Times(1)
				m.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).Times(1)
				e.baseApp.SetGfSpClient(m)
				return e
			},
		},
		{
			name: "failed to get bucket by bucket name",
			task: &gfsptask.GfSpGCObjectTask{
				Task:               &gfsptask.GfSpTask{},
				CurrentBlockNumber: 0,
			},
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				waitingGCObjects := []*metadatatypes.Object{
					{
						ObjectInfo: &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)},
					},
				}
				m.EXPECT().ListDeletedObjectsByBlockNumberRange(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(waitingGCObjects, uint64(0), nil).Times(1)
				m.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).Times(1)
				m.EXPECT().GetBucketInfoByBucketName(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
				e.baseApp.SetGfSpClient(m)

				m1 := consensus.NewMockConsensus(ctrl)
				m1.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Id: 1}, nil).Times(1)
				e.baseApp.SetConsensus(m1)

				m2 := piecestore.NewMockPieceOp(ctrl)
				e.baseApp.SetPieceOp(m2)

				m3 := piecestore.NewMockPieceStore(ctrl)
				m3.EXPECT().DeletePiecesByPrefix(gomock.Any(), gomock.Any()).Return(uint64(0), nil).Times(1)
				e.baseApp.SetPieceStore(m3)
				return e
			},
		},
		{
			name: "failed to get global virtual group",
			task: &gfsptask.GfSpGCObjectTask{
				Task:               &gfsptask.GfSpTask{},
				CurrentBlockNumber: 0,
			},
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				waitingGCObjects := []*metadatatypes.Object{
					{
						ObjectInfo: &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)},
					},
				}
				m.EXPECT().ListDeletedObjectsByBlockNumberRange(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(waitingGCObjects, uint64(0), nil).Times(1)
				m.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).Times(1)
				m.EXPECT().GetBucketInfoByBucketName(gomock.Any(), gomock.Any()).Return(&metadatatypes.Bucket{
					BucketInfo: &storagetypes.BucketInfo{Id: sdkmath.NewUint(1)},
				}, nil).Times(1)
				m.EXPECT().GetGlobalVirtualGroup(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
				e.baseApp.SetGfSpClient(m)

				m1 := consensus.NewMockConsensus(ctrl)
				m1.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Id: 1}, nil).Times(1)
				e.baseApp.SetConsensus(m1)

				m2 := piecestore.NewMockPieceOp(ctrl)
				e.baseApp.SetPieceOp(m2)

				m3 := piecestore.NewMockPieceStore(ctrl)
				m3.EXPECT().DeletePiecesByPrefix(gomock.Any(), gomock.Any()).Return(uint64(0), nil).Times(1)
				e.baseApp.SetPieceStore(m3)
				return e
			},
		},
		{
			name: "failed to get sp id",
			task: &gfsptask.GfSpGCObjectTask{
				Task:               &gfsptask.GfSpTask{},
				CurrentBlockNumber: 0,
			},
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				waitingGCObjects := []*metadatatypes.Object{
					{
						ObjectInfo: &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)},
					},
				}
				m.EXPECT().ListDeletedObjectsByBlockNumberRange(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(waitingGCObjects, uint64(0), nil).Times(1)
				m.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).Times(1)
				e.baseApp.SetGfSpClient(m)

				m1 := consensus.NewMockConsensus(ctrl)
				m1.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
				e.baseApp.SetConsensus(m1)

				return e
			},
		},
		{
			name: "succeed to gc an object",
			task: &gfsptask.GfSpGCObjectTask{
				Task:               &gfsptask.GfSpTask{},
				CurrentBlockNumber: 0,
			},
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				waitingGCObjects := []*metadatatypes.Object{
					{
						ObjectInfo: &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)},
					},
				}
				m.EXPECT().ListDeletedObjectsByBlockNumberRange(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(waitingGCObjects, uint64(0), nil).AnyTimes()
				m.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				m.EXPECT().GetBucketInfoByBucketName(gomock.Any(), gomock.Any()).Return(&metadatatypes.Bucket{
					BucketInfo: &storagetypes.BucketInfo{Id: sdkmath.NewUint(1)},
				}, nil).Times(1)
				m.EXPECT().GetGlobalVirtualGroup(gomock.Any(), gomock.Any(), gomock.Any()).Return(&virtual_types.GlobalVirtualGroup{
					SecondarySpIds: []uint32{1},
				}, nil).Times(1)
				e.baseApp.SetGfSpClient(m)

				m1 := consensus.NewMockConsensus(ctrl)
				m1.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Id: 1}, nil).Times(1)
				e.baseApp.SetConsensus(m1)

				m2 := piecestore.NewMockPieceOp(ctrl)
				e.baseApp.SetPieceOp(m2)

				m3 := piecestore.NewMockPieceStore(ctrl)
				m3.EXPECT().DeletePiecesByPrefix(gomock.Any(), gomock.Any()).Return(uint64(0), nil).Times(2)
				e.baseApp.SetPieceStore(m3)

				m4 := corespdb.NewMockSPDB(ctrl)
				m4.EXPECT().DeleteObjectIntegrity(gomock.Any(), gomock.Any()).Return(nil).Times(1)
				e.baseApp.SetGfSpDB(m4)
				return e
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tt.fn().HandleGCObjectTask(context.TODO(), tt.task)
		})
	}
}

func TestExecuteModular_HandleGCZombiePieceTask(t *testing.T) {
	cases := []struct {
		name string
		task coretask.GCZombiePieceTask
		fn   func() *ExecuteModular
	}{
		{
			name: "succeed to gc an zombie piece",
			task: &gfsptask.GfSpGCZombiePieceTask{
				Task: &gfsptask.GfSpTask{},
			},
			fn: func() *ExecuteModular {
				e := setup(t)

				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				waitingIntegrityPieces := []*corespdb.IntegrityMeta{
					{
						ObjectID:          1,
						RedundancyIndex:   1,
						IntegrityChecksum: []byte("mock integrity checksum"),
						PieceChecksumList: [][]byte{{
							35, 13, 131, 88, 220, 142, 136, 144, 180, 197, 141, 238, 182, 41, 18, 238, 47,
							32, 53, 122, 233, 42, 92, 200, 97, 185, 142, 104, 254, 49, 172, 181,
						}},
					},
				}

				objectInfo := &storagetypes.ObjectInfo{
					ObjectStatus: storagetypes.OBJECT_STATUS_SEALED, Id: sdkmath.NewUint(1), BucketName: mockBucketName,
				}

				bucketInfo := &storagetypes.BucketInfo{Id: sdkmath.NewUint(1), BucketStatus: storagetypes.BUCKET_STATUS_CREATED}

				m.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				m.EXPECT().GetObjectByID(gomock.Any(), gomock.Any()).Return(objectInfo, nil).AnyTimes()
				m.EXPECT().GetBucketInfoByBucketName(gomock.Any(), gomock.Any()).Return(&metadatatypes.Bucket{
					BucketInfo: bucketInfo,
				}, nil).AnyTimes()
				m.EXPECT().GetGlobalVirtualGroup(gomock.Any(), gomock.Any(), gomock.Any()).Return(&virtual_types.GlobalVirtualGroup{
					PrimarySpId:    1,
					SecondarySpIds: []uint32{2, 3, 4, 5, 6, 7},
				}, nil).Times(1)
				e.baseApp.SetGfSpClient(m)

				resourceMock := corercmgr.NewMockResourceManager(ctrl)
				m1 := corercmgr.NewMockResourceScope(ctrl)
				resourceMock.EXPECT().OpenService(gomock.Any()).DoAndReturn(func(svc string) (corercmgr.ResourceScope, error) {
					return m1, nil
				}).Times(1)
				e.baseApp.SetResourceManager(resourceMock)

				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Id: 1}, nil).Times(1)
				consensusMock.EXPECT().ListSPs(gomock.Any()).Return([]*sptypes.StorageProvider{
					{Id: 1, Endpoint: "endpoint"},
				}, nil).Times(1)
				consensusMock.EXPECT().QuerySwapInInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(&virtual_types.SwapInInfo{
					SuccessorSpId: 1, TargetSpId: 1,
				}, nil).Times(1)

				e.baseApp.SetConsensus(consensusMock)

				m2 := piecestore.NewMockPieceOp(ctrl)
				m2.EXPECT().ECPieceKey(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("test").AnyTimes()
				e.baseApp.SetPieceOp(m2)

				m3 := piecestore.NewMockPieceStore(ctrl)
				m3.EXPECT().DeletePiece(gomock.Any(), gomock.Any()).Return(nil).Times(2)
				e.baseApp.SetPieceStore(m3)

				m4 := corespdb.NewMockSPDB(ctrl)
				m4.EXPECT().DeleteObjectIntegrity(gomock.Any(), gomock.Any()).Return(nil).Times(1)
				m4.EXPECT().ListIntegrityMetaByObjectIDRange(gomock.Any(), gomock.Any(), gomock.Any()).Return(waitingIntegrityPieces, nil).AnyTimes()
				m4.EXPECT().ListReplicatePieceChecksumByObjectIDRange(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
				e.baseApp.SetGfSpDB(m4)

				e.statisticsOutputInterval = 1
				err := e.Start(context.TODO())
				assert.Equal(t, nil, err)

				return e
			},
		},
		{
			name: "succeed to gc an zombie piece from piece hash",
			task: &gfsptask.GfSpGCZombiePieceTask{
				Task: &gfsptask.GfSpTask{},
			},
			fn: func() *ExecuteModular {
				e := setup(t)

				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				waitingGCPieces := []*corespdb.GCPieceMeta{
					{
						ObjectID:        1,
						SegmentIndex:    0,
						RedundancyIndex: 1,
						PieceChecksum:   "mock integrity checksum",
					},
				}

				objectInfo := &storagetypes.ObjectInfo{
					ObjectStatus: storagetypes.OBJECT_STATUS_SEALED, Id: sdkmath.NewUint(1), BucketName: mockBucketName,
				}

				bucketInfo := &storagetypes.BucketInfo{Id: sdkmath.NewUint(1), BucketStatus: storagetypes.BUCKET_STATUS_CREATED}

				m.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				m.EXPECT().GetObjectByID(gomock.Any(), gomock.Any()).Return(objectInfo, nil).AnyTimes()
				m.EXPECT().GetBucketInfoByBucketName(gomock.Any(), gomock.Any()).Return(&metadatatypes.Bucket{
					BucketInfo: bucketInfo,
				}, nil).AnyTimes()
				m.EXPECT().GetGlobalVirtualGroup(gomock.Any(), gomock.Any(), gomock.Any()).Return(&virtual_types.GlobalVirtualGroup{
					PrimarySpId:    1,
					SecondarySpIds: []uint32{2, 3, 4, 5, 6, 7},
				}, nil).Times(1)
				e.baseApp.SetGfSpClient(m)

				resourceMock := corercmgr.NewMockResourceManager(ctrl)
				m1 := corercmgr.NewMockResourceScope(ctrl)
				resourceMock.EXPECT().OpenService(gomock.Any()).DoAndReturn(func(svc string) (corercmgr.ResourceScope, error) {
					return m1, nil
				}).Times(1)
				e.baseApp.SetResourceManager(resourceMock)

				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Id: 1}, nil).Times(1)
				consensusMock.EXPECT().ListSPs(gomock.Any()).Return([]*sptypes.StorageProvider{
					{Id: 1, Endpoint: "endpoint"},
				}, nil).Times(1)
				consensusMock.EXPECT().QuerySwapInInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(&virtual_types.SwapInInfo{
					SuccessorSpId: 1, TargetSpId: 1,
				}, nil).Times(1)
				e.baseApp.SetConsensus(consensusMock)

				m2 := piecestore.NewMockPieceOp(ctrl)
				m2.EXPECT().ECPieceKey(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("test").AnyTimes()
				e.baseApp.SetPieceOp(m2)

				m3 := piecestore.NewMockPieceStore(ctrl)
				m3.EXPECT().DeletePiece(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				e.baseApp.SetPieceStore(m3)

				m4 := corespdb.NewMockSPDB(ctrl)
				m4.EXPECT().ListIntegrityMetaByObjectIDRange(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
				m4.EXPECT().ListReplicatePieceChecksumByObjectIDRange(gomock.Any(), gomock.Any()).Return(waitingGCPieces, nil).AnyTimes()
				m4.EXPECT().DeleteReplicatePieceChecksum(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				e.baseApp.SetGfSpDB(m4)

				e.statisticsOutputInterval = 1
				err := e.Start(context.TODO())
				assert.Equal(t, nil, err)

				return e
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tt.fn().HandleGCZombiePieceTask(context.TODO(), tt.task)
		})
	}
}

func TestExecuteModular_HandleGCMetaTask(t *testing.T) {
	cases := []struct {
		name string
		task coretask.GCMetaTask
		fn   func() *ExecuteModular
	}{
		{
			name: "succeed to gc an meta task",
			task: &gfsptask.GfSpGCMetaTask{
				Task: &gfsptask.GfSpTask{},
			},
			fn: func() *ExecuteModular {
				e := setup(t)

				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)

				objectInfo := &storagetypes.ObjectInfo{
					ObjectStatus: storagetypes.OBJECT_STATUS_SEALED, Id: sdkmath.NewUint(1), BucketName: mockBucketName,
				}

				bucketInfo := &storagetypes.BucketInfo{Id: sdkmath.NewUint(1), BucketStatus: storagetypes.BUCKET_STATUS_CREATED}

				m.EXPECT().ReportTask(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				m.EXPECT().GetObjectByID(gomock.Any(), gomock.Any()).Return(objectInfo, nil).AnyTimes()
				m.EXPECT().GetBucketInfoByBucketName(gomock.Any(), gomock.Any()).Return(&metadatatypes.Bucket{
					BucketInfo: bucketInfo,
				}, nil).AnyTimes()
				e.baseApp.SetGfSpClient(m)

				resourceMock := corercmgr.NewMockResourceManager(ctrl)
				m1 := corercmgr.NewMockResourceScope(ctrl)
				resourceMock.EXPECT().OpenService(gomock.Any()).DoAndReturn(func(svc string) (corercmgr.ResourceScope, error) {
					return m1, nil
				}).Times(1)
				e.baseApp.SetResourceManager(resourceMock)

				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().ListSPs(gomock.Any()).Return([]*sptypes.StorageProvider{
					{Id: 1, Endpoint: "endpoint"},
				}, nil).Times(1)
				e.baseApp.SetConsensus(consensusMock)

				m2 := piecestore.NewMockPieceOp(ctrl)
				m2.EXPECT().ECPieceKey(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("test").AnyTimes()
				e.baseApp.SetPieceOp(m2)

				m4 := corespdb.NewMockSPDB(ctrl)
				m4.EXPECT().DeleteExpiredReadRecord(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				m4.EXPECT().DeleteExpiredBucketTraffic(gomock.Any()).Return(nil).AnyTimes()
				e.baseApp.SetGfSpDB(m4)

				e.statisticsOutputInterval = 1
				err := e.Start(context.TODO())
				assert.Equal(t, nil, err)

				return e
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tt.fn().HandleGCMetaTask(context.TODO(), tt.task)
		})
	}
}

func TestExecuteModular_HandleRecoverPieceTaskFailure1(t *testing.T) {
	t.Log("Failure case description: ErrDanglingPointer")
	e := setup(t)
	task := &gfsptask.GfSpRecoverPieceTask{
		Task:          &gfsptask.GfSpTask{},
		StorageParams: &storagetypes.Params{},
	}
	e.HandleRecoverPieceTask(context.TODO(), task)
}

func TestExecuteModular_HandleRecoverPieceTaskFailure2(t *testing.T) {
	t.Log("Failure case description: ErrRecoveryRedundancyType")
	e := setup(t)
	task := &gfsptask.GfSpRecoverPieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:             sdkmath.NewUint(1),
			RedundancyType: storagetypes.REDUNDANCY_REPLICA_TYPE,
		},
		StorageParams: &storagetypes.Params{},
	}
	e.HandleRecoverPieceTask(context.TODO(), task)
}

func TestExecuteModular_HandleRecoverPieceTaskFailure3(t *testing.T) {
	t.Log("Failure case description: ErrRecoveryPieceIndex")
	e := setup(t)
	task := &gfsptask.GfSpRecoverPieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:             sdkmath.NewUint(1),
			RedundancyType: storagetypes.REDUNDANCY_EC_TYPE,
		},
		StorageParams: &storagetypes.Params{},
		EcIdx:         -2,
	}
	e.HandleRecoverPieceTask(context.TODO(), task)
}

func TestExecuteModular_recoverByPrimarySPSuccess(t *testing.T) {
	e := setup(t)

	ctrl := gomock.NewController(t)
	m := gfspclient.NewMockGfSpClientAPI(ctrl)
	m.EXPECT().GetBucketMeta(gomock.Any(), gomock.Any(), true).Return(&metadatatypes.VGFInfoBucket{
		BucketInfo: &storagetypes.BucketInfo{Id: sdkmath.NewUint(1)},
	}, nil, nil).Times(1)
	m.EXPECT().SignRecoveryTask(gomock.Any(), gomock.Any()).Return([]byte("mockSig"), nil).Times(1)
	m.EXPECT().GetPieceFromECChunks(gomock.Any(), gomock.Any(), gomock.Any()).Return(io.NopCloser(
		strings.NewReader("body")), nil).Times(1)
	e.baseApp.SetGfSpClient(m)

	m1 := consensus.NewMockConsensus(ctrl)
	m1.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&virtual_types.GlobalVirtualGroupFamily{
		PrimarySpId: 1,
	}, nil).Times(1)
	m1.EXPECT().ListSPs(gomock.Any()).Return([]*sptypes.StorageProvider{
		{Id: 1, Endpoint: "endpoint"},
	}, nil).Times(1)
	e.baseApp.SetConsensus(m1)

	m2 := corespdb.NewMockSPDB(ctrl)
	m2.EXPECT().GetObjectIntegrity(gomock.Any(), gomock.Any()).Return(&corespdb.IntegrityMeta{
		PieceChecksumList: [][]byte{{
			35, 13, 131, 88, 220, 142, 136, 144, 180, 197, 141, 238, 182, 41, 18, 238, 47,
			32, 53, 122, 233, 42, 92, 200, 97, 185, 142, 104, 254, 49, 172, 181,
		}},
	}, nil).Times(1)
	e.baseApp.SetGfSpDB(m2)

	m3 := piecestore.NewMockPieceOp(ctrl)
	m3.EXPECT().ECPieceKey(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("test").Times(1)
	e.baseApp.SetPieceOp(m3)

	m4 := piecestore.NewMockPieceStore(ctrl)
	m4.EXPECT().PutPiece(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)
	e.baseApp.SetPieceStore(m4)

	task := &gfsptask.GfSpRecoverPieceTask{
		Task:          &gfsptask.GfSpTask{},
		ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)},
		StorageParams: &storagetypes.Params{},
	}
	err := e.recoverByPrimarySP(context.TODO(), task)
	assert.Nil(t, err)
}

func TestExecuteModular_recoverBySecondarySPFailure1(t *testing.T) {
	e := setup(t)

	ctrl := gomock.NewController(t)
	m := gfspclient.NewMockGfSpClientAPI(ctrl)
	m.EXPECT().GetBucketByBucketName(gomock.Any(), gomock.Any(), true).Return(nil, mockErr).Times(1)
	e.baseApp.SetGfSpClient(m)

	task := &gfsptask.GfSpRecoverPieceTask{
		Task:          &gfsptask.GfSpTask{},
		ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)},
		StorageParams: &storagetypes.Params{},
	}
	err := e.recoverBySecondarySP(context.TODO(), task, true)
	assert.Equal(t, mockErr, err)
}

func TestExecuteModular_recoverBySecondarySPFailure2(t *testing.T) {
	e := setup(t)

	ctrl := gomock.NewController(t)
	m := gfspclient.NewMockGfSpClientAPI(ctrl)
	m.EXPECT().GetBucketByBucketName(gomock.Any(), gomock.Any(), true).Return(
		&metadatatypes.Bucket{BucketInfo: &storagetypes.BucketInfo{Id: sdkmath.NewUint(1)}}, nil).Times(1)
	m.EXPECT().GetGlobalVirtualGroup(gomock.Any(), gomock.Any(), gomock.Any()).Return(
		&virtual_types.GlobalVirtualGroup{SecondarySpIds: []uint32{1}}, nil).Times(1)
	e.baseApp.SetGfSpClient(m)

	m1 := consensus.NewMockConsensus(ctrl)
	m1.EXPECT().ListSPs(gomock.Any()).Return([]*sptypes.StorageProvider{{Id: 1, Endpoint: "endpoint"}}, nil).Times(1)
	e.baseApp.SetConsensus(m1)

	task := &gfsptask.GfSpRecoverPieceTask{
		Task:          &gfsptask.GfSpTask{},
		ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(1)},
		StorageParams: &storagetypes.Params{},
	}
	err := e.recoverBySecondarySP(context.TODO(), task, true)
	assert.Equal(t, ErrRecoveryPieceNotEnough, err)
}

func TestExecuteModular_getECPieceBySegment(t *testing.T) {
	cases := []struct {
		name          string
		redundancyIdx int32
		objectInfo    *storagetypes.ObjectInfo
		params        *storagetypes.Params
		fn            func() *ExecuteModular
		wantedIsErr   bool
		wantedErrStr  string
	}{
		{
			name:          "invalid redundancyIdx",
			redundancyIdx: -1,
			objectInfo:    &storagetypes.ObjectInfo{},
			params:        &storagetypes.Params{},
			fn:            func() *ExecuteModular { return setup(t) },
			wantedIsErr:   true,
			wantedErrStr:  "invalid redundancyIdx",
		},
		{
			name:          "no error",
			redundancyIdx: 1,
			objectInfo:    &storagetypes.ObjectInfo{},
			params:        &storagetypes.Params{VersionedParams: storagetypes.VersionedParams{RedundantDataChunkNum: 4}},
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := piecestore.NewMockPieceOp(ctrl)
				m.EXPECT().ECPieceSize(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)
				e.baseApp.SetPieceOp(m)
				return e
			},
			wantedIsErr: false,
		},
		{
			name:          "no error",
			redundancyIdx: 4,
			objectInfo:    &storagetypes.ObjectInfo{},
			params: &storagetypes.Params{VersionedParams: storagetypes.VersionedParams{
				RedundantDataChunkNum:   4,
				RedundantParityChunkNum: 2,
			}},
			fn:          func() *ExecuteModular { return setup(t) },
			wantedIsErr: false,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.fn().getECPieceBySegment(context.TODO(), tt.redundancyIdx, tt.objectInfo, tt.params,
				[]byte("test"), 1)
			if tt.wantedIsErr {
				assert.Contains(t, err.Error(), tt.wantedErrStr)
				assert.Nil(t, result)
			} else {
				assert.Nil(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

func TestExecuteModular_checkRecoveryChecksum(t *testing.T) {
	cases := []struct {
		name         string
		fn           func() *ExecuteModular
		wantedIsErr  bool
		wantedErrStr string
	}{
		{
			name: "failed to get object integrity hash",
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := corespdb.NewMockSPDB(ctrl)
				m.EXPECT().GetObjectIntegrity(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
				e.baseApp.SetGfSpDB(m)
				return e
			},
			wantedIsErr:  true,
			wantedErrStr: "failed to get object integrity hash in db",
		},
		{
			name: "check integrity hash of recovery data err",
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := corespdb.NewMockSPDB(ctrl)
				m.EXPECT().GetObjectIntegrity(gomock.Any(), gomock.Any()).Return(&corespdb.IntegrityMeta{
					PieceChecksumList: [][]byte{[]byte("a")},
				}, nil).Times(1)
				e.baseApp.SetGfSpDB(m)
				return e
			},
			wantedIsErr:  true,
			wantedErrStr: ErrRecoveryPieceChecksum.Error(),
		},
		{
			name: "success",
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := corespdb.NewMockSPDB(ctrl)
				m.EXPECT().GetObjectIntegrity(gomock.Any(), gomock.Any()).Return(&corespdb.IntegrityMeta{
					PieceChecksumList: [][]byte{[]byte("test")},
				}, nil).Times(1)
				e.baseApp.SetGfSpDB(m)
				return e
			},
			wantedIsErr: false,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fn().checkRecoveryChecksum(context.TODO(), &gfsptask.GfSpRecoverPieceTask{
				Task: &gfsptask.GfSpTask{},
				ObjectInfo: &storagetypes.ObjectInfo{
					ObjectName: "mockObjectName",
					Id:         sdkmath.NewUint(1),
				},
			}, []byte{116, 101, 115, 116})
			if tt.wantedIsErr {
				assert.Contains(t, err.Error(), tt.wantedErrStr)
			} else {
				assert.Nil(t, err)
			}
		})
	}
}

func TestExecuteModular_doRecoveryPiece(t *testing.T) {
	cases := []struct {
		name        string
		fn          func() *ExecuteModular
		wantedIsErr bool
		wantedErr   error
	}{
		{
			name: "failed to get piece from ec chunks",
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetPieceFromECChunks(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
				e.baseApp.SetGfSpClient(m)
				return e
			},
			wantedIsErr: true,
			wantedErr:   mockErr,
		},
		{
			name: "failed to read recovery piece data from sp",
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				e.baseApp.SetGfSpClient(m)

				m1 := gfspclient.NewMockstdLib(ctrl)
				m1.EXPECT().Read(gomock.Any()).Return(0, mockErr).Times(1)
				m1.EXPECT().Close().AnyTimes()
				m.EXPECT().GetPieceFromECChunks(gomock.Any(), gomock.Any(), gomock.Any()).Return(m1, nil).Times(1)
				return e
			},
			wantedIsErr: true,
			wantedErr:   mockErr,
		},
		{
			name: "success",
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				e.baseApp.SetGfSpClient(m)
				m.EXPECT().GetPieceFromECChunks(gomock.Any(), gomock.Any(), gomock.Any()).Return(io.NopCloser(
					strings.NewReader("body")), nil).Times(1)
				return e
			},
			wantedIsErr: false,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.fn().doRecoveryPiece(context.TODO(), &gfsptask.GfSpRecoverPieceTask{
				Task: &gfsptask.GfSpTask{},
				ObjectInfo: &storagetypes.ObjectInfo{
					ObjectName: "mockObjectName",
					Id:         sdkmath.NewUint(1),
				},
			}, "mockEndpoint")
			if tt.wantedIsErr {
				assert.Equal(t, tt.wantedErr, err)
				assert.Nil(t, result)
			} else {
				assert.Nil(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

func TestExecuteModular_getObjectSecondaryEndpoints(t *testing.T) {
	cases := []struct {
		name        string
		fn          func() *ExecuteModular
		wantedIsErr bool
		wantedErr   error
	}{
		{
			name: "failed to GetBucketByBucketName",
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetBucketByBucketName(gomock.Any(), gomock.Any(), true).Return(nil, mockErr).Times(1)
				e.baseApp.SetGfSpClient(m)
				return e
			},
			wantedIsErr: true,
			wantedErr:   mockErr,
		},
		{
			name: "failed to GetGlobalVirtualGroup",
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetBucketByBucketName(gomock.Any(), gomock.Any(), true).Return(
					&metadatatypes.Bucket{BucketInfo: &storagetypes.BucketInfo{Id: sdkmath.NewUint(1)}}, nil).Times(1)
				m.EXPECT().GetGlobalVirtualGroup(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
				e.baseApp.SetGfSpClient(m)
				return e
			},
			wantedIsErr: true,
			wantedErr:   mockErr,
		},
		{
			name: "failed to ListSPs",
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetBucketByBucketName(gomock.Any(), gomock.Any(), true).Return(
					&metadatatypes.Bucket{BucketInfo: &storagetypes.BucketInfo{Id: sdkmath.NewUint(1)}}, nil).Times(1)
				m.EXPECT().GetGlobalVirtualGroup(gomock.Any(), gomock.Any(), gomock.Any()).Return(
					&virtual_types.GlobalVirtualGroup{SecondarySpIds: []uint32{1}}, nil).Times(1)
				e.baseApp.SetGfSpClient(m)

				m1 := consensus.NewMockConsensus(ctrl)
				m1.EXPECT().ListSPs(gomock.Any()).Return(nil, mockErr).Times(1)
				e.baseApp.SetConsensus(m1)
				return e
			},
			wantedIsErr: true,
			wantedErr:   mockErr,
		},
		{
			name: "success",
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetBucketByBucketName(gomock.Any(), gomock.Any(), true).Return(
					&metadatatypes.Bucket{BucketInfo: &storagetypes.BucketInfo{Id: sdkmath.NewUint(1)}}, nil).Times(1)
				m.EXPECT().GetGlobalVirtualGroup(gomock.Any(), gomock.Any(), gomock.Any()).Return(
					&virtual_types.GlobalVirtualGroup{SecondarySpIds: []uint32{1}}, nil).Times(1)
				e.baseApp.SetGfSpClient(m)

				m1 := consensus.NewMockConsensus(ctrl)
				m1.EXPECT().ListSPs(gomock.Any()).Return([]*sptypes.StorageProvider{{Id: 1, Endpoint: "endpoint"}}, nil).Times(1)
				e.baseApp.SetConsensus(m1)
				return e
			},
			wantedIsErr: false,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			result1, result2, err := tt.fn().getObjectSecondaryEndpoints(context.TODO(), &storagetypes.ObjectInfo{
				BucketName: "mockBucketName", LocalVirtualGroupId: 1,
			})
			if tt.wantedIsErr {
				assert.Equal(t, tt.wantedErr, err)
				assert.Nil(t, result1)
				assert.Equal(t, 0, result2)
			} else {
				assert.Nil(t, err)
				assert.Equal(t, []string{"endpoint"}, result1)
				assert.Equal(t, 1, result2)
			}
		})
	}
}

func TestExecuteModular_getBucketPrimarySPEndpoint(t *testing.T) {
	cases := []struct {
		name        string
		fn          func() *ExecuteModular
		wantedIsErr bool
		wantedErr   error
	}{
		{
			name: "failed to GetBucketMeta",
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetBucketMeta(gomock.Any(), gomock.Any(), true).Return(nil, nil, mockErr).Times(1)
				e.baseApp.SetGfSpClient(m)
				return e
			},
			wantedIsErr: true,
			wantedErr:   mockErr,
		},
		{
			name: "failed to GetBucketPrimarySPID",
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetBucketMeta(gomock.Any(), gomock.Any(), true).Return(&metadatatypes.VGFInfoBucket{
					BucketInfo: &storagetypes.BucketInfo{Id: sdkmath.NewUint(1)},
				}, nil, nil).Times(1)
				e.baseApp.SetGfSpClient(m)

				m1 := consensus.NewMockConsensus(ctrl)
				m1.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
				e.baseApp.SetConsensus(m1)
				return e
			},
			wantedIsErr: true,
			wantedErr:   mockErr,
		},
		{
			name: "failed to ListSPs",
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetBucketMeta(gomock.Any(), gomock.Any(), true).Return(&metadatatypes.VGFInfoBucket{
					BucketInfo: &storagetypes.BucketInfo{Id: sdkmath.NewUint(1)},
				}, nil, nil).Times(1)
				e.baseApp.SetGfSpClient(m)

				m1 := consensus.NewMockConsensus(ctrl)
				m1.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&virtual_types.GlobalVirtualGroupFamily{
					PrimarySpId: 1,
				}, nil).Times(1)
				m1.EXPECT().ListSPs(gomock.Any()).Return(nil, mockErr).Times(1)
				e.baseApp.SetConsensus(m1)
				return e
			},
			wantedIsErr: true,
			wantedErr:   mockErr,
		},
		{
			name: "ErrPrimaryNotFound",
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetBucketMeta(gomock.Any(), gomock.Any(), true).Return(&metadatatypes.VGFInfoBucket{
					BucketInfo: &storagetypes.BucketInfo{Id: sdkmath.NewUint(1)},
				}, nil, nil).Times(1)
				e.baseApp.SetGfSpClient(m)

				m1 := consensus.NewMockConsensus(ctrl)
				m1.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&virtual_types.GlobalVirtualGroupFamily{
					PrimarySpId: 1,
				}, nil).Times(1)
				m1.EXPECT().ListSPs(gomock.Any()).Return([]*sptypes.StorageProvider{{Id: 2}}, nil).Times(1)
				e.baseApp.SetConsensus(m1)
				return e
			},
			wantedIsErr: true,
			wantedErr:   ErrPrimaryNotFound,
		},
		{
			name: "success",
			fn: func() *ExecuteModular {
				e := setup(t)
				ctrl := gomock.NewController(t)
				m := gfspclient.NewMockGfSpClientAPI(ctrl)
				m.EXPECT().GetBucketMeta(gomock.Any(), gomock.Any(), true).Return(&metadatatypes.VGFInfoBucket{
					BucketInfo: &storagetypes.BucketInfo{Id: sdkmath.NewUint(1)},
				}, nil, nil).Times(1)
				e.baseApp.SetGfSpClient(m)

				m1 := consensus.NewMockConsensus(ctrl)
				m1.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&virtual_types.GlobalVirtualGroupFamily{
					PrimarySpId: 1,
				}, nil).Times(1)
				m1.EXPECT().ListSPs(gomock.Any()).Return([]*sptypes.StorageProvider{
					{Id: 1, Endpoint: "endpoint"},
				}, nil).Times(1)
				e.baseApp.SetConsensus(m1)
				return e
			},
			wantedIsErr: false,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.fn().getBucketPrimarySPEndpoint(context.TODO(), "bucketName")
			if tt.wantedIsErr {
				assert.Equal(t, tt.wantedErr, err)
				assert.Empty(t, result)
			} else {
				assert.Nil(t, err)
				assert.NotEmpty(t, result)
			}
		})
	}
}
