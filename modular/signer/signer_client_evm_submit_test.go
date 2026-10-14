package signer

import (
	"context"
	"math/big"
	"testing"
	"time"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
)

// MOCA-1284: the first deposit is committed but its response is lost, the
// next eth_gasPrice differs, so a re-signed transaction at the same nonce
// misses the tx cache and is rejected with "invalid nonce". The deposit must
// not then be re-signed at the next nonce.
func TestDepositEvmAmbiguousSubmitDoesNotResignAtNextNonce(t *testing.T) {
	const nonce = uint64(7)
	node := newFakeEvmNode(t, nonce)
	node.gasPrices = []*big.Int{big.NewInt(1_000_000_000), big.NewInt(1_100_000_000)}
	node.script = func(send int, _ *ethtypes.Transaction) fakeEvmAction {
		if send == 1 {
			return fakeEvmAction{kind: fakeEvmAdmitThenDrop}
		}
		return fakeEvmAction{}
	}
	installFakeEvmSeams(t, node)
	signClient := newFakeEvmSignClient(t, node, nonce)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	txHash, err := signClient.DepositEvm(ctx, SignOperator, fakeDepositMsg(1000))
	t.Logf("DepositEvm returned hash=%q err=%v", txHash, err)

	accepted, sends, _ := node.snapshot()
	for _, tx := range accepted {
		if tx.Nonce() != nonce {
			t.Fatalf("fake accepted a deposit at nonce %d after the nonce-%d deposit was committed; %s",
				tx.Nonce(), nonce, node.describe())
		}
	}
	for _, send := range sends {
		if send.nonce != nonce {
			t.Fatalf("a transaction at nonce %d was sent; %s", send.nonce, node.describe())
		}
	}
}
