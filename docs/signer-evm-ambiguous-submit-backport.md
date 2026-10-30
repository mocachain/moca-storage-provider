# Ambiguous EVM submission: release/1.4.x backport design

This design specifies the minimum release backport for [MOCA-1284](https://linear.app/mocanetwork/issue/MOCA-1284), using the signer changes from [#199](https://github.com/mocachain/moca-storage-provider/pull/199). The initial Draft contains this document only. Implementation has not started; no release regression, mutation test, build, or integration result is claimed by this document.

## Problem and safety guarantee

The release signer calls abigen session methods that sign and broadcast together. It records pending state only after a successful send response. If the node admits a transaction at nonce N but the response is lost, no pending record protects the operation. A retry signs again after querying gas price. Identical bytes may hit CometBFT's transaction cache, but changed gas, cache eviction, or a different node can bypass that protection. Once the original commits, an explicit nonce error can refresh the cached nonce to N+1 and allow the same operation to execute again.

`DepositEvm` has no operation-level on-chain deduplication and can transfer funds twice. `CreateGlobalVirtualGroupEvm` also carries a deposit, although identical secondary SP sets within the same family are rejected on chain. A receipt timeout after a successful send already has pending protection; the missing interval is admission before an unsuccessful send response.

The guarantee is: while pending evidence remains in this process, an operation with an ambiguous send outcome must retain its signed transaction and may only rebroadcast those identical bytes until its receipt establishes the outcome. A consumed nonce alone does not establish that outcome.

## Pinned source and target

| Role | Revision |
| --- | --- |
| Target release baseline | `96565bcb43c1436d3e3c1b1a80b62f94b350a661` |
| Source #199 final head | `5634f7081e4c5576e9133758405e2f6543a0f89f` |
| Source #199 merge | `67c821bf830cdad4971725798794f4bb5acb8d5c` |

The source head and merge have the same tree. The release baseline still directly broadcasts from sessions and lacks `NoSend`, `submitEvmTx`, and stored raw transaction evidence. Existing release signer backports [#145](https://github.com/mocachain/moca-storage-provider/pull/145) and [#174](https://github.com/mocachain/moca-storage-provider/pull/174) do not supply this guarantee. Recheck the target ref and related feedback before implementation; changes affecting these assumptions require a revised design and review.

Port the logical changes in these five source files, adapting them to release rather than copying current main files:

- `modular/signer/signer_client.go`
- `modular/signer/signer_client_evm_fake_test.go`
- `modular/signer/signer_client_evm_submit_test.go`
- `modular/signer/signer_client_nonce_error_test.go`
- `modular/signer/signer_client_pending_evm_test.go`

Do not port unrelated changes merged into the source branch, main's DB/schema, durable state store, lease, migration or disposition CLI, or dependency/configuration changes. Do not port [#188](https://github.com/mocachain/moca-storage-provider/pull/188)'s nonce-only pending clearing. That PR closed as superseded, not as evidence that clearing was safe. Do not include [#222](https://github.com/mocachain/moca-storage-provider/pull/222)'s bounded retry addition for the two methods without loops. Durable recovery requires a separate design; it is not an implicit dependency of this minimum backport.

## Submission boundary and ordering

Keep existing operation fingerprints, scope locks, key selection, and nonce caches. Resolve any pending transaction in the scope before preparing another operation. Set `txOpts.NoSend = true` on the exact options passed by value to `Create*Session`, before creating the session. The session method then signs locally without broadcasting.

All 19 EVM submission entry points must use the common `submitEvmTx` boundary:

| Entry points | Retry boundary |
| --- | --- |
| `SealObjectEvm`, `RejectUnSealObjectEvm`, `CreateGlobalVirtualGroupEvm`, `CompleteMigrateBucketEvm`, `SwapOutEvm`, `CompleteSwapOutEvm`, `SPExitEvm`, `CompleteSPExitEvm`, `RejectMigrateBucketEvm`, `DepositEvm`, `DeleteGlobalVirtualGroupEvm`, `DelegateCreateObjectEvm`, `DelegateUpdateObjectContentEvm`, `ReserveSwapInEvm`, `CompleteSwapInEvm`, `CancelSwapInEvm`, `SealObjectV2Evm` | Preserve existing bounded loops; only a verified first-send nonce refusal permits nonce refresh and another signature. |
| `DiscontinueBucketEvm`, `UpdateSPPriceEvm` | Preserve #199 behavior: a verified nonce refusal may refresh the cache, but the current call returns an error and the caller must retry. No #222 loop is added. |

The internal helper owns cancellation checks, pending evidence, submission classification, and identical-byte rebroadcast. Keep `getNonceOnChainFn` as an internal test seam; no public RPC or gRPC signature changes are needed.

```text
resolve pending for the scope
prepare options with an explicit nonce and existing fee limits
set NoSend before the session copies those options
sign locally
check caller context
record operation fingerprint, nonce, hash, signed raw transaction
send the recorded transaction with the caller context
classify the result; preserve evidence if ambiguous
wait for receipt; resolve the recorded hash
```

Cancellation before recording must produce zero records and zero sends. Cancellation during send, or after recording but before delivery, leaves the outcome protected. A subsequent call with a fresh context must be able to send the recorded transaction even when the first call delivered zero requests to the server. Test the server's observed sends separately from calls to a client seam.

Keep #199's caller-side nonce-cache updates: a non-empty submitted hash advances the cache to N+1 even when receipt waiting returns an error; rebroadcast does not allocate another nonce. A successful send response is not confirmation: `ethclient.SendTransaction` discards the response hash, and a nil error only reports submission acceptance. Receipt processing remains authoritative.

## Conservative refusal classification

Only a verified JSON-RPC refusal establishing non-admission on the first send, with no previous ambiguous attempt, may release that submission's pending evidence. An `rpc.Error` type or a `failed to broadcast transaction` prefix alone is insufficient.

Classify HTTP failures, transport failures, timeouts, cancellation, `-32002`, and `-32603` before matching rejection reasons. An HTTP 503 body containing an allowlisted phrase remains ambiguous. Unknown errors, tx-cache hits, `replacement ... underpriced`, `account sequence mismatch`, and `invalid sequence` remain ambiguous; they must not trigger the old broad `isNonceError` path.

Verified initial nonce-low/high refusals may refresh the nonce and permit re-signing according to the existing retry boundary. A verified initial non-nonce validation/funds/fee refusal returns an error without re-signing and leaves the nonce cache unchanged. Validate every retained allowlist reason against the pinned release node's actual response and pre-admission path. If the release RawLog cannot establish the same guarantee, keep it ambiguous instead of broadening substring matching.

Once any send is ambiguous, a later nonce refusal, cache rejection, or other explicit refusal cannot clear pending evidence. Rebroadcast only the stored bytes, preserving nonce and hash. Preserve #199's bounded rebroadcast behavior, including its per-attempt timeout and stopping at an explicit JSON-RPC answer; this is separate from granting permission to sign again. Receipt waiting retains the existing five-minute upper bound and scope lock behavior.

## Receipt resolution and error contract

`resolvePendingEvmTx` queries the recorded hash. With `NotFound`, another receipt error, or `(nil, nil)`, it retains pending evidence, attempts to rebroadcast the original signed transaction, and returns the original hash with the unconfirmed error. It must not clear pending merely because the Cosmos/account nonce exceeds the recorded nonce, including when another hash consumed that nonce.

A successful receipt clears the matching pending hash; the same operation returns that hash without another signature. A failed receipt also establishes a terminal outcome and clears pending under the existing #199 retry/error contract. Keep operation fingerprint comparisons and hash-matched clearing so one operation cannot erase another pending record.

Preserve the existing public hash-plus-error contract and method-specific errors. In particular, return local construction/signing errors in the six loops that report `broadcastErr`: SealObject, RejectUnSealObject, CreateGVG, CompleteMigrateBucket, SwapOut, and CompleteSwapOut. Do not lose the error or return `error: <nil>`.

## Release compatibility and preserved constraints

The release baseline uses Go language `1.23.6` and toolchain `go1.26.10`. Its `evmos/v12` replacement pins `mocachain/moca/v12` at `9f8f08384ec3dabac9435d11830f9ce728a2581c`; its geth replacement pins `e849a5b4edee084d1fdba7d722a5c1a9301e3f35`. That geth bind implementation supports `NoSend` before `SendTransaction`, so a dependency upgrade is not required by this design. Full compilation and ABI compatibility remain implementation acceptance checks.

Retain release v12 message types, precompile addresses, ABI/session constructors, denomination and integer conversions. Do not import main's moca/v2 or newer cosmos/evm protocol types. Preserve max EVM gas-price enforcement, gas limits, deposit bounds from #174, validation before submission, signer key validation, and existing scope serialization. The change must not bypass these checks to make the fake or port compile.

The release v12 `SendRawTransaction` path builds the Cosmos transaction and uses `BroadcastSync`/`ABCIError`; it differs from modern experimental EVM mempool behavior. Its actual rejection text and admission boundary must be checked before accepting any refusal classification. Source compatibility is not proof of the running node's configuration.

## Implementation and acceptance matrix

All checks below are required future work. The documentation-only Draft does not execute them or claim GREEN results.

First add the release-compatible fake, nonce seam, and core regression. On the unchanged release implementation, make the fake admit nonce N, lose the response, change suggested gas, report N consumed, and accept N+1 if attempted. Assert no second accepted deposit at N+1. RED means this behavior assertion fails; dependency setup or compilation failure does not count. Then implement the five-file logical port and rerun the identical assertion for GREEN.

| Guarantee | Required evidence on the implementation head |
| --- | --- |
| No duplicate fund operation | Core RED fails behaviorally on release; GREEN accepts exactly one operation and preserves the original raw bytes, hash, and nonce across retries and repeated calls. |
| Missing receipt protects scope | Consumed nonce plus `NotFound` and `(nil, nil)` both retain pending and return original hash/unconfirmed. Fake Cosmos/EVM nonce queries share the same state. Test a different hash consuming N too. |
| Refusal recovery is narrow | Verified initial nonce low/high allows refresh; verified initial non-nonce refusal does not re-sign; cache/underpriced/sequence text, nil response, HTTP 503 containing allowlist text, `-32002`, `-32603`, unknown errors, and refusal after ambiguity retain protection. |
| Cancellation preserves evidence | Before record: zero record/send. During send and before delivery: original evidence remains. A fresh context recovers an actual server-observed zero-delivery first attempt using only original raw. |
| All 19 entry points comply | AST guard checks the same options object and `NoSend < Create*Session < session method`; helper guard checks `context check < record < send`. Behaviorally exercise both methods without loops. |
| Pending resolves correctly | Successful/failed receipt, repeated same operation, different operation in the same scope, timeout, local signing failure, cached nonce, and hash-matched clearing follow #199. |
| Existing release protections hold | Signer/scope/key regressions, fee cap failures, deposit bounds, v12 ABI/types, and error propagation remain covered. |
| Known restart limit is explicit | Preserve the test demonstrating pending loss on restart; do not describe restart as recovery or present this limitation as solved. |

Run mutations separately and restore the implementation between them. Each must fail a relevant assertion: treat cache errors as nonce refusal; clear pending on a later refusal; remove pre-record context checking; match allowlist text before HTTP classification; deprioritize `-32603`; skip resolve-time rebroadcast; set `NoSend` after the session copies options; clear pending on consumed nonce without receipt. A mutation that fails only because an unrelated Cosmos connection/setup fails is not evidence that the safety test detects it.

Serialize tests that replace global seams and restore them with cleanup. Keep the fake offline and deterministic. Record the exact code head, commands, assertion outputs, and restored tree for each RED/GREEN or mutation result.

| Consumer / integration boundary | Required verification |
| --- | --- |
| Signer gRPC dispatch | All 19 dispatched EVM methods reach the common boundary; wire messages and public method signatures remain unchanged. |
| SP manager, executor, GC and signer callers | A hash plus unconfirmed error remains an in-flight outcome. Retry through the same scope so pending resolution runs; do not switch keys/scopes or allocate a new nonce to bypass it. Verify task retry and confirmation handling. |
| `moca-cmd`, `moca-go-sdk` | Compile/contract checks against the release API; no protocol/dependency upgrade is introduced by the backport. |
| Release node and geth fork | Check v12 ABI, exact RPC non-admission reasons, receipt lookup, and the pinned `NoSend` implementation. |
| `moca-e2e`, `moca-devcontainer` | Pin compatible release chain/SP/SDK refs; run SP, SP Go-SDK, SP Exit, and BlockSyncer E2E on the implementation head in an authorized test environment. |

Use the release toolchain and dependencies. Required focused commands include:

```sh
timeout 600 go test ./modular/signer/... -race -count=1
timeout 600 go test ./modular/signer -run 'TestEvmSubmit|TestEvmLocal|TestDepositEvm' -race -count=20
```

Also run the release README/Makefile and CI build/lint checks and existing affected signer tests. Keep hard command time limits; long suites belong in CI. Report unavailable dependencies, failures and skipped checks explicitly. Historical #199/main CI and historical lint exceptions do not prove that a new release head passes.

## Operational prerequisites and retained limits

Before any later deployment, establish the actual node/SP versions and topology, account/scope ownership, and receipt retrieval by known transaction hash through the configured RPC. Receipt implementations can fall back from an indexer to transaction search; an indexer setting alone does not prove retrieval works. Check the actual mempool configuration. Experimental EVM mempool behavior, including queued nonce gaps or nil rejection responses, requires separate review.

Use one coordinated signer writer for each protected account/scope; this in-memory design does not coordinate independent processes. Inventory unresolved hashes before upgrade or restart and establish their chain outcomes. Do not connect production RPC/DB, start a chain, sign real transactions, or deploy as part of documentation or offline regression work; those operations need their own authorized execution.

Pending state remains in memory. Process restart loses it, and a subsequent retry may sign at a fresh nonce. A fixed transaction whose nonce was consumed elsewhere, whose receipt cannot be retrieved, or whose bytes are no longer affordable can block its scope indefinitely. Identical-byte rebroadcast cannot make an invalid transaction valid. Preserve these known limits; this backport neither adds durable recovery nor makes restart a safe clearing mechanism.

This design protects the ambiguous-send window while evidence is retained. It does not provide end-to-end operation deduplication after pending is cleared or lost, including a successful gRPC reply lost after confirmation. Future persistence or operator disposition work must retain its own evidence and upgrade requirements.
