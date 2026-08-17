package gater

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	commonhttp "github.com/mocachain/moca-common/go/http"
	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/base/types/gfsptask"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	"github.com/mocachain/moca-storage-provider/core/piecestore"
	"github.com/mocachain/moca-storage-provider/util"
	"github.com/mocachain/moca/v2/sdk/keys"
	permissiontypes "github.com/mocachain/moca/v2/x/permission/types"
	sptypes "github.com/mocachain/moca/v2/x/sp/types"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
	virtual_types "github.com/mocachain/moca/v2/x/virtualgroup/types"
)

func mockNotifyMigrateSwapOutHandlerRoute(t *testing.T, g *GateModular) *mux.Router {
	t.Helper()
	router := mux.NewRouter().SkipClean(true)
	router.Path(NotifyMigrateSwapOutTaskPath).Name(notifyMigrateSwapOutRouterName).Methods(http.MethodPost).
		HandlerFunc(g.notifyMigrateSwapOutHandler)
	return router
}

func TestGateModular_notifyMigrateSwapOutHandler(t *testing.T) {
	cases := []struct {
		name         string
		fn           func() *GateModular
		request      func() *http.Request
		wantedResult string
	}{
		{
			name: "failed to parse migrate swap out header",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, NotifyMigrateSwapOutTaskPath)
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdMigrateSwapOutMsgHeader, "48656c6c6f20476f706865722")
				return req
			},
			wantedResult: "gnfd msg decoding error",
		},
		{
			name: "failed to unmarshal migrate swap out msg",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, NotifyMigrateSwapOutTaskPath)
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdMigrateSwapOutMsgHeader, "48656c6c6f20476f7068657221")
				return req
			},
			wantedResult: "gnfd msg decoding error",
		},
		{
			// mockSwapOutMsgHeader names this SP (mockSelfSPID) as successor, but the
			// claimed source SP is not in an exiting status - checkSwapOutApproval must
			// reject this before NotifyMigrateSwapOut is ever called (no EXPECT set up
			// for it below, so gomock fails the test if the handler still calls it).
			name: "refuse to notify when swap out authorization fails",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				setupSwapOutChain(t, g, ctrl, sptypes.STATUS_IN_SERVICE, mockSwapOutSPID)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, NotifyMigrateSwapOutTaskPath)
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdMigrateSwapOutMsgHeader, mockSwapOutMsgHeader)
				return req
			},
			wantedResult: "no permission",
		},
		{
			name: "failed to notify migrate swap out",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				clientMock.EXPECT().NotifyMigrateSwapOut(gomock.Any(), gomock.Any()).Return(mockErr).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				setupSwapOutChain(t, g, ctrl, sptypes.STATUS_GRACEFUL_EXITING, mockSwapOutSPID)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, NotifyMigrateSwapOutTaskPath)
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdMigrateSwapOutMsgHeader, mockSwapOutMsgHeader)
				return req
			},
			wantedResult: "failed to notify migrate swap out",
		},
		{
			name: "success",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				clientMock.EXPECT().NotifyMigrateSwapOut(gomock.Any(), gomock.Any()).Return(nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				setupSwapOutChain(t, g, ctrl, sptypes.STATUS_GRACEFUL_EXITING, mockSwapOutSPID)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, NotifyMigrateSwapOutTaskPath)
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdMigrateSwapOutMsgHeader, mockSwapOutMsgHeader)
				return req
			},
			wantedResult: "",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			router := mockNotifyMigrateSwapOutHandlerRoute(t, tt.fn())
			w := httptest.NewRecorder()
			router.ServeHTTP(w, tt.request())
			assert.Contains(t, w.Body.String(), tt.wantedResult)
		})
	}
}

func mockMigratePieceHandlerRoute(t *testing.T, g *GateModular) *mux.Router {
	t.Helper()
	router := mux.NewRouter().SkipClean(true)
	router.Path(MigratePiecePath).Name(migratePieceRouterName).Methods(http.MethodGet).HandlerFunc(g.migratePieceHandler)
	return router
}

