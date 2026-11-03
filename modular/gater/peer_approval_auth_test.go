package gater

import (
	"crypto/ecdsa"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
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

func signedPeerApprovalRequest(t *testing.T, path string, key *ecdsa.PrivateKey, expiry time.Time) *http.Request {
	t.Helper()
	if path == SecondarySPMigrationBucketApprovalPath {
		return signedSecondaryBlsMigrationRequest(t, key, expiry)
	}
	req := httptest.NewRequest(http.MethodGet, "https://"+testDomain+path, nil)
	req.Header.Set(GnfdUnsignedApprovalMsgHeader, mockSwapOutMsgHeader)
	req.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, expiry.UTC().Format(ExpiryDateFormat))
	sig, err := crypto.Sign(commonhttp.GetMsgToSignInGNFD1Auth(req), key)
	require.NoError(t, err)
	req.Header.Set(GnfdAuthorizationHeader, commonhttp.Gnfd1Ecdsa+",Signature="+hex.EncodeToString(sig))
	return req
}

func TestAuthenticatePeerApprovalRequestECDSA(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	operator := crypto.PubkeyToAddress(key.PublicKey).Hex()
	for _, mode := range []string{"permissive", "required"} {
		for _, path := range []string{SecondarySPMigrationBucketApprovalPath, SwapOutApprovalPath} {
			for _, tc := range []struct {
				name             string
				lifetime         time.Duration
				expectedOperator string
				wantErr          error
			}{
				{"valid operator", time.Minute, operator, nil},
				{"wrong operator", time.Minute, mockSwapOutSPAddress, ErrNoPermission},
				{"expired", -time.Minute, operator, ErrInvalidExpiryDateHeader},
				{"receiver clock behind", 5*time.Minute + 15*time.Second, operator, nil},
				{"excessive future expiry", 6 * time.Minute, operator, ErrInvalidExpiryDateHeader},
			} {
				t.Run(mode+path+"/"+tc.name, func(t *testing.T) {
					req := signedPeerApprovalRequest(t, path, key, time.Now().Add(tc.lifetime))
					reqCtx := &RequestContext{request: req}
					// Verify the real signature independently of expiry, so failures below
					// exercise the peer approval checks rather than malformed authentication.
					account, verifyErr := reqCtx.VerifySignature()
					require.NoError(t, verifyErr)
					require.True(t, strings.EqualFold(operator, account))
					reqCtx.account = account
					g := &GateModular{peerApprovalAuthMode: mode}
					assert.Equal(t, tc.wantErr, g.authenticatePeerApprovalRequest(req, reqCtx, nil, tc.expectedOperator))
				})
			}
		}
	}
}

func TestPeerApprovalHandlersRejectWrongOperatorAndExpiredRequest(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	require.NotEqual(t, mockSwapOutSPAddress, crypto.PubkeyToAddress(key.PublicKey).Hex())
	for _, mode := range []string{"permissive", "required"} {
		for _, path := range []string{SecondarySPMigrationBucketApprovalPath, SwapOutApprovalPath} {
			for _, tc := range []struct {
				name     string
				lifetime time.Duration
				status   int
				message  string
			}{
				{"wrong operator", time.Minute, http.StatusUnauthorized, "no permission"},
				{"expired", -time.Minute, http.StatusBadRequest, "header is incorrect"},
			} {
				t.Run(mode+path+"/"+tc.name, func(t *testing.T) {
					g := setup(t)
					g.peerApprovalAuthMode = mode
					ctrl := gomock.NewController(t)
					// No signer calls are allowed for either rejection.
					g.baseApp.SetGfSpClient(gfspclient.NewMockGfSpClientAPI(ctrl))
					chain := consensus.NewMockConsensus(ctrl)
					sp := &sptypes.StorageProvider{Id: mockSelfSPID, OperatorAddress: mockSwapOutSPAddress}
					if path == SecondarySPMigrationBucketApprovalPath {
						chain.EXPECT().QuerySPByID(gomock.Any(), mockSelfSPID).Return(sp, nil)
					} else {
						chain.EXPECT().QuerySP(gomock.Any(), mockSwapOutSPAddress).Return(sp, nil)
					}
					g.baseApp.SetConsensus(chain)
					req := signedPeerApprovalRequest(t, path, key, time.Now().Add(tc.lifetime))
					w := httptest.NewRecorder()
					if path == SecondarySPMigrationBucketApprovalPath {
						mockGetSecondaryBlsMigrationBucketApprovalHandlerRoute(t, g).ServeHTTP(w, req)
					} else {
						mockGetSwapOutApprovalRoute(t, g).ServeHTTP(w, req)
					}
					assert.Equal(t, tc.status, w.Code)
					assert.Contains(t, w.Body.String(), tc.message)
				})
			}
		}
	}
}
