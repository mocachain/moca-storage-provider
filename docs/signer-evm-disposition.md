# Signer EVM state: manual disposition

The persisted signer never clears an EVM transaction it cannot prove the end of. A
pending row stays until its receipt appears or the node refuses its first send, and a
`blocked_unknown` outcome stays forever. This document covers the four commands that
end those rows by hand, what they decide from, and what nobody can decide for you.

All four connect to the SP DB directly with the CLI account (the runtime grants in the
[upgrade runbook](signer-evm-upgrade-runbook.md)), hold no signer lease, and can run
while the signer runs. Each write is one transaction together with its audit row, and
is conditioned on the exact row the decision was made on. A signer that changes the row
first wins; the command then exits 3.

```
moca-sp signer-evm resolve         -c config.toml --scope operator --tx-hash 0x.. --operator NAME --reason TEXT \
        [--evidence-tx 0x..] [--evidence-cosmos-tx HASH] [--scan-blocks N] [--accept-unknown] [--accept-gap] [--dry-run]
moca-sp signer-evm resolve-outcome -c config.toml --outcome-id ID --operator NAME --reason TEXT \
        [--evidence-tx 0x..] [--evidence-cosmos-tx HASH] [--scan-blocks N] [--dry-run]
moca-sp signer-evm release         -c config.toml --outcome-id ID --operator NAME --reason TEXT \
        --decision-ref URL --business-evidence TEXT [--dry-run]
moca-sp signer-evm dispose-outcome -c config.toml --outcome-id ID --operator NAME --reason TEXT [--dry-run]
```

Take `--tx-hash`, `--scope` and `--outcome-id` from `moca-sp signer-evm list`. Every
command accepts `--json`.

## What a decision rests on

`resolve` and `resolve-outcome` first verify the row's signed transaction evidence
(V(e)): the stored raw bytes decode, hash to the row's `tx_hash`, recover to its
`from_address`, and carry its nonce and EVM chain id; the row agrees with the evidence
column by column. Everything about the original transaction (sender, nonce N, call
content, chain id, the block it was recorded at) comes from that evidence. The RPC is
never asked for the original transaction, so a node that never saw it, or pruned it,
changes nothing.

The EVM RPC (`[Chain] RpcAddress`) must serve the evidence's EVM chain id, and the
CometBFT RPC (`[Chain] ChainAddress`), when Cosmos transactions are searched, the
row's chain id. The command then reads the chain at a height H, waits for a height
H' > H (`--block-wait`, default 2 minutes), reads it again, and acts only when both
reads reach the same conclusion. Receipts and transactions above the height do not
count at that height.

At each height:

1. **The original receipt.** Success keeps the original hash as the operation's
   result. Failure lets the operation be signed again.
2. **No receipt: the sender's nonce C at H against N.**
   - C = N: the original bytes are still the next valid transaction and can be included
     at any time. Refused.
   - C < N: a nonce gap (below).
   - C > N: some transaction T used N. The command looks for it: `--evidence-tx`,
     then `--evidence-cosmos-tx`, then, with `--scan-blocks N`, the EVM blocks from the
     recorded block (at most 10000, never past H) and a CometBFT `tx_search` for
     `tx.acc_seq='<sender>/<N>'`. T must be from the sender at nonce N, other than the
     original, at or below the height.
3. **What T proves.** A different hash alone proves nothing: T can execute the same
   business with another fee, or by another call path.

