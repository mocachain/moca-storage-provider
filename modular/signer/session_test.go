package signer

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
)

const testPrivateKey = "0000000000000000000000000000000000000000000000000000000000000001"

func TestCreateTxOpts_UsesConfiguredChainID(t *testing.T) {
	server := newGasPriceServer(t, "0x2a", false)
	client, err := ethclient.Dial(server.URL)
	require.NoError(t, err)
	t.Cleanup(client.Close)

	chainID := big.NewInt(5151)
	txOpts, err := CreateTxOpts(context.Background(), client, testPrivateKey, chainID, 700_000, 9)
	require.NoError(t, err)
	require.Equal(t, uint64(700_000), txOpts.GasLimit)
	require.Equal(t, big.NewInt(42), txOpts.GasPrice)
	require.Equal(t, big.NewInt(9), txOpts.Nonce)

	signedTx, err := txOpts.Signer(txOpts.From, types.NewTx(&types.LegacyTx{Nonce: 9}))
	require.NoError(t, err)
	require.Equal(t, chainID, signedTx.ChainId())
}

func TestCreateTxOpts_ReturnsGasPriceError(t *testing.T) {
	server := newGasPriceServer(t, "", true)
	client, err := ethclient.Dial(server.URL)
	require.NoError(t, err)
	t.Cleanup(client.Close)

	txOpts, err := CreateTxOpts(context.Background(), client, testPrivateKey, big.NewInt(5151), 700_000, 9)
	require.Error(t, err)
	require.Nil(t, txOpts)
}

func newGasPriceServer(t *testing.T, gasPrice string, fail bool) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()

		var rpcRequest struct {
			ID json.RawMessage `json:"id"`
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&rpcRequest))

		writer.Header().Set("Content-Type", "application/json")
		if fail {
			require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      rpcRequest.ID,
				"error": map[string]any{
					"code":    -32000,
					"message": "gas price unavailable",
				},
			}))
			return
		}

		require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      rpcRequest.ID,
			"result":  gasPrice,
		}))
	}))
	t.Cleanup(server.Close)

	return server
}
