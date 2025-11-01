package approver

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mocachain/moca-storage-provider/base/gfspapp"
	"github.com/mocachain/moca-storage-provider/base/gfspconfig"
	"github.com/mocachain/moca-storage-provider/base/gfsptqueue"
	"github.com/mocachain/moca-storage-provider/core/taskqueue"
)

func TestNewApprovalModular(t *testing.T) {
	app := &gfspapp.GfSpBaseApp{}
	cfg := &gfspconfig.GfSpConfig{
		Customize: &gfspconfig.Customize{
			NewStrategyTQueueFunc: mockQueueOnStrategy,
		},
	}
	result, err := NewApprovalModular(app, cfg)
	assert.Nil(t, err)
	assert.NotNil(t, result)
}

func mockQueueOnStrategy(name string, cap int) taskqueue.TQueueOnStrategy {
	return gfsptqueue.NewGfSpTQueue(name, cap)
}
