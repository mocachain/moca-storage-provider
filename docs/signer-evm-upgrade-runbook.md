# Signer EVM state: upgrade runbook

This runbook takes a storage provider from the in-memory signer ("A": pending EVM
submissions live only in process memory) to the persisted signer ("B": pending
submissions, outcomes and the lease live in four SP DB tables). It also covers the
rollback from B to A.

A forgets every pending EVM submission when it stops. B cannot import them, and an
empty B table proves nothing about what A left in flight. So the first upgrade only
happens after A has drained and there is evidence of it. Without that evidence, do
not upgrade.

## What B adds

| Table | Holds | Runtime account |
|---|---|---|
| `signer_evm_signed_tx` | every signed EVM transaction B is about to broadcast (raw bytes, EVM chain id, block height at record time); append-only | `SELECT, INSERT` |
| `signer_evm_state` | pending submissions and retained outcomes; one pending per scope and one outcome per operation | `SELECT, INSERT, UPDATE, DELETE` |
| `signer_evm_lease` | one row per chain id: the signer allowed to write | `SELECT, INSERT, UPDATE, DELETE` |
| `signer_evm_audit` | every state change; append-only | `SELECT, INSERT` |

Pending rows have `state = 'pending'`; retained outcomes use `succeeded`,
`executed_elsewhere`, or `blocked_unknown`. Stored generated columns enforce the
separate uniqueness constraints. Retaining a success updates the same row, keeping
its ID and evidence reference, in the transaction that appends its audit events.

### Earlier draft schemas

This four-table schema replaces the unreleased five-table draft. Migration (using the
migration account) refuses databases containing `signer_evm_pending` or
`signer_evm_outcome`; it never automatically deletes those tables. Startup also rejects
legacy tables visible to the runtime account; restricted grants can hide them, so the
migration-account check is required. Do not drop them to bypass the check. If a draft
signer has run, stop it, preserve a database backup and its signed evidence/audit, and
reconcile every pending transaction and retained result before a separately reviewed
data migration. This command supports a fresh schema, not an automatic conversion of
existing draft state.

B fails closed: when the DB is unreachable, the tables are missing, or this process
does not hold the lease, EVM requests return an error and nothing is signed.

Only one signer per SP may write. The lease (TTL 30s, renewed every 10s) enforces this.
The old A version does not know about the lease, so A and B must never run at the
same time.

### What B does not guarantee: a lost response can still mean a second signature

B makes sure the signer itself never signs the same business operation twice while
its record of the first transaction is in the DB. It does not give end-to-end
exactly-once execution. One window stays open:

1. A request's transaction succeeds on chain, and B hands the result to that
   request. In the same DB transaction it deletes the pending row and keeps no
   outcome, because the caller is about to get the hash.
2. The gRPC response is lost on the way back: the caller times out, the connection
   drops, or the process restarts before the reply leaves.
3. The caller retries the same operation. The DB holds nothing for it any more, so
   B signs and broadcasts it again.

B keeps a little memory for the case where its own consume write had an unknown
result, but that memory is gone after a restart and does not help when the response
itself is lost. Do not read it as closing this window.

What this means for operators: a duplicate after a timeout or restart is still
possible, and it shows up as two successful hashes for one business action. When a
caller reports an EVM request that timed out, check the operation's hashes on chain
(`signer_evm_signed_tx` keeps every hash B signed, with its `operation`) before anyone retries by hand.
Callers that need exactly-once behaviour have to deduplicate on their side.

## Before you start (gates G1 and G2)

Collect these and attach them to the deployment task. If any is missing, stop.

1. Deployment shape of the production signer: standalone pod or all-in-one, replica
   count, rollout strategy.
2. How to stop the upstream modules that call the signer's EVM methods (manager,
   executor, gc): which switch, scale-down, or config change does it.
3. The longest business retry interval upstream, to size the drain window.
4. A's log retention: the logs must cover the whole time since A last restarted.
5. The MySQL `innodb_flush_log_at_trx_commit` value (expect `1`).
6. The signer replica count is 1 and you have a way to show that only one signer
   process runs at a time during the switch (`kubectl get pods` or equivalent).

### DB accounts (D2)

Three accounts. The commands below are a reference for ops; adjust the host part and
database name.

```sql
-- migration account: creates the tables, including the foreign keys
GRANT CREATE, ALTER, REFERENCES, SELECT, INDEX ON sp_db.* TO 'sp_signer_migrate'@'%';

-- signer runtime account and CLI account (same grants)
GRANT SELECT, INSERT, UPDATE, DELETE ON sp_db.signer_evm_state TO 'sp_signer'@'%';
GRANT SELECT, INSERT, UPDATE, DELETE ON sp_db.signer_evm_lease   TO 'sp_signer'@'%';
GRANT SELECT, INSERT                 ON sp_db.signer_evm_audit   TO 'sp_signer'@'%';
GRANT SELECT, INSERT                 ON sp_db.signer_evm_signed_tx TO 'sp_signer'@'%';
```

The audit and evidence tables are append-only because the runtime account cannot
`UPDATE` or `DELETE` them. Do not give it more.

In an all-in-one deployment the SP's own DB initialisation also creates these tables,
with the SP DB account. The signer then uses that account, so the append-only
guarantee rests on the code alone. Prefer a standalone signer with the accounts above.

## First upgrade: A to B

### 1. Stop new business

Stop or scale down the upstream modules so they stop sending new EVM work to the
signer. Keep A running.

### 2. Let in-flight requests finish on A

