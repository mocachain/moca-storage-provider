package utils

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mocachain/moca-storage-provider/base/gfspconfig"
)

func TestMakeGfSpClientRejectsMissingGRPCTLS(t *testing.T) {
	client, err := MakeGfSpClient(&gfspconfig.GfSpConfig{})
	require.Nil(t, client)
	require.ErrorContains(t, err, "GRPCTLS.CACertFile is required")
}
