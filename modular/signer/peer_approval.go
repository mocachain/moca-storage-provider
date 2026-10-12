package signer

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"

	commonhttp "github.com/mocachain/moca-common/go/http"
	"github.com/mocachain/moca-storage-provider/base/types/gfspserver"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
	virtualgrouptypes "github.com/mocachain/moca/v2/x/virtualgroup/types"
)

const (
	peerApprovalMigrationPath = "/moca/migrate/v1/migration-bucket-approval"
	peerApprovalSwapOutPath   = "/moca/migrate/v1/get-swap-out-approval"
	peerApprovalBlsHeader     = "X-Gnfd-Secondary-Migration-Bucket-Msg"
)

// SignPeerApprovalRequest signs only the two fixed peer approval request shapes.
func (s *SignModular) SignPeerApprovalRequest(_ context.Context, approval *gfspserver.GfSpSignPeerApprovalRequest) ([]byte, error) {
	req, err := peerApprovalHTTPRequest(approval)
	if err != nil {
		return nil, err
	}
	return s.client.Sign(SignOperator, commonhttp.GetMsgToSignInGNFD1Auth(req))
}

func peerApprovalHTTPRequest(approval *gfspserver.GfSpSignPeerApprovalRequest) (*http.Request, error) {
	if approval == nil || approval.GetHost() == "" || approval.GetExpiryTimestamp() == "" {
		return nil, fmt.Errorf("invalid peer approval request")
	}
	var (
		path       string
		headerName string
		msg        []byte
		err        error
	)
	switch request := approval.GetRequest().(type) {
	case *gfspserver.GfSpSignPeerApprovalRequest_SecondaryMigrationBucket:
		if request.SecondaryMigrationBucket == nil {
			return nil, fmt.Errorf("missing secondary migration bucket request")
		}
		path = peerApprovalMigrationPath
		headerName = peerApprovalBlsHeader
		msg, err = storagetypes.ModuleCdc.MarshalJSON(request.SecondaryMigrationBucket)
	case *gfspserver.GfSpSignPeerApprovalRequest_SwapOutApproval:
		if request.SwapOutApproval == nil {
			return nil, fmt.Errorf("missing swap out approval request")
		}
		path = peerApprovalSwapOutPath
		headerName = commonhttp.HTTPHeaderUnsignedMsg
		msg, err = virtualgrouptypes.ModuleCdc.MarshalJSON(request.SwapOutApproval)
	default:
		return nil, fmt.Errorf("unsupported peer approval request")
	}
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, "https://"+approval.GetHost()+path, nil)
	if err != nil {
		return nil, err
	}
	encodedMsg := hex.EncodeToString(msg)
	req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, approval.GetExpiryTimestamp())
	req.Header.Set(commonhttp.HTTPHeaderUnsignedMsg, encodedMsg)
	req.Header.Set(headerName, encodedMsg)
	return req, nil
}
