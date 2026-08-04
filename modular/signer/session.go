package signer

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/mocachain/moca/v2/precompiles/storage"
	"github.com/mocachain/moca/v2/precompiles/storageprovider"
	"github.com/mocachain/moca/v2/precompiles/virtualgroup"
)

func CreateTxOpts(ctx context.Context, client *ethclient.Client, privateKey *ecdsa.PrivateKey, chain *big.Int, gasLimit uint64, nonce uint64, maxGasPrice *big.Int) (*bind.TransactOpts, error) {
	if privateKey == nil {
		return nil, ErrDanglingPointer
	}

	// Build transact tx opts with private key
	txOpts, err := bind.NewKeyedTransactorWithChainID(privateKey, chain)
	if err != nil {
		return nil, err
	}

	// set gas limit and gas price
	txOpts.GasLimit = gasLimit
	gasPrice, err := client.SuggestGasPrice(ctx)
	if err != nil {
		return nil, err
	}
	if maxGasPrice == nil || maxGasPrice.Sign() <= 0 || gasPrice.Cmp(maxGasPrice) > 0 {
		return nil, fmt.Errorf("suggested gas price %s exceeds configured maximum %v", gasPrice, maxGasPrice)
	}
	txOpts.GasPrice = gasPrice

	txOpts.Nonce = big.NewInt(int64(nonce))

	return txOpts, nil
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
