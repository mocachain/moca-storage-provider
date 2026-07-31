package gater

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	commonhttp "github.com/mocachain/moca-common/go/http"
	"github.com/mocachain/moca-storage-provider/base/gfspclient"
)

// replayedRequest returns a captured, already-signed off-chain-auth request. The bytes
// are identical on every call, which is exactly what an attacker who observed one
// request would send.
func replayedRequest(expiry time.Time) *http.Request {
	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("%s%s.%s/%s", scheme, mockBucketName, testDomain, mockObjectName), nil)
	req.Header.Set(GnfdUserAddressHeader, testAccount)
	req.Header.Set(GnfdOffChainAuthAppDomainHeader, SampleDAppDomain)
	req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, expiry.UTC().Format(ExpiryDateFormat))
	req.Header.Set(GnfdAuthorizationHeader, commonhttp.Gnfd1Eddsa+",Signature=48656c6c6f20476f7068657221")
	return req
}

// routeAs runs the request through mux so that NewRequestContext resolves a route name
// that is not in skipAuthRouterNames, then hands the request to fn.
func routeAs(g *GateModular, req *http.Request, fn func(*http.Request)) {
	router := mux.NewRouter().SkipClean(true)
	router.Host("{bucket:.+}." + g.domain).Subrouter().
		NewRoute().Name(getObjectRouterName).Methods(http.MethodGet).Path("/{object:.+}").
		HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { fn(r) })
	router.ServeHTTP(httptest.NewRecorder(), req)
}

// TestNewRequestContext_RejectsAReplayedOffChainSignature reproduces F-083.
//
// The off-chain EDDSA signature covers the request and an expiry timestamp, and nothing
// else. It carries no nonce and the gateway keeps no record of the signatures it has
// already honoured, so a captured request authenticates again, unchanged, for as long
// as its expiry allows — which CheckIfSigExpiry permits to be up to MaxExpiryAgeInSec,
// seven days. A signed request is therefore a bearer token, not a one-time proof.
//
// This test FAILS on purpose. See the PR for why the fix is blocked.
func TestNewRequestContext_RejectsAReplayedOffChainSignature(t *testing.T) {
	g := setup(t)
	ctrl := gomock.NewController(t)
	clientMock := gfspclient.NewMockGfSpClientAPI(ctrl)
	clientMock.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(true, nil).AnyTimes()
	g.baseApp.SetGfSpClient(clientMock)

	expiry := time.Now().Add(6 * 24 * time.Hour)

	var firstErr, replayErr error
	var firstAccount string
	routeAs(g, replayedRequest(expiry), func(r *http.Request) {
		reqCtx, err := NewRequestContext(r, g)
		firstErr, firstAccount = err, reqCtx.Account()
	})
	routeAs(g, replayedRequest(expiry), func(r *http.Request) {
		_, replayErr = NewRequestContext(r, g)
	})

	require.NoError(t, firstErr, "the original request must authenticate")
	require.NotEmpty(t, firstAccount)

	assert.Error(t, replayErr,
		"a byte-identical captured request must not authenticate a second time")
}

// TestCheckIfSigExpiry_AcceptsASevenDayWindow documents how long a captured signature
// stays usable. It passes today; it is here to record the size of the window the test
// above is about, so that a change to MaxExpiryAgeInSec is a deliberate one.
func TestCheckIfSigExpiry_AcceptsASevenDayWindow(t *testing.T) {
	assert.Equal(t, int32(7*24*60*60), MaxExpiryAgeInSec)

	g := setup(t)
	reqCtx := &RequestContext{g: g, request: replayedRequest(time.Now().Add(6*24*time.Hour + 23*time.Hour))}
	assert.NoError(t, reqCtx.CheckIfSigExpiry(),
		"a signature issued now stays replayable for very nearly a week")
}
