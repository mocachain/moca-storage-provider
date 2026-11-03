package signer

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum"
	ethcmn "github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	virtualgrouptypes "github.com/mocachain/moca/v2/x/virtualgroup/types"
)

// fakeDisposeChain is the chain as the signer-evm CLI reads it, kept
// consistent with a fakeEvmNode: every transaction the node commits, and
// every transaction a test adds, is an entry at its own height in nonce
// order. Each eth_blockNumber call mints an empty block, so the second
// sampling height comes at once.
type fakeDisposeChain struct {
	t    *testing.T
	node *fakeEvmNode

	mu      sync.Mutex
	base    uint64
	height  uint64
	entries []*fakeChainEntry
	synced  int
	hidden  map[string]bool
	chainID uint64
	network string
	index   bool
	// codeAt overrides eth_getCode; nil means no address has code.
	codeAt func(addr ethcmn.Address, height uint64) ([]byte, error)
	// sendOverride answers eth_sendRawTransaction instead of the node.
	sendOverride func(tx *ethtypes.Transaction) error
	// failNonceAt makes eth_getTransactionCount fail.
	failNonceAt error
	// onBlockNumber runs before the n-th eth_blockNumber call (1-based).
	onBlockNumber map[int]func()
	blockCalls    int
	byHashCalls   []ethcmn.Hash
	blockRequests []uint64
	sends         []ethcmn.Hash
}

type fakeChainEntry struct {
	height uint64
	nonce  uint64
	evm    *ethtypes.Transaction
	status uint64
	cosmos *CosmosTxView
	// foreign is another account's transaction: it uses none of the signer's nonces.
	foreign bool
}

func (e *fakeChainEntry) hash() string {
	if e.evm != nil {
		return e.evm.Hash().Hex()
	}
	return e.cosmos.Hash
}

func newFakeDisposeChain(t *testing.T, node *fakeEvmNode) *fakeDisposeChain {
	_, _, nonce := node.snapshot()
	return &fakeDisposeChain{
		t: t, node: node, base: nonce, height: 101, hidden: map[string]bool{}, chainID: 5151, network: fakeEvmChainID,
		index: true, onBlockNumber: map[int]func(){},
	}
}

