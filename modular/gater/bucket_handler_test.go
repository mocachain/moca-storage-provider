package gater

import (
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sptypes "github.com/mocachain/moca/v2/x/sp/types"
	virtualgrouptypes "github.com/mocachain/moca/v2/x/virtualgroup/types"

	sdkmath "cosmossdk.io/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	commonhttp "github.com/mocachain/moca-common/go/http"
	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/base/types/gfspserver"
	"github.com/mocachain/moca-storage-provider/core/consensus"
	metadatatypes "github.com/mocachain/moca-storage-provider/modular/metadata/types"
	storetypes "github.com/mocachain/moca-storage-provider/store/types"
	storagetypes "github.com/mocachain/moca/v2/x/storage/types"
)

func mockGetBucketReadQuotaRoute(t *testing.T, g *GateModular) *mux.Router {
	t.Helper()
	router := mux.NewRouter().SkipClean(true)
	var routers []*mux.Router
	routers = append(routers, router.Host("{bucket:.+}."+g.domain).Subrouter())
	routers = append(routers, router.PathPrefix("/{bucket}").Subrouter())
	for _, r := range routers {
		r.NewRoute().Name(getBucketReadQuotaRouterName).Methods(http.MethodGet).HandlerFunc(g.getBucketReadQuotaHandler).
			Queries(GetBucketReadQuotaQuery, "", GetBucketReadQuotaMonthQuery, "{year_month}")
	}
	return router
}

func TestGateModular_getBucketReadQuotaHandler(t *testing.T) {
	cases := []struct {
		name         string
		fn           func() *GateModular
		request      func() *http.Request
		wantedResult string
	}{
		{
			name: "no permission to operate",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				g.baseApp.SetGfSpClient(clientMock)
				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Return(&storagetypes.BucketInfo{
					BucketName: mockBucketName, Id: sdkmath.NewUint(1),
				}, nil).Times(1)
				consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Id: 2}, nil).Times(1)
				consensusMock.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&virtualgrouptypes.GlobalVirtualGroupFamily{Id: 1, PrimarySpId: 1}, nil).Times(1)
				g.baseApp.SetConsensus(consensusMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s.%s/?%s&%s", scheme, mockBucketName, testDomain, GetBucketReadQuotaQuery,
					GetBucketReadQuotaMonthQuery)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				signAsRandomAccount(req)
				return req
			},
			wantedResult: "mismatch sp",
		},
		{
			name: "failed to get bucket info from consensus",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				g.baseApp.SetGfSpClient(clientMock)
				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
				g.baseApp.SetConsensus(consensusMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s.%s/?%s&%s", scheme, mockBucketName, testDomain, GetBucketReadQuotaQuery,
					GetBucketReadQuotaMonthQuery)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				signAsRandomAccount(req)
				return req
			},
			wantedResult: "failed to get bucket info from consensus",
		},
		{
			name: "failed to get bucket read quota",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().GetBucketReadQuota(gomock.Any(), gomock.Any(), gomock.Any()).Return(uint64(0),
					uint64(0), uint64(0), uint64(0), uint64(0), uint64(0), mockErr).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Return(&storagetypes.BucketInfo{
					BucketName: mockBucketName, Id: sdkmath.NewUint(1),
				}, nil).Times(1)
				consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Id: 1}, nil).Times(1)
				consensusMock.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&virtualgrouptypes.GlobalVirtualGroupFamily{Id: 1, PrimarySpId: 1}, nil).Times(1)
				g.baseApp.SetConsensus(consensusMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s.%s/?%s&%s", scheme, mockBucketName, testDomain, GetBucketReadQuotaQuery,
					GetBucketReadQuotaMonthQuery)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				signAsRandomAccount(req)
				return req
			},
			wantedResult: "mock error",
		},
		{
			name: "success",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().GetBucketReadQuota(gomock.Any(), gomock.Any(), gomock.Any()).Return(uint64(0),
					uint64(0), uint64(0), uint64(0), uint64(0), uint64(0), nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Return(&storagetypes.BucketInfo{
					BucketName: mockBucketName, Id: sdkmath.NewUint(1),
				}, nil).Times(1)
				consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Id: 1}, nil).Times(1)
				consensusMock.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&virtualgrouptypes.GlobalVirtualGroupFamily{Id: 1, PrimarySpId: 1}, nil).Times(1)
				g.baseApp.SetConsensus(consensusMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s.%s/?%s&%s", scheme, mockBucketName, testDomain, GetBucketReadQuotaQuery,
					GetBucketReadQuotaMonthQuery)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				signAsRandomAccount(req)
				return req
			},
			wantedResult: "",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			router := mockGetBucketReadQuotaRoute(t, tt.fn())
			w := httptest.NewRecorder()
			router.ServeHTTP(w, tt.request())
			assert.Contains(t, w.Body.String(), tt.wantedResult)
		})
	}
}