func makeMockMigrateGVGTaskHeader(t *testing.T, addValidExpireTime bool) string {
	mockTask := &gfsptask.GfSpMigrateGVGTask{}
	if addValidExpireTime {
		mockTask.ExpireTime = time.Now().Unix() + 5*60
	} else {
		mockTask.ExpireTime = time.Now().Unix() - 5*60
	}
	mockKM, err := keys.NewPrivateKeyManager(util.RandHexKey())
	assert.Nil(t, err)
	signature, err := mockKM.Sign(mockTask.GetSignBytes())
	assert.Nil(t, err)
	mockTask.SetSignature(signature)
	msg, err := json.Marshal(mockTask)
	assert.Nil(t, err)
	return hex.EncodeToString(msg)
}

func TestGateModular_migratePieceHandler(t *testing.T) {
	cases := []struct {
		name         string
		fn           func() *GateModular
		request      func() *http.Request
		wantedResult string
	}{
		{
			name: "failed to parse migrate piece header",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, MigratePiecePath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdMigratePieceMsgHeader, "48656c6c6f20476f706865722")
				req.Header.Set(GnfdMigrateGVGMsgHeader, makeMockMigrateGVGTaskHeader(t, true))
				return req
			},
			wantedResult: "gnfd msg decoding error",
		},
		{
			name: "failed to unmarshal migrate piece msg",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, MigratePiecePath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdMigratePieceMsgHeader, "48656c6c6f20476f7068657221")
				req.Header.Set(GnfdMigrateGVGMsgHeader, makeMockMigrateGVGTaskHeader(t, true))
				return req
			},
			wantedResult: "gnfd msg decoding error",
		},
		{
			name: "failed to get migrate piece object info due to gvg expire time",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, MigratePiecePath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdMigratePieceMsgHeader, "7b227461736b223a7b7d2c2273746f726167655f706172616d73223a7b2276657273696f6e65645f706172616d73223a7b226d61785f7365676d656e745f73697a65223a31302c22726564756e64616e745f646174615f6368756e6b5f6e756d223a342c22726564756e64616e745f7061726974795f6368756e6b5f6e756d223a327d7d7d")
				req.Header.Set(GnfdMigrateGVGMsgHeader, makeMockMigrateGVGTaskHeader(t, false))
				return req
			},
			wantedResult: "no permission",
		},
		{
			name: "failed to get migrate piece object info due to query sp error",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				chainMock := consensus.NewMockConsensus(ctrl)
				chainMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{}, fmt.Errorf("failed to query sp")).Times(1)
				g.spCachePool = NewSPCachePool(chainMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, MigratePiecePath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdMigratePieceMsgHeader, "7b227461736b223a7b7d2c2273746f726167655f706172616d73223a7b2276657273696f6e65645f706172616d73223a7b226d61785f7365676d656e745f73697a65223a31302c22726564756e64616e745f646174615f6368756e6b5f6e756d223a342c22726564756e64616e745f7061726974795f6368756e6b5f6e756d223a327d7d7d")
				req.Header.Set(GnfdMigrateGVGMsgHeader, makeMockMigrateGVGTaskHeader(t, true))
				return req
			},
			wantedResult: "failed to query sp",
		},
		{
			name: "failed to get migrate piece object info due to metadata api error",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				clientMock.EXPECT().VerifyMigrateGVGPermission(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("failed to query metadata")).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				chainMock := consensus.NewMockConsensus(ctrl)
				chainMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{}, nil).Times(1)
				g.spCachePool = NewSPCachePool(chainMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, MigratePiecePath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdMigratePieceMsgHeader, "7b227461736b223a7b7d2c2273746f726167655f706172616d73223a7b2276657273696f6e65645f706172616d73223a7b226d61785f7365676d656e745f73697a65223a31302c22726564756e64616e745f646174615f6368756e6b5f6e756d223a342c22726564756e64616e745f7061726974795f6368756e6b5f6e756d223a327d7d7d")
				req.Header.Set(GnfdMigrateGVGMsgHeader, makeMockMigrateGVGTaskHeader(t, true))
				return req
			},
			wantedResult: "failed to query metadata",
		},
		{
			name: "failed to get migrate piece object info due to metadata no permission",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				mockEffect := permissiontypes.EFFECT_DENY
				clientMock.EXPECT().VerifyMigrateGVGPermission(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(&mockEffect, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				chainMock := consensus.NewMockConsensus(ctrl)
				chainMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{}, nil).Times(1)
				g.spCachePool = NewSPCachePool(chainMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, MigratePiecePath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdMigratePieceMsgHeader, "7b227461736b223a7b7d2c2273746f726167655f706172616d73223a7b2276657273696f6e65645f706172616d73223a7b226d61785f7365676d656e745f73697a65223a31302c22726564756e64616e745f646174615f6368756e6b5f6e756d223a342c22726564756e64616e745f7061726974795f6368756e6b5f6e756d223a327d7d7d")
				req.Header.Set(GnfdMigrateGVGMsgHeader, makeMockMigrateGVGTaskHeader(t, true))
				return req
			},
			wantedResult: "no permission",
		},
		{
			name: "failed to get migrate piece object info due to has no object info",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				mockEffect := permissiontypes.EFFECT_ALLOW
				clientMock.EXPECT().VerifyMigrateGVGPermission(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(&mockEffect, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				chainMock := consensus.NewMockConsensus(ctrl)
				chainMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{}, nil).Times(1)
				g.spCachePool = NewSPCachePool(chainMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, MigratePiecePath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdMigratePieceMsgHeader, "7b227461736b223a7b7d2c2273746f726167655f706172616d73223a7b2276657273696f6e65645f706172616d73223a7b226d61785f7365676d656e745f73697a65223a31302c22726564756e64616e745f646174615f6368756e6b5f6e756d223a342c22726564756e64616e745f7061726974795f6368756e6b5f6e756d223a327d7d7d")
				req.Header.Set(GnfdMigrateGVGMsgHeader, makeMockMigrateGVGTaskHeader(t, true))
				return req
			},
			wantedResult: "invalid request header",
		},
		{
			name: "failed to get object on chain meta",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				mockEffect := permissiontypes.EFFECT_ALLOW
				clientMock.EXPECT().VerifyMigrateGVGPermission(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(&mockEffect, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)

				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QueryObjectInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil,
					mockErr).Times(1)
				g.baseApp.SetConsensus(consensusMock)
				chainMock := consensus.NewMockConsensus(ctrl)
				chainMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{}, nil).Times(1)
				g.spCachePool = NewSPCachePool(chainMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, MigratePiecePath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdMigratePieceMsgHeader, "7b227461736b223a7b7d2c226f626a6563745f696e666f223a7b226f626a6563745f6e616d65223a226d6f636b2d6f626a6563742d6e616d65222c226964223a2231227d2c2273746f726167655f706172616d73223a7b2276657273696f6e65645f706172616d73223a7b226d61785f7365676d656e745f73697a65223a31302c22726564756e64616e745f646174615f6368756e6b5f6e756d223a342c22726564756e64616e745f7061726974795f6368756e6b5f6e756d223a327d7d7d")
				req.Header.Set(GnfdMigrateGVGMsgHeader, makeMockMigrateGVGTaskHeader(t, true))
				return req
			},
			wantedResult: "invalid request header",
		},
		{
			name: "invalid redundancy index",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				mockEffect := permissiontypes.EFFECT_ALLOW
				clientMock.EXPECT().VerifyMigrateGVGPermission(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(&mockEffect, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)

				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QueryObjectInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(&storagetypes.ObjectInfo{
					ObjectName: mockObjectName, CreateAt: 1,
				}, nil).Times(1)
				consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Return(&storagetypes.BucketInfo{
					BucketName: mockBucketName,
				}, nil).Times(1)
				consensusMock.EXPECT().QueryStorageParamsByTimestamp(gomock.Any(), gomock.Any()).Return(&storagetypes.Params{
					MaxPayloadSize: DefaultMaxPayloadSize,
				}, nil).Times(1)
				g.baseApp.SetConsensus(consensusMock)
				chainMock := consensus.NewMockConsensus(ctrl)
				chainMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{}, nil).Times(1)
				g.spCachePool = NewSPCachePool(chainMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, MigratePiecePath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdMigratePieceMsgHeader, "7b227461736b223a7b7d2c226f626a6563745f696e666f223a7b226f626a6563745f6e616d65223a226d6f636b2d6f626a6563742d6e616d65222c226964223a2231227d2c2273746f726167655f706172616d73223a7b2276657273696f6e65645f706172616d73223a7b226d61785f7365676d656e745f73697a65223a31302c22726564756e64616e745f646174615f6368756e6b5f6e756d223a342c22726564756e64616e745f7061726974795f6368756e6b5f6e756d223a327d7d2c22726564756e64616e63795f696478223a2d327d")
				req.Header.Set(GnfdMigrateGVGMsgHeader, makeMockMigrateGVGTaskHeader(t, true))
				return req
			},
			wantedResult: "invalid redundancy index",
		},
		{
			name: "redundancy index is -1 and failed to download piece",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				mockEffect := permissiontypes.EFFECT_ALLOW
				clientMock.EXPECT().VerifyMigrateGVGPermission(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(&mockEffect, nil).Times(1)
				clientMock.EXPECT().GetPiece(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
				g.baseApp.SetGfSpClient(clientMock)

				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QueryObjectInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(&storagetypes.ObjectInfo{
					ObjectName: mockObjectName, CreateAt: 1,
				}, nil).Times(1)
				consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Return(&storagetypes.BucketInfo{
					BucketName: mockBucketName,
				}, nil).Times(1)
				consensusMock.EXPECT().QueryStorageParamsByTimestamp(gomock.Any(), gomock.Any()).Return(&storagetypes.Params{
					MaxPayloadSize: DefaultMaxPayloadSize,
				}, nil).Times(1)
				g.baseApp.SetConsensus(consensusMock)

				pieceOpMock := piecestore.NewMockPieceOp(ctrl)
				g.baseApp.SetPieceOp(pieceOpMock)
				pieceOpMock.EXPECT().SegmentPieceKey(gomock.Any(), gomock.Any(), gomock.Any()).Return("test").Times(1)
				pieceOpMock.EXPECT().SegmentPieceSize(gomock.Any(), gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)

				consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{}, nil).Times(1)
				g.spCachePool = NewSPCachePool(consensusMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, MigratePiecePath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdMigratePieceMsgHeader, "7b227461736b223a7b7d2c226f626a6563745f696e666f223a7b226f626a6563745f6e616d65223a226d6f636b2d6f626a6563742d6e616d65222c226964223a2231227d2c2273746f726167655f706172616d73223a7b2276657273696f6e65645f706172616d73223a7b226d61785f7365676d656e745f73697a65223a31302c22726564756e64616e745f646174615f6368756e6b5f6e756d223a342c22726564756e64616e745f7061726974795f6368756e6b5f6e756d223a327d7d2c22726564756e64616e63795f696478223a2d317d")
				req.Header.Set(GnfdMigrateGVGMsgHeader, makeMockMigrateGVGTaskHeader(t, true))
				return req
			},
			wantedResult: "mock error",
		},
		{
			name: "redundancy index is not  -1 and succeed to migrate one piece",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				mockEffect := permissiontypes.EFFECT_ALLOW
				clientMock.EXPECT().VerifyMigrateGVGPermission(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(&mockEffect, nil).Times(1)
				clientMock.EXPECT().GetPiece(gomock.Any(), gomock.Any()).Return([]byte("data"), nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)

				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QueryObjectInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(&storagetypes.ObjectInfo{
					ObjectName: mockObjectName, CreateAt: 1,
				}, nil).Times(1)
				consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Return(&storagetypes.BucketInfo{
					BucketName: mockBucketName,
				}, nil).Times(1)
				consensusMock.EXPECT().QueryStorageParamsByTimestamp(gomock.Any(), gomock.Any()).Return(&storagetypes.Params{
					MaxPayloadSize: DefaultMaxPayloadSize,
				}, nil).Times(1)
				g.baseApp.SetConsensus(consensusMock)

				pieceOpMock := piecestore.NewMockPieceOp(ctrl)
				g.baseApp.SetPieceOp(pieceOpMock)
				pieceOpMock.EXPECT().ECPieceKey(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("test").Times(1)
				pieceOpMock.EXPECT().ECPieceSize(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(int64(1)).Times(1)

				consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{}, nil).Times(1)
				g.spCachePool = NewSPCachePool(consensusMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, MigratePiecePath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdMigratePieceMsgHeader, "7b227461736b223a7b7d2c226f626a6563745f696e666f223a7b226f626a6563745f6e616d65223a226d6f636b2d6f626a6563742d6e616d65222c226964223a2231227d2c2273746f726167655f706172616d73223a7b2276657273696f6e65645f706172616d73223a7b226d61785f7365676d656e745f73697a65223a31302c22726564756e64616e745f646174615f6368756e6b5f6e756d223a342c22726564756e64616e745f7061726974795f6368756e6b5f6e756d223a327d7d2c22726564756e64616e63795f696478223a317d")
				req.Header.Set(GnfdMigrateGVGMsgHeader, makeMockMigrateGVGTaskHeader(t, true))
				return req
			},
			wantedResult: "",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			router := mockMigratePieceHandlerRoute(t, tt.fn())
			w := httptest.NewRecorder()
			router.ServeHTTP(w, tt.request())
			assert.Contains(t, w.Body.String(), tt.wantedResult)
		})
	}
}

