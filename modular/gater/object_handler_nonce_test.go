package gater

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"

	commonhttp "github.com/mocachain/moca-common/go/http"
	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/core/module"
	"github.com/mocachain/moca-storage-provider/core/spdb"
	permissiontypes "github.com/mocachain/moca/v2/x/permission/types"
)

func TestGetObjectPresignedNonce(t *testing.T) {
	cases := []struct {
		name          string
		nonce         string
		headerNonces  []string
		requireNonce  bool
		claims        []bool
		wantAuthCalls int
		wantErrors    []string
	}{
		{
			name:  "fresh query nonce then replay",
			nonce: validAuthNonce, headerNonces: []string{"", ""}, claims: []bool{true, false},
			wantAuthCalls: 1, wantErrors: []string{"nonce gate passed", "already used"},
		},
		{
			name:  "malformed query nonce",
			nonce: "not-a-nonce", headerNonces: []string{""},
			wantErrors: []string{"must be exactly 16 random bytes"},
		},
		{
			name:         "nonce-less URL remains reusable when nonce required",
			headerNonces: []string{"", ""}, requireNonce: true,
			wantAuthCalls: 2, wantErrors: []string{"nonce gate passed", "nonce gate passed"},
		},
		{
			name:         "header cannot replace signed query nonce",
			nonce:        validAuthNonce,
			headerNonces: []string{"11112222333344445555666677778888", "88887777666655554444333322221111"},
			claims:       []bool{true, false}, wantAuthCalls: 1,
			wantErrors: []string{"nonce gate passed", "already used"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := setup(t)
			g.requireAuthNonce = tc.requireNonce
			ctrl := gomock.NewController(t)
			queryExpiry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
			client := gfspclient.NewMockGfSpClientAPI(ctrl)
			effect := permissiontypes.EFFECT_DENY
			client.EXPECT().VerifyPermission(gomock.Any(), gomock.Any(), mockBucketName, mockObjectName, gomock.Any()).
				Return(&effect, nil).Times(len(tc.headerNonces))
			client.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), testAccount, SampleDAppDomain, gomock.Any(), gomock.Any()).
				Return(true, nil).Times(len(tc.headerNonces))
			authCalls := 0
			client.EXPECT().VerifyAuthentication(gomock.Any(), gomock.Any(), gomock.Any(), mockBucketName, mockObjectName).
				DoAndReturn(func(_ context.Context, _ module.AuthOpType, _ string, _ string, _ string, _ ...grpc.DialOption) (bool, error) {
					authCalls++
					return false, errors.New("nonce gate passed")
				}).AnyTimes()
			g.baseApp.SetGfSpClient(client)
			db := spdb.NewMockSPDB(ctrl)
			var calls []any
			for _, claimed := range tc.claims {
				calls = append(calls, db.EXPECT().ClaimAuthNonce(gomock.Any(), tc.nonce, queryExpiry).Return(claimed, nil))
			}
			gomock.InOrder(calls...)
			g.baseApp.SetGfSpDB(db)
			router := mux.NewRouter().SkipClean(true)
			router.Host("{bucket:.+}." + g.domain).Subrouter().NewRoute().Name(getObjectRouterName).
				Methods(http.MethodGet).Path("/{object:.+}").HandlerFunc(g.getObjectHandler)
			for i, headerNonce := range tc.headerNonces {
				query := url.Values{
					GnfdUserAddressHeader: {testAccount}, GnfdOffChainAuthAppDomainHeader: {SampleDAppDomain},
					GnfdAuthorizationHeader:              {commonhttp.Gnfd1Eddsa + ",Signature=48656c6c6f20476f7068657221"},
					commonhttp.HTTPHeaderExpiryTimestamp: {queryExpiry.Format(ExpiryDateFormat)},
				}
				if tc.nonce != "" {
					query.Set(commonhttp.HTTPHeaderNonce, tc.nonce)
				}
				req := httptest.NewRequest(http.MethodGet,
					fmt.Sprintf("%s%s.%s/%s?%s", scheme, mockBucketName, testDomain, mockObjectName, query.Encode()), nil)
				if headerNonce != "" {
					req.Header.Set(commonhttp.HTTPHeaderNonce, headerNonce)
					req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, time.Now().Add(-time.Hour).UTC().Format(ExpiryDateFormat))
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, req)
				assert.Contains(t, response.Body.String(), tc.wantErrors[i])
			}
			assert.Equal(t, tc.wantAuthCalls, authCalls)
		})
	}
}
