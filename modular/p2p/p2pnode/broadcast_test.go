package p2pnode

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stretchr/testify/require"
)

func TestBroadcastToPeersDoesNotWaitForSlowPeer(t *testing.T) {
	slowPeer := peer.ID("slow")
	fastPeer := peer.ID("fast")
	fastSent := make(chan struct{})
	releaseSlowPeer := make(chan struct{})

	broadcastToPeers([]peer.ID{slowPeer, fastPeer}, peer.ID("self"), func(id peer.ID) {
		if id == slowPeer {
			<-releaseSlowPeer
			return
		}
		close(fastSent)
	})

	require.Eventually(t, func() bool {
		select {
		case <-fastSent:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
	close(releaseSlowPeer)
}
