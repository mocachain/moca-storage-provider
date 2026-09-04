package gater

import (
	"testing"

	virtualgrouptypes "github.com/evmos/evmos/v12/x/virtualgroup/types"
	"github.com/stretchr/testify/require"
)

func TestExpectedSecondarySPRejectsOutOfRangeRedundancyIndex(t *testing.T) {
	gvg := &virtualgrouptypes.GlobalVirtualGroup{SecondarySpIds: []uint32{7}}

	_, err := expectedSecondarySP(gvg, 1)

	require.Error(t, err)
}