func mockListBucketReadRecordHandler(t *testing.T, g *GateModular) *mux.Router {
	t.Helper()
	router := mux.NewRouter().SkipClean(true)
	var routers []*mux.Router
	routers = append(routers, router.Host("{bucket:.+}."+g.domain).Subrouter())
	routers = append(routers, router.PathPrefix("/{bucket}").Subrouter())
	for _, r := range routers {
		r.NewRoute().Name(listBucketReadRecordRouterName).Methods(http.MethodGet).HandlerFunc(g.listBucketReadRecordHandler).
			Queries(ListBucketReadRecordQuery, "", ListBucketReadRecordMaxRecordsQuery, "{max_records}",
				StartTimestampUs, "{start_ts}", EndTimestampUs, "{end_ts}")
	}
	return router
}

// testBucketOwnerKey owns mockBucketName in the read record cases below, since the
// handler serves that list only to the bucket owner.
var testBucketOwnerKey, testBucketOwnerAddress = func() (*ecdsa.PrivateKey, string) {
	key, err := crypto.GenerateKey()
	if err != nil {
		panic(err)
	}
	return key, crypto.PubkeyToAddress(key.PublicKey).Hex()
}()

func TestGateModular_listBucketReadRecordHandler(t *testing.T) {
	cases := []struct {
		name         string
		fn           func() *GateModular
		request      func() *http.Request
		wantedResult string
	}{
		{
			name: "no permission to operate",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Return(&storagetypes.BucketInfo{
					BucketName: mockBucketName, Id: sdkmath.NewUint(1), Owner: testBucketOwnerAddress,
				}, nil).Times(1)
				consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Id: 2}, nil).Times(1)
				consensusMock.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&virtualgrouptypes.GlobalVirtualGroupFamily{Id: 1, PrimarySpId: 1}, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				g.baseApp.SetConsensus(consensusMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s.%s/?%s&%s&%s&%s", scheme, mockBucketName, testDomain, ListBucketReadRecordQuery,
					ListBucketReadRecordMaxRecordsQuery, StartTimestampUs, EndTimestampUs)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				signAsAccount(req, testBucketOwnerKey)
				return req
			},
			wantedResult: "mismatch sp",
		},
		{
			name: "failed to get bucket info from consensus",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				g.baseApp.SetGfSpClient(clientMock)
				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
				g.baseApp.SetConsensus(consensusMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s.%s/?%s&%s&%s&%s", scheme, mockBucketName, testDomain, ListBucketReadRecordQuery,
					ListBucketReadRecordMaxRecordsQuery, StartTimestampUs, EndTimestampUs)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				signAsAccount(req, testBucketOwnerKey)
				return req
			},
			wantedResult: "failed to get bucket info from consensus",
		},
		{
			name: "failed to parse start_ts query",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				g.baseApp.SetGfSpClient(clientMock)
				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Return(&storagetypes.BucketInfo{
					BucketName: mockBucketName, Id: sdkmath.NewUint(1), Owner: testBucketOwnerAddress,
				}, nil).Times(1)
				consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Id: 1}, nil).Times(1)
				consensusMock.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&virtualgrouptypes.GlobalVirtualGroupFamily{Id: 1, PrimarySpId: 1}, nil).Times(1)
				g.baseApp.SetConsensus(consensusMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s.%s/?%s&%s=%s&%s=%s&%s=%s", scheme, mockBucketName, testDomain, ListBucketReadRecordQuery,
					ListBucketReadRecordMaxRecordsQuery, "a", StartTimestampUs, "b", EndTimestampUs, "c")
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				signAsAccount(req, testBucketOwnerKey)
				return req
			},
			wantedResult: "invalid request params for query",
		},
		{
			name: "failed to parse end_ts query",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				g.baseApp.SetGfSpClient(clientMock)
				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Return(&storagetypes.BucketInfo{
					BucketName: mockBucketName, Id: sdkmath.NewUint(1), Owner: testBucketOwnerAddress,
				}, nil).Times(1)
				consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Id: 1}, nil).Times(1)
				consensusMock.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&virtualgrouptypes.GlobalVirtualGroupFamily{Id: 1, PrimarySpId: 1}, nil).Times(1)
				g.baseApp.SetConsensus(consensusMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s.%s/?%s&%s=%s&%s=%s&%s=%s", scheme, mockBucketName, testDomain, ListBucketReadRecordQuery,
					ListBucketReadRecordMaxRecordsQuery, "a", StartTimestampUs, "10", EndTimestampUs, "c")
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				signAsAccount(req, testBucketOwnerKey)
				return req
			},
			wantedResult: "invalid request params for query",
		},
		{
			name: "failed to parse max records query",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				g.baseApp.SetGfSpClient(clientMock)
				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Return(&storagetypes.BucketInfo{
					BucketName: mockBucketName, Id: sdkmath.NewUint(1), Owner: testBucketOwnerAddress,
				}, nil).Times(1)
				consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Id: 1}, nil).Times(1)
				consensusMock.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&virtualgrouptypes.GlobalVirtualGroupFamily{Id: 1, PrimarySpId: 1}, nil).Times(1)
				g.baseApp.SetConsensus(consensusMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s.%s/?%s&%s=%s&%s=%s&%s=%s", scheme, mockBucketName, testDomain, ListBucketReadRecordQuery,
					ListBucketReadRecordMaxRecordsQuery, "a", StartTimestampUs, "10", EndTimestampUs, "2")
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				signAsAccount(req, testBucketOwnerKey)
				return req
			},
			wantedResult: "invalid request params for query",
		},
		{
			name: "failed to list bucket read record",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().ListBucketReadRecord(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(nil, int64(0), mockErr)
				g.baseApp.SetGfSpClient(clientMock)
				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Return(&storagetypes.BucketInfo{
					BucketName: mockBucketName, Id: sdkmath.NewUint(1), Owner: testBucketOwnerAddress,
				}, nil).Times(1)
				consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Id: 1}, nil).Times(1)
				consensusMock.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&virtualgrouptypes.GlobalVirtualGroupFamily{Id: 1, PrimarySpId: 1}, nil).Times(1)
				g.baseApp.SetConsensus(consensusMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s.%s/?%s&%s=%s&%s=%s&%s=%s", scheme, mockBucketName, testDomain, ListBucketReadRecordQuery,
					ListBucketReadRecordMaxRecordsQuery, "-1", StartTimestampUs, "10", EndTimestampUs, "2")
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				signAsAccount(req, testBucketOwnerKey)
				return req
			},
			wantedResult: "mock error",
		},
		{
			name: "success",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				records := []*metadatatypes.ReadRecord{
					{
						ObjectName: mockObjectName,
						ObjectId:   1,
					},
				}
				clientMock.EXPECT().ListBucketReadRecord(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(records, int64(0), nil)
				g.baseApp.SetGfSpClient(clientMock)
				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Return(&storagetypes.BucketInfo{
					BucketName: mockBucketName, Id: sdkmath.NewUint(1), Owner: testBucketOwnerAddress,
				}, nil).Times(1)
				consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Id: 1}, nil).Times(1)
				consensusMock.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).Return(&virtualgrouptypes.GlobalVirtualGroupFamily{Id: 1, PrimarySpId: 1}, nil).Times(1)
				g.baseApp.SetConsensus(consensusMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s.%s/?%s&%s=%s&%s=%s&%s=%s", scheme, mockBucketName, testDomain, ListBucketReadRecordQuery,
					ListBucketReadRecordMaxRecordsQuery, "-1", StartTimestampUs, "10", EndTimestampUs, "2")
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				signAsAccount(req, testBucketOwnerKey)
				return req
			},
			wantedResult: "",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			router := mockListBucketReadRecordHandler(t, tt.fn())
			w := httptest.NewRecorder()
			router.ServeHTTP(w, tt.request())
			assert.Contains(t, w.Body.String(), tt.wantedResult)
		})
	}
}

