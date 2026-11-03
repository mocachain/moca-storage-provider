package manager

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	sptypes "github.com/mocachain/moca/v2/x/sp/types"
	virtualgrouptypes "github.com/mocachain/moca/v2/x/virtualgroup/types"
)

// A swap-out whose send keeps failing is reported, not treated as already on chain.
func TestGetSwapOutApprovalAndSendTxReturnsSendError(t *testing.T) {
	noSendAndConfirmBackoff(t)
	ctrl := gomock.NewController(t)
	manager := setup(t)
	manager.baseApp.SetConsensus(consensus.NewMockConsensus(ctrl))
	client := gfspclient.NewMockGfSpClientAPI(ctrl)
	manager.baseApp.SetGfSpClient(client)

	destSP := &sptypes.StorageProvider{Endpoint: "https://dest-sp.example"}
	msg := &virtualgrouptypes.MsgSwapOut{StorageProvider: "0xoperator", SuccessorSpId: 2}
	approval := &virtualgrouptypes.MsgSwapOut{StorageProvider: "0xoperator", SuccessorSpId: 2, GlobalVirtualGroupFamilyId: 1}
	client.EXPECT().GetSwapOutApproval(gomock.Any(), destSP.GetEndpoint(), msg).Return(approval, nil)
	sendErr := errors.New("failed to broadcast swap out tx")
	client.EXPECT().SwapOut(gomock.Any(), approval).Return("", sendErr).Times(confirmRetryNumber)

	got, err := GetSwapOutApprovalAndSendTx(manager.baseApp, destSP, msg)

	require.ErrorIs(t, err, sendErr)
	assert.Nil(t, got)
}
