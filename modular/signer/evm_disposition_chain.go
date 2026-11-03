package signer

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	sdkclient "github.com/cosmos/cosmos-sdk/client"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	ethcmn "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"

	mocasdktypes "github.com/mocachain/moca/v2/sdk/types"
)

// NewEvmDisposeChain reads the chain through an EVM JSON-RPC client.
func NewEvmDisposeChain(client *ethclient.Client) EvmDisposeChain {
	return &rpcEvmDisposeChain{Client: client}
}

type rpcEvmDisposeChain struct {
	*ethclient.Client
}

// BlockTransactions reads only hash, sender and nonce of each transaction,
// so it does not depend on how the node fills the block header.
func (c *rpcEvmDisposeChain) BlockTransactions(ctx context.Context, height uint64) ([]EvmBlockTx, error) {
	var block *struct {
		Transactions []struct {
			Hash  ethcmn.Hash    `json:"hash"`
			From  ethcmn.Address `json:"from"`
			Nonce hexutil.Uint64 `json:"nonce"`
		} `json:"transactions"`
	}
	if err := c.Client.Client().CallContext(ctx, &block, "eth_getBlockByNumber", hexutil.EncodeUint64(height), true); err != nil {
		return nil, err
	}
	if block == nil {
		return nil, errEvmBlockUnavailable
	}
	txs := make([]EvmBlockTx, 0, len(block.Transactions))
	for _, tx := range block.Transactions {
		txs = append(txs, EvmBlockTx{Hash: tx.Hash, From: tx.From, Nonce: uint64(tx.Nonce)})
	}
	return txs, nil
}

// NewCosmosDisposeChain reads Cosmos transactions through the CometBFT RPC
// at address. Looking a transaction up, by hash or by tx.acc_seq, needs the
// node's transaction index.
func NewCosmosDisposeChain(address string) (CosmosDisposeChain, error) {
	client, err := rpchttp.New(address, "/websocket")
	if err != nil {
		return nil, err
	}
	cdc := mocasdktypes.Codec()
	txConfig, err := authtx.NewTxConfigWithOptions(cdc, authtx.ConfigOptions{SigningContext: cdc.InterfaceRegistry().SigningContext()})
	if err != nil {
		return nil, err
	}
	return &cometCosmosDisposeChain{client: client, txConfig: txConfig}, nil
}

type cometCosmosDisposeChain struct {
	client   *rpchttp.HTTP
	txConfig sdkclient.TxConfig
}

func (c *cometCosmosDisposeChain) Network(ctx context.Context) (string, error) {
	status, err := c.client.Status(ctx)
	if err != nil {
		return "", err
	}
	return status.NodeInfo.Network, nil
}

func cometIndexError(err error) error {
	if err != nil && strings.Contains(err.Error(), "indexing is disabled") {
		return fmt.Errorf("%w: %v", errCosmosIndexDisabled, err)
	}
	return err
}

func (c *cometCosmosDisposeChain) Tx(ctx context.Context, hash string) (*CosmosTxView, error) {
	raw, err := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(hash, "0x"), "0X"))
	if err != nil {
		return nil, fmt.Errorf("invalid cosmos tx hash %q: %w", hash, err)
	}
	result, err := c.client.Tx(ctx, raw, false)
	if err != nil {
		// rpc/core/tx.go answers an unknown hash with exactly this message.
		if strings.Contains(err.Error(), fmt.Sprintf("tx (%X) not found", raw)) {
			return nil, nil
		}
		return nil, cometIndexError(err)
	}
	return c.view(result), nil
}

func (c *cometCosmosDisposeChain) SearchAccSeq(ctx context.Context, accSeq string, maxHeight uint64) ([]*CosmosTxView, error) {
	query := fmt.Sprintf("tx.acc_seq='%s' AND tx.height<=%d", accSeq, maxHeight)
	var views []*CosmosTxView
	perPage := 100
	for page := 1; ; page++ {
		result, err := c.client.TxSearch(ctx, query, false, &page, &perPage, "asc")
		if err != nil {
			return nil, cometIndexError(err)
		}
		for _, tx := range result.Txs {
			views = append(views, c.view(tx))
		}
		if len(result.Txs) == 0 || len(views) >= result.TotalCount {
			return views, nil
		}
	}
}

func (c *cometCosmosDisposeChain) view(result *coretypes.ResultTx) *CosmosTxView {
	view := &CosmosTxView{Hash: strings.ToUpper(hex.EncodeToString(result.Hash)), Code: result.TxResult.Code}
	if result.Height > 0 {
		view.Height = uint64(result.Height)
	}
	for _, event := range result.TxResult.Events {
		if event.Type != "tx" {
			continue
		}
		for _, attr := range event.Attributes {
			if attr.Key == "acc_seq" {
				view.AccSeqs = append(view.AccSeqs, attr.Value)
			}
		}
	}
	tx, err := c.txConfig.TxDecoder()(result.Tx)
	if err != nil {
		view.DecodeError = err.Error()
		return view
	}
	view.Msgs = tx.GetMsgs()
	return view
}