func mockQueryBucketMigrationProgressRoute(t *testing.T, g *GateModular) *mux.Router {
	t.Helper()
	router := mux.NewRouter().SkipClean(true)
	var routers []*mux.Router
	routers = append(routers, router.Host("{bucket:.+}."+g.domain).Subrouter())
	routers = append(routers, router.PathPrefix("/{bucket}").Subrouter())
	for _, r := range routers {
		r.NewRoute().Name(queryMigrationProgressRouterName).Methods(http.MethodGet).HandlerFunc(g.queryBucketMigrationProgressHandler).
			Queries(GetBucketMigrationProgressQuery, "")
	}
	return router
}

func TestGateModular_queryBucketMigrationProgressHandler(t *testing.T) {
	cases := []struct {
		name         string
		fn           func() *GateModular
		request      func() *http.Request
		wantedResult string
	}{
		{
			name: "new request context error",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, mockErr).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s/%s?%s", scheme, mockBucketName, testDomain, GetBucketMigrationProgressQuery)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				return req
			},
			wantedResult: "mock error",
		},
		{
			name: "failed to verify authentication",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				clientMock.EXPECT().VerifyAuthentication(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, mockErr).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s/%s?%s", scheme, mockBucketName, testDomain, GetBucketMigrationProgressQuery)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				return req
			},
			wantedResult: "mock error",
		},
		{
			name: "no permission to operate",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				clientMock.EXPECT().VerifyAuthentication(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s/%s?%s", scheme, mockBucketName, testDomain, GetBucketMigrationProgressQuery)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				return req
			},
			wantedResult: "no permission",
		},
		{
			name: "failed to get bucket info from consensus",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				clientMock.EXPECT().VerifyAuthentication(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(true, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)

				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Return(nil, mockErr).Times(1)
				g.baseApp.SetConsensus(consensusMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s/%s?%s", scheme, mockBucketName, testDomain, GetBucketMigrationProgressQuery)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				return req
			},
			wantedResult: "failed to get bucket info from consensus",
		},
		{
			name: "failed to get bucket migrate progress",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				progress := &gfspserver.MigrateBucketProgressMeta{BucketId: 2, MigrateState: uint32(storetypes.BucketMigrationState_BUCKET_MIGRATION_STATE_MIGRATION_FINISHED)}
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				clientMock.EXPECT().VerifyAuthentication(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(true, nil).Times(1)
				clientMock.EXPECT().GetMigrateBucketProgress(gomock.Any(), gomock.Any()).Return(progress, mockErr).Times(1)
				g.baseApp.SetGfSpClient(clientMock)

				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Return(&storagetypes.BucketInfo{
					BucketName: mockBucketName, Id: sdkmath.NewUint(1),
				}, nil).Times(1)
				g.baseApp.SetConsensus(consensusMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s/%s?%s", scheme, mockBucketName, testDomain, GetBucketMigrationProgressQuery)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				return req
			},
			wantedResult: "mock error",
		},
		{
			name: "success",
			fn: func() *GateModular {
				g := setup(t)
				ctrl := gomock.NewController(t)
				progress := &gfspserver.MigrateBucketProgressMeta{BucketId: 2, MigrateState: uint32(storetypes.BucketMigrationState_BUCKET_MIGRATION_STATE_MIGRATION_FINISHED)}
				clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
				clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(false, nil).Times(1)
				clientMock.EXPECT().VerifyAuthentication(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
					gomock.Any()).Return(true, nil).Times(1)
				clientMock.EXPECT().GetMigrateBucketProgress(gomock.Any(), gomock.Any()).Return(progress, nil).Times(1)
				g.baseApp.SetGfSpClient(clientMock)

				consensusMock := consensus.NewMockConsensus(ctrl)
				consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Return(&storagetypes.BucketInfo{
					BucketName: mockBucketName, Id: sdkmath.NewUint(1),
				}, nil).Times(1)
				g.baseApp.SetConsensus(consensusMock)
				return g
			},
			request: func() *http.Request {
				path := fmt.Sprintf("%s%s/%s?%s", scheme, mockBucketName, testDomain, GetBucketMigrationProgressQuery)
				req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
				validExpiryDateStr := time.Now().Add(time.Hour * 60).Format(ExpiryDateFormat)
				req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, validExpiryDateStr)
				req.Header.Set(GnfdAuthorizationHeader, "GNFD1-EDDSA,Signature=48656c6c6f20476f7068657221")
				return req
			},
			wantedResult: "",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			router := mockQueryBucketMigrationProgressRoute(t, tt.fn())
			w := httptest.NewRecorder()
			router.ServeHTTP(w, tt.request())
			assert.Contains(t, w.Body.String(), tt.wantedResult)
		})
	}
}

