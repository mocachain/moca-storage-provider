package gater

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonhttp "github.com/mocachain/moca-common/go/http"
)

func TestNonceCacheHonorsEachKeyOnce(t *testing.T) {
	c := newNonceCache(100)
	expiry := time.Now().Add(time.Hour).Unix()
	assert.True(t, c.checkAndStore("acct|n1", expiry))
	assert.False(t, c.checkAndStore("acct|n1", expiry))
	assert.True(t, c.checkAndStore("acct|n2", expiry))
}

func TestNonceCacheForgetsExpiredEntries(t *testing.T) {
	c := newNonceCache(100)
	assert.True(t, c.checkAndStore("acct|n1", time.Now().Add(-time.Minute).Unix()))
	assert.True(t, c.checkAndStore("acct|n1", time.Now().Add(time.Hour).Unix()))
}

func TestNonceCacheEvictsWhenFull(t *testing.T) {
	c := newNonceCache(10)
	expiry := time.Now().Add(time.Hour).Unix()
	for i := 0; i < 25; i++ {
		assert.True(t, c.checkAndStore(strings.Repeat("k", i+1), expiry))
	}
	assert.LessOrEqual(t, len(c.entries), 25)
}

func hardenedRequestContext(g *GateModular, req *http.Request) *RequestContext {
	return &RequestContext{g: g, request: req, account: "0xAbCd"}
}

func TestEnforceSingleUseRejectsReplayedRequest(t *testing.T) {
	g := &GateModular{authNonces: newNonceCache(100)}
	expiry := time.Now().Add(10 * time.Minute).UTC().Format(ExpiryDateFormat)

	req1, _ := http.NewRequest(http.MethodGet, "https://sp/x", nil)
	req1.Header.Set(commonhttp.HTTPHeaderNonce, "abc")
	req1.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, expiry)
	require.NoError(t, hardenedRequestContext(g, req1).enforceSingleUse())

	req2, _ := http.NewRequest(http.MethodGet, "https://sp/x", nil)
	req2.Header.Set(commonhttp.HTTPHeaderNonce, "abc")
	req2.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, expiry)
	assert.ErrorIs(t, hardenedRequestContext(g, req2).enforceSingleUse(), ErrReusedAuthRequest)
}

func TestEnforceSingleUsePassesWithoutNonceUntilRequired(t *testing.T) {
	g := &GateModular{authNonces: newNonceCache(100)}
	req, _ := http.NewRequest(http.MethodGet, "https://sp/x", nil)
	require.NoError(t, hardenedRequestContext(g, req).enforceSingleUse())

	g.requireAuthNonce = true
	assert.ErrorIs(t, hardenedRequestContext(g, req).enforceSingleUse(), ErrMissingAuthNonce)
}

func TestVerifySignedContentHash(t *testing.T) {
	g := &GateModular{}
	body := []byte(`{"op":"delete"}`)
	sum := sha256.Sum256(body)

	req, _ := http.NewRequest(http.MethodPost, "https://sp/x", bytes.NewReader(body))
	req.Header.Set(commonhttp.HTTPHeaderContentSHA256, hex.EncodeToString(sum[:]))
	rc := hardenedRequestContext(g, req)
	require.NoError(t, rc.verifySignedContentHash())
	replay, _ := io.ReadAll(rc.request.Body)
	assert.Equal(t, body, replay)

	tampered, _ := http.NewRequest(http.MethodPost, "https://sp/x", strings.NewReader("something else"))
	tampered.Header.Set(commonhttp.HTTPHeaderContentSHA256, hex.EncodeToString(sum[:]))
	assert.ErrorIs(t, hardenedRequestContext(g, tampered).verifySignedContentHash(), ErrContentHashMismatch)

	unsigned, _ := http.NewRequest(http.MethodPost, "https://sp/x", strings.NewReader("anything"))
	require.NoError(t, hardenedRequestContext(g, unsigned).verifySignedContentHash())
}

func TestCheckIfSigExpiryCapsMutatingRequests(t *testing.T) {
	g := &GateModular{mutatingExpiryCapSec: 600}
	farAhead := time.Now().Add(24 * time.Hour).UTC().Format(ExpiryDateFormat)

	put, _ := http.NewRequest(http.MethodPut, "https://sp/x", nil)
	put.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, farAhead)
	assert.ErrorIs(t, hardenedRequestContext(g, put).CheckIfSigExpiry(), ErrExpiryTooFarAhead)

	get, _ := http.NewRequest(http.MethodGet, "https://sp/x", nil)
	get.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, farAhead)
	require.NoError(t, hardenedRequestContext(g, get).CheckIfSigExpiry())

	soon := time.Now().Add(5 * time.Minute).UTC().Format(ExpiryDateFormat)
	putSoon, _ := http.NewRequest(http.MethodPut, "https://sp/x", nil)
	putSoon.Header.Set(commonhttp.HTTPHeaderExpiryTimestamp, soon)
	require.NoError(t, hardenedRequestContext(g, putSoon).CheckIfSigExpiry())
}
