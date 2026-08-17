package p2pnode

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	ggio "github.com/cosmos/gogoproto/io"
	"github.com/libp2p/go-libp2p/core/network"

	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	coretask "github.com/mocachain/moca-storage-provider/core/task"
	"github.com/mocachain/moca-storage-provider/pkg/log"
)

// MaxP2PMessageSize bounds how much a single p2p control message (ping/pong/
// approval request/response) can be. These are small metadata messages, not
// piece data, so this is generous headroom rather than a tight fit.
const MaxP2PMessageSize = 1 << 20 // 1MiB

// P2PStreamReadTimeout bounds how long a stream handler waits to receive a
// complete message before giving up, so a peer that opens a stream and then
// sends slowly (or not at all) can't hold the handler goroutine open forever.
const P2PStreamReadTimeout = 30 * time.Second

// pattern: /protocol-name/request-or-response-message/version
const (
	GetApprovalRequest  = "/approval/request/0.0.1"
	GetApprovalResponse = "/approval/response/0.0.1"
)

// ResponseChannelSize defines the approval response size
const ResponseChannelSize = 12

// ApprovalProtocol define the approval protocol and callback
// maintains requests for getting approvals in memory
type ApprovalProtocol struct {
	node     *Node
	response map[uint64]chan coretask.ApprovalReplicatePieceTask
	mux      sync.RWMutex
}

// NewApprovalProtocol return an instance of ApprovalProtocol
func NewApprovalProtocol(host *Node) *ApprovalProtocol {
	approval := &ApprovalProtocol{
		node:     host,
		response: make(map[uint64]chan coretask.ApprovalReplicatePieceTask),
	}
	host.node.SetStreamHandler(GetApprovalRequest, approval.onGetApprovalRequest)
	host.node.SetStreamHandler(GetApprovalResponse, approval.onGetApprovalResponse)
	return approval
}

// hangApprovalRequest records the approval request in memory for response to router
// notice: the caller need to call cancelApprovalRequest to delete the record
func (a *ApprovalProtocol) hangApprovalRequest(id uint64) (chan coretask.ApprovalReplicatePieceTask, error) {
	a.mux.Lock()
	defer a.mux.Unlock()
	if _, ok := a.response[id]; ok {
		return nil, errors.New("the get approval request is running")
	}
	a.response[id] = make(chan coretask.ApprovalReplicatePieceTask, ResponseChannelSize)
	return a.response[id], nil
}

func (a *ApprovalProtocol) cancelApprovalRequest(id uint64) {
	a.mux.Lock()
	defer a.mux.Unlock()
	if _, ok := a.response[id]; !ok {
		return
	}
	ch := a.response[id]
	delete(a.response, id)
	close(ch)
}

// ErrApprovalResponseChannelFull is returned when a response arrives for a
// request whose channel is already at capacity.
var ErrApprovalResponseChannelFull = errors.New("approval response channel is full")

// notifyApprovalResponse notifies the approval response by the approval related channel.
// The send is non-blocking: this is called while holding a.mux, which
// hangApprovalRequest and cancelApprovalRequest also need, so a blocking send
// here (e.g. because the consumer of this channel has already stopped reading,
// or moved on after ComputeApprovalExpiredHeight's deadline) would hold the
// lock forever and deadlock every other approval operation on this node.
func (a *ApprovalProtocol) notifyApprovalResponse(
	resp coretask.ApprovalReplicatePieceTask,
) error {
	a.mux.Lock()
	defer a.mux.Unlock()
	object := resp.GetObjectInfo()
	if object == nil {
		return errors.New("approval response missing object info")
	}
	id := object.Id.Uint64()
	ch, ok := a.response[id]
	if !ok {
		return errors.New("approval response has been canceled")
	}
	select {
	case ch <- resp:
		return nil
	default:
		return ErrApprovalResponseChannelFull
	}
}

