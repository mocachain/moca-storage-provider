package signer

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/mocachain/moca/v2/precompiles/storage"
	"github.com/mocachain/moca/v2/precompiles/storageprovider"
	"github.com/mocachain/moca/v2/precompiles/virtualgroup"
)

// CreateTxOpts builds the transactor for one EVM transaction signed with privateKey at
// nonce. The gas price is the node's suggestion and is refused above maxGasPrice.
//
// The gas limit is not fixed. When the transactor signs, it estimates the exact call
// it was given, adds gasMarginPercent, and refuses the transaction when the estimate
// alone is above maxGasLimit (0 = no upper bound). A call the node cannot estimate,
// for example because it would revert, is refused too, so it is never broadcast.
func CreateTxOpts(ctx context.Context, client *ethclient.Client, privateKey *ecdsa.PrivateKey, chain *big.Int,
	maxGasLimit, gasMarginPercent, nonce uint64, maxGasPrice *big.Int,
) (*bind.TransactOpts, error) {
	if privateKey == nil {
		return nil, ErrDanglingPointer
	}

	// Build transact tx opts with private key
	txOpts, err := bind.NewKeyedTransactorWithChainID(privateKey, chain)
	if err != nil {
		return nil, err
	}

	gasPrice, err := client.SuggestGasPrice(ctx)
	if err != nil {
		return nil, err
	}
	if maxGasPrice == nil || maxGasPrice.Sign() <= 0 {
		return nil, fmt.Errorf("max evm gas price is not configured (got %v)", maxGasPrice)
	}
	if gasPrice.Cmp(maxGasPrice) > 0 {
		return nil, fmt.Errorf("suggested gas price %s exceeds configured maximum %s", gasPrice, maxGasPrice)
	}
	txOpts.GasPrice = gasPrice

	txOpts.Nonce = big.NewInt(int64(nonce))

	// bind estimates the gas itself only when GasLimit is 0, and that estimate
	// refuses an address without code, which every moca precompile is. So bind gets
	// a placeholder and the signer replaces it with the estimate of the call bind
	// has built.
	txOpts.GasLimit = placeholderGasLimit
	signWithKey := txOpts.Signer
	txOpts.Signer = func(from common.Address, tx *types.Transaction) (*types.Transaction, error) {
		gasLimit, err := estimateGasLimit(ctx, client, from, tx, maxGasLimit, gasMarginPercent)
		if err != nil {
			return nil, err
		}
		estimated, err := withGasLimit(tx, gasLimit)
		if err != nil {
			return nil, err
		}
		return signWithKey(from, estimated)
	}

	return txOpts, nil
}

// placeholderGasLimit is the gas limit bind builds a transaction with before the
// signer replaces it with the estimate.
const placeholderGasLimit = 1

// gasEstimator is the part of the EVM client estimateGasLimit uses.
type gasEstimator interface {
	EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error)
}

// estimateGasLimit returns the gas limit for tx sent by from: the node's estimate of
// the call plus gasMarginPercent, never above maxGasLimit (0 = no upper bound). It
// fails when the node cannot estimate the call or the estimate is above maxGasLimit.
//
// The margin covers state that changes between the estimate and the block. It costs
// little: the fee market charges max(gas used, gas limit x min_gas_multiplier), and
// with the default multiplier of 0.5 a margin below 100% is never charged.
func estimateGasLimit(ctx context.Context, client gasEstimator, from common.Address, tx *types.Transaction,
	maxGasLimit, gasMarginPercent uint64,
) (uint64, error) {
	// No gas price: the node would otherwise cap the estimate at what the sender's
	// balance pays for at that price.
	estimate, err := client.EstimateGas(ctx, ethereum.CallMsg{From: from, To: tx.To(), Value: tx.Value(), Data: tx.Data()})
	if err != nil {
		return 0, fmt.Errorf("failed to estimate gas: %w", err)
	}
	if maxGasLimit > 0 && estimate > maxGasLimit {
		return 0, fmt.Errorf("estimated gas %d exceeds the configured gas limit %d", estimate, maxGasLimit)
	}
	gasLimit := estimate + estimate*gasMarginPercent/100
	if maxGasLimit > 0 && gasLimit > maxGasLimit {
		gasLimit = maxGasLimit
	}
	return gasLimit, nil
}

// withGasLimit returns tx with its gas limit replaced. CreateTxOpts sets a gas price,
// so bind only builds legacy transactions.
func withGasLimit(tx *types.Transaction, gasLimit uint64) (*types.Transaction, error) {
	if tx.Type() != types.LegacyTxType {
		return nil, fmt.Errorf("unexpected EVM transaction type %d", tx.Type())
	}
	return types.NewTx(&types.LegacyTx{
		Nonce:    tx.Nonce(),
		GasPrice: tx.GasPrice(),
		Gas:      gasLimit,
		To:       tx.To(),
		Value:    tx.Value(),
		Data:     tx.Data(),
	}), nil
}

func CreateStorageSession(client *ethclient.Client, txOpts bind.TransactOpts, contractAddress string) (*storage.IStorageSession, error) {
	storageContract, err := storage.NewIStorage(common.HexToAddress(contractAddress), client)
	if err != nil {
		return nil, err
	}
	session := &storage.IStorageSession{
		Contract: storageContract,
		CallOpts: bind.CallOpts{
			Pending: false,
		},
		TransactOpts: txOpts,
	}
	return session, nil
}

func CreateVirtualGroupSession(client *ethclient.Client, txOpts bind.TransactOpts, contractAddress string) (*virtualgroup.IVirtualGroupSession, error) {
	virtualgroupContract, err := virtualgroup.NewIVirtualGroup(common.HexToAddress(contractAddress), client)
	if err != nil {
		return nil, err
	}
	session := &virtualgroup.IVirtualGroupSession{
		Contract: virtualgroupContract,
		CallOpts: bind.CallOpts{
			Pending: false,
		},
		TransactOpts: txOpts,
	}
	return session, nil
}

func CreateStorageProviderSession(client *ethclient.Client, txOpts bind.TransactOpts, contractAddress string) (*storageprovider.IStorageProviderSession, error) {
	storageproviderContract, err := storageprovider.NewIStorageProvider(common.HexToAddress(contractAddress), client)
	if err != nil {
		return nil, err
	}
	session := &storageprovider.IStorageProviderSession{
		Contract: storageproviderContract,
		CallOpts: bind.CallOpts{
			Pending: false,
		},
		TransactOpts: txOpts,
	}
	return session, nil
}
