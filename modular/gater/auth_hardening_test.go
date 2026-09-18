package gater

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	commonhttp "github.com/mocachain/moca-common/go/http"
	"github.com/mocachain/moca-storage-provider/core/spdb"
)

const validAuthNonce = "00112233445566778899aabbccddeeff"

func authNonceRequest(nonce string) *http.Request {
	req, _ := http.NewRequest(http.MethodGet, "https://sp.example/x", nil)
	req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, time.Now().Add(10*time.Minute).UTC().Format(ExpiryDateFormat))
	if nonce != "" {
		req.Header.Set(commonhttp.HTTPHeaderNonce, nonce)
	}
	return req
}

func authNonceRequestContext(t *testing.T, nonce string) (*RequestContext, *spdb.MockSPDB) {
	t.Helper()
	ctrl := gomock.NewController(t)
	db := spdb.NewMockSPDB(ctrl)
	g := setup(t)
	g.baseApp.SetGfSpDB(db)
	return &RequestContext{
		g:       g,
		request: authNonceRequest(nonce),
		account: "0xAbCd",
	}, db
}

func TestEnforceSingleUseClaimsValidLowercaseNonce(t *testing.T) {
	reqCtx, db := authNonceRequestContext(t, validAuthNonce)
	db.EXPECT().ClaimAuthNonce("0xAbCd", validAuthNonce, gomock.Any()).Return(true, nil)

	require.NoError(t, reqCtx.enforceSingleUse())
}

func TestEnforceSingleUseClaimsValidUppercaseNonce(t *testing.T) {
	nonce := "00112233445566778899AABBCCDDEEFF"
	reqCtx, db := authNonceRequestContext(t, nonce)
	db.EXPECT().ClaimAuthNonce("0xAbCd", nonce, gomock.Any()).Return(true, nil)

	require.NoError(t, reqCtx.enforceSingleUse())
}

func TestEnforceSingleUseRejectsMalformedNonceSeparatelyFromReplay(t *testing.T) {
	for _, nonce := range []string{
		"abc",
		"00112233445566778899aabbccddeef",
		"00112233445566778899aabbccddeeff0",
		"00112233445566778899aabbccddeefg",
	} {
		t.Run(nonce, func(t *testing.T) {
			reqCtx, _ := authNonceRequestContext(t, nonce)
			err := reqCtx.enforceSingleUse()
			assert.ErrorIs(t, err, ErrInvalidAuthNonce)
			assert.NotErrorIs(t, err, ErrReusedAuthRequest)
		})
	}
}

func TestEnforceSingleUseAllowsNonceLessOptionalMode(t *testing.T) {
	g := setup(t)
	reqCtx := &RequestContext{g: g, request: authNonceRequest(""), account: "0xAbCd"}

	require.NoError(t, reqCtx.enforceSingleUse())
}

func TestEnforceSingleUseRejectsNonceLessRequiredMode(t *testing.T) {
	g := setup(t)
	g.requireAuthNonce = true
	reqCtx := &RequestContext{g: g, request: authNonceRequest(""), account: "0xAbCd"}

	assert.ErrorIs(t, reqCtx.enforceSingleUse(), ErrMissingAuthNonce)
}

func TestEnforceSingleUseRejectsDuplicateClaim(t *testing.T) {
	reqCtx, db := authNonceRequestContext(t, validAuthNonce)
	db.EXPECT().ClaimAuthNonce("0xAbCd", validAuthNonce, gomock.Any()).Return(false, nil)

	assert.ErrorIs(t, reqCtx.enforceSingleUse(), ErrReusedAuthRequest)
}

func TestEnforceSingleUseFailsClosedOnStoreError(t *testing.T) {
	reqCtx, db := authNonceRequestContext(t, validAuthNonce)
	db.EXPECT().ClaimAuthNonce("0xAbCd", validAuthNonce, gomock.Any()).Return(false, errors.New("database unavailable"))

	assert.ErrorIs(t, reqCtx.enforceSingleUse(), ErrAuthNonceStore)
}

func TestCleanupAuthNoncesRunsUntilContextCancellation(t *testing.T) {
	ctrl := gomock.NewController(t)
	db := spdb.NewMockSPDB(ctrl)
	g := setup(t)
	g.baseApp.SetGfSpDB(db)
	ctx, cancel := context.WithCancel(context.Background())
	db.EXPECT().DeleteExpiredAuthNonces(gomock.Any()).DoAndReturn(func(time.Time) error {
		cancel()
		return nil
	})

	g.cleanupAuthNonces(ctx, time.Millisecond)
}
