package authenticator

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mocachain/moca-storage-provider/base/gfspapp"
	"github.com/mocachain/moca-storage-provider/base/gfspconfig"
)

func TestNewAuthenticationModular(t *testing.T) {
	result, err := NewAuthenticationModular(&gfspapp.GfSpBaseApp{}, &gfspconfig.GfSpConfig{})
	assert.Nil(t, err)
	assert.NotNil(t, result)
}