// signAsRandomAccount signs the request with a freshly generated account so that it
// passes gateway authentication. These handlers authenticate but do not restrict which
// account may call them, so the identity itself does not matter to these cases.
func signAsRandomAccount(req *http.Request) *http.Request {
	accountKey, err := crypto.GenerateKey()
	if err != nil {
		panic(err)
	}
	signature, err := crypto.Sign(commonhttp.GetMsgToSignInGNFD1Auth(req), accountKey)
	if err != nil {
		panic(err)
	}
	req.Header.Set(GnfdAuthorizationHeader, commonhttp.Gnfd1Ecdsa+",Signature="+hex.EncodeToString(signature))
	return req
}

func mockListBucketReadRecordRoute(t *testing.T, g *GateModular) *mux.Router {
	t.Helper()
	router := mux.NewRouter().SkipClean(true)
	var routers []*mux.Router
	routers = append(routers, router.Host("{bucket:.+}."+g.domain).Subrouter())
	routers = append(routers, router.PathPrefix("/{bucket}").Subrouter())
	for _, r := range routers {
		r.NewRoute().Name(listBucketReadRecordRouterName).Methods(http.MethodGet).HandlerFunc(g.listBucketReadRecordHandler).
			Queries(ListBucketReadRecordQuery, "", ListBucketReadRecordMaxRecordsQuery, "{max_records}",
				StartTimestampUs, "{start_ts}", EndTimestampUs, "{end_ts}")
	}
	return router
}

