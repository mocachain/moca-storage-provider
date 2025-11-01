package downloader

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/mocachain/moca-storage-provider/base/gfspapp"
	"github.com/mocachain/moca-storage-provider/base/gfspconfig"
)

func TestNewDownloadModular(t *testing.T) {
	app := &gfspapp.GfSpBaseApp{}
	cfg := &gfspconfig.GfSpConfig{}
	result, err := NewDownloadModular(app, cfg)
	assert.Nil(t, err)
	assert.NotNil(t, result)
}
