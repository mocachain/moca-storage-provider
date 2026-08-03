package p2pnode

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPeerProvider_CheckSPRejectsUnspecifiedSentinel(t *testing.T) {
	provider := NewPeerProvider(nil)
	provider.UpdateSp([]string{"0x1000000000000000000000000000000000000001"})

	assert.False(t, provider.checkSP(PeerSpUnspecified),
		"the unspecified sentinel is a placeholder bucket, not a whitelisted storage provider")
}

func TestPeerProvider_CheckSPAcceptsKnownSPOnly(t *testing.T) {
	known := "0x1000000000000000000000000000000000000001"
	provider := NewPeerProvider(nil)
	provider.UpdateSp([]string{known})

	assert.True(t, provider.checkSP(known))
	assert.False(t, provider.checkSP("0x2000000000000000000000000000000000000002"))
	assert.False(t, provider.checkSP(""))
}
