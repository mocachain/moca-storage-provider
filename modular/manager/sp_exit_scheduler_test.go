package manager

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	"github.com/mocachain/moca-storage-provider/core/spdb"
	"github.com/mocachain/moca-storage-provider/core/vgmgr"

	sptypes "github.com/evmos/evmos/v12/x/sp/types"
	virtualgrouptypes "github.com/evmos/evmos/v12/x/virtualgroup/types"
)

func TestSPExitSchedulerProduceSwapOutPlanLoadsSecondaryGVGsFromChain(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	manager := setup(t)
	manager.baseApp.SetOperatorAddress("operator-1")

	chain := consensus.NewMockConsensus(ctrl)
	manager.baseApp.SetConsensus(chain)

	metadataClient := gfspclient.NewMockGfSpClientAPI(ctrl)
	manager.baseApp.SetGfSpClient(metadataClient)

	db := spdb.NewMockSPDB(ctrl)
	manager.baseApp.SetGfSpDB(db)

	virtualGroupManager := vgmgr.NewMockVirtualGroupManager(ctrl)
	manager.virtualGroupManager = virtualGroupManager

	selfSP := &sptypes.StorageProvider{
		Id:              1,
		OperatorAddress: "operator-1",
	}
	primarySP := &sptypes.StorageProvider{Id: 2}
	destSP := &sptypes.StorageProvider{Id: 9}
	secondaryGVG := &virtualgrouptypes.GlobalVirtualGroup{
		Id:             18,
		FamilyId:       7,
		PrimarySpId:    2,
		SecondarySpIds: []uint32{1, 3},
	}

	chain.EXPECT().ListVirtualGroupFamilies(gomock.Any(), uint32(1)).Return(nil, nil)
	chain.EXPECT().ListSPs(gomock.Any()).Return([]*sptypes.StorageProvider{selfSP, primarySP}, nil)
	chain.EXPECT().ListVirtualGroupFamilies(gomock.Any(), uint32(2)).Return([]*virtualgrouptypes.GlobalVirtualGroupFamily{
		{Id: 7},
	}, nil)
	chain.EXPECT().ListGlobalVirtualGroupsByFamilyID(gomock.Any(), uint32(7)).Return([]*virtualgrouptypes.GlobalVirtualGroup{
		secondaryGVG,
	}, nil)

	virtualGroupManager.EXPECT().PickSPByFilter(gomock.Any()).Return(destSP, nil)

	expectedSwapOut := &virtualgrouptypes.MsgSwapOut{
		StorageProvider:            selfSP.GetOperatorAddress(),
		GlobalVirtualGroupFamilyId: 0,
		GlobalVirtualGroupIds:      []uint32{secondaryGVG.GetId()},
		SuccessorSpId:              destSP.GetId(),
	}
	db.EXPECT().QuerySwapOutUnitInSrcSP(makeSwapOutKey(expectedSwapOut)).Return(&spdb.SwapOutMeta{
		SwapOutMsg: expectedSwapOut,
	}, nil)
	db.EXPECT().InsertSwapOutUnit(gomock.Any()).Return(nil)

	scheduler := &SPExitScheduler{
		manager: manager,
		selfSP:  selfSP,
	}

	plan, err := scheduler.produceSwapOutPlan(true)
	require.NoError(t, err)
	require.Len(t, plan.swapOutUnitMap, 1)

	unit, ok := plan.swapOutUnitMap[makeSwapOutKey(expectedSwapOut)]
	require.True(t, ok)
	require.True(t, unit.isSecondary)
	require.False(t, unit.isFamily)
	require.False(t, unit.isConflicted)
	require.Equal(t, []uint32{uint32(18)}, unit.swapOut.GetGlobalVirtualGroupIds())
	require.Equal(t, uint32(9), unit.swapOut.GetSuccessorSpId())
}
