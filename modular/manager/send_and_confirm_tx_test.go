package manager

import (
	"errors"
	"fmt"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	"github.com/mocachain/moca-storage-provider/core/spdb"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
)

const testSendAndConfirmTxHash = "0xafd500ddbb76aedc5044c17f02820bf776657a9d82af201c8eb2ae37d0657b06"

func noSendAndConfirmBackoff(t *testing.T) {
	t.Helper()
	orig := sendAndConfirmBackoff
	sendAndConfirmBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { sendAndConfirmBackoff = orig })
}

// scriptedSend returns the results in order and counts the calls.
type scriptedSend struct {
	results []sendResult
	calls   int
}

type sendResult struct {
	hash string
	err  error
}

func (s *scriptedSend) send() (string, error) {
	r := s.results[min(s.calls, len(s.results)-1)]
	s.calls++
	return r.hash, r.err
}

func TestSendAndConfirmTxDoesNotResendAfterConfirmError(t *testing.T) {
	noSendAndConfirmBackoff(t)
	ctrl := gomock.NewController(t)
	chain := consensus.NewMockConsensus(ctrl)
	confirmErr := fmt.Errorf("failed to confirm transaction, tx_hash=%s", testSendAndConfirmTxHash)
	chain.EXPECT().ConfirmTransaction(gomock.Any(), testSendAndConfirmTxHash).
		Return(nil, confirmErr).Times(confirmRetryNumber)
	send := &scriptedSend{results: []sendResult{{hash: testSendAndConfirmTxHash}}}

	err := SendAndConfirmTx(chain, send.send)

	require.ErrorIs(t, err, confirmErr)
	assert.Equal(t, 1, send.calls, "a tx whose hash is known must not be sent again")
}

func TestSendAndConfirmTxRetriesConfirmOnSameHash(t *testing.T) {
	noSendAndConfirmBackoff(t)
	ctrl := gomock.NewController(t)
	chain := consensus.NewMockConsensus(ctrl)
	gomock.InOrder(
		chain.EXPECT().ConfirmTransaction(gomock.Any(), testSendAndConfirmTxHash).
			Return(nil, errors.New("rpc error: code = Unavailable")),
		chain.EXPECT().ConfirmTransaction(gomock.Any(), testSendAndConfirmTxHash).
			Return(&sdk.TxResponse{}, nil),
	)
	send := &scriptedSend{results: []sendResult{{hash: testSendAndConfirmTxHash}}}

	err := SendAndConfirmTx(chain, send.send)

	require.NoError(t, err)
	assert.Equal(t, 1, send.calls)
}

func TestSendAndConfirmTxStopsOnFailedExecution(t *testing.T) {
	noSendAndConfirmBackoff(t)
	ctrl := gomock.NewController(t)
	chain := consensus.NewMockConsensus(ctrl)
	execErr := fmt.Errorf("%w: tx_hash=%s, reason=Object already exists", consensus.ErrTxExecutionFailed, testSendAndConfirmTxHash)
	chain.EXPECT().ConfirmTransaction(gomock.Any(), testSendAndConfirmTxHash).
		Return(&sdk.TxResponse{}, execErr).Times(1)
	send := &scriptedSend{results: []sendResult{{hash: testSendAndConfirmTxHash}}}

	err := SendAndConfirmTx(chain, send.send)

	require.ErrorIs(t, err, consensus.ErrTxExecutionFailed)
	assert.Equal(t, 1, send.calls)
}

func TestSendAndConfirmTxRetriesSendErrors(t *testing.T) {
	noSendAndConfirmBackoff(t)
	ctrl := gomock.NewController(t)
	chain := consensus.NewMockConsensus(ctrl)
	chain.EXPECT().ConfirmTransaction(gomock.Any(), testSendAndConfirmTxHash).
		Return(&sdk.TxResponse{}, nil).Times(1)
	sendErr := errors.New("failed to broadcast tx")
	send := &scriptedSend{results: []sendResult{{err: sendErr}, {err: sendErr}, {hash: testSendAndConfirmTxHash}}}

	err := SendAndConfirmTx(chain, send.send)

	require.NoError(t, err)
	assert.Equal(t, 3, send.calls)
}

func TestSendAndConfirmTxReturnsLastSendError(t *testing.T) {
	noSendAndConfirmBackoff(t)
	ctrl := gomock.NewController(t)
	chain := consensus.NewMockConsensus(ctrl)
	sendErr := errors.New("failed to broadcast tx")
	send := &scriptedSend{results: []sendResult{{err: sendErr}}}

	err := SendAndConfirmTx(chain, send.send)

	require.ErrorIs(t, err, sendErr)
	assert.Equal(t, confirmRetryNumber, send.calls)
}

func TestSendAndConfirmTxSkipsConfirmWithoutHash(t *testing.T) {
	noSendAndConfirmBackoff(t)
	ctrl := gomock.NewController(t)
	chain := consensus.NewMockConsensus(ctrl)
	send := &scriptedSend{results: []sendResult{{}}}

	err := SendAndConfirmTx(chain, send.send)

	require.NoError(t, err)
	assert.Equal(t, 1, send.calls)
}

func TestRetrySealTaskDeletesProgressAfterLateConfirm(t *testing.T) {
	noSendAndConfirmBackoff(t)
	ctrl := gomock.NewController(t)
	manager := setup(t)
	manager.baseApp.SetOperatorAddress("operator-1")

	chain := consensus.NewMockConsensus(ctrl)
	manager.baseApp.SetConsensus(chain)
	client := gfspclient.NewMockGfSpClientAPI(ctrl)
	manager.baseApp.SetGfSpClient(client)
	db := spdb.NewMockSPDB(ctrl)
	manager.baseApp.SetGfSpDB(db)

	chain.EXPECT().QueryObjectInfoByID(gomock.Any(), "7").Return(&storagetypes.ObjectInfo{
		Id:           sdkmath.NewUint(7),
		BucketName:   "bucket",
		ObjectName:   "object",
		ObjectStatus: storagetypes.OBJECT_STATUS_CREATED,
	}, nil)
	client.EXPECT().SealObject(gomock.Any(), gomock.Any()).Return(testSendAndConfirmTxHash, nil).Times(1)
	gomock.InOrder(
		chain.EXPECT().ConfirmTransaction(gomock.Any(), testSendAndConfirmTxHash).
			Return(nil, errors.New("rpc error: code = Unavailable")),
		chain.EXPECT().ConfirmTransaction(gomock.Any(), testSendAndConfirmTxHash).
			Return(&sdk.TxResponse{}, nil),
	)
	db.EXPECT().DeleteUploadProgress(uint64(7)).Return(nil).Times(1)

	scheduler := &TaskRetryScheduler{manager: manager}
	err := scheduler.retrySealTask(&spdb.UploadObjectMeta{ObjectID: 7, GlobalVirtualGroupID: 1})

	require.NoError(t, err)
}
