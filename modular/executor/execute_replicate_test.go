package executor

import (
	"context"
	"testing"

	sdkmath "cosmossdk.io/math"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	"github.com/mocachain/moca-storage-provider/core/piecestore"
	storagetypes "github.com/evmos/evmos/v12/x/storage/types"
	virtual_types "github.com/evmos/evmos/v12/x/virtualgroup/types"
)

// TestExecuteModular_handleReplicatePiece_RejectsCardinalityMismatchBeforeReplicating
// covers a gvg whose secondary SP count no longer matches the task's secondary
// endpoints. The mismatch must be rejected before any piece is touched: no
// PieceStore expectation is registered, so gomock fails loudly if the handler
// still reaches the replication loop.
func TestExecuteModular_handleReplicatePiece_RejectsCardinalityMismatchBeforeReplicating(t *testing.T) {
	e := setup(t)
	ctrl := gomock.NewController(t)

	pieceOp := piecestore.NewMockPieceOp(ctrl)
	pieceOp.EXPECT().SegmentPieceCount(gomock.Any(), gomock.Any()).Return(uint32(1)).Times(1)
	e.baseApp.SetPieceOp(pieceOp)
	e.baseApp.SetPieceStore(piecestore.NewMockPieceStore(ctrl))

	client := gfspclient.NewMockGfSpClientAPI(ctrl)
	// the gvg has only one secondary SP, but the task names two secondary
	// endpoints - a cardinality mismatch that must be caught up front.
	client.EXPECT().GetGlobalVirtualGroupByGvgID(gomock.Any(), gomock.Any()).
		Return(&virtual_types.GlobalVirtualGroup{SecondarySpIds: []uint32{1}}, nil).Times(1)
	e.baseApp.SetGfSpClient(client)

	rTask := &gfsptask.GfSpReplicatePieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:          sdkmath.NewUint(1),
			PayloadSize: 100,
		},
		StorageParams: &storagetypes.Params{
			VersionedParams: storagetypes.VersionedParams{
				MaxSegmentSize:          16 * 1024 * 1024,
				RedundantDataChunkNum:   1,
				RedundantParityChunkNum: 1,
			},
		},
		SecondaryEndpoints: []string{"secondary-1", "secondary-2"},
	}

	err := e.handleReplicatePiece(context.Background(), rTask)
	assert.ErrorIs(t, err, ErrReplicateIdsOutOfBounds)
}

// TestExecuteModular_handleReplicatePiece_PropagatesGvgQueryErrorBeforeReplicating
// covers the sibling failure mode: if the gvg lookup itself fails, that must
// also be caught before any piece is touched.
func TestExecuteModular_handleReplicatePiece_PropagatesGvgQueryErrorBeforeReplicating(t *testing.T) {
	e := setup(t)
	ctrl := gomock.NewController(t)

	pieceOp := piecestore.NewMockPieceOp(ctrl)
	pieceOp.EXPECT().SegmentPieceCount(gomock.Any(), gomock.Any()).Return(uint32(1)).Times(1)
	e.baseApp.SetPieceOp(pieceOp)
	e.baseApp.SetPieceStore(piecestore.NewMockPieceStore(ctrl))

	client := gfspclient.NewMockGfSpClientAPI(ctrl)
	client.EXPECT().GetGlobalVirtualGroupByGvgID(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
	e.baseApp.SetGfSpClient(client)

	rTask := &gfsptask.GfSpReplicatePieceTask{
		Task: &gfsptask.GfSpTask{},
		ObjectInfo: &storagetypes.ObjectInfo{
			Id:          sdkmath.NewUint(1),
			PayloadSize: 100,
		},
		StorageParams: &storagetypes.Params{
			VersionedParams: storagetypes.VersionedParams{
				MaxSegmentSize:          16 * 1024 * 1024,
				RedundantDataChunkNum:   1,
				RedundantParityChunkNum: 1,
			},
		},
		SecondaryEndpoints: []string{"secondary-1", "secondary-2"},
	}

	err := e.handleReplicatePiece(context.Background(), rTask)
	assert.Error(t, err)
}
