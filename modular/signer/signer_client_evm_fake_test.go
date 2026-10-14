package signer

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum"
	ethcmn "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/mocachain/moca/v2/sdk/client"
	"github.com/mocachain/moca/v2/sdk/keys"
	virtualgrouptypes "github.com/mocachain/moca/v2/x/virtualgroup/types"
)

const (
	fakeEvmChainID    = "moca_5151-1"
	fakeEvmPrivateKey = "b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291"
	fakeEvmCacheError = "failed to broadcast transaction: tx already exists in cache"
)

type fakeEvmActionKind int

const (
	// fakeEvmDefault applies the Moca node semantics modelled by admit().
	fakeEvmDefault fakeEvmActionKind = iota
	// fakeEvmAdmitThenDrop admits by the default rules, then closes the
	// connection without a response.
	fakeEvmAdmitThenDrop
	// fakeEvmDropOnly closes the connection without looking at the transaction.
	fakeEvmDropOnly
	fakeEvmRPCError
	fakeEvmHTTPStatus
	// fakeEvmAdmitThenRPCError admits by the default rules, then answers with a
	// JSON-RPC error (for example -32002 after the handler kept running).
	fakeEvmAdmitThenRPCError
	// fakeEvmAdmitThenHTTPStatus admits by the default rules, then answers with
	// a non-2xx HTTP status.
	fakeEvmAdmitThenHTTPStatus
	// fakeEvmBlock waits until the request context is done without admitting.
	fakeEvmBlock
	// fakeEvmAdmitThenBlock admits, then waits until the request context is done.
	fakeEvmAdmitThenBlock
	// fakeEvmAnswerHashOnly answers with the hash but admits nothing, like the
	// experimental EVM mempool paths that return nil without queuing.
	fakeEvmAnswerHashOnly
)

type fakeEvmAction struct {
	kind    fakeEvmActionKind
	code    int
	message string
	status  int
	body    string
}

type fakeEvmSend struct {
	raw    []byte
	hash   ethcmn.Hash
	nonce  uint64
	result string
}

// fakeEvmNode is an offline JSON-RPC endpoint modelling the parts of a Moca
// node that DepositEvm touches. It keeps a committed set and a CometBFT-style
// tx cache: cached bytes are rejected before any nonce check, uncached bytes
// below the account nonce get "invalid nonce", above it get the nonce-gap
// error, and the exact account nonce is committed at once.
type fakeEvmNode struct {
	t      *testing.T
	server *httptest.Server

	mu             sync.Mutex
	accountNonce   uint64
	gasPrices      []*big.Int
	gasPriceCalls  int
	cache          map[ethcmn.Hash]bool
	committed      map[ethcmn.Hash]*ethtypes.Transaction
	accepted       []*ethtypes.Transaction
	sends          []fakeEvmSend
	receiptStatus  map[ethcmn.Hash]uint64
	hideReceipts   bool
	disableCache   bool
	script         func(send int, tx *ethtypes.Transaction) fakeEvmAction
	unknownMethods []string
}

func newFakeEvmNode(t *testing.T, accountNonce uint64) *fakeEvmNode {
	t.Helper()
	node := &fakeEvmNode{
		t:             t,
		accountNonce:  accountNonce,
		gasPrices:     []*big.Int{big.NewInt(1_000_000_000)},
		cache:         map[ethcmn.Hash]bool{},
		committed:     map[ethcmn.Hash]*ethtypes.Transaction{},
		receiptStatus: map[ethcmn.Hash]uint64{},
	}
	node.server = httptest.NewServer(http.HandlerFunc(node.serveHTTP))
	t.Cleanup(node.server.Close)
	return node
}