func (a *ApprovalProtocol) ComputeApprovalExpiredHeight(task coretask.ApprovalReplicatePieceTask) (uint64, error) {
	if task == nil || task.GetObjectInfo() == nil || task.GetStorageParams() == nil {
		return 0, fmt.Errorf("ask replicate piece approval param invalied")
	}
	var (
		computeUnit      uint64 = 1024 * 1024
		speedUnit        uint64 = 8
		redundancyHeight uint64 = 100
	)
	totalUnit := task.GetObjectInfo().GetPayloadSize() /
		uint64(task.GetStorageParams().VersionedParams.GetRedundantDataChunkNum()+1) / computeUnit
	return totalUnit/speedUnit + redundancyHeight, nil
}

// ErrApprovalMissingObjectInfo is returned when an approval request or
// response has no object info attached.
var ErrApprovalMissingObjectInfo = errors.New("approval message missing object info")

// validateApprovalRequest checks the request's SP whitelist status before its
// object info is touched. The whitelist check needs only the claimed sender
// address, so it must run before anything that assumes ObjectInfo is set -
// otherwise a peer that omits object_info entirely can crash the handler
// (via a nil-pointer deref) before it's even been identified as unknown.
func (a *ApprovalProtocol) validateApprovalRequest(req *gfsptask.GfSpReplicatePieceApprovalTask) error {
	if !a.node.peers.checkSP(req.GetAskSpOperatorAddress()) {
		return ErrApprovalUnknownSP
	}
	if req.GetObjectInfo() == nil {
		return ErrApprovalMissingObjectInfo
	}
	return nil
}

// onGetApprovalRequest defines the get approval request protocol callback
func (a *ApprovalProtocol) onGetApprovalRequest(s network.Stream) {
	req := &gfsptask.GfSpReplicatePieceApprovalTask{}
	if err := s.SetReadDeadline(time.Now().Add(P2PStreamReadTimeout)); err != nil {
		log.Errorw("failed to set read deadline for replicate piece approval request stream", "error", err)
		s.Reset()
		return
	}
	if err := ggio.NewFullReader(s, MaxP2PMessageSize).ReadMsg(req); err != nil {
		log.Errorw("failed to read replicate piece approval request msg from stream", "error", err)
		s.Reset()
		return
	}
	s.Close()
	if err := a.validateApprovalRequest(req); err != nil {
		log.Warnw("ignore invalid replicate piece approval request", "sp",
			req.GetAskSpOperatorAddress(), "local", s.Conn().LocalPeer(), "remote", s.Conn().RemotePeer(), "error", err)
		return
	}
	ctx := log.WithValue(context.Background(), log.CtxKeyTask, req.Key().String())
	log.Debugf("%s received replicate piece approval request from %s, object_id: %d",
		s.Conn().LocalPeer(), s.Conn().RemotePeer(), req.GetObjectInfo().Id.Uint64())
	if strings.Compare(req.GetAskSpOperatorAddress(), a.node.baseApp.OperatorAddress()) == 0 {
		log.CtxWarnw(ctx, "ignore self replicate piece approval request", "sp",
			req.GetAskSpOperatorAddress(), "local", s.Conn().LocalPeer(), "remote", s.Conn().RemotePeer())
		return
	}
	err := VerifySignature(req.GetAskSpOperatorAddress(), req.GetSignBytes(), req.GetAskSignature())
	if err != nil {
		log.CtxErrorw(ctx, "failed to verify replicate piece approval request signature",
			"local", s.Conn().LocalPeer(), "remote", s.Conn().RemotePeer(), "error", err)
		return
	}
	current, err := a.node.baseApp.Consensus().CurrentHeight(ctx)
	if err != nil {
		log.CtxErrorw(ctx, "failed to consensus get current height", "local", s.Conn().LocalPeer(),
			"remote", s.Conn().RemotePeer(), "error", err)
		return
	}
	expiredHeight, _ := a.ComputeApprovalExpiredHeight(req)
	if expiredHeight < a.node.secondaryApprovalExpiredHeight {
		expiredHeight = a.node.secondaryApprovalExpiredHeight
	}
	log.CtxErrorw(ctx, "allow replicate piece approval", "expired_height", expiredHeight)
	req.SetExpiredHeight(current + expiredHeight)
	// TODO:: customized approval strategy, if refuse will fill back resp refuse field
	signature, err := a.node.baseApp.GfSpClient().SignReplicatePieceApproval(ctx, req)
	if err != nil {
		log.CtxErrorw(ctx, "failed to sign replicate piece approval", "local", s.Conn().LocalPeer(),
			"remote", s.Conn().RemotePeer(), "error", err)
		return
	}
	req.SetApprovedSignature(signature)
	req.SetApprovedSpOperatorAddress(a.node.baseApp.OperatorAddress())
	err = a.node.sendToPeer(ctx, s.Conn().RemotePeer(), GetApprovalResponse, req)
	log.Infof("%s response to %s approval request, task_key: %s, error: %v",
		s.Conn().LocalPeer(), s.Conn().RemotePeer(), req.Key().String(), err)
}

