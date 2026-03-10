package common

import storagetypes "github.com/evmos/evmos/v12/x/storage/types"

var destChainIDToSourceType = map[uint32]storagetypes.SourceType{
	2:  storagetypes.SOURCE_TYPE_BSC_CROSS_CHAIN,
	3:  storagetypes.SOURCE_TYPE_OP_CROSS_CHAIN,
	4:  storagetypes.SOURCE_TYPE_POLYGON_CROSS_CHAIN,
	5:  storagetypes.SOURCE_TYPE_SCROLL_CROSS_CHAIN,
	6:  storagetypes.SOURCE_TYPE_LINEA_CROSS_CHAIN,
	7:  storagetypes.SOURCE_TYPE_MANTLE_CROSS_CHAIN,
	8:  storagetypes.SOURCE_TYPE_ARBITRUM_CROSS_CHAIN,
	9:  storagetypes.SOURCE_TYPE_OPTIMISM_CROSS_CHAIN,
	10: storagetypes.SOURCE_TYPE_BASE_CROSS_CHAIN,
}

func MapDestChainIDToSourceType(destChainID uint32) (storagetypes.SourceType, bool) {
	v, ok := destChainIDToSourceType[destChainID]
	return v, ok
}
