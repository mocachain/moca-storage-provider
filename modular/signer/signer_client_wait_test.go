package signer

import (
	"context"
	"testing"
	"time"

	ethcmn "github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/evmos/evmos/v12/sdk/client"
)

// Test that waitForTransactionReceipt calls the underlying WaitForEvmTx and returns its receipt.
func TestWaitForTransactionReceipt_Success(t *testing.T) {
	orig := waitForEvmTxFn
	defer func() { waitForEvmTxFn = orig }()

	called := false
	waitForEvmTxFn = func(ctx context.Context, evmClient *ethclient.Client, gnfdCli *client.MocaClient, hash ethcmn.Hash) (*ethtypes.Receipt, error) {
		called = true
		return &ethtypes.Receipt{Status: ethtypes.ReceiptStatusSuccessful}, nil
	}

	svc := &MocaChainSignClient{
		mocaClients: map[SignType]*client.MocaClient{
			SignOperator: nil, // not used by the stub
		},
	}

	rcpt, err := svc.waitForTransactionReceipt(context.Background(), ethcmn.HexToHash("0x01"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rcpt == nil || rcpt.Status != ethtypes.ReceiptStatusSuccessful {
		t.Fatalf("unexpected receipt: %+v", rcpt)
	}
	if !called {
		t.Fatalf("waitForEvmTxFn was not called")
	}
}

// Test that the 5-minute upper bound logic is preserved (we use a tiny timeout in test).
func TestWaitForTransactionReceipt_Timeout(t *testing.T) {
	orig := waitForEvmTxFn
	defer func() { waitForEvmTxFn = orig }()

	// Block until context is done to simulate a long wait.
	waitForEvmTxFn = func(ctx context.Context, evmClient *ethclient.Client, gnfdCli *client.MocaClient, hash ethcmn.Hash) (*ethtypes.Receipt, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	svc := &MocaChainSignClient{
		mocaClients: map[SignType]*client.MocaClient{
			SignOperator: nil,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := svc.waitForTransactionReceipt(ctx, ethcmn.HexToHash("0x02"))
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if got := err.Error(); got == "" || (got != "" && !contains(got, "timeout waiting for transaction receipt")) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(s) > len(sub) && (indexOf(s, sub) >= 0)))
}

func indexOf(s, sub string) int {
	// simple substring search to avoid importing strings for a single use
	n, m := len(s), len(sub)
	if m == 0 {
		return 0
	}
	if m > n {
		return -1
	}
	for i := 0; i <= n-m; i++ {
		if s[i:i+m] == sub {
			return i
		}
	}
	return -1
}