func mockGetSecondaryBlsMigrationBucketApprovalHandlerRoute(t *testing.T, g *GateModular) *mux.Router {
	t.Helper()
	router := mux.NewRouter().SkipClean(true)
	router.Path(SecondarySPMigrationBucketApprovalPath).Name(migrationBucketApprovalName).Methods(http.MethodGet).
		HandlerFunc(g.getSecondaryBlsMigrationBucketApprovalHandler)
	return router
}

func TestGateModular_getSecondaryBlsMigrationBucketApprovalHandler(t *testing.T) {
	cases := []struct {
		name         string
		fn           func() *GateModular
		request      func() *http.Request
		wantedResult string
	}{
		{
			name: "failed to parse secondary migration bucket approval header",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, SecondarySPMigrationBucketApprovalPath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdSecondarySPMigrationBucketMsgHeader, "48656c6c6f20476f706865722")
				return req
			},
			wantedResult: "gnfd msg decoding error",
		},
		{
			name: "failed to unmarshal migration bucket approval msg",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, SecondarySPMigrationBucketApprovalPath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdSecondarySPMigrationBucketMsgHeader, "48656c6c6f20476f7068657221")
				return req
			},
			wantedResult: "gnfd msg decoding error",
		},
		{
			name: "failed to sign secondary sp migration bucket",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				clientMock.EXPECT().SignSecondarySPMigrationBucket(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				setupSecondaryBlsMigrationChain(t, g, clientMock, permissiontypes.EFFECT_ALLOW)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, SecondarySPMigrationBucketApprovalPath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdSecondarySPMigrationBucketMsgHeader, "7b22636861696e5f6964223a2231222c226473745f7072696d6172795f73705f6964223a312c227372635f676c6f62616c5f7669727475616c5f67726f75705f6964223a322c226473745f676c6f62616c5f7669727475616c5f67726f75705f6964223a332c226275636b65745f6964223a2231227d")
				return req
			},
			wantedResult: "failed to sign secondary sp migration bucket",
		},
		{
			name: "refuse to sign when the bucket is not migrating to the declared dest sp",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				setupSecondaryBlsMigrationChain(t, g, clientMock, permissiontypes.EFFECT_DENY)
				return g
			},
			request:      newSecondaryBlsMigrationRequest,
			wantedResult: "no permission",
		},
		{
			name: "refuse to sign when this sp is not a secondary sp of the dst gvg",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				g.baseApp.SetChainID(mockChainID)
				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).
					Return(&sptypes.StorageProvider{Id: mockSecondarySPID}, nil).AnyTimes()
				consensusMock.EXPECT().QueryGlobalVirtualGroup(gomock.Any(), uint32(3)).
					Return(&virtual_types.GlobalVirtualGroup{Id: 3, PrimarySpId: mockSelfSPID, SecondarySpIds: []uint32{42}}, nil).Times(1)
				g.baseApp.SetConsensus(consensusMock)
				g.spCachePool = NewSPCachePool(consensusMock)
				return g
			},
			request:      newSecondaryBlsMigrationRequest,
			wantedResult: "no permission",
		},
		{
			name: "refuse to sign a sign doc without a bucket id",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				g.baseApp.SetChainID(mockChainID)
				return g
			},
			request: func() *http.Request {
				req := newSecondaryBlsMigrationRequest()
				req.Header.Set(GnfdSecondarySPMigrationBucketMsgHeader, mockSignDocWithoutBucketIDHeader)
				return req
			},
			wantedResult: "gnfd msg validate error",
		},
		{
			name: "refuse to sign a sign doc of another chain",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				g.baseApp.SetChainID("another-chain")
				return g
			},
			request:      newSecondaryBlsMigrationRequest,
			wantedResult: "gnfd msg validate error",
		},
		{
			name: "success",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				clientMock.EXPECT().SignSecondarySPMigrationBucket(gomock.Any(), gomock.Any()).Return([]byte("mockSig"), nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				setupSecondaryBlsMigrationChain(t, g, clientMock, permissiontypes.EFFECT_ALLOW)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, SecondarySPMigrationBucketApprovalPath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdSecondarySPMigrationBucketMsgHeader, "7b22636861696e5f6964223a2231222c226473745f7072696d6172795f73705f6964223a312c227372635f676c6f62616c5f7669727475616c5f67726f75705f6964223a322c226473745f676c6f62616c5f7669727475616c5f67726f75705f6964223a332c226275636b65745f6964223a2231227d")
				return req
			},
			wantedResult: "",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			router := mockGetSecondaryBlsMigrationBucketApprovalHandlerRoute(t, tt.fn())
			w := httptest.NewRecorder()
			router.ServeHTTP(w, tt.request())
			assert.Contains(t, w.Body.String(), tt.wantedResult)
		})
	}
}

