# MOCA-896 Sequence Retry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace PR #97's per-broadcast nonce query with bounded retry that resynchronizes only after an explicit Cosmos sequence mismatch.

**Architecture:** Keep the cached account sequence as the zero-overhead fast path. A shared helper owns the retry loop, calls a single-attempt broadcaster, refreshes the caller-owned sequence cache only after `ErrWrongSequence`, and reports the sequence used by the successful attempt. Existing account locks remain the serialization boundary.

**Tech Stack:** Go 1.25, Cosmos SDK transaction types, `github.com/mocachain/moca/v2/sdk/client`, Testify, repository Make targets.

## Global Constraints

- Do not query the chain sequence before a successful first broadcast attempt.
- Retry only `sdkErrors.ErrWrongSequence`; never retry transport or ambiguous errors.
- Use the existing `BroadcastTxRetry` bound.
- Do not add an in-memory queue, confirmation wait, persistent outbox, EVM changes, or broadcast-mode changes.
- PR #100 remains responsible for `DiscontinueBucket` and `UpdateSPPrice` retry coverage.
- Preserve one Linear issue per PR and keep changes limited to signer sequence recovery.

---

### Task 1: Shared Cosmos Sequence Retry Helper

**Files:**
- Modify: `modular/signer/signer_client.go:33-42`
- Modify: `modular/signer/signer_client.go:2544-2570`
- Replace tests: `modular/signer/signer_client_broadcast_test.go`

**Interfaces:**
- Consumes: `getCosmosNonceFn`, `broadcastCosmosTxFn`, `BroadcastTxRetry`, `sdkErrors.ErrWrongSequence`.
- Produces: `broadcastTxWithSequenceRetry(ctx context.Context, gnfdClient *client.MocaClient, msgs []sdk.Msg, txOpt *ctypes.TxOption, nonceCache *uint64, opts ...grpc.CallOption) (string, uint64, error)`.
- Produces: `broadcastTxOnce(ctx context.Context, gnfdClient *client.MocaClient, msgs []sdk.Msg, txOpt *ctypes.TxOption, opts ...grpc.CallOption) (string, error)`.

- [ ] **Step 1: Replace the pre-query tests with failing retry tests**

Write tests that stub the existing seams and assert the exact call sequences:

```go
func TestBroadcastTxWithSequenceRetryUsesCachedNonceOnFirstAttempt(t *testing.T) {
    restoreBroadcastRetrySeams(t)
    nonceCache := uint64(9)
    nonceQueries := 0
    getCosmosNonceFn = func(*client.MocaClient, context.Context) (uint64, error) {
        nonceQueries++
        return 0, nil
    }
    var attempted []uint64
    broadcastCosmosTxFn = func(_ *client.MocaClient, _ context.Context, _ []sdk.Msg, txOpt *ctypes.TxOption, _ ...grpc.CallOption) (*tx.BroadcastTxResponse, error) {
        attempted = append(attempted, txOpt.Nonce)
        return &tx.BroadcastTxResponse{TxResponse: &sdk.TxResponse{TxHash: "ABC123"}}, nil
    }

    hash, usedNonce, err := (&MocaChainSignClient{}).broadcastTxWithSequenceRetry(
        context.Background(), nil, nil, &ctypes.TxOption{}, &nonceCache,
    )

    require.NoError(t, err)
    require.Equal(t, "ABC123", hash)
    require.Equal(t, uint64(9), usedNonce)
    require.Equal(t, []uint64{9}, attempted)
    require.Zero(t, nonceQueries)
}
```

