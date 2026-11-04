package gater

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"

	commonhttp "github.com/mocachain/moca-common/go/http"
	"github.com/mocachain/moca-storage-provider/base/gfspclient"
	"github.com/mocachain/moca-storage-provider/core/module"
	"github.com/mocachain/moca-storage-provider/core/spdb"
	permissiontypes "github.com/mocachain/moca/v2/x/permission/types"
)

// memNonceStore keeps nonce claims in memory with the semantics of
// SpDBImpl.ClaimAuthNonce and DeleteExpiredAuthNonces, so a test can run the
// hourly cleanup between requests. Other SPDB calls fail on the embedded mock.
type memNonceStore struct {
	spdb.SPDB
	mu     sync.Mutex
	claims map[string]int64 // normalized account|nonce -> expiry in Unix seconds
}

func newMemNonceStore(ctrl *gomock.Controller) *memNonceStore {
	return &memNonceStore{SPDB: spdb.NewMockSPDB(ctrl), claims: map[string]int64{}}
}

func (s *memNonceStore) ClaimAuthNonce(account, nonce string, expiresAt time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(strings.TrimSpace(account)) + "|" + strings.ToLower(strings.TrimSpace(nonce))
	// like the upsert, a live claim stays and an expired one is replaced only
	// by a different expiry, since MySQL reports an unchanged row as unaffected
	if old, ok := s.claims[key]; ok && (old >= time.Now().Unix() || old == expiresAt.Unix()) {
		return false, nil
	}
	s.claims[key] = expiresAt.Unix()
	return true, nil
}

func (s *memNonceStore) DeleteExpiredAuthNonces(expiredBefore time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, expiry := range s.claims {
		if expiry < expiredBefore.Unix() {
			delete(s.claims, key)
		}
	}
	return nil
}

func TestGetObjectPresignedNonce(t *testing.T) {
	const expiryError = "expiry date is expected to be within"
	cases := []struct {
		name           string
		nonce          string
		queryExpiryIn  time.Duration // zero means one hour
		headerExpiryIn time.Duration // zero leaves the header unset
		headerNonces   []string      // one request each, "" sends no header nonce
		requireNonce   bool
		cleanup        bool // delete expired claims after each request
		wantAuthCalls  int
		wantErrors     []string
		wantClaim      bool // the query nonce is claimed until the query expiry
	}{
		{
			name:  "fresh query nonce then replay",
			nonce: validAuthNonce, headerNonces: []string{"", ""},
			wantAuthCalls: 1, wantErrors: []string{"nonce gate passed", "already used"}, wantClaim: true,
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
			name:  "header cannot replace signed query nonce",
			nonce: validAuthNonce, headerExpiryIn: -time.Hour,
			headerNonces:  []string{"11112222333344445555666677778888", "88887777666655554444333322221111"},
			wantAuthCalls: 1, wantErrors: []string{"nonce gate passed", "already used"}, wantClaim: true,
		},
		{
			name:  "expired query expiry claims nothing before or after cleanup",
			nonce: validAuthNonce, queryExpiryIn: -time.Hour, headerNonces: []string{"", ""}, cleanup: true,
			wantErrors: []string{expiryError, expiryError},
		},
		{
			name:  "fresh header expiry cannot extend an expired query expiry",
			nonce: validAuthNonce, queryExpiryIn: -time.Hour, headerExpiryIn: time.Hour, headerNonces: []string{""},
			wantErrors: []string{expiryError},
		},
		{
			name:  "query expiry beyond the accepted window",
			nonce: validAuthNonce, queryExpiryIn: 8 * 24 * time.Hour, headerNonces: []string{""},
			wantErrors: []string{expiryError},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := setup(t)
			g.requireAuthNonce = tc.requireNonce
			ctrl := gomock.NewController(t)
			queryExpiryIn := tc.queryExpiryIn
			if queryExpiryIn == 0 {
				queryExpiryIn = time.Hour
			}
			queryExpiry := time.Now().Add(queryExpiryIn).UTC().Truncate(time.Second)
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
			store := newMemNonceStore(ctrl)
			g.baseApp.SetGfSpDB(store)
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
				}
				if tc.headerExpiryIn != 0 {
					req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, time.Now().Add(tc.headerExpiryIn).UTC().Format(ExpiryDateFormat))
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, req)
				assert.Contains(t, response.Body.String(), tc.wantErrors[i])
				if tc.cleanup {
					require.NoError(t, store.DeleteExpiredAuthNonces(time.Now()))
				}
			}
			assert.Equal(t, tc.wantAuthCalls, authCalls)
			wantClaims := map[string]int64{}
			if tc.wantClaim {
				wantClaims[strings.ToLower(testAccount)+"|"+tc.nonce] = queryExpiry.Unix()
			}
			assert.Equal(t, wantClaims, store.claims)
		})
	}
}