Requests already sent, and their retries, must still reach A. A hands a confirmed
result to the retry of the same operation; stopping A now would lose it, which is the
gap B closes.

Wait at least the longest upstream retry interval plus 5 minutes (the signer's
receipt wait).

### 3. Collect the drain evidence (gate G3)

All three must hold.

**(b) first: every hash A logged as unresolved has a receipt.** From A's logs since its
last restart, collect the `tx_hash` of every line with one of these messages:

- `EVM tx submitted but unconfirmed`
- `EVM tx submit outcome unknown; re-broadcasting the same signed bytes`
- `re-broadcast EVM tx`
- `re-broadcast unconfirmed EVM tx; check the hash on chain before any manual action`

**(a) the three signing accounts have nothing pending.** Run preflight with those
hashes:

```bash
moca-sp signer-evm preflight -c config.toml \
  --hash 0x... --hash 0x... \
  --json > preflight.json
```

Preflight always checks all three accounts: operator, seal and gc. It derives each
address from the configured key (the `SIGNER_*_PRIV_KEY` environment variables win,
as in the signer). If a key is not available where you run it, pass that scope's
address, for example `--address seal=0x...`; the other scopes still come from their
keys. A scope with neither a key nor an `--address` is an error, and so is an
unknown or repeated scope. There is no way to skip a scope.

For each account it reads the block height H1, then `eth_getTransactionCount(addr, H1)`
and `eth_getTransactionCount(addr, "pending")`. It waits for a later height H2 (up to
`--block-wait`, default 2 minutes) and reads both again. Check (a) holds when, for
every account, the pending count equals the count at the height in both samples and the
count did not move between them. Check (b) holds when every `--hash` has a receipt,
successful or failed.

Exit code 0: (a) and (b) hold. Exit code 2: not drained, do not upgrade. Exit code 1:
usage or connection error, fix and rerun.

What the nonce numbers mean on Moca, read from the RPC code the SP builds against
(`github.com/cosmos/evm` v0.6.0, `rpc/backend/account_info.go` and `rpc/backend/utils.go`,
`getAccountNonce`; the node's own version was not checked):

- The count at a height is the account's auth `sequence`. Cosmos and EVM
  transactions share it, so a Cosmos transaction from the same account also moves it.
- The `"pending"` count adds only `MsgEthereumTx` from that account found in the
  mempool **of the node you query**. Pending Cosmos transactions, and EVM transactions
  sitting only in other nodes' mempools, do not show up.
- If the node fails to read its mempool, it logs the error and returns the committed
  count as the `"pending"` count. Check (a) then passes without proving anything about
  the mempool.

So point the RPC at the node A broadcasts to, and treat (a) as necessary but not
sufficient. Check (b) is what covers A's own submissions.

**(c) business confirmation.** For every hash in (b) whose receipt is `success`, the
business owner confirms the effect upstream (object sealed, deposit credited, and so
on) and signs off. The CLI cannot check this.

### 4. Switch

Only when (a), (b) and (c) hold:

1. Stop A. Confirm no A process is running.
2. Run the migration with the migration account:

   ```bash
   SP_DB_USER=sp_signer_migrate SP_DB_PASSWORD=... \
     moca-sp signer-evm migrate -c config.toml
   ```

   It creates the four tables if missing, then runs the same schema check the signer
   runs on start.
3. Start B with the runtime account.
4. Confirm B holds the lease and has no pending rows:

   ```bash
   moca-sp signer-evm list -c config.toml
   ```

   Expect `lease: owner=<host>/<pid>/<nonce> ... live=true`, no `PENDING` rows and no
   `OUTCOME` rows.
5. Resume upstream dispatch.

### 5. If you cannot get the evidence

Missing logs, a hash that never gets a receipt, a business effect that does not match:
do not upgrade. A keeps running. Hand the evidence to the CTO for a Board decision on
the risk. An empty B table is not a reason to switch.

## After the switch (gate G4)

Read-only checks for the reviewer:

```sql
SELECT chain_id, owner, epoch, expires_at FROM signer_evm_lease;           -- one row, one owner
SELECT event, created_at FROM signer_evm_audit
 WHERE event = 'lease_acquired' ORDER BY id DESC LIMIT 5;                  -- at least one
```

If the lease shows two owners alternating, or no `lease_acquired`, roll back.

## Rollback: B to A

1. Stop upstream dispatch and let in-flight requests finish (steps 1 and 2 above).
2. `moca-sp signer-evm list -c config.toml` must show no `PENDING` rows and no
   `OUTCOME` rows. If there are rows, wait for them to be consumed or handled by the
   resolve commands. If you cannot clear them, do not roll back; report it.
3. Stop B (it releases the lease on exit). Confirm no B process is running. Start A.
   Resume dispatch.

Upstream stays stopped from step 2 to step 3, so no new pending row can appear after
the empty `list`.

The tables stay. If you later upgrade again, B reads whatever is left. Rows that no
longer match the chain (A used the nonce in the meantime) are handled with the
resolve commands.

## Signer behaviour you will see in logs

- `acquired the signer EVM lease`: this signer now writes.
- `signer EVM lease is held by another signer; EVM requests fail until it expires`:
  normal for up to 30s during a rollout overlap. Persisting longer means two signers
  are running.
- A signer whose renewals keep failing stops signing after 15s, and drops the lease
  after 30s.
- On start, a pending row whose evidence fails verification (corrupted raw, wrong
  chain) stops the signer from starting. `list` marks such rows `INVALID`. They need
  manual handling.
