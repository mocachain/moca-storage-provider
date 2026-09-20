package gater

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	commonhttp "github.com/mocachain/moca-common/go/http"
	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	sptypes "github.com/mocachain/moca/v2/x/sp/types"
)

func TestAuthenticatePeerApprovalRequestRejectsEddsaInAuthenticatedModes(t *testing.T) {
	for _, mode := range []string{"permissive", "required"} {
		for _, signType := range []string{commonhttp.Gnfd1Eddsa, commonhttp.Gnfd2Eddsa} {
			t.Run(mode+"/"+signType, func(t *testing.T) {
				g := setup(t)
				g.peerApprovalAuthMode = mode
				req := httptest.NewRequest(http.MethodGet, SwapOutApprovalPath, nil)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, time.Now().Add(time.Minute).UTC().Format(time.RFC3339))
				req.Header.Set(GnfdAuthorizationHeader, signType+",Signature=deadbeef")

				err := g.authenticatePeerApprovalRequest(req, &RequestContext{account: "operator"}, nil, "operator")

				assert.Equal(t, ErrUnsupportedSignType, err)
			})
		}
	}
}

func TestGateModular_getSecondaryBlsMigrationBucketApprovalHandlerAcceptsPermissiveLegacyRequest(t *testing.T) {
	g := setup(t)
	g.peerApprovalAuthMode = "permissive"
	ctrl := gomock.NewController(t)
	clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
	clientMock.EXPECT().SignSecondarySPMigrationBucket(gomock.Any(), gomock.Any()).Return([]byte("signature"), nil)
	g.baseApp.SetGfSpClient(clientMock)
	setupRequiredSecondaryBlsMigrationChain(t, g, ctrl, clientMock, mockSwapOutSPAddress)

	req := httptest.NewRequest(http.MethodGet, SecondarySPMigrationBucketApprovalPath, nil)
	req.Header.Set(GnfdSecondarySPMigrationBucketMsgHeader, mockSecondaryBlsSignDocHeader)
	w := httptest.NewRecorder()

	mockGetSecondaryBlsMigrationBucketApprovalHandlerRoute(t, g).ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "7369676e6174757265", w.Header().Get(GnfdSecondarySPMigrationBucketApprovalHeader))
}

func TestGateModular_getSecondaryBlsMigrationBucketApprovalHandlerRejectsEddsa(t *testing.T) {
	g := setup(t)
	g.peerApprovalAuthMode = "required"
	ctrl := gomock.NewController(t)
	clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
	clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(true, nil)
	g.baseApp.SetGfSpClient(clientMock)
	consensusMock := consensus.NewMockConsensus(ctrl)
	consensusMock.EXPECT().QuerySPByID(gomock.Any(), mockSelfSPID).
		Return(&sptypes.StorageProvider{Id: mockSelfSPID, OperatorAddress: mockSwapOutSPAddress}, nil).Times(1)
	g.baseApp.SetConsensus(consensusMock)

	req := httptest.NewRequest(http.MethodGet, SecondarySPMigrationBucketApprovalPath, nil)
	req.Header.Set(GnfdSecondarySPMigrationBucketMsgHeader, mockSecondaryBlsSignDocHeader)
	req.Header.Set(GnfdUnsignedApprovalMsgHeader, mockSecondaryBlsSignDocHeader)
	req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, time.Now().Add(time.Minute).UTC().Format(time.RFC3339))
	req.Header.Set(GnfdUserAddressHeader, mockSwapOutSPAddress)
	req.Header.Set(GnfdOffChainAuthAppDomainHeader, testDomain)
	req.Header.Set(GnfdAuthorizationHeader, commonhttp.Gnfd1Eddsa+",Signature=deadbeef")
	w := httptest.NewRecorder()

	mockGetSecondaryBlsMigrationBucketApprovalHandlerRoute(t, g).ServeHTTP(w, req)

	assert.Contains(t, w.Body.String(), "unsupported sign type")
}

func TestGateModular_getSwapOutApprovalRejectsEddsa(t *testing.T) {
	g := setup(t)
	g.peerApprovalAuthMode = "required"
	ctrl := gomock.NewController(t)
	clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
	clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(true, nil)
	g.baseApp.SetGfSpClient(clientMock)
	setupSwapOutChain(t, g, ctrl, sptypes.STATUS_GRACEFUL_EXITING, mockSwapOutSPID)

	req := httptest.NewRequest(http.MethodGet, SwapOutApprovalPath, nil)
	req.Header.Set(GnfdUnsignedApprovalMsgHeader, mockSwapOutMsgHeader)
	req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, time.Now().Add(time.Minute).UTC().Format(time.RFC3339))
	req.Header.Set(GnfdUserAddressHeader, mockSwapOutSPAddress)
	req.Header.Set(GnfdOffChainAuthAppDomainHeader, testDomain)
	req.Header.Set(GnfdAuthorizationHeader, commonhttp.Gnfd1Eddsa+",Signature=deadbeef")
	w := httptest.NewRecorder()

	mockGetSwapOutApprovalRoute(t, g).ServeHTTP(w, req)

	assert.Contains(t, w.Body.String(), "unsupported sign type")
}
