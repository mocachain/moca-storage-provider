package p2p

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mocachain/moca-storage-provider/base/gfspconfig"
	coretaskqueue "github.com/mocachain/moca-storage-provider/core/taskqueue"
)

func TestDefaultP2POptions_RejectsEmptyPrivateKeyEnv(t *testing.T) {
	t.Setenv(P2PPrivateKey, "")

	cfg := &gfspconfig.GfSpConfig{Customize: &gfspconfig.Customize{
		NewStrategyTQueueFunc: func(string, int) coretaskqueue.TQueueOnStrategy { return nil },
	}}
	cfg.P2P.P2PPrivateKey = "0f6e0d9c8b7a695847362514f3e2d1c0b9a8978685746352413f2e1d0c9b8a79"

	err := DefaultP2POptions(&P2PModular{}, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), P2PPrivateKey,
		"an empty key env var must fail startup instead of silently generating a throwaway node key")
}