func mockGetSwapOutApprovalRoute(t *testing.T, g *GateModular) *mux.Router {
	t.Helper()
	router := mux.NewRouter().SkipClean(true)
	router.Path(SwapOutApprovalPath).Name(swapOutApprovalName).Methods(http.MethodGet).HandlerFunc(g.getSwapOutApproval)
	return router
}

func TestGateModular_getSwapOutApproval(t *testing.T) {
	cases := []struct {
		name         string
		fn           func() *GateModular
		request      func() *http.Request
		wantedResult string
	}{
		{
			name: "failed to parse swap out approval header",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, SwapOutApprovalPath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdUnsignedApprovalMsgHeader, "48656c6c6f20476f706865722")
				return req
			},
			wantedResult: "gnfd msg decoding error",
		},
		{
			name: "failed to unmarshal swap out approval msg",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, SwapOutApprovalPath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdUnsignedApprovalMsgHeader, "48656c6c6f20476f7068657221")
				return req
			},
			wantedResult: "gnfd msg decoding error",
		},
		{
			name: "failed to basic check approval msg",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, SwapOutApprovalPath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdUnsignedApprovalMsgHeader, "7b2273746f726167655f70726f7669646572223a22307831433743384136363865323361454432393166373866433266336231383635416363383762364636222c22676c6f62616c5f7669727475616c5f67726f75705f66616d696c795f6964223a322c22676c6f62616c5f7669727475616c5f67726f75705f696473223a5b5d2c22737563636573736f725f73705f6964223a302c22737563636573736f725f73705f617070726f76616c223a6e756c6c7d")
				return req
			},
			wantedResult: "gnfd msg validate error",
		},
		{
			name: "failed to sign swap out",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				clientMock.EXPECT().SignSwapOut(gomock.Any(), gomock.Any()).Return(nil, mockErr)
				g.baseApp.SetGfSpClient(clientMock)
				setupSwapOutChain(t, g, ctrl, sptypes.STATUS_GRACEFUL_EXITING, mockSwapOutSPID)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, SwapOutApprovalPath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdUnsignedApprovalMsgHeader, "7b2273746f726167655f70726f7669646572223a22307831433743384136363865323361454432393166373866433266336231383635416363383762364636222c22676c6f62616c5f7669727475616c5f67726f75705f66616d696c795f6964223a322c22676c6f62616c5f7669727475616c5f67726f75705f696473223a5b5d2c22737563636573736f725f73705f6964223a312c22737563636573736f725f73705f617070726f76616c223a6e756c6c7d")
				return req
			},
			wantedResult: "failed to sign swap out",
		},
		{
			name: "refuse to sign when the requesting sp is not exiting",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				setupSwapOutChain(t, g, ctrl, sptypes.STATUS_IN_SERVICE, mockSwapOutSPID)
				return g
			},
			request:      newSwapOutApprovalRequest,
			wantedResult: "no permission",
		},
		{
			name: "refuse to sign when the family belongs to another sp",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				setupSwapOutChain(t, g, ctrl, sptypes.STATUS_GRACEFUL_EXITING, mockSwapOutSPID+1)
				return g
			},
			request:      newSwapOutApprovalRequest,
			wantedResult: "no permission",
		},
		{
			name: "refuse to sign when this sp is not the successor",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).
					Return(&sptypes.StorageProvider{Id: 99}, nil).AnyTimes()
				g.baseApp.SetConsensus(consensusMock)
				g.spCachePool = NewSPCachePool(consensusMock)
				return g
			},
			request:      newSwapOutApprovalRequest,
			wantedResult: "no permission",
		},
		{
			name: "success",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				clientMock.EXPECT().SignSwapOut(gomock.Any(), gomock.Any()).Return([]byte("mockSig"), nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				setupSwapOutChain(t, g, ctrl, sptypes.STATUS_GRACEFUL_EXITING, mockSwapOutSPID)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s%s", scheme, testDomain, SwapOutApprovalPath)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				req.Header.Set(GnfdUnsignedApprovalMsgHeader, "7b2273746f726167655f70726f7669646572223a22307831433743384136363865323361454432393166373866433266336231383635416363383762364636222c22676c6f62616c5f7669727475616c5f67726f75705f66616d696c795f6964223a322c22676c6f62616c5f7669727475616c5f67726f75705f696473223a5b5d2c22737563636573736f725f73705f6964223a312c22737563636573736f725f73705f617070726f76616c223a6e756c6c7d")
				return req
			},
			wantedResult: "",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			router := mockGetSwapOutApprovalRoute(t, tt.fn())
			w := httptest.NewRecorder()
			router.ServeHTTP(w, tt.request())
			assert.Contains(t, w.Body.String(), tt.wantedResult)
		})
	}
}

