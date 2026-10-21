package signer

import (
	"math/big"
	"testing"

	ethcmn "github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// signFakeTx signs a plain transfer with the fake key at nonce.
func signFakeTx(t *testing.T, nonce uint64, gasPrice int64) *ethtypes.Transaction {
	t.Helper()
	key, err := crypto.HexToECDSA(fakeEvmPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	to := ethcmn.HexToAddress("0x00000000000000000000000000000000000000aa")
	tx, err := ethtypes.SignNewTx(key, ethtypes.LatestSignerForChainID(big.NewInt(5151)), &ethtypes.LegacyTx{
		Nonce: nonce, GasPrice: big.NewInt(gasPrice), Gas: 21000, To: &to, Value: big.NewInt(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	return tx
}