Add focused cases for mismatch then success, non-sequence error, nonce query error, and retry exhaustion. The assertions must prove the exact broadcast nonces and query counts.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
go test ./modular/signer -run 'TestBroadcastTxWithSequenceRetry' -count=1
```

Expected: build failure because `broadcastTxWithSequenceRetry` is undefined.

- [ ] **Step 3: Implement the single-attempt boundary and bounded retry helper**

Replace the current pre-query behavior with:

```go
func (client *MocaChainSignClient) broadcastTxWithSequenceRetry(
    ctx context.Context,
    gnfdClient *client.MocaClient,
    msgs []sdk.Msg,
    txOpt *ctypes.TxOption,
    nonceCache *uint64,
    opts ...grpc.CallOption,
) (string, uint64, error) {
    var err error
    for attempt := 0; attempt < BroadcastTxRetry; attempt++ {
        nonce := *nonceCache
        txOpt.Nonce = nonce
        hash, broadcastErr := client.broadcastTxOnce(ctx, gnfdClient, msgs, txOpt, opts...)
        if broadcastErr == nil {
            return hash, nonce, nil
        }
        err = broadcastErr
        if !errors.IsOf(err, sdkErrors.ErrWrongSequence) || attempt == BroadcastTxRetry-1 {
            return "", nonce, err
        }
        refreshed, refreshErr := getCosmosNonceFn(gnfdClient, ctx)
        if refreshErr != nil {
            return "", nonce, errors.Wrap(refreshErr, "failed to get nonce on chain")
        }
        *nonceCache = refreshed
    }
    return "", *nonceCache, err
}
```

Move the existing CheckTx/error mapping into `broadcastTxOnce` without a nonce query. Keep `broadcastCosmosTxFn` as its broadcast seam.

- [ ] **Step 4: Run the focused tests and verify GREEN**

Run:

```bash
go test ./modular/signer -run 'TestBroadcastTxWithSequenceRetry' -count=1
```

Expected: all retry-helper tests pass.

- [ ] **Step 5: Commit the helper and regression tests**

```bash
git add modular/signer/signer_client.go modular/signer/signer_client_broadcast_test.go
git commit -m "fix(signer): retry cosmos sequence mismatches"
```

### Task 2: Migrate Existing Retry-Capable Cosmos Callers

**Files:**
- Modify: `modular/signer/signer_client.go:258-3070`
- Test: `modular/signer/signer_client_broadcast_test.go`

**Interfaces:**
- Consumes: `broadcastTxWithSequenceRetry(..., nonceCache *uint64, ...) (string, uint64, error)` from Task 1.
- Produces: every existing loop-based Cosmos caller uses the shared retry helper and advances its cache from `usedNonce`.

- [ ] **Step 1: Add a source-level guard for the migration boundary**

Add a test that parses `signer_client.go` and verifies that migrated production calls use `broadcastTxWithSequenceRetry` with one of these cache pointers:

```go
&client.sealAccNonce
&client.operatorAccNonce
```

The guard must explicitly allow `DiscontinueBucket` and `UpdateSPPrice` to retain their current single-attempt implementation until PR #100 rebases.

- [ ] **Step 2: Run the migration guard and verify RED**

Run:

```bash
go test ./modular/signer -run 'TestCosmosSequenceRetryCallSites' -count=1
```

Expected: failure listing call sites that still use the old outer retry loops.

- [ ] **Step 3: Replace each existing outer retry loop with one helper call**

For seal-account operations use:

```go
txHash, nonce, err := client.broadcastTxWithSequenceRetry(
    ctx, client.mocaClients[scope], []sdk.Msg{msg}, txOpt, &client.sealAccNonce,
)
if err != nil {
    // Preserve the caller's existing log and typed module error.
    return "", callerError
}
client.sealAccNonce = nonce + 1
return txHash, nil
```

For operator-account operations use the same shape with:

```go
&client.operatorAccNonce
client.operatorAccNonce = nonce + 1
```

Migrate `SealObject`, `RejectUnSealObject`, `CreateGlobalVirtualGroup`, `CompleteMigrateBucket`, `SwapOut`, `CompleteSwapOut`, `SPExit`, `CompleteSPExit`, `RejectMigrateBucket`, `Deposit`, `DeleteGlobalVirtualGroup`, `DelegateCreateObject`, `DelegateUpdateObjectContent`, `ReserveSwapIn`, `CompleteSwapIn`, `CancelSwapIn`, and `SealObjectV2`.

Do not migrate `DiscontinueBucket` or `UpdateSPPrice`; PR #100 owns those two paths.

- [ ] **Step 4: Format and run signer tests**

Run:

```bash
gofmt -w modular/signer/signer_client.go modular/signer/signer_client_broadcast_test.go
go test ./modular/signer/... -count=1
go test -race ./modular/signer/... -count=1
```

Expected: formatting produces no subsequent diff and all signer tests pass with no race report.

- [ ] **Step 5: Commit the call-site migration**

```bash
git add modular/signer/signer_client.go modular/signer/signer_client_broadcast_test.go
git commit -m "refactor(signer): centralize cosmos sequence retries"
```

### Task 3: Full Verification, Self-Review, and PR Update

**Files:**
- Modify if required: `docs/superpowers/specs/2026-08-05-moca-896-sequence-retry-design.md`
- Modify: GitHub PR #97 body and review comment.

**Interfaces:**
- Consumes: completed retry-only implementation from Tasks 1-2.
- Produces: a clean branch, full local validation evidence, updated PR description, and a new CI run.

- [ ] **Step 1: Run full repository validation**

Run:

```bash
make test-local
make lint
go vet ./...
go build ./...
git diff --check
```

Expected: every command exits 0; lint reports zero issues.

- [ ] **Step 2: Review the complete branch diff**

Run:

```bash
git diff --check origin/main...HEAD
git diff --stat origin/main...HEAD
git diff origin/main...HEAD -- modular/signer docs/superpowers/specs docs/superpowers/plans
```

Verify that the diff contains no per-broadcast nonce query, no queue/outbox, no EVM change, and no `DiscontinueBucket`/`UpdateSPPrice` retry implementation.

- [ ] **Step 3: Commit any documentation correction found by self-review**

If the implementation required a factual design correction, edit only the affected sentence and commit:

```bash
git add docs/superpowers/specs/2026-08-05-moca-896-sequence-retry-design.md
git commit -m "docs: align cosmos retry design"
```

If no correction is required, do not create an empty commit.

- [ ] **Step 4: Push #97 and update its PR description**

```bash
git push origin fix/moca-896-confirm-cosmos-nonce
gh pr edit 97 --repo mocachain/moca-storage-provider --body-file /tmp/moca-896-pr-body.md
```

The body must state that normal broadcasts perform no added nonce query, only explicit sequence mismatches resync, non-sequence errors are not retried, and PR #100 must rebase after #97.

- [ ] **Step 5: Monitor all GitHub checks**

Run:

```bash
gh pr checks 97 --repo mocachain/moca-storage-provider --watch --interval 30
```

Expected: 15 checks pass, with zero pending and zero failed checks.
