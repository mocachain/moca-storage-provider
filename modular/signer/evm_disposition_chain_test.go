package signer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	ethcmn "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// The block listing reads hash, sender and nonce from eth_getBlockByNumber
// with full transactions, and reports a block the node lacks as unavailable.
func TestEvmDisposeChainBlockTransactions(t *testing.T) {
	from := fakeFrom(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		var result interface{}
		if req.Method == "eth_getBlockByNumber" && string(req.Params[0]) == `"0x65"` && string(req.Params[1]) == "true" {
			result = map[string]interface{}{"number": "0x65", "transactions": []map[string]interface{}{
				{"hash": ethcmn.HexToHash("0xabc").Hex(), "from": from.Hex(), "nonce": "0x7", "input": "0x"},
			}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()
	client, err := ethclient.Dial(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	chain := NewEvmDisposeChain(client)
	txs, err := chain.BlockTransactions(context.Background(), 101)
	if err != nil || len(txs) != 1 || txs[0].From != from || txs[0].Nonce != 7 || txs[0].Hash != ethcmn.HexToHash("0xabc") {
		t.Fatalf("unexpected block: %+v %v", txs, err)
	}
	if _, err := chain.BlockTransactions(context.Background(), 102); !errors.Is(err, errEvmBlockUnavailable) {
		t.Fatalf("a missing block must be unavailable, got %v", err)
	}
}

// A Cosmos transaction decodes with the Moca codec, its messages give the
// signer's operation fingerprint, and its tx.acc_seq attributes are read.
func TestCosmosDisposeChainView(t *testing.T) {
	chain, err := NewCosmosDisposeChain("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	comet := chain.(*cometCosmosDisposeChain)
	builder := comet.txConfig.NewTxBuilder()
	if err := builder.SetMsgs(depositMsgFor(t, 1000)); err != nil {
		t.Fatal(err)
	}
	raw, err := comet.txConfig.TxEncoder()(builder.GetTx())
	if err != nil {
		t.Fatal(err)
	}
	accSeq := fmt.Sprintf("%s/%d", fakeFrom(t).Hex(), 7)
	view := comet.view(&coretypes.ResultTx{Hash: []byte{0xab, 0xcd}, Height: 120, Tx: raw, TxResult: abci.ExecTxResult{
		Code: 0, Events: []abci.Event{{Type: "tx", Attributes: []abci.EventAttribute{{Key: "acc_seq", Value: accSeq}}}},
	}})
	if view.DecodeError != "" || view.Hash != "ABCD" || view.Height != 120 || len(view.AccSeqs) != 1 || view.AccSeqs[0] != accSeq {
		t.Fatalf("unexpected view: %+v", view)
	}
	if len(view.Msgs) != 1 {
		t.Fatalf("expected one message, got %d", len(view.Msgs))
	}
	operation, err := evmOperationOf(view.Msgs[0])
	if err != nil || operation != depositOperation(t, 1000) {
		t.Fatalf("the decoded message must give the deposit's operation: %v", err)
	}
	if bad := comet.view(&coretypes.ResultTx{Tx: []byte("not a tx")}); bad.DecodeError == "" {
		t.Fatal("undecodable bytes must be reported")
	}
	if err := cometIndexError(errors.New("transaction indexing is disabled")); !errors.Is(err, errCosmosIndexDisabled) {
		t.Fatalf("a disabled index must be recognised, got %v", err)
	}
}