var (
	// ErrApprovalUnknownSP defines the approval sender is not a known storage provider
	ErrApprovalUnknownSP = errors.New("the approval sp is not in the sp whitelist")
	// ErrApprovalSelfSP defines the approval was sent by this storage provider itself
	ErrApprovalSelfSP = errors.New("the approval sp is the local sp")
)

// checkApprovalResponse checks the sender of an approval response. The sp whitelist
// is checked before the signature, so that a peer that is not a known storage
// provider cannot make this node run a secp256k1 ecrecover.
func (a *ApprovalProtocol) checkApprovalResponse(resp *gfsptask.GfSpReplicatePieceApprovalTask) error {
	sp := resp.GetApprovedSpOperatorAddress()
	if !a.node.peers.checkSP(sp) {
		return ErrApprovalUnknownSP
	}
	if strings.Compare(sp, a.node.baseApp.OperatorAddress()) == 0 {
		return ErrApprovalSelfSP
	}
	return VerifySignature(sp, resp.GetSignBytes(), resp.GetApprovedSignature())
}

// onGetApprovalRequest defines the get approval response protocol callback
func (a *ApprovalProtocol) onGetApprovalResponse(s network.Stream) {
	resp := &gfsptask.GfSpReplicatePieceApprovalTask{}
	if err := s.SetReadDeadline(time.Now().Add(P2PStreamReadTimeout)); err != nil {
		log.Errorw("failed to set read deadline for approval response stream", "error", err)
		s.Reset()
		return
	}
	if err := ggio.NewFullReader(s, MaxP2PMessageSize).ReadMsg(resp); err != nil {
		log.Errorw("failed to read replicate piece approval response msg from stream", "error", err)
		s.Reset()
		return
	}
	s.Close()
	// checkApprovalResponse only touches the SP address and signature fields,
	// not ObjectInfo, so it's safe to run before the object-info nil check
	// below - and it must run first, so an unrecognized/unsigned sender can't
	// reach the ObjectInfo dereferences in Key()/the debug log at all.
	if err := a.checkApprovalResponse(resp); err != nil {
		log.Warnw("ignore invalid approval response", "sp", resp.GetApprovedSpOperatorAddress(),
			"local", s.Conn().LocalPeer(), "remote", s.Conn().RemotePeer(), "error", err)
		return
	}
	if resp.GetObjectInfo() == nil {
		log.Warnw("ignore approval response missing object info", "sp", resp.GetApprovedSpOperatorAddress(),
			"local", s.Conn().LocalPeer(), "remote", s.Conn().RemotePeer())
		return
	}
	ctx := log.WithValue(context.Background(), log.CtxKeyTask, resp.Key().String())
	log.Debugf("%s received approval response from %s, object_id: %d",
		s.Conn().LocalPeer(), s.Conn().RemotePeer(), resp.GetObjectInfo().Id.Uint64())

	err := a.notifyApprovalResponse(resp)
	log.CtxInfow(ctx, "received approval response and notified hang request", "local", s.Conn().LocalPeer(),
		"remote", s.Conn().RemotePeer(), "task_key", resp.Key().String(), "error", err)
}