func TestGateModular_getBucketReadQuotaHandlerRejectsUnsignedRequest(t *testing.T) {
	g := setup(t)
	ctrl := gomock.NewController(t)
	consensusMock := consensus.NewMockConsensus(ctrl)
	consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Times(0)
	g.baseApp.SetConsensus(consensusMock)

	path := fmt.Sprintf("%s%s.%s/?%s&%s", scheme, mockBucketName, testDomain, GetBucketReadQuotaQuery,
		GetBucketReadQuotaMonthQuery)
	req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))

	w := httptest.NewRecorder()
	mockGetBucketReadQuotaRoute(t, g).ServeHTTP(w, req)

	assert.NotEqual(t, http.StatusOK, w.Code,
		"the quota of a bucket must not be readable without a verified signature")
}

func TestGateModular_listBucketReadRecordHandlerRejectsUnsignedRequest(t *testing.T) {
	g := setup(t)
	ctrl := gomock.NewController(t)
	consensusMock := consensus.NewMockConsensus(ctrl)
	consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Times(0)
	g.baseApp.SetConsensus(consensusMock)

	path := fmt.Sprintf("%s%s.%s/?%s&%s=1&%s=1&%s=2", scheme, mockBucketName, testDomain, ListBucketReadRecordQuery,
		ListBucketReadRecordMaxRecordsQuery, StartTimestampUs, EndTimestampUs)
	req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))

	w := httptest.NewRecorder()
	mockListBucketReadRecordRoute(t, g).ServeHTTP(w, req)

	assert.NotEqual(t, http.StatusOK, w.Code,
		"the read records of a bucket must not be readable without a verified signature")
}

