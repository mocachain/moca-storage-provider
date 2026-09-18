package gater

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	commonhttp "github.com/mocachain/moca-common/go/http"
)

const peerApprovalMaxExpiry = 5 * time.Minute

func (g *GateModular) authenticatePeerApprovalRequest(r *http.Request, reqCtx *RequestContext, authErr error, expectedOperator string) error {
	mode := g.peerApprovalAuthMode
	if mode == "" {
		mode = "disabled"
	}
	if mode == "disabled" {
		return nil
	}
	authorization := r.Header.Get(commonhttp.HTTPHeaderAuthorization)
	expiryHeader := r.Header.Get(commonhttp.HTTPHeaderExpiryTimestamp)
	if mode == "permissive" && authorization == "" && expiryHeader == "" {
		return nil
	}
	if authorization != "" && !strings.HasPrefix(authorization, commonhttp.Gnfd1Ecdsa+",") {
		return ErrUnsupportedSignType
	}
	if authErr != nil {
		return authErr
	}
	expiry, err := time.Parse(ExpiryDateFormat, r.Header.Get(commonhttp.HTTPHeaderExpiryTimestamp))
	if err != nil || time.Until(expiry) <= 0 || time.Until(expiry) > peerApprovalMaxExpiry {
		return ErrInvalidExpiryDateHeader
	}
	if !strings.EqualFold(reqCtx.Account(), expectedOperator) {
		return ErrNoPermission
	}
	return nil
}

func (g *GateModular) peerApprovalOperatorByID(ctx *RequestContext, spID uint32) (string, error) {
	sp, err := g.baseApp.Consensus().QuerySPByID(ctx.Context(), spID)
	if err != nil || sp == nil || sp.GetOperatorAddress() == "" {
		return "", ErrConsensusWithDetail(fmt.Sprintf("failed to query peer sp %d, error: %v", spID, err))
	}
	return sp.GetOperatorAddress(), nil
}

func (g *GateModular) peerApprovalSwapOutOperator(ctx *RequestContext, operator string) (string, error) {
	sp, err := g.baseApp.Consensus().QuerySP(ctx.Context(), operator)
	if err != nil || sp == nil || !strings.EqualFold(sp.GetOperatorAddress(), operator) {
		return "", ErrConsensusWithDetail(fmt.Sprintf("failed to query exiting sp %s, error: %v", operator, err))
	}
	return sp.GetOperatorAddress(), nil
}