type fakeEvmRequest struct {
	ID     json.RawMessage   `json:"id"`
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

func (node *fakeEvmNode) serveHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req fakeEvmRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	switch req.Method {
	case "eth_chainId":
		node.writeResult(w, req.ID, hexutil.EncodeUint64(5151))
	case "eth_gasPrice":
		node.mu.Lock()
		idx := node.gasPriceCalls
		if idx >= len(node.gasPrices) {
			idx = len(node.gasPrices) - 1
		}
		price := node.gasPrices[idx]
		node.gasPriceCalls++
		node.mu.Unlock()
		node.writeResult(w, req.ID, (*hexutil.Big)(price))
	case "eth_sendRawTransaction":
		node.serveSend(w, r, req)
	default:
		node.mu.Lock()
		node.unknownMethods = append(node.unknownMethods, req.Method)
		node.mu.Unlock()
		node.writeError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

func (node *fakeEvmNode) serveSend(w http.ResponseWriter, r *http.Request, req fakeEvmRequest) {
	var rawHex hexutil.Bytes
	if len(req.Params) != 1 || json.Unmarshal(req.Params[0], &rawHex) != nil {
		node.writeError(w, req.ID, -32602, "invalid argument 0")
		return
	}
	tx := new(ethtypes.Transaction)
	if err := tx.UnmarshalBinary(rawHex); err != nil {
		node.writeError(w, req.ID, -32602, "failed to decode transaction")
		return
	}

	node.mu.Lock()
	sendIndex := len(node.sends) + 1
	node.sends = append(node.sends, fakeEvmSend{raw: append([]byte(nil), rawHex...), hash: tx.Hash(), nonce: tx.Nonce()})
	script := node.script
	node.mu.Unlock()

	action := fakeEvmAction{}
	if script != nil {
		action = script(sendIndex, tx)
	}

	record := func(result string) {
		node.mu.Lock()
		node.sends[sendIndex-1].result = result
		node.mu.Unlock()
	}

	switch action.kind {
	case fakeEvmDropOnly:
		record("dropped")
		node.drop(w)
		return
	case fakeEvmRPCError:
		record(action.message)
		node.writeError(w, req.ID, action.code, action.message)
		return
	case fakeEvmHTTPStatus:
		record(fmt.Sprintf("http %d", action.status))
		w.WriteHeader(action.status)
		_, _ = io.WriteString(w, action.body)
		return
	case fakeEvmBlock:
		record("blocked")
		<-r.Context().Done()
		return
	case fakeEvmAnswerHashOnly:
		record("hash only")
		node.writeResult(w, req.ID, tx.Hash())
		return
	}

	admitErr := node.admit(tx)
	switch action.kind {
	case fakeEvmAdmitThenDrop:
		record(fmt.Sprintf("admit=%v, dropped", admitErr))
		node.drop(w)
	case fakeEvmAdmitThenRPCError:
		record(fmt.Sprintf("admit=%v, %s", admitErr, action.message))
		node.writeError(w, req.ID, action.code, action.message)
	case fakeEvmAdmitThenHTTPStatus:
		record(fmt.Sprintf("admit=%v, http %d", admitErr, action.status))
		w.WriteHeader(action.status)
		_, _ = io.WriteString(w, action.body)
	case fakeEvmAdmitThenBlock:
		record(fmt.Sprintf("admit=%v, blocked", admitErr))
		<-r.Context().Done()
	default:
		if admitErr != nil {
			record(admitErr.Error())
			node.writeError(w, req.ID, -32000, admitErr.Error())
			return
		}
		record("accepted")
		node.writeResult(w, req.ID, tx.Hash())
	}
}

func (node *fakeEvmNode) admit(tx *ethtypes.Transaction) error {
	node.mu.Lock()
	defer node.mu.Unlock()
	if !node.disableCache && node.cache[tx.Hash()] {
		return errors.New(fakeEvmCacheError)
	}
	if tx.Nonce() < node.accountNonce {
		return fmt.Errorf("failed to broadcast transaction: invalid nonce; got %d, expected %d: tx nonce is lower than account nonce",
			tx.Nonce(), node.accountNonce)
	}
	if tx.Nonce() > node.accountNonce {
		return fmt.Errorf("failed to broadcast transaction: tx nonce: %d, account accountNonce: %d: tx nonce is higher than account nonce",
			tx.Nonce(), node.accountNonce)
	}
	node.accountNonce++
	node.cache[tx.Hash()] = true
	node.committed[tx.Hash()] = tx
	node.accepted = append(node.accepted, tx)
	return nil
}

// commitExternal consumes the account nonce with a transaction this client
// never signed, as another process sharing the key would.
func (node *fakeEvmNode) commitExternal() {
	node.mu.Lock()
	defer node.mu.Unlock()
	node.accountNonce++
}

func (node *fakeEvmNode) setHideReceipts(hide bool) {
	node.mu.Lock()
	defer node.mu.Unlock()
	node.hideReceipts = hide
}

func (node *fakeEvmNode) drop(w http.ResponseWriter) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		node.t.Errorf("response writer cannot be hijacked")
		return
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		node.t.Errorf("hijack: %v", err)
		return
	}
	_ = conn.Close()
}

func (node *fakeEvmNode) writeResult(w http.ResponseWriter, id json.RawMessage, result interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": result})
}

func (node *fakeEvmNode) writeError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]interface{}{"code": code, "message": message},
	})
}

func (node *fakeEvmNode) receipt(hash ethcmn.Hash) (*ethtypes.Receipt, bool) {
	node.mu.Lock()
	defer node.mu.Unlock()
	if node.hideReceipts {
		return nil, false
	}
	if _, ok := node.committed[hash]; !ok {
		return nil, false
	}
	status := ethtypes.ReceiptStatusSuccessful
	if override, ok := node.receiptStatus[hash]; ok {
		status = override
	}
	return &ethtypes.Receipt{Status: status, TxHash: hash, BlockNumber: big.NewInt(100)}, true
}

