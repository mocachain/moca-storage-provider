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
	"github.com/mocachain/moca-storage-provider/core/spdb"
)

func replayedRequest(expiry time.Time) *http.Request {
	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("%s%s.%s/%s", scheme, mockBucketName, testDomain, mockObjectName), nil)
	req.Header.Set(GnfdUserAddressHeader, testAccount)
	req.Header.Set(GnfdOffChainAuthAppDomainHeader, SampleDAppDomain)
	req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, expiry.UTC().Format(ExpiryDateFormat))
	req.Header.Set(commonhttp.HTTPHeaderNonce, validAuthNonce)
	req.Header.Set(GnfdAuthorizationHeader, commonhttp.Gnfd1Eddsa+",Signature=48656c6c6f20476f7068657221")
	return req
}

func routeAs(g *GateModular, req *http.Request, fn func(*http.Request)) {
	router := mux.NewRouter().SkipClean(true)
	router.Host("{bucket:.+}." + g.domain).Subrouter().
		NewRoute().Name(getObjectRouterName).Methods(http.MethodGet).Path("/{object:.+}").
		HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { fn(r) })
	router.ServeHTTP(httptest.NewRecorder(), req)
}

func TestNewRequestContextRejectsReplayedOffChainSignature(t *testing.T) {
	g := setup(t)
	ctrl := gomock.NewController(t)
	client := gfspclient.NewMockGfSpClientAPI(ctrl)
	client.EXPECT().VerifyGNFD1EddsaSignature(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(true, nil).Times(2)
	g.baseApp.SetGfSpClient(client)
	db := spdb.NewMockSPDB(ctrl)
	gomock.InOrder(
		db.EXPECT().ClaimAuthNonce(gomock.Any(), validAuthNonce, gomock.Any()).Return(true, nil),
		db.EXPECT().ClaimAuthNonce(gomock.Any(), validAuthNonce, gomock.Any()).Return(false, nil),
	)
	g.baseApp.SetGfSpDB(db)

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

	require.NoError(t, firstErr)
	require.NotEmpty(t, firstAccount)
	assert.ErrorIs(t, replayErr, ErrReusedAuthRequest)
}
