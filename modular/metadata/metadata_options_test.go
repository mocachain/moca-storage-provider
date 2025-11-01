package metadata

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/mocachain/moca-storage-provider/base/gfspapp"
	"github.com/mocachain/moca-storage-provider/base/gfspconfig"
)

func TestDefaultMetadataOptions(t *testing.T) {
	app := &gfspapp.GfSpBaseApp{}
	cfg := &gfspconfig.GfSpConfig{
		Parallel:    gfspconfig.ParallelConfig{},
		BlockSyncer: gfspconfig.BlockSyncerConfig{},
		Chain:       gfspconfig.ChainConfig{},
		SpAccount:   gfspconfig.SpAccountConfig{},
		Gateway:     gfspconfig.GatewayConfig{},
	}

	metadata := &MetadataModular{
		baseApp: app,
	}

	err := DefaultMetadataOptions(metadata, cfg)
	assert.Nil(t, err)

	assert.Equal(t, DefaultQuerySPParallelPerNode, metadata.maxMetadataRequest)
}