func (node *fakeEvmNode) snapshot() (accepted []*ethtypes.Transaction, sends []fakeEvmSend, nonce uint64) {
	node.mu.Lock()
	defer node.mu.Unlock()
	return append([]*ethtypes.Transaction(nil), node.accepted...), append([]fakeEvmSend(nil), node.sends...), node.accountNonce
}

func (node *fakeEvmNode) describe() string {
	accepted, sends, nonce := node.snapshot()
	var b strings.Builder
	fmt.Fprintf(&b, "account nonce=%d; accepted:", nonce)
	for _, tx := range accepted {
		fmt.Fprintf(&b, " [nonce=%d hash=%s gasPrice=%s]", tx.Nonce(), tx.Hash().Hex(), tx.GasPrice())
	}
	b.WriteString("; sends:")
	for i, send := range sends {
		fmt.Fprintf(&b, " #%d[nonce=%d hash=%s result=%q]", i+1, send.nonce, send.hash.Hex(), send.result)
	}
	return b.String()
}

// installFakeEvmSeams routes receipt lookups and nonce refreshes to the fake.
// Receipt waiting returns at once instead of polling blocks.
func installFakeEvmSeams(t *testing.T, node *fakeEvmNode) {
	t.Helper()
	originalWait, originalReceipt, originalNonce := waitForEvmTxFn, transactionReceiptFn, getNonceOnChainFn
	t.Cleanup(func() {
		waitForEvmTxFn, transactionReceiptFn, getNonceOnChainFn = originalWait, originalReceipt, originalNonce
	})
	waitForEvmTxFn = func(ctx context.Context, _ *ethclient.Client, _ *client.MocaClient, hash ethcmn.Hash) (*ethtypes.Receipt, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		receipt, ok := node.receipt(hash)
		if !ok {
			return nil, fmt.Errorf("fetching tx '%s'", hash)
		}
		if receipt.Status != ethtypes.ReceiptStatusSuccessful {
			return nil, fmt.Errorf("txn: %s has failed", hash)
		}
		return receipt, nil
	}
	transactionReceiptFn = func(ctx context.Context, _ *ethclient.Client, hash ethcmn.Hash) (*ethtypes.Receipt, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		receipt, ok := node.receipt(hash)
		if !ok {
			return nil, ethereum.NotFound
		}
		return receipt, nil
	}
	getNonceOnChainFn = func(context.Context, *MocaChainSignClient, *client.MocaClient) (uint64, error) {
		_, _, nonce := node.snapshot()
		return nonce, nil
	}
}

func newFakeEvmSignClient(t *testing.T, node *fakeEvmNode, nonce uint64) *MocaChainSignClient {
	t.Helper()
	km, err := keys.NewPrivateKeyManager(fakeEvmPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	// Both clients connect lazily, so nothing below dials until a call is made.
	mocaClient, err := client.NewMocaClient("http://127.0.0.1:1", node.server.URL, fakeEvmChainID, client.WithKeyManager(km))
	if err != nil {
		t.Fatal(err)
	}
	evmClient, err := ethclient.Dial(node.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(evmClient.Close)
	privateKey, err := crypto.HexToECDSA(fakeEvmPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	gasInfo := map[GasInfoType]GasInfo{}
	for _, gasType := range []GasInfoType{Seal, RejectSeal, DelegateCreateObject, DelegateUpdateObjectContent, DiscontinueBucket,
		CreateGlobalVirtualGroup, DeleteGlobalVirtualGroup, CompleteMigrateBucket, RejectMigrateBucket, SwapOut, ReserveSwapIn,
		CancelSwapIn, CompleteSwapIn, CompleteSwapOut, SPExit, CompleteSPExit, UpdateSPPrice, Deposit} {
		gasInfo[gasType] = GasInfo{GasLimit: 300000}
	}
	return &MocaChainSignClient{
		gasInfo:          gasInfo,
		maxEvmGasPrice:   big.NewInt(10_000_000_000),
		mocaClients:      map[SignType]*client.MocaClient{SignOperator: mocaClient, SignSeal: mocaClient, SignGc: mocaClient},
		evmPrivateKeys:   map[SignType]*ecdsa.PrivateKey{SignOperator: privateKey, SignSeal: privateKey, SignGc: privateKey},
		evmClient:        evmClient,
		operatorAccNonce: nonce,
		sealAccNonce:     nonce,
		gcAccNonce:       nonce,
		pendingEvmTxs:    map[SignType]pendingEvmTx{},
	}
}

func fakeDepositMsg(amount int64) *virtualgrouptypes.MsgDeposit {
	return &virtualgrouptypes.MsgDeposit{
		GlobalVirtualGroupId: 1,
		Deposit:              sdk.NewCoin("amoca", sdkmath.NewInt(amount)),
	}
}
