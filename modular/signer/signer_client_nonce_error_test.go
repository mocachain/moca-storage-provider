package signer

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/rpc"
)

type testRPCError struct {
	code    int
	message string
}

func (e testRPCError) Error() string  { return e.message }
func (e testRPCError) ErrorCode() int { return e.code }

func TestClassifyEvmSendError(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want evmSendOutcome
	}{
		{"nil", context.Background(), nil, evmSendAccepted},
		{"moca nonce low", context.Background(),
			testRPCError{-32000, "failed to broadcast transaction: invalid nonce; got 7, expected 8: tx nonce is lower than account nonce"},
			evmSendNonceRejected},
		{"moca nonce gap", context.Background(),
			testRPCError{-32000, "failed to broadcast transaction: tx nonce: 9, account accountNonce: 8: tx nonce is higher than account nonce"},
			evmSendNonceRejected},
		{"geth nonce too low", context.Background(), testRPCError{-32000, "nonce too low"}, evmSendNonceRejected},
		{"geth nonce too high", context.Background(), testRPCError{-32000, "nonce too high"}, evmSendNonceRejected},
		{"insufficient funds", context.Background(),
			testRPCError{-32000, "failed to broadcast transaction: insufficient funds"}, evmSendRejected},
		{"validation", context.Background(), testRPCError{-32000, "failed to validate transaction: bad"}, evmSendRejected},
		{"invalid params", context.Background(), testRPCError{-32602, "invalid argument 0"}, evmSendRejected},
		{"tx cache", context.Background(), testRPCError{-32000, fakeEvmCacheError}, evmSendAmbiguous},
		{"underpriced", context.Background(), testRPCError{-32000, "replacement transaction underpriced"}, evmSendAmbiguous},
		{"account sequence mismatch", context.Background(), testRPCError{-32000, "account sequence mismatch"}, evmSendAmbiguous},
		{"unknown broadcast failure", context.Background(),
			testRPCError{-32000, "failed to broadcast transaction: post failed: EOF"}, evmSendAmbiguous},
		// -32002/-32603 are ambiguous before any reason matching.
		{"timeout code with nonce text", context.Background(),
			testRPCError{-32002, "request timed out: invalid nonce"}, evmSendAmbiguous},
		{"internal code with funds text", context.Background(),
			testRPCError{-32603, "insufficient funds"}, evmSendAmbiguous},
		// HTTP and transport failures are ambiguous whatever their text says.
		{"http 503 with nonce body", context.Background(),
			rpc.HTTPError{StatusCode: 503, Status: "503 Service Unavailable", Body: []byte(`{"error":{"message":"invalid nonce"}}`)},
			evmSendAmbiguous},
		{"wrapped http error", context.Background(),
			fmt.Errorf("send: %w", rpc.HTTPError{StatusCode: 502, Status: "502", Body: []byte("insufficient funds")}),
			evmSendAmbiguous},
		{"transport text with funds", context.Background(), errors.New("dial tcp: insufficient funds"), evmSendAmbiguous},
		{"cancelled context", cancelled, testRPCError{-32000, "insufficient funds"}, evmSendAmbiguous},
		{"deadline", context.Background(), fmt.Errorf("post: %w", context.DeadlineExceeded), evmSendAmbiguous},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyEvmSendError(tc.ctx, tc.err); got != tc.want {
				t.Fatalf("classifyEvmSendError(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestEvmNonceRejectionIsTypedNotTextual(t *testing.T) {
	inner := errors.New("invalid nonce; got 7, expected 8")
	if isEvmNonceRejection(inner) {
		t.Fatal("a plain error with nonce text must not permit re-signing")
	}
	if !isEvmNonceRejection(fmt.Errorf("wrapped: %w", &evmNonceRejectedError{err: inner})) {
		t.Fatal("wrapped evmNonceRejectedError not recognised")
	}
}