| Branch | T | Conclusion | `resolve` | `resolve-outcome` |
|---|---|---|---|---|
| a | failed (EVM status 0, Cosmos code != 0) | the operation did not run (V1 below) | delete pending; may sign again | delete outcome |
| b | succeeded, signed by this signer (its evidence row passes V(e)) for another operation | unrelated | delete pending; may sign again | delete outcome |
| c | succeeded plain EVM transfer: no calldata, recipient below 0x10000 excluded, no code at the recipient in T's block and the block before | unrelated | delete pending; may sign again | delete outcome |
| d | succeeded EVM transaction with the original's `(to, value, input)`, or b for the same operation | the operation ran in T | outcome `succeeded`, result T's hash | update to `succeeded`, result T's hash |
| d' | succeeded Cosmos transaction with a message whose signer fingerprint is the operation's | the operation ran in T | outcome `executed_elsewhere` | update to `executed_elsewhere` |
| e | anything else succeeded (other contract or precompile call, contract creation, no matching Cosmos message, code history unavailable, undecodable Cosmos tx) | unknown | refused; `--accept-unknown`: `blocked_unknown(other_tx_unverified)` | refused (exit 2) |
| f | T not found | unknown | refused; `--accept-unknown`: `blocked_unknown(receipt_missing)` | refused (exit 2) |

`succeeded` hands the result hash to the next request for the operation instead of
signing it. `executed_elsewhere` refuses the operation with T's hash in the error;
the business owner confirms and then runs `dispose-outcome`. `blocked_unknown` refuses
the operation until `resolve-outcome` or `release`. `--accept-unknown` therefore never
lets an operation be signed again: it only unblocks the scope for other operations.

### Nonce gap (branch g)

C < N means the node refused the original's first send as nonce too high and that
refusal was lost (a failed write and a restart). No transaction used N, so there is no
T to look for; the original bytes can still be included once the account reaches N.
The signer keeps re-sending them, which is the only automatic recovery.

`resolve` confirms the gap only when, at both heights, the nonce at the height equals
the node's pending nonce and a re-send of the same bytes is refused as nonce too high
(a JSON-RPC error matching the signer's too-high patterns). The re-send is the probe:
its effect is the same as the signer's own re-sends, and if the gap has closed the
bytes are included and the signer finds the receipt. With `--accept-gap` the pending
row becomes `blocked_unknown(nonce_gap)`. The scope then signs other operations from
the chain nonce, which fills the gap; once N is used, `resolve-outcome` settles the
operation from the chain. If nothing else is signed in the scope, N is never used and
only `release` ends the row.

`--dry-run` never sends anything, so it cannot confirm a gap: it reports "not probed"
and exits 2.

## Release (D8)

`release` deletes a `blocked_unknown` outcome without on-chain proof. The operation is
then signed again, and if it did run before, it runs twice. Every release, whatever the
reason (`receipt_missing`, `other_tx_unverified`, `nonce_gap`), needs:

- a Board decision on this one transaction, linked with `--decision-ref`;
- the business owner's verifiable evidence that the operation did not take effect
  (for example: the object is not sealed at height H, the deposit balance did not
  change), given as `--business-evidence`.

There is no standing rule that pre-approves any class of release. The CLI only checks
that both values are non-empty and records them in the audit; it cannot check that the
decision exists or that the evidence is true. That check is the operator's and the
Board's.

There is no `--force` on any command (D5). Insufficient funds are fixed by funding the
account under its own authorization; the original bytes are then included. A
replacement transaction at the same nonce is separate work.

## Dispose

`dispose-outcome` deletes a `succeeded` or `executed_elsewhere` outcome, when the
business owner confirms the operation will not be retried, or when a new, identical
request must be signed instead of receiving the old result.

## Exit codes

| Code | Meaning | DB |
|---|---|---|
| 0 | done (or, with `--dry-run`, would be done) | changed with one audit row (unchanged in a dry run) |
| 1 | usage, RPC or DB error; chain id mismatch | unchanged; RPC errors and chain id mismatches audit `manual_rejected` |
| 2 | the chain does not decide the case, the heights disagree, or a dry run could not probe | unchanged; `manual_rejected` |
| 3 | the row is gone or not in the expected state (stale id, second run, changed meanwhile) | unchanged; `manual_rejected`, except a `resolve` whose `--tx-hash` or `--scope` does not match the pending row, which writes nothing |
| 4 | the evidence row is missing or fails V(e) | unchanged; `evidence_invalid` |

A DB read error while reading the evidence exits 1 and writes nothing. `--dry-run`
never writes the DB or the audit and never broadcasts.

If a commit's result is unknown (the connection dropped during COMMIT), the command
reads the row and the audit back; when it cannot confirm either way it exits 1 and says
so. Check `list` and the audit before running it again.

## Audit

Every manual row in `signer_evm_audit` carries `actor` (`--operator` plus the OS user),
`reason`, `tool_version`, the chain nonce and height of the deciding read, the
occupying transaction (`evidence_tx_hash`, `evidence_kind`) when there is one, and for
`release` the `decision_ref` and the business evidence in `detail`. Events:
`manual_resolve_success`, `manual_resolve_failed`, `manual_resolve_other_tx_failed`,
`manual_resolve_other_tx_unrelated`, `manual_resolve_other_tx_same_call`,
`manual_resolve_executed_elsewhere`, `manual_resolve_unknown`,
`manual_resolve_nonce_gap`, `manual_outcome_resolved`, `manual_release_unknown`,
`manual_dispose_outcome`, `manual_rejected`, `evidence_invalid`.

## Verified facts and limits

These were checked against the exact dependencies in `go.mod` (node
`github.com/mocachain/moca/v2 v2.0.0-rc0`, `github.com/cosmos/evm v0.6.0`,
`github.com/mocachain/moca-cosmos-sdk v1.4.0-rc0`), not on a running node.

**V1: a failed transaction leaves no state.** Branch a depends on it.

- EVM: `cosmos/evm x/vm/keeper/state_transition.go`, `ApplyTransaction`, runs the
  message on `ctx.CacheContext()`; when `res.Failed()` it replaces that cache with a
  fresh one and never calls the first `commitFn`. Moca precompiles write through
  `RunNativeAction` (`precompiles/common/precompile.go`), into the StateDB cache
  context derived from the same discarded context. Only `PostTxProcessing` hooks write
  after a failure, and Moca's `app/app.go` registers no EVM hooks (`SetHooks` is only
  called on staking and gov).