// signAsAccount signs the request with the given account key, so the caller
// authenticates as a specific address rather than an arbitrary one.
func signAsAccount(req *http.Request, accountKey *ecdsa.PrivateKey) *http.Request {
	signature, err := crypto.Sign(commonhttp.GetMsgToSignInGNFD1Auth(req), accountKey)
	if err != nil {
		panic(err)
	}
	req.Header.Set(GnfdAuthorizationHeader, commonhttp.Gnfd1Ecdsa+",Signature="+hex.EncodeToString(signature))
	return req
}

func newListReadRecordRequest() *http.Request {
	path := fmt.Sprintf("%s%s.%s/?%s&%s=1&%s=1&%s=2", scheme, mockBucketName, testDomain, ListBucketReadRecordQuery,
		ListBucketReadRecordMaxRecordsQuery, StartTimestampUs, EndTimestampUs)
	req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
	req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, time.Now().Add(time.Hour).Format(ExpiryDateFormat))
	return req
}

// setupReadRecordChain makes the bucket resolve to this SP and be owned by owner.
func setupReadRecordChain(t *testing.T, g *GateModular, ctrl *gomock.Controller, owner string) *gfspclient.MockGfSpClientAPI {
	t.Helper()
	consensusMock := consensus.NewMockConsensus(ctrl)
	consensusMock.EXPECT().QueryBucketInfo(gomock.Any(), gomock.Any()).Return(&storagetypes.BucketInfo{
		BucketName: mockBucketName, Id: sdkmath.NewUint(1), Owner: owner,
	}, nil).AnyTimes()
	consensusMock.EXPECT().QuerySP(gomock.Any(), gomock.Any()).Return(&sptypes.StorageProvider{Id: 1}, nil).AnyTimes()
	consensusMock.EXPECT().QueryVirtualGroupFamily(gomock.Any(), gomock.Any()).
		Return(&virtualgrouptypes.GlobalVirtualGroupFamily{Id: 1, PrimarySpId: 1}, nil).AnyTimes()
	g.baseApp.SetConsensus(consensusMock)

	clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
	g.baseApp.SetGfSpClient(clientMock)
	return clientMock
}

