# MOCA-896 Cosmos Nonce Confirmation Design

Linear: https://linear.app/moca/issue/MOCA-896

## Problem

Cosmos transactions are broadcast with `BROADCAST_MODE_SYNC`. A zero CheckTx code
only means the mempool accepted the transaction. The signer currently treats that
response as success, returns to the operation, and advances its cached account
sequence. If the transaction is dropped before inclusion, the cache remains ahead
of the chain and later transactions repeatedly fail with the wrong sequence.

## Considered Approaches

1. Confirm inclusion in the shared `broadcastTx` helper. This preserves SYNC
   broadcasting, covers every Cosmos signing operation at one boundary, and keeps
   existing callers from advancing their nonce until DeliverTx succeeds.
2. Resync the nonce before every broadcast. This avoids stale caches but adds a
   chain query to every attempt and still permits races between the query and send.
3. Change broadcasts to block mode. The current client and node path is built
   around SYNC mode, and block-mode support is not guaranteed by the gRPC service.

Approach 1 is selected because it directly aligns the cached sequence with chain
inclusion while preserving the existing broadcast mode and retry structure.

## Design

`MocaChainSignClient.broadcastTx` will retain the existing broadcast and CheckTx
validation. After a zero CheckTx code, it will call the existing consensus
`ConfirmTransaction` API with the returned hash. It will only return the hash when
the transaction is found on chain and its DeliverTx code is zero.

If confirmation fails or DeliverTx reports a non-zero code, `broadcastTx` returns
an error. Existing callers already advance their cached nonce only after a nil
error, so no per-operation changes are required and failed confirmation leaves the
cached sequence unchanged. The existing caller retry policy remains intact.

A narrow function seam around confirmation will make the shared boundary testable
without a live chain. It follows the existing `waitForEvmTxFn` seam and will be
restored by each test.

## Tests

Focused unit tests will verify:

- a SYNC-accepted transaction is not reported successful when confirmation fails;
- a confirmed transaction with a zero DeliverTx code returns its hash;
- a confirmed transaction with a non-zero DeliverTx code returns an error.

The first test will be added and run before production changes to record RED. After
the minimal implementation, all focused tests and the signer package tests must be
GREEN. Repository lint, vet/type checking, and a build check will run before the
Draft PR is opened.
