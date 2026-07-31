package p2pnode

import (
	"testing"

	sdkmath "cosmossdk.io/math"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mocachain/moca-storage-provider/base/gfspapp"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
)

const unknownSPAddress = "0x1000000000000000000000000000000000000001"

// newApprovalTask returns an approval task with the minimum fields GetSignBytes needs.
func newApprovalTask() *gfsptask.GfSpReplicatePieceApprovalTask {
	return &gfsptask.GfSpReplicatePieceApprovalTask{
		ObjectInfo:    &storagetypes.ObjectInfo{Id: sdkmath.NewUint(0)},
		StorageParams: &storagetypes.Params{VersionedParams: storagetypes.VersionedParams{MaxSegmentSize: 0}},
	}
}

// newApprovalProtocol returns an ApprovalProtocol whose peer whitelist holds knownSP.
func newApprovalProtocol(knownSP, operatorAddress string) *ApprovalProtocol {
	baseApp := &gfspapp.GfSpBaseApp{}
	baseApp.SetOperatorAddress(operatorAddress)
	peers := NewPeerProvider(nil)
	peers.UpdateSp([]string{knownSP})
	return &ApprovalProtocol{node: &Node{baseApp: baseApp, peers: peers}}
}

func TestCheckApprovalResponse_ChecksWhitelistBeforeSignature(t *testing.T) {
	km, err := setupKM()
	require.NoError(t, err)
	protocol := newApprovalProtocol(km.GetAddr().String(), "0x2000000000000000000000000000000000000002")

	resp := newApprovalTask()
	resp.SetApprovedSpOperatorAddress(unknownSPAddress)
	// a 65-byte signature reaches the ecrecover path in VerifySignature, so an
	// unknown sender must be rejected before the signature is looked at at all
	resp.SetApprovedSignature(make([]byte, 65))

	err = protocol.checkApprovalResponse(resp)
	assert.ErrorIs(t, err, ErrApprovalUnknownSP)
}

func TestCheckApprovalResponse_RejectsSelf(t *testing.T) {
	km, err := setupKM()
	require.NoError(t, err)
	self := km.GetAddr().String()
	protocol := newApprovalProtocol(self, self)

	resp := newApprovalTask()
	resp.SetApprovedSpOperatorAddress(self)
	resp.SetApprovedSignature(make([]byte, 65))

	err = protocol.checkApprovalResponse(resp)
	assert.ErrorIs(t, err, ErrApprovalSelfSP)
}

func TestCheckApprovalResponse_RejectsInvalidSignatureFromKnownSP(t *testing.T) {
	km, err := setupKM()
	require.NoError(t, err)
	protocol := newApprovalProtocol(km.GetAddr().String(), "0x2000000000000000000000000000000000000002")

	resp := newApprovalTask()
	resp.SetApprovedSpOperatorAddress(km.GetAddr().String())
	resp.SetApprovedSignature(make([]byte, 65))

	err = protocol.checkApprovalResponse(resp)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrApprovalUnknownSP)
	assert.NotErrorIs(t, err, ErrApprovalSelfSP)
}

func TestCheckApprovalResponse_AcceptsSignedResponseFromKnownSP(t *testing.T) {
	km, err := setupKM()
	require.NoError(t, err)
	protocol := newApprovalProtocol(km.GetAddr().String(), "0x2000000000000000000000000000000000000002")

	resp := newApprovalTask()
	signature, err := km.Sign(resp.GetSignBytes())
	require.NoError(t, err)
	resp.SetApprovedSpOperatorAddress(km.GetAddr().String())
	resp.SetApprovedSignature(signature)

	assert.NoError(t, protocol.checkApprovalResponse(resp))
}
