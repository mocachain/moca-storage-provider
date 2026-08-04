# MOCA-896 Cosmos Nonce Confirmation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent cached Cosmos account sequences from advancing until a SYNC-broadcast transaction is included and executes successfully.

**Architecture:** Keep every caller and `BROADCAST_MODE_SYNC` unchanged. Strengthen the shared `MocaChainSignClient.broadcastTx` boundary so it waits through the existing consensus `ConfirmTransaction` API and returns success only for a zero DeliverTx code; existing callers then advance cached nonces only after confirmed success.

**Tech Stack:** Go, Cosmos SDK transaction responses, existing `core/consensus.Consensus`, testify, gomock-free function seams.

## Global Constraints

- Scope is Cosmos transaction confirmation for MOCA-896 only.
- Do not change the EVM pending-transaction behavior tracked by MOCA-899.
- Preserve `BROADCAST_MODE_SYNC` and existing per-operation retry behavior.
- Confirmation failure and non-zero DeliverTx code must return an error.
- No cached account nonce may advance unless confirmation succeeds with code zero.

---

### Task 1: Confirm Cosmos Transaction Inclusion Before Success

**Files:**
- Modify: `modular/signer/signer_client.go:35-38,2531-2547`
- Create: `modular/signer/signer_client_broadcast_test.go`

**Interfaces:**
- Consumes: `Consensus.ConfirmTransaction(context.Context, string) (*sdk.TxResponse, error)`
- Produces: unchanged `MocaChainSignClient.broadcastTx(...) (string, error)` semantics strengthened to require successful inclusion

- [ ] **Step 1: Write focused failing tests**

Add test seams that the wished-for production API will expose, then cover all outcomes through the real `broadcastTx` method:

```go
func TestBroadcastTxRequiresSuccessfulInclusion(t *testing.T) {
	tests := []struct {
		name        string
		confirmResp *sdk.TxResponse
		confirmErr  error
		wantHash    string
		wantErr     string
	}{
		{name: "included", confirmResp: &sdk.TxResponse{}, wantHash: "ABC123"},
		{name: "confirmation failed", confirmErr: fmt.Errorf("transaction dropped"), wantErr: "failed to confirm tx"},
		{name: "execution failed", confirmResp: &sdk.TxResponse{Code: 7, Codespace: "storage"}, wantErr: "failed to execute tx"},
	}
	// Each case stubs broadcastCosmosTxFn to return CheckTx code zero and
	// confirmCosmosTxFn to return the case result, then calls broadcastTx.
}
```

- [ ] **Step 2: Run the test and record RED**

Run:

```bash
go test ./modular/signer -run TestBroadcastTxRequiresSuccessfulInclusion -count=1
```

Expected: FAIL because `broadcastCosmosTxFn` and `confirmCosmosTxFn` do not exist yet, proving the production confirmation boundary is absent.

- [ ] **Step 3: Add the minimal confirmation boundary**

Add seams matching the existing `waitForEvmTxFn` pattern:

```go
var broadcastCosmosTxFn = (*client.MocaClient).BroadcastTx

var confirmCosmosTxFn = func(ctx context.Context, signClient *MocaChainSignClient, txHash string) (*sdk.TxResponse, error) {
	return signClient.signer.baseApp.Consensus().ConfirmTransaction(ctx, txHash)
}
```

In `broadcastTx`, call `broadcastCosmosTxFn` instead of the concrete method. After existing CheckTx validation, confirm the returned hash and reject confirmation errors or non-zero DeliverTx codes:

```go
confirmed, err := confirmCosmosTxFn(ctx, client, resp.TxResponse.TxHash)
if err != nil {
	return "", errors.Wrap(err, "failed to confirm tx")
}
if confirmed.Code != 0 {
	return "", fmt.Errorf("failed to execute tx, resp code: %d, code space: %s", confirmed.Code, confirmed.Codespace)
}
return resp.TxResponse.TxHash, nil
```

- [ ] **Step 4: Run focused tests and record GREEN**

Run:

```bash
go test ./modular/signer -run TestBroadcastTxRequiresSuccessfulInclusion -count=1
```

Expected: PASS for inclusion success, confirmation failure, and non-zero DeliverTx code.

- [ ] **Step 5: Run package and repository verification**

Run:

```bash
go test ./modular/signer/... -count=1
make lint
go vet ./...
go build ./...
```

Expected: all commands exit zero; golangci-lint reports zero issues.

- [ ] **Step 6: Review and commit**

Review `git diff --check`, `git diff --stat origin/main`, and the full diff. Confirm only the approved Cosmos boundary, focused tests, spec, and plan are present. Then commit:

```bash
git add modular/signer/signer_client.go modular/signer/signer_client_broadcast_test.go docs/superpowers
git commit -m "fix(signer): confirm cosmos tx before advancing nonce"
```

- [ ] **Step 7: Push and open Draft PR**

Push `fix/moca-896-confirm-cosmos-nonce`, open a Draft PR titled `fix(signer): confirm cosmos tx before advancing nonce`, and include the Linear URL plus exact RED/GREEN outputs in its body.