func TestGateModular_listBucketReadRecordHandlerRefusesANonOwner(t *testing.T) {
	g := setup(t)
	ctrl := gomock.NewController(t)

	strangerKey, err := crypto.GenerateKey()
	assert.NoError(t, err)

	clientMock := setupReadRecordChain(t, g, ctrl, testAccount)
	clientMock.EXPECT().ListBucketReadRecord(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	w := httptest.NewRecorder()
	mockListBucketReadRecordRoute(t, g).ServeHTTP(w, signAsAccount(newListReadRecordRequest(), strangerKey))

	assert.Equal(t, http.StatusUnauthorized, w.Code,
		"read records name every account that downloaded from the bucket, only the owner may read them")
}

func TestGateModular_listBucketReadRecordHandlerAllowsTheOwner(t *testing.T) {
	g := setup(t)
	ctrl := gomock.NewController(t)

	ownerKey, err := crypto.GenerateKey()
	assert.NoError(t, err)
	owner := crypto.PubkeyToAddress(ownerKey.PublicKey).Hex()

	clientMock := setupReadRecordChain(t, g, ctrl, owner)
	clientMock.EXPECT().ListBucketReadRecord(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]*metadatatypes.ReadRecord{}, int64(0), nil).Times(1)

	w := httptest.NewRecorder()
	mockListBucketReadRecordRoute(t, g).ServeHTTP(w, signAsAccount(newListReadRecordRequest(), ownerKey))

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestGateModular_listBucketReadRecordHandlerAllowsTheOwnerInAnyAddressCase(t *testing.T) {
	g := setup(t)
	ctrl := gomock.NewController(t)

	ownerKey, err := crypto.GenerateKey()
	assert.NoError(t, err)
	owner := strings.ToLower(crypto.PubkeyToAddress(ownerKey.PublicKey).Hex())

	clientMock := setupReadRecordChain(t, g, ctrl, owner)
	clientMock.EXPECT().ListBucketReadRecord(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]*metadatatypes.ReadRecord{}, int64(0), nil).Times(1)

	w := httptest.NewRecorder()
	mockListBucketReadRecordRoute(t, g).ServeHTTP(w, signAsAccount(newListReadRecordRequest(), ownerKey))

	assert.Equal(t, http.StatusOK, w.Code)
}

func mockListBucketReadQuotaRoute(t *testing.T, g *GateModular) *mux.Router {
	t.Helper()
	router := mux.NewRouter().SkipClean(true)
	router.Path("/").Name(listBucketReadQuotaRouterName).Methods(http.MethodGet).
		Queries(ListBucketReadRecordQuery, "").HandlerFunc(g.listBucketReadQuotaHandler)
	return router
}

func mockGetBucketReadQuotaCountRoute(t *testing.T, g *GateModular) *mux.Router {
	t.Helper()
	router := mux.NewRouter().SkipClean(true)
	router.Path("/").Name(getBucketReadQuotaCountRouterName).Methods(http.MethodGet).
		Queries(ListBucketReadCountQuery, "").HandlerFunc(g.getBucketReadQuotaCountHandler)
	return router
}

// TestGateModular_listBucketReadQuotaHandlerRejectsUnsignedRequest pins that the
// handler builds a request context, the step that verifies the signature, before it
// reads anything. It is the sibling of the two endpoints fixed in MOCA-873 / #87 and
// carries the same quota consumption data.
func TestGateModular_listBucketReadQuotaHandlerRejectsUnsignedRequest(t *testing.T) {
	g := setup(t)
	ctrl := gomock.NewController(t)
	clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
	clientMock.EXPECT().ListBucketReadQuota(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
	g.baseApp.SetGfSpClient(clientMock)

	path := fmt.Sprintf("%s%s/?%s&offset=0&limit=1", scheme, testDomain, ListBucketReadRecordQuery)
	req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))

	w := httptest.NewRecorder()
	mockListBucketReadQuotaRoute(t, g).ServeHTTP(w, req)

	assert.NotEqual(t, http.StatusOK, w.Code,
		"the bucket read quota list must not be readable without a verified signature")
}

// TestGateModular_getBucketReadQuotaCountHandlerRejectsUnsignedRequest pins the same
// invariant for the count endpoint.
func TestGateModular_getBucketReadQuotaCountHandlerRejectsUnsignedRequest(t *testing.T) {
	g := setup(t)
	ctrl := gomock.NewController(t)
	clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
	clientMock.EXPECT().GetBucketReadQuotaCount(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
	g.baseApp.SetGfSpClient(clientMock)

	path := fmt.Sprintf("%s%s/?%s", scheme, testDomain, ListBucketReadCountQuery)
	req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))

	w := httptest.NewRecorder()
	mockGetBucketReadQuotaCountRoute(t, g).ServeHTTP(w, req)

	assert.NotEqual(t, http.StatusOK, w.Code,
		"the bucket read quota count must not be readable without a verified signature")
}
