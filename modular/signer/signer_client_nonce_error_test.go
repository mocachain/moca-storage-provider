package signer

import "testing"

// errString is a tiny helper type that implements error so we can easily
// construct error values from string literals in tests.
type errString string

func (e errString) Error() string { return string(e) }

func TestIsNonceError_PositiveCases(t *testing.T) {
	cases := []string{
		"nonce too low",
		"nonce too high",
		"invalid nonce; got 10, expected 11",
		"account sequence mismatch, expected 5, got 4",
		"invalid sequence",
		"replacement transaction underpriced",
		"replacement tx underpriced",
	}

	for _, msg := range cases {
		if !isNonceError(errString(msg)) {
			t.Fatalf("expected isNonceError(%q) to be true", msg)
		}
	}
}

func TestIsNonceError_NegativeCases(t *testing.T) {
	cases := []string{
		"",
		"execution reverted: something else",
		"insufficient funds for gas * price + value",
		"some random error",
	}

	for _, msg := range cases {
		if isNonceError(errString(msg)) {
			t.Fatalf("expected isNonceError(%q) to be false", msg)
		}
	}
}

func TestIsNonceError_WithContext(t *testing.T) {
	cases := []string{
		"failed to broadcast tx: nonce too low for address 0x123",
		"tx execution failed: nonce too high for sender 0x456",
		"error: invalid nonce; got 10, expected 11",
		"rpc error: account sequence mismatch, expected 5, got 4",
		"execution error: replacement transaction underpriced, need higher gas price",
		"tx failed: replacement tx underpriced by txpool",
	}

	for _, msg := range cases {
		if !isNonceError(errString(msg)) {
			t.Fatalf("expected isNonceError(%q) to be true", msg)
		}
	}
}

func TestIsNonceError_NilError(t *testing.T) {
	if isNonceError(nil) {
		t.Fatal("expected isNonceError(nil) to be false")
	}
}