- Cosmos: `moca-cosmos-sdk baseapp/baseapp.go`, `runTx`, runs the messages on
  `cacheTxContext` and calls `msCache.Write()` only when `err == nil`.

**`tx.acc_seq`.** `moca-cosmos-sdk x/auth/ante/sigverify.go` emits
`tx.acc_seq = "<address>/<sequence>"` for every signer of an ordered Cosmos
transaction, with the address from `sdk.AccAddress.String()`, which in Moca is the
EIP-55 checksummed `0x` address (`types/address.go`, `getETHAddrString`). The command
searches with `common.Address.Hex()`, the same format. EVM transactions do not emit it;
they are found by the block scan. Cosmos and EVM transactions share the account
sequence (`cosmos/evm ante/evm/09_increment_sequence.go`).

**Limits.**

- Looking up a Cosmos transaction, by `--evidence-cosmos-tx` or by `tx.acc_seq`,
  needs the node's transaction index. Without it a Cosmos occupier is not found and
  the case is branch f. Whether production nodes index transactions is an ops check
  (G1/Q6).
- Branch c reads `eth_getCode` at T's block and the block before. A node without that
  historical state (not an archive node) gives branch e.
- The scan reads at most 10000 blocks from the recorded block. A pruned block is
  skipped and noted; if T was in it, the case is branch f.
- Every address below 0x10000 is treated as a possible precompile (Moca's are
  0x1000–0x2002 in `types/contract.go`; cosmos/evm's 0x100–0x806), so a transfer to
  one is never branch c.
- Branch e is deliberately broad: an external call with other calldata is not decoded,
  even when it could be proven unrelated.

## What this does not guarantee (R6)

The persisted signer guarantees that the result of a transaction it signed survives
restarts, background resolution and these manual steps. It does not guarantee
end-to-end exactly-once: if the signer returns a hash and the response is lost on the
way to the caller, the caller's retry is signed again. Callers that need exactly-once
must deduplicate on their side.
