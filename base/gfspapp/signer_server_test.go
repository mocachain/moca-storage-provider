package gfspapp

import (
	"context"
	"testing"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mocachain/moca-storage-provider/base/types/gfspp2p"
	"github.com/mocachain/moca-storage-provider/base/types/gfspserver"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	"github.com/mocachain/moca-storage-provider/core/module"
	sptypes "github.com/mocachain/moca/v2/x/sp/types"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
	virtual_types "github.com/mocachain/moca/v2/x/virtualgroup/types"
)

var (
	mockSig    = []byte("mockSig")
	mockTxHash = "mockTxHash"
)

func TestGfSpBaseApp_GfSpSignSuccess1(t *testing.T) {
	t.Log("Success case description: sign create bucket info")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignCreateBucketApproval(gomock.Any(), gomock.Any()).Return(mockSig, nil).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_CreateBucketInfo{CreateBucketInfo: mockCreateBucketInfo}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockSig, result.GetSignature())
}

func TestGfSpBaseApp_GfSpSignSuccess2(t *testing.T) {
	t.Log("Success case description: sign migrate bucket info")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignMigrateBucketApproval(gomock.Any(), gomock.Any()).Return(mockSig, nil).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_MigrateBucketInfo{
		MigrateBucketInfo: &storagetypes.MsgMigrateBucket{
			Operator:       "mockOperator",
			BucketName:     "mockBucketName",
			DstPrimarySpId: 1,
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockSig, result.GetSignature())
}

func TestGfSpBaseApp_GfSpSignSuccess3(t *testing.T) {
	t.Log("Success case description: sign create object info")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignCreateObjectApproval(gomock.Any(), gomock.Any()).Return(mockSig, nil).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_CreateObjectInfo{
		CreateObjectInfo: &storagetypes.MsgCreateObject{
			ObjectName: "mockObjectName",
			BucketName: "mockBucketName",
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockSig, result.GetSignature())
}

func TestGfSpBaseApp_GfSpSignSuccess4(t *testing.T) {
	t.Log("Success case description: sign seal object")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SealObjectEvm(gomock.Any(), gomock.Any()).Return(mockTxHash, nil).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_SealObjectInfo{
		SealObjectInfo: &storagetypes.MsgSealObject{
			ObjectName: "mockObjectName",
			BucketName: "mockBucketName",
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockTxHash, result.GetTxHash())
}

func TestGfSpBaseApp_GfSpSignSuccess5(t *testing.T) {
	t.Log("Success case description: sign seal object")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().RejectUnSealObjectEvm(gomock.Any(), gomock.Any()).Return(mockTxHash, nil).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_RejectObjectInfo{
		RejectObjectInfo: &storagetypes.MsgRejectSealObject{
			ObjectName: "mockObjectName",
			BucketName: "mockBucketName",
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockTxHash, result.GetTxHash())
}

func TestGfSpBaseApp_GfSpSignSuccess6(t *testing.T) {
	t.Log("Success case description: sign discontinue bucket")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().DiscontinueBucketEvm(gomock.Any(), gomock.Any()).Return(mockTxHash, nil).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_DiscontinueBucketInfo{
		DiscontinueBucketInfo: &storagetypes.MsgDiscontinueBucket{
			BucketName: "mockBucketName",
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockTxHash, result.GetTxHash())
}

func TestGfSpBaseApp_GfSpSignSuccess7(t *testing.T) {
	t.Log("Success case description: sign secondary bls signature")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignSecondarySealBls(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(mockSig, nil).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_SignSecondarySealBls{
		SignSecondarySealBls: &gfspserver.GfSpSignSecondarySealBls{
			ObjectId: 1,
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockSig, result.GetSignature())
}

func TestGfSpBaseApp_GfSpSignSuccess8(t *testing.T) {
	t.Log("Success case description: sign p2p ping msg")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignP2PPingMsg(gomock.Any(), gomock.Any()).Return(mockSig, nil).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_PingMsg{
		PingMsg: &gfspp2p.GfSpPing{
			SpOperatorAddress: "mockSpOperatorAddress",
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockSig, result.GetSignature())
}

func TestGfSpBaseApp_GfSpSignSuccess9(t *testing.T) {
	t.Log("Success case description: sign p2p pong msg")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignP2PPongMsg(gomock.Any(), gomock.Any()).Return(mockSig, nil).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_PongMsg{
		PongMsg: &gfspp2p.GfSpPong{
			SpOperatorAddress: "mockSpOperatorAddress",
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockSig, result.GetSignature())
}

func TestGfSpBaseApp_GfSpSignSuccess10(t *testing.T) {
	t.Log("Success case description: sign receive piece task")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignReceivePieceTask(gomock.Any(), gomock.Any()).Return(mockSig, nil).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_GfspReceivePieceTask{
		GfspReceivePieceTask: &gfsptask.GfSpReceivePieceTask{
			Task:          &gfsptask.GfSpTask{},
			ObjectInfo:    mockObjectInfo,
			StorageParams: mockStorageParams,
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockSig, result.GetSignature())
}

func TestGfSpBaseApp_GfSpSignSuccess11(t *testing.T) {
	t.Log("Success case description: sign replicate piece task")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignReplicatePieceApproval(gomock.Any(), gomock.Any()).Return(mockSig, nil).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_GfspReplicatePieceApprovalTask{
		GfspReplicatePieceApprovalTask: &gfsptask.GfSpReplicatePieceApprovalTask{
			Task:          &gfsptask.GfSpTask{},
			ObjectInfo:    mockObjectInfo,
			StorageParams: mockStorageParams,
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockSig, result.GetSignature())
}

func TestGfSpBaseApp_GfSpSignSuccess13(t *testing.T) {
	t.Log("Success case description: sign recover piece task")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignRecoveryPieceTask(gomock.Any(), gomock.Any()).Return(mockSig, nil).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_GfspRecoverPieceTask{
		GfspRecoverPieceTask: &gfsptask.GfSpRecoverPieceTask{
			Task:          &gfsptask.GfSpTask{},
			ObjectInfo:    mockObjectInfo,
			StorageParams: mockStorageParams,
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockSig, result.GetSignature())
}

func TestGfSpBaseApp_GfSpSignSuccess14(t *testing.T) {
	t.Log("Success case description: sign migrate gvg task")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignMigrateGVG(gomock.Any(), gomock.Any()).Return(mockSig, nil).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_GfspMigrateGvgTask{}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockSig, result.GetSignature())
}

func TestGfSpBaseApp_GfSpSignSuccess16(t *testing.T) {
	t.Log("Success case description: sign secondary sp bls migration bucket")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignSecondarySPMigrationBucket(gomock.Any(), gomock.Any()).Return(mockSig, nil).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_SignSecondarySpMigrationBucket{
		SignSecondarySpMigrationBucket: &storagetypes.SecondarySpMigrationBucketSignDoc{
			BucketId: sdkmath.NewUint(1),
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockSig, result.GetSignature())
}

func TestGfSpBaseApp_GfSpSignFailure1(t *testing.T) {
	t.Log("Failure case description: failed to sign seal object")
	g := setup(t)
	result, err := g.GfSpSign(context.TODO(), nil)
	assert.Nil(t, err)
	assert.Equal(t, ErrSingTaskDangling, result.GetErr())
}

func TestGfSpBaseApp_GfSpSignFailure2(t *testing.T) {
	t.Log("Failure case description: failed to sign create bucket approval")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignCreateBucketApproval(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_CreateBucketInfo{CreateBucketInfo: mockCreateBucketInfo}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockErr.Error(), result.GetErr().GetDescription())
}

func TestGfSpBaseApp_GfSpSignFailure3(t *testing.T) {
	t.Log("Failure case description: failed to sign migrate bucket info")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignMigrateBucketApproval(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_MigrateBucketInfo{
		MigrateBucketInfo: &storagetypes.MsgMigrateBucket{
			Operator:       "mockOperator",
			BucketName:     "mockBucketName",
			DstPrimarySpId: 1,
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockErr.Error(), result.GetErr().GetDescription())
}

func TestGfSpBaseApp_GfSpSignFailure4(t *testing.T) {
	t.Log("Failure case description: failed to sign create object info")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignCreateObjectApproval(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_CreateObjectInfo{
		CreateObjectInfo: &storagetypes.MsgCreateObject{
			ObjectName: "mockObjectName",
			BucketName: "mockBucketName",
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockErr.Error(), result.GetErr().GetDescription())
}

func TestGfSpBaseApp_GfSpSignFailure5(t *testing.T) {
	t.Log("Failure case description: failed to sign seal object")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SealObjectEvm(gomock.Any(), gomock.Any()).Return("", mockErr).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_SealObjectInfo{
		SealObjectInfo: &storagetypes.MsgSealObject{
			ObjectName: "mockObjectName",
			BucketName: "mockBucketName",
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockErr.Error(), result.GetErr().GetDescription())
}

func TestGfSpBaseApp_GfSpSignFailure6(t *testing.T) {
	t.Log("Failure case description: failed to sign seal object")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().RejectUnSealObjectEvm(gomock.Any(), gomock.Any()).Return("", mockErr).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_RejectObjectInfo{
		RejectObjectInfo: &storagetypes.MsgRejectSealObject{
			ObjectName: "mockObjectName",
			BucketName: "mockBucketName",
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockErr.Error(), result.GetErr().GetDescription())
}

func TestGfSpBaseApp_GfSpSignFailure7(t *testing.T) {
	t.Log("Failure case description: failed to sign discontinue bucket")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().DiscontinueBucketEvm(gomock.Any(), gomock.Any()).Return("", mockErr).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_DiscontinueBucketInfo{
		DiscontinueBucketInfo: &storagetypes.MsgDiscontinueBucket{
			BucketName: "mockBucketName",
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockErr.Error(), result.GetErr().GetDescription())
}

func TestGfSpBaseApp_GfSpSignFailure8(t *testing.T) {
	t.Log("Failure case description: failed to sign secondary bls signature")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignSecondarySealBls(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_SignSecondarySealBls{
		SignSecondarySealBls: &gfspserver.GfSpSignSecondarySealBls{
			ObjectId: 1,
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockErr.Error(), result.GetErr().GetDescription())
}

func TestGfSpBaseApp_GfSpSignFailure9(t *testing.T) {
	t.Log("Failure case description: failed to sign p2p ping msg")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignP2PPingMsg(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_PingMsg{
		PingMsg: &gfspp2p.GfSpPing{
			SpOperatorAddress: "mockSpOperatorAddress",
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockErr.Error(), result.GetErr().GetDescription())
}

func TestGfSpBaseApp_GfSpSignFailure10(t *testing.T) {
	t.Log("Failure case description: failed to sign p2p pong msg")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignP2PPongMsg(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_PongMsg{
		PongMsg: &gfspp2p.GfSpPong{
			SpOperatorAddress: "mockSpOperatorAddress",
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockErr.Error(), result.GetErr().GetDescription())
}

func TestGfSpBaseApp_GfSpSignFailure11(t *testing.T) {
	t.Log("Failure case description: failed to sign receive piece task")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignReceivePieceTask(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_GfspReceivePieceTask{
		GfspReceivePieceTask: &gfsptask.GfSpReceivePieceTask{
			Task:          &gfsptask.GfSpTask{},
			ObjectInfo:    mockObjectInfo,
			StorageParams: mockStorageParams,
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockErr.Error(), result.GetErr().GetDescription())
}

func TestGfSpBaseApp_GfSpSignFailure12(t *testing.T) {
	t.Log("Failure case description: failed to sign replicate piece task")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignReplicatePieceApproval(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_GfspReplicatePieceApprovalTask{
		GfspReplicatePieceApprovalTask: &gfsptask.GfSpReplicatePieceApprovalTask{
			Task:          &gfsptask.GfSpTask{},
			ObjectInfo:    mockObjectInfo,
			StorageParams: mockStorageParams,
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockErr.Error(), result.GetErr().GetDescription())
}

func TestGfSpBaseApp_GfSpSignFailure14(t *testing.T) {
	t.Log("Failure case description: failed to sign recover piece")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignRecoveryPieceTask(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_GfspRecoverPieceTask{
		GfspRecoverPieceTask: &gfsptask.GfSpRecoverPieceTask{
			Task:          &gfsptask.GfSpTask{},
			ObjectInfo:    mockObjectInfo,
			StorageParams: mockStorageParams,
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockErr.Error(), result.GetErr().GetDescription())
}

func TestGfSpBaseApp_GfSpSignFailure15(t *testing.T) {
	t.Log("Failure case description: failed to sign migrate gvg task")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignMigrateGVG(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_GfspMigrateGvgTask{}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockErr.Error(), result.GetErr().GetDescription())
}

func TestGfSpBaseApp_GfSpSignFailure17(t *testing.T) {
	t.Log("Failure case description: failed to sign secondary sp bls migration bucket")
	g := setup(t)
	ctrl := gomock.NewController(t)
	m := module.NewMockSigner(ctrl)
	g.signer = m
	m.EXPECT().SignSecondarySPMigrationBucket(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
	req := &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_SignSecondarySpMigrationBucket{
		SignSecondarySpMigrationBucket: &storagetypes.SecondarySpMigrationBucketSignDoc{
			BucketId: sdkmath.NewUint(1),
		},
	}}
	result, err := g.GfSpSign(context.TODO(), req)
	assert.Nil(t, err)
	assert.Equal(t, mockErr.Error(), result.GetErr().GetDescription())
}

func TestGfSpBaseApp_GfSpSignRejectsRequestsWithoutContentAuthorization(t *testing.T) {
	deposit := sdk.NewCoin("amoca", sdkmath.NewInt(1))
	tests := []struct {
		name    string
		request *gfspserver.GfSpSignRequest
	}{
		{
			name: "create global virtual group with deposit and members",
			request: &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_CreateGlobalVirtualGroup{
				CreateGlobalVirtualGroup: &gfspserver.GfSpCreateGlobalVirtualGroup{
					VirtualGroupFamilyId: 1,
					SecondarySpIds:       []uint32{2, 3},
					Deposit:              &deposit,
				},
			}},
		},
		{
			name: "complete bucket migration with target GVG membership",
			request: &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_CompleteMigrateBucket{
				CompleteMigrateBucket: &storagetypes.MsgCompleteMigrateBucket{
					GlobalVirtualGroupFamilyId: 1,
					GvgMappings: []*storagetypes.GVGMapping{{
						SrcGlobalVirtualGroupId: 2,
						DstGlobalVirtualGroupId: 3,
					}},
				},
			}},
		},
		{
			name: "swap out with successor target",
			request: &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_SwapOut{
				SwapOut: &virtual_types.MsgSwapOut{SuccessorSpId: 2, GlobalVirtualGroupIds: []uint32{3}},
			}},
		},
		{
			name: "sign swap out approval with successor target",
			request: &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_SignSwapOut{
				SignSwapOut: &virtual_types.MsgSwapOut{SuccessorSpId: 2, GlobalVirtualGroupIds: []uint32{3}},
			}},
		},
		{
			name: "complete swap out state",
			request: &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_CompleteSwapOut{
				CompleteSwapOut: &virtual_types.MsgCompleteSwapOut{GlobalVirtualGroupIds: []uint32{1}},
			}},
		},
		{
			name: "start storage provider exit",
			request: &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_SpExit{
				SpExit: &virtual_types.MsgStorageProviderExit{StorageProvider: "operator"},
			}},
		},
		{
			name: "complete storage provider exit for target",
			request: &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_CompleteSpExit{
				CompleteSpExit: &virtual_types.MsgCompleteStorageProviderExit{StorageProvider: "target"},
			}},
		},
		{
			name: "update storage prices",
			request: &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_SpStoragePrice{
				SpStoragePrice: &sptypes.MsgUpdateSpStoragePrice{
					ReadPrice:  sdkmath.LegacyNewDec(1),
					StorePrice: sdkmath.LegacyNewDec(2),
				},
			}},
		},
		{
			name: "reject target bucket migration",
			request: &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_RejectMigrateBucket{
				RejectMigrateBucket: &storagetypes.MsgRejectMigrateBucket{BucketName: "target-bucket"},
			}},
		},
		{
			name: "reserve swap in for target SP",
			request: &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_ReserveSwapIn{
				ReserveSwapIn: &virtual_types.MsgReserveSwapIn{TargetSpId: 2, GlobalVirtualGroupId: 3},
			}},
		},
		{
			name: "complete swap in state",
			request: &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_CompleteSwapIn{
				CompleteSwapIn: &virtual_types.MsgCompleteSwapIn{GlobalVirtualGroupId: 3},
			}},
		},
		{
			name: "cancel swap in state",
			request: &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_CancelSwapIn{
				CancelSwapIn: &virtual_types.MsgCancelSwapIn{GlobalVirtualGroupId: 3},
			}},
		},
		{
			name: "deposit caller controlled amount",
			request: &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_Deposit{
				Deposit: &virtual_types.MsgDeposit{GlobalVirtualGroupId: 1, Deposit: deposit},
			}},
		},
		{
			name: "delete target global virtual group",
			request: &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_DeleteGlobalVirtualGroup{
				DeleteGlobalVirtualGroup: &virtual_types.MsgDeleteGlobalVirtualGroup{GlobalVirtualGroupId: 1},
			}},
		},
		{
			name: "delegate object creation to recipient",
			request: &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_DelegateCreateObject{
				DelegateCreateObject: &storagetypes.MsgDelegateCreateObject{Creator: "recipient"},
			}},
		},
		{
			name: "delegate object update to recipient",
			request: &gfspserver.GfSpSignRequest{Request: &gfspserver.GfSpSignRequest_DelegateUpdateObjectContent{
				DelegateUpdateObjectContent: &storagetypes.MsgDelegateUpdateObjectContent{Updater: "recipient"},
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := setup(t)
			g.SetOperatorAddress(sdk.AccAddress(make([]byte, 20)).String())
			g.signer = module.NewMockSigner(gomock.NewController(t))

			resp, err := g.GfSpSign(context.Background(), test.request)

			assert.Nil(t, resp)
			assert.Equal(t, codes.PermissionDenied, status.Code(err))
		})
	}
}