func fakeFrom(t *testing.T) ethcmn.Address {
	t.Helper()
	key, err := crypto.HexToECDSA(fakeEvmPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	return crypto.PubkeyToAddress(key.PublicKey)
}

// sync turns the node's newly committed transactions into entries.
func (c *fakeDisposeChain) sync() {
	accepted, _, _ := c.node.snapshot()
	for ; c.synced < len(accepted); c.synced++ {
		tx := accepted[c.synced]
		c.node.mu.Lock()
		status, ok := c.node.receiptStatus[tx.Hash()]
		c.node.mu.Unlock()
		if !ok {
			status = ethtypes.ReceiptStatusSuccessful
		}
		c.entries = append(c.entries, &fakeChainEntry{height: c.height, nonce: tx.Nonce(), evm: tx, status: status})
		c.height++
	}
}

// addEvmTx commits tx through the node, failed when status is 0.
func (c *fakeDisposeChain) addEvmTx(tx *ethtypes.Transaction, status uint64) {
	c.t.Helper()
	if status != ethtypes.ReceiptStatusSuccessful {
		c.node.setReceiptStatus(tx.Hash(), status)
	}
	if err := c.node.admit(tx); err != nil {
		c.t.Fatalf("admit %s: %v", tx.Hash().Hex(), err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sync()
}

// addForeignEvmTx commits a successful transaction of another account.
func (c *fakeDisposeChain) addForeignEvmTx(tx *ethtypes.Transaction) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sync()
	c.entries = append(c.entries, &fakeChainEntry{height: c.height, nonce: tx.Nonce(), evm: tx,
		status: ethtypes.ReceiptStatusSuccessful, foreign: true})
	c.height++
}

// addCosmosTx uses the account's next nonce with a Cosmos transaction. A
// successful one counts as an execution of countsAs, when given.
func (c *fakeDisposeChain) addCosmosTx(hash string, code uint32, msgs []sdk.Msg, countsAs *ethtypes.Transaction) *CosmosTxView {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sync()
	c.node.mu.Lock()
	nonce := c.node.accountNonce
	c.node.accountNonce++
	if code == 0 && countsAs != nil {
		c.node.executions[executionKey(countsAs)]++
	}
	c.node.mu.Unlock()
	view := &CosmosTxView{Hash: hash, Height: c.height, Code: code, Msgs: msgs,
		AccSeqs: []string{fmt.Sprintf("%s/%d", fakeFrom(c.t).Hex(), nonce)}}
	c.entries = append(c.entries, &fakeChainEntry{height: c.height, nonce: nonce, cosmos: view})
	c.height++
	return view
}

func (c *fakeDisposeChain) hide(hash string, hidden bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hidden[strings.ToLower(hash)] = hidden
}

func (c *fakeDisposeChain) visible(e *fakeChainEntry) bool {
	return !c.hidden[strings.ToLower(e.hash())]
}

func (c *fakeDisposeChain) evmEntry(hash ethcmn.Hash) *fakeChainEntry {
	for _, e := range c.entries {
		if e.evm != nil && e.evm.Hash() == hash && c.visible(e) {
			return e
		}
	}
	return nil
}

func (c *fakeDisposeChain) ChainID(context.Context) (*big.Int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return new(big.Int).SetUint64(c.chainID), nil
}

func (c *fakeDisposeChain) BlockNumber(context.Context) (uint64, error) {
	c.mu.Lock()
	c.blockCalls++
	hook := c.onBlockNumber[c.blockCalls]
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sync()
	height := c.height
	c.height++
	return height, nil
}

func (c *fakeDisposeChain) TransactionReceipt(_ context.Context, hash ethcmn.Hash) (*ethtypes.Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sync()
	if e := c.evmEntry(hash); e != nil {
		return &ethtypes.Receipt{Status: e.status, TxHash: hash, BlockNumber: new(big.Int).SetUint64(e.height)}, nil
	}
	return nil, ethereum.NotFound
}

func (c *fakeDisposeChain) TransactionByHash(_ context.Context, hash ethcmn.Hash) (*ethtypes.Transaction, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sync()
	c.byHashCalls = append(c.byHashCalls, hash)
	if e := c.evmEntry(hash); e != nil {
		return e.evm, false, nil
	}
	return nil, false, ethereum.NotFound
}

func (c *fakeDisposeChain) NonceAt(_ context.Context, _ ethcmn.Address, height *big.Int) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failNonceAt != nil {
		return 0, c.failNonceAt
	}
	c.sync()
	nonce := c.base
	for _, e := range c.entries {
		if e.height <= height.Uint64() && !e.foreign {
			nonce++
		}
	}
	return nonce, nil
}

func (c *fakeDisposeChain) PendingNonceAt(context.Context, ethcmn.Address) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sync()
	_, _, nonce := c.node.snapshot()
	return nonce, nil
}

func (c *fakeDisposeChain) CodeAt(_ context.Context, addr ethcmn.Address, height *big.Int) ([]byte, error) {
	c.mu.Lock()
	codeAt := c.codeAt
	c.mu.Unlock()
	if codeAt == nil {
		return nil, nil
	}
	return codeAt(addr, height.Uint64())
}

// fakeRPCError is a JSON-RPC error object, as go-ethereum returns it.
type fakeRPCError struct {
	code int
	msg  string
}

func (e *fakeRPCError) Error() string  { return e.msg }
func (e *fakeRPCError) ErrorCode() int { return e.code }

