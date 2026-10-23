# Recovery verification and staging

Recovery verifies the sealed chain object and historical storage parameters, reads
all required pieces for that version, and checks their ordered checksums. SPDB
rows and blocksyncer enumeration alone cannot prove possession. RPC, permission,
timeout and unclassified storage errors leave recovery unresolved. Chain-query errors matching the repository's `No such object` convention permit
failed-row cleanup, as do typed `ErrNoSuchObject` errors. Canceled, deadline and
Unavailable transport errors are excluded. Verify the actual error on the isolated
target chain before rollout; the local gRPC regression does not establish that
transport contract. Generic gRPC NotFound is not a deletion proof.

Each internal recovery dispatch carries protobuf field 11, `recovery_fence`, with
32 random bytes. It is excluded from cross-SP source request signing and is not an
authorization credential. Reports must match the manager's registered immutable
snapshot and current dispatch. Manager restart discards these registrations.

Executors write only flat `rstage-<fencehex>-<id>-<version>-<redundancy>-<segment>`
keys. Both source paths validate the chain before download and before staging.
The manager verifies the complete candidate, rechecks staged data before copying,
verifies canonical data, atomically publishes the full integrity row, reads it
back and checks the chain again. Publication failures retain the unfinished marker. A single retry worker retries
transient publication failures from accepted staging every three seconds, for at
most five failed publication attempts. Unknown validation, chain RPC errors at any phase, and context
cancellation/deadline failures remain unresolved without consuming this budget
or the durable data-recovery RetryTime. After exhaustion it persists a failed-object row
before releasing the slot and marking successor segments failed. Failed persistence
remains unresolved and retries only persistence. The failed-object scheduler then
owns the existing durable RetryTime (incremented once after full enqueue), maximum
five recovery attempts and manual discontinue exit. A changed chain snapshot replaces the
old attempt; invalid candidate data needs a fresh recovery attempt.
Normal single-piece recovery also uses this publisher; its other pieces must
already satisfy the complete chain checksum. Partial copies never prove recovery.

## Required local capacity configuration

Set these on the single active manager before admitting recovery:

| Variable | Meaning |
| --- | --- |
| `RECOVERY_STAGING_LEDGER_DIR` | Absolute path on persistent storage, dedicated to this piece store, preserved across manager restarts. |
| `RECOVERY_STAGING_CAPACITY_BYTES` | Positive unsigned decimal byte budget for staging, including orphan writes. |

**Upgrade impact requiring an explicit rollout decision:** without both variables,
recovery is unavailable, including ordinary single-piece recovery. There is no
automatic default or fallback. Missing, invalid or exhausted configuration refuses
admission and new retries.
The budget is independent of free disk space: provision it below available space
with room for canonical data and unrelated writers. Each attempt reserves its
whole redundancy size before admission; each subsequent dispatch reserves another
piece. Source downloads reject truncated or oversized responses. Reservations are
synced before dispatch and survive timeout and restart. Files are named
`<reserved-bytes>-<random-id>`. Failed reservations can overcount safely.
The attempt tracks its claim and each redispatch's extra claim. Retirement deletes
only accepted terminal dispatches' exact keys and releases their claims after
successful deletion. The initial whole-attempt claim is released only if no
redispatch/orphan writer exists and cleanup is confirmed for all dispatched pieces.
If any dispatch was replaced, the initial claim remains conservatively reserved;
the current extra claim can be released after terminal cleanup. Deletion failures
retain capacity. Ledger IO holds only its dedicated capacity lock and the affected
object gate, not the global registry lock. Old orphan claims can still accumulate
and eventually stop admission until operator cleanup. It is not a distributed lease, quota against unrelated
writers, or protection against losing/replacing the ledger volume.

Monitor ledger bytes (sum filename byte prefixes), file count, free storage,
`recovery staging capacity exhausted`, publication errors and unfinished tasks.
The deployment must arrange alerts before rollout. Do not delete canonical data
to free staging space. Stop new admission when an alert fires; retain failed rows.
An uninterruptible disk operation may hold its object gate past the 120-second
context budget; the code does not abandon that writer and start a second publisher.

## Reclaiming staging and capacity

Only the operator with deployment authority performs these steps. This document
records requirements; it does not authorize a deployment or cleanup.

1. Stop recovery admission and the manager. Confirm the old manager has exited.
   Stop all recovery executors that could still write to this store and retain
   process/job evidence. Queue emptiness, elapsed timeout and one success report
   do not prove writer termination. Never mix pre-fence and fenced recovery writers.
2. Inventory **every** configured disk shard or cloud bucket with backend-native,
   fully paginated listing. Capture each exact `rstage-` key, byte size and shard,
   plus total orphan bytes. No generic PieceStore listing is provided here.
3. Delete only validated exact staging keys and verify absence on each backend.
   Never use PieceStore `DeletePiecesByPrefix` for global cleanup: sharding lists
   only the shard selected by the prefix. Preserve all `s...` and `e...` keys.
4. Only after all staging writers are stopped and every staging key is absent,
   archive the ledger inventory and reset its claim files while the manager is
   stopped. Orphan claims left by retired attempts cannot be mapped reliably to surviving
   keys; do not reclaim them based on age.
5. Restart exactly one updated manager with the same persistent ledger mount and
   configured budget. Incomplete GVGs undergo fresh chain/piece verification.
   Old reports cannot rebuild registrations. Already persisted Completed GVGs
   require a separate, explicitly authorized historical audit.

Disk and four-shard disk exact-key behavior has a local regression test. Cloud
key compatibility, permission/missing-object classification, cleanup, maximum
object sizes, slow stores and multiple schedulers require deployment-specific
measurements. No cloud or load acceptance is implied by local tests.

## Verification before release

Use Go 1.26.10. `make test` requires the repository's MySQL fixture. The additional
`TestRecoveryIntegrityMySQLPublishesWholeRows` needs `RECOVERY_TEST_MYSQL_DSN`
pointing to an **empty disposable MySQL database**; it creates and drops
`integrity_meta_00` and refuses an existing table. It checks insertion, replacement,
idempotence, the existing insert-only contract and concurrent whole-row reads.
A skipped test is not database validation.

Before release, retain pass/fail evidence for these criteria:

- Given valid staged data and a stable sealed object, recovery publishes the
  correct canonical pieces and complete metadata before marking success.
- Given any missing/corrupt piece, changed version, updating object, failed chain
  call, failed publication or failed failed-row cleanup, GVG stays unresolved and
  no CompleteSwapIn is submitted.
- Given an object deleted on the isolated target chain, actual RPC errors map to
  the deletion classifier; failed-row cleanup failure prevents cursor progress.
- Given a delayed old executor/report or manager restart, current canonical data,
  queue and metadata remain protected. Test single-active handoff and partial copy.
- Given exhausted staging capacity, admission stops across restart. Given a slow
  or maximum-sized object, retain byte/read counts, memory and duration evidence
  under the 120-second total verification/publication budget. No per-stage reset.
- Given target disk/sharded/cloud backends, exact cleanup preserves current and
  canonical keys; verify missing, denied and transient errors independently.

Isolated-chain SP-exit E2E needs separate authorization to start services and send
test transactions. This local implementation does not perform that operation.
MOCA-1292 at `ef8daedf6e098987787559b8f33b1fb4c383d53f` remains an integration dependency;
retain its ticker cleanup and failed-persistence cursor guard when rebasing.
Backport to release/1.4.x is separate and must include fencing, staging, publication,
full-row upsert and the dependency fix together.