const (
	// values encoded in the request headers used by the approval handler tests
	mockChainID                             = "1"
	mockSelfSPID                     uint32 = 1
	mockSecondarySPID                uint32 = 5
	mockSwapOutSPID                  uint32 = 7
	mockSwapOutSPAddress                    = "0x1C7C8A668e23aED291f78fC2f3b1865Acc87b6F6"
	mockSecondaryBlsSignDocHeader           = "7b22636861696e5f6964223a2231222c226473745f7072696d6172795f73705f6964223a312c227372635f676c6f62616c5f7669727475616c5f67726f75705f6964223a322c226473745f676c6f62616c5f7669727475616c5f67726f75705f6964223a332c226275636b65745f6964223a2231227d"
	mockSignDocWithoutBucketIDHeader        = "7b22636861696e5f6964223a2231222c226473745f7072696d6172795f73705f6964223a312c227372635f676c6f62616c5f7669727475616c5f67726f75705f6964223a322c226473745f676c6f62616c5f7669727475616c5f67726f75705f6964223a337d"
	mockSwapOutMsgHeader                    = "7b2273746f726167655f70726f7669646572223a22307831433743384136363865323361454432393166373866433266336231383635416363383762364636222c22676c6f62616c5f7669727475616c5f67726f75705f66616d696c795f6964223a322c22676c6f62616c5f7669727475616c5f67726f75705f696473223a5b5d2c22737563636573736f725f73705f6964223a312c22737563636573736f725f73705f617070726f76616c223a6e756c6c7d"
)

