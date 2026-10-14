package gater

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	commonhttp "github.com/mocachain/moca-common/go/http"
	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	coremodule "github.com/mocachain/moca-storage-provider/core/module"
	sptypes "github.com/mocachain/moca/v2/x/sp/types"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
)

func TestDelegateCreateFolderFailsClosedWhenHeadObjectRPCFails(t *testing.T) {
	g := setup(t)
	ctrl := gomock.NewController(t)
	clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
	clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(false, nil)
	clientMock.EXPECT().VerifyAuthentication(gomock.Any(), coremodule.AuthOpTypeAgentPutObject, gomock.Any(), mockBucketName, mockObjectName).Return(true, nil)
	g.baseApp.SetGfSpClient(clientMock)

	consensusMock := consensus.NewMockConsensus(ctrl)
	consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Status: sptypes.STATUS_IN_SERVICE}, nil)
	consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), mockBucketName).Return(&storagetypes.BucketInfo{BucketStatus: storagetypes.BUCKET_STATUS_CREATED}, nil)
	consensusMock.EXPECT().QueryObjectInfo(gomock.Any(), mockBucketName, mockObjectName).Return(nil, errors.New("head object RPC unavailable"))
	g.baseApp.SetConsensus(consensusMock)

	req := httptest.NewRequest(http.MethodPost,
		"https://"+mockBucketName+"."+testDomain+"/"+mockObjectName+"?"+CreateFolderQuery+"&visibility=0",
		strings.NewReader(""))
	req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, time.Now().Add(time.Minute).UTC().Format(ExpiryDateFormat))
	req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
	req.Header.Set(GnfdUserAddressHeader, strings.Repeat("0", 40))
	req.Header.Set(GnfdOffChainAuthAppDomainHeader, testDomain)
	req.Header.Set(GnfdUnsignedApprovalMsgHeader, "00")
	req = mux.SetURLVars(req, map[string]string{"bucket": mockBucketName, "object": mockObjectName})

	resp := httptest.NewRecorder()
	g.delegateCreateFolderHandler(resp, req)

	require.NotEqual(t, http.StatusOK, resp.Code)
	require.Contains(t, resp.Body.String(), "head object RPC unavailable")
}