func (c *fakeDisposeChain) SendTransaction(_ context.Context, tx *ethtypes.Transaction) error {
	c.mu.Lock()
	c.sends = append(c.sends, tx.Hash())
	override := c.sendOverride
	c.mu.Unlock()
	if override != nil {
		return override(tx)
	}
	if err := c.node.admit(tx); err != nil {
		return &fakeRPCError{code: -32000, msg: err.Error()}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sync()
	return nil
}

func (c *fakeDisposeChain) BlockTransactions(_ context.Context, height uint64) ([]EvmBlockTx, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sync()
	c.blockRequests = append(c.blockRequests, height)
	var txs []EvmBlockTx
	for _, e := range c.entries {
		if e.height == height && e.evm != nil && c.visible(e) {
			from, err := ethtypes.Sender(ethtypes.LatestSignerForChainID(e.evm.ChainId()), e.evm)
			if err != nil {
				return nil, err
			}
			txs = append(txs, EvmBlockTx{Hash: e.evm.Hash(), From: from, Nonce: e.nonce})
		}
	}
	return txs, nil
}

// fakeCosmosChain is the CometBFT side of the same chain.
type fakeCosmosChain struct{ c *fakeDisposeChain }

func (f fakeCosmosChain) Network(context.Context) (string, error) {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	return f.c.network, nil
}

func (f fakeCosmosChain) Tx(_ context.Context, hash string) (*CosmosTxView, error) {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	if !f.c.index {
		return nil, fmt.Errorf("%w: transaction indexing is disabled", errCosmosIndexDisabled)
	}
	for _, e := range f.c.entries {
		if e.cosmos != nil && strings.EqualFold(e.cosmos.Hash, hash) && f.c.visible(e) {
			return e.cosmos, nil
		}
	}
	return nil, nil
}

func (f fakeCosmosChain) SearchAccSeq(_ context.Context, accSeq string, maxHeight uint64) ([]*CosmosTxView, error) {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	if !f.c.index {
		return nil, fmt.Errorf("%w: transaction indexing is disabled", errCosmosIndexDisabled)
	}
	var views []*CosmosTxView
	for _, e := range f.c.entries {
		if e.cosmos == nil || e.height > maxHeight || !f.c.visible(e) {
			continue
		}
		for _, s := range e.cosmos.AccSeqs {
			if s == accSeq {
				views = append(views, e.cosmos)
			}
		}
	}
	return views, nil
}

// disposeEnv runs signer-evm commands against a client's store and chain.
type disposeEnv struct {
	t      *testing.T
	node   *fakeEvmNode
	chain  *fakeDisposeChain
	store  *memEvmStateStore
	client *MocaChainSignClient
}

func newDisposeEnv(t *testing.T) *disposeEnv {
	t.Helper()
	node := newFakeEvmNode(t, scenarioNonce)
	installFakeEvmSeams(t, node)
	client := newFakeEvmSignClient(t, node, scenarioNonce)
	return &disposeEnv{t: t, node: node, chain: newFakeDisposeChain(t, node), store: memStoreOf(t, client), client: client}
}

func (env *disposeEnv) restart() {
	env.t.Helper()
	env.client = restartClient(env.t, env.node, env.store)
}

func (env *disposeEnv) run(req EvmDisposeRequest) *EvmDisposeReport {
	env.t.Helper()
	req.ChainID = fakeEvmChainID
	if req.Operator == "" {
		req.Operator = "ops-test"
	}
	if req.Reason == "" {
		req.Reason = "test"
	}
	req.BlockWait, req.Poll = time.Second, time.Millisecond
	return disposeEvmState(context.Background(), env.store, req, env.chain, fakeCosmosChain{env.chain})
}

func (env *disposeEnv) resolve(scope SignType, hash ethcmn.Hash, req EvmDisposeRequest) *EvmDisposeReport {
	req.Command, req.Scope, req.TxHash = EvmDisposeResolve, string(scope), hash
	return env.run(req)
}

func (env *disposeEnv) outcomeCmd(command string, id uint64, req EvmDisposeRequest) *EvmDisposeReport {
	req.Command, req.OutcomeID = command, id
	return env.run(req)
}

func expectExit(t *testing.T, report *EvmDisposeReport, code int, branch string) {
	t.Helper()
	if report.ExitCode != code || (branch != "" && report.Branch != branch) {
		t.Fatalf("want exit %d branch %q, got exit %d branch %q: %+v", code, branch, report.ExitCode, report.Branch, report)
	}
}

// stuckDeposit leaves a deposit pending at the next nonce N without the
// node ever admitting it: the connection drops before the node reads it.
func (env *disposeEnv) stuckDeposit(amount int64) (pendingRecord, *ethtypes.Transaction) {
	env.t.Helper()
	env.node.script = scriptAll(fakeEvmAction{kind: fakeEvmDropOnly})
	hash, err := deposit(scenarioCtx(env.t), env.client, amount)
	env.node.script = nil
	pending, ok := env.store.pendingOf(SignOperator)
	if err == nil || !ok || hash != pending.TxHash.String() {
		env.t.Fatalf("expected a pending deposit, got hash=%q err=%v", hash, err)
	}
	return pending, env.node.sentTx(env.t, 0)
}

// signTx signs a transaction with the fake key.
func signTx(t *testing.T, nonce uint64, to ethcmn.Address, value int64, data []byte, gasPrice int64) *ethtypes.Transaction {
	t.Helper()
	key, err := crypto.HexToECDSA(fakeEvmPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := ethtypes.SignNewTx(key, ethtypes.LatestSignerForChainID(big.NewInt(5151)), &ethtypes.LegacyTx{
		Nonce: nonce, GasPrice: big.NewInt(gasPrice), Gas: 300000, To: &to, Value: big.NewInt(value), Data: data,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// sameCall re-signs original's call at the same nonce with another gas price.
func sameCall(t *testing.T, original *ethtypes.Transaction) *ethtypes.Transaction {
	return signTx(t, original.Nonce(), *original.To(), original.Value().Int64(), original.Data(), original.GasPrice().Int64()+7)
}

// putOwnEvidence records tx as signed by this signer for operation in scope gc.
func (env *disposeEnv) putOwnEvidence(tx *ethtypes.Transaction, operation ethcmn.Hash) {
	env.t.Helper()
	raw, err := tx.MarshalBinary()
	if err != nil {
		env.t.Fatal(err)
	}
	env.store.mu.Lock()
	defer env.store.mu.Unlock()
	id := env.store.id()
	env.store.signed[id] = signedTxEvidence{ID: id, ChainID: fakeEvmChainID, EvmChainID: 5151, Scope: SignGc, Operation: operation,
		From: fakeFrom(env.t), Nonce: tx.Nonce(), TxHash: tx.Hash(), Raw: raw, RecordedBlock: 100, LeaseEpoch: 1}
}

func depositOperation(t *testing.T, amount int64) ethcmn.Hash {
	t.Helper()
	msg := depositMsgFor(t, amount)
	operation, err := evmOperationFingerprint(msg)
	if err != nil {
		t.Fatal(err)
	}
	return operation
}

// depositMsgFor is the message DepositEvm fingerprints for fakeDepositMsg(amount).
func depositMsgFor(t *testing.T, amount int64) *virtualgrouptypes.MsgDeposit {
	return virtualgrouptypes.NewMsgDeposit(sdk.AccAddress(fakeFrom(t).Bytes()), 1, sdk.NewCoin("amoca", sdkmath.NewInt(amount)))
}

// auditCount counts audit rows of event.
func (env *disposeEnv) auditCount(event string) int {
	return len(env.store.auditEvents(event))
}

// snapshotState is the DB state a refused or dry command must leave unchanged.
func (env *disposeEnv) snapshotState() string {
	env.store.mu.Lock()
	defer env.store.mu.Unlock()
	return fmt.Sprintf("pending=%v outcomes=%v audits=%d signed=%d", env.store.pending, env.store.outcomes,
		len(env.store.audits), len(env.store.signed))
}
