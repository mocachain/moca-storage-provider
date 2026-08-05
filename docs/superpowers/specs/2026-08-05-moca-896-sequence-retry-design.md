# MOCA-896 Sequence Retry Design

Linear: https://linear.app/mocanetwork/issue/MOCA-896

## Goal

Recover a Cosmos signing account whose cached sequence is ahead of the chain after
a dropped `BROADCAST_MODE_SYNC` transaction, without adding a chain query to every
successful broadcast.

This change restores account progress after a sequence mismatch. It does not
guarantee that an earlier transaction accepted by CheckTx but later dropped will
eventually execute.

## Design

The normal path continues to broadcast with the sequence cached by
`MocaChainSignClient`. No nonce query or confirmation wait is added before a
broadcast.

A shared sequence-retry helper performs the following steps while the caller's
existing account lock is held:

1. Broadcast once with the cached sequence.
2. Return immediately on success and report the sequence that was used.
3. Return immediately on any error other than `ErrWrongSequence`.
4. On `ErrWrongSequence`, query the account sequence from the chain, update the
   caller-owned cache, and retry the same messages with the refreshed sequence.
5. Stop after the existing bounded sequence retry limit.

Callers advance their cache to `usedSequence + 1` only after a successful
broadcast. A transport timeout, EOF, or other ambiguous error is never retried by
this helper because rebuilding and broadcasting a transaction after an unknown
outcome can duplicate a non-idempotent operation.

The existing `sealLock`, `gcLock`, and `opLock` remain the serialization boundary.
No additional in-memory queue is introduced.

## PR Boundaries

PR #97 introduces the shared retry behavior and migrates the Cosmos paths that
already have sequence retry loops. PR #100 remains responsible for adding retry
coverage to `DiscontinueBucket` and `UpdateSPPrice`, which currently only refresh
their caches and return an error.

PR #97 must merge before #100. After #97 merges, #100 must rebase and use the
shared helper rather than retaining a second retry implementation.

## Tests

Focused tests must prove that:

- the successful path does not query the chain sequence;
- a wrong-sequence response queries once and retries with the refreshed value;
- a non-sequence error is broadcast only once;
- a sequence query failure stops without another broadcast;
- successful callers cache `usedSequence + 1`;
- exhausting the bounded sequence retries returns an error.

The signer race tests, repository unit tests, lint, vet, build, and full GitHub CI
must pass before the PR is returned to review.

## Out Of Scope

- transaction inclusion confirmation on every broadcast;
- a persistent transaction outbox;
- replaying an exact signed transaction after an ambiguous transport failure;
- changing Cosmos broadcast mode;
- changing EVM nonce or pending-transaction behavior.