func newSecondaryBlsMigrationRequest() *http.Request {
	path := fmt.Sprintf("%s%s%s", scheme, testDomain, SecondarySPMigrationBucketApprovalPath)
	req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
	req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, time.Now().Add(time.Hour*60).Format(ExpiryDateFormat))
	req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
	req.Header.Set(GnfdSecondarySPMigrationBucketMsgHeader, mockSecondaryBlsSignDocHeader)
	return req
}

func newSwapOutApprovalRequest() *http.Request {
	path := fmt.Sprintf("%s%s%s", scheme, testDomain, SwapOutApprovalPath)
	req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
	req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, time.Now().Add(time.Hour*60).Format(ExpiryDateFormat))
	req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
	req.Header.Set(GnfdUnsignedApprovalMsgHeader, mockSwapOutMsgHeader)
	return req
}

// setupSecondaryBlsMigrationChain wires the chain state the sign doc in
// mockSecondaryBlsSignDocHeader claims: this SP is a secondary SP of dst gvg 3,
// which belongs to dst primary SP 1.
func setupSecondaryBlsMigrationChain(t *testing.T, g *GateModular, clientMock *gfspclient.MockGfSpClientAPI, effect permissiontypes.Effect) {
	t.Helper()
	g.baseApp.SetChainID(mockChainID)
	ctrl := gomock.NewController(t)
	consensusMock := consensus.NewMockConsensus(ctrl)
	consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).
		Return(&sptypes.StorageProvider{Id: mockSecondarySPID}, nil).AnyTimes()
	consensusMock.EXPECT().QueryGlobalVirtualGroup(gomock.Any(), uint32(3)).
		Return(&virtual_types.GlobalVirtualGroup{Id: 3, PrimarySpId: mockSelfSPID, SecondarySpIds: []uint32{mockSecondarySPID}}, nil).AnyTimes()
	g.baseApp.SetConsensus(consensusMock)
	g.spCachePool = NewSPCachePool(consensusMock)
	clientMock.EXPECT().VerifyMigrateGVGPermission(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&effect, nil).AnyTimes()
}

// setupSwapOutChain wires the chain state the swap out in mockSwapOutMsgHeader
// claims: this SP is successor 1, the requesting SP owns family 2.
func setupSwapOutChain(t *testing.T, g *GateModular, ctrl *gomock.Controller, srcStatus sptypes.Status, familyPrimarySPID uint32) {
	t.Helper()
	consensusMock := consensus.NewMockConsensus(ctrl)
	consensusMock.EXPECT().QuerySP(gomock.Any(), mockSwapOutSPAddress).
		Return(&sptypes.StorageProvider{Id: mockSwapOutSPID, OperatorAddress: mockSwapOutSPAddress, Status: srcStatus}, nil).AnyTimes()
	consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).
		Return(&sptypes.StorageProvider{Id: mockSelfSPID}, nil).AnyTimes()
	consensusMock.EXPECT().QueryVirtualGroupFamily(gomock.Any(), uint32(2)).
		Return(&virtual_types.GlobalVirtualGroupFamily{Id: 2, PrimarySpId: familyPrimarySPID}, nil).AnyTimes()
	g.baseApp.SetConsensus(consensusMock)
	g.spCachePool = NewSPCachePool(consensusMock)
}
