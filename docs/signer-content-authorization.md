# Signer content authorization proposal

Status: proposal for MOCA-924 and PR #64. The content guard remains enabled in
the PR, and the PR remains Draft. This document enables no signer operation.
Implementation targets the v2 upgrade; it is not a release/1.4.x backport.

## Problem and trust model

The existing `signerAuthUnaryInterceptor` verifies the client certificate and
checks URI SANs against `SignerAuth.AllowedClientURIs`. This identifies a caller;
it does not approve its recipients, amounts, prices, groups or lifecycle changes.
A compromised allowed caller must not be able to invent those decisions.

The recommended first issuer is an operator-controlled approval wallet with a
separate key. Its private key must not be available to the SP manager, gateway,
signer process or their application database. The signer stores only enrolled
approval addresses and their explicit operation, SP and caller-URI scopes.
Enrollment and revocation are deployment-admin actions, not signer RPCs.

An automated manager may prepare an unsigned request and submit an approved
permit. It cannot issue a permit. A future automated issuer needs an independently
controlled key and reviewed business policies; copying the manager's own decision
into an automatically approved token would not fix MOCA-924.

This first design therefore has an operator-approval cost. It is not a claim that
all existing automated manager workflows can immediately run unattended. Define
and test their approval process before enabling them in production.

The scheme protects against a compromised RPC caller, not a compromised signer
host or deployment administrator. Modules sharing one certificate have one
authenticated identity. Do not invent a manager/CLI role from request metadata;
separate role identities require separately issued certificates and credentials.

## Reuse and alternatives

Use [EIP-712](https://eips.ethereum.org/EIPS/eip-712) for structured signatures and
domain separation. It does not provide replay prevention; the durable claim
below supplies that property. The repository already resolves
`github.com/ethereum/go-ethereum` to
`github.com/cosmos/go-ethereum v1.16.2-cosmos-1`. Its
`signer/core/apitypes.TypedDataAndHash` and `crypto` functions can hash, sign and
recover a permit issuer. No new dependency or version change is proposed.
These existing library files are LGPL-3.0-or-later; this proposal adds no copied
third-party implementation or new licensing dependency.

A generic role allowlist or amount ceiling is not sufficient: neither approves
the complete request. A new signing format would add encoding and maintenance
work without an identified gap in the existing EIP-712 implementation.

Do not reuse `evmOperationFingerprint` as the authorization digest. It is an
internal transaction-recovery identifier based on a Go type and protobuf bytes,
not an authorization protocol. [Protobuf serialization is not canonical](https://protobuf.dev/programming-guides/serialization-not-canonical/),
including deterministic serialization across schema/runtime changes.

## Signed permit

The verifier owns a versioned EIP-712 schema for each protected operation. A
caller cannot supply its own type definitions or select a weaker schema. The
permit signs a common header plus a nested, operation-specific typed request so
the approval tool can display the actual approved content rather than only an
opaque hash.

| Binding | Required value |
|---|---|
| Domain | Name `Moca SP Signer Authorization`, protocol version, EVM chain ID, deployment salt |
| Deployment | Cosmos chain ID, SP ID, configured deployment ID and authorization epoch |
| Caller | Exact verified certificate URI, not an HTTP/gRPC identity header |
| Operation | Exact RPC oneof variant and execution mode; approval and transaction operations differ |
| Signing identity | Server-derived signing scope and key identity, recomputed before dispatch |
| Content | Every field of the operation's request, including nested approvals and ordered arrays |
| Transaction cost | Explicit caps for server-generated gas/fees and value where applicable; units and denominations are fixed by the schema |
| Validity | First and last allowed chain height, with a deployment-configured maximum span |
| Replay identity | Random 32-byte nonce, issuer key ID and authorization epoch |

Derive the deployment salt from a typed, versioned encoding of the deployment
identity, not ambiguous string concatenation. The signer obtains chain identity
and a fresh finalized height from its configured trusted chain access. Define a maximum observed header age
and an operational clock-skew bound; stale or unsynchronized observations must
not extend an expired permit. Unknown chain identity, unavailable height,
unsupported mode or exceeded fee caps deny
new execution. Permit validity must not depend solely on the caller's clock.

Only explicitly enrolled EOA approval keys are supported in the first version.
Use fixed-length recoverable secp256k1 signatures, validate recovery ID and
low-S values, recover the address, then check its configured scopes. Contract
wallet/EIP-1271 support requires a separate design; it must not silently fall
through to EOA verification.

Examples of request types:

- `StorageProviderExitRequest` binds the actual `storage_provider` address.
- `CreateGlobalVirtualGroupRequest` binds family ID, primary SP address, the
  ordered secondary SP IDs, and deposit denomination and integer amount.
- `UpdateSPPriceRequest` must bind every price, quota and timing field present
  in the request. Decimal values need an explicit fixed-unit integer encoding.

Each projection must enumerate all source fields. Reject invalid values,
unsupported extensions and ambiguous representations. A new protobuf field or
oneof variant requires classification and schema coverage before it can reach
the signer. Descriptor coverage and per-field mutation tests enforce this.
Neither JSON map order nor generic protobuf hashing defines the signed content.

## Complete operation inventory

The current guard covers these 16 RPC variants:

| Group | Variants |
|---|---|
| GVG funding/lifecycle | `create_global_virtual_group`, `deposit`, `delete_global_virtual_group` |
| Bucket migration | `complete_migrate_bucket`, `reject_migrate_bucket` |
| Swap out | `sign_swap_out`, `swap_out`, `complete_swap_out` |
| Swap in | `reserve_swap_in`, `complete_swap_in`, `cancel_swap_in` |
| SP lifecycle | `sp_exit`, `complete_sp_exit` |
| Prices | `sp_storage_price` |
| Delegation | `delegate_create_object`, `delegate_update_object_content` |

Create one explicit schema and caller integration for each variant. Permission
to sign a swap-out approval is not permission to submit its transaction.
Audit the nested `peer_approval_request` variants separately: their HTTP
authentication signature is not proof that financial request content was
approved. Existing deterministic task approvals and other unguarded variants
need an explicit classification; do not automatically exempt every new variant.

## Verification and dispatch ordering

Keep verification and durable claiming in one internal authorization module,
called from `GfSpSign` before any protected signer/private-key method. One
authorization entry point accepts the verified principal, immutable request
snapshot and permit, and returns a durable claim reference or an error. A
pluggable issuer framework is not needed for the initial implementation.

1. Validate the request shape and choose its fixed operation schema. Freeze the
   request and the relevant signer configuration for the whole dispatch.
2. Obtain an unambiguous allowed URI from the verified client certificate. If
   more than one enrolled identity matches, deny rather than choosing the first.
3. Derive signing identity, execution mode and the complete typed request from
   server configuration and the frozen request. Check domain, issuer enrollment,
   issuer scopes, caller binding, chain identity, height window and fee caps.
4. Recompute the typed-data digest and verify the issuer signature.
5. Atomically insert the consumed nonce and its audit record in durable SQL. A
   uniqueness constraint on `(deployment_id, authorization_epoch, issuer_key_id,
   nonce)` gives concurrent callers exactly one successful claim. A missing
   table or database failure denies execution; there is no memory-only path.
6. Only after a confirmed commit may the request reach its protected signer
   method. Never mutate the approved request between claim and dispatch.
7. Record the result or its durable transaction-evidence reference before
   returning success. Return retained results only to the same authenticated
   caller with the same complete permit digest.

Store the permit digest, typed-request digest, operation, principal, signing
identity, validity, claim ID and outcome/evidence reference. Do not store issuer
private keys. Use a dedicated table: the #175 gateway `auth_nonce_claims` table has expiry cleanup
and different identity/retention semantics. Reusing its deletion behavior could
re-enable a financial authorization.

## Crash, retry and ambiguous outcomes

One nonce authorizes at most one dispatch. A consumed nonce never becomes free
because of a timeout, restart, fee-estimation failure or expired lease. Repeating
an identical permit may fetch a retained result; it must not invoke signing again
just because a result was lost. A different digest for that nonce is always denied.

Consumption before signing introduces a deliberate crash window: a committed
claim may exist without a known result. Mark it unresolved and reconcile it;
do not automatically grant a replacement permit. Issuers must check the previous
attempt before approving the same economic action again under a fresh nonce.
The verifier alone cannot prevent a trusted issuer from approving a duplicate.

For EVM calls, link the claim to the existing persisted signed-transaction and
pending-state machinery before enabling the operation. Recovery may resend only
the recorded signed bytes. It cannot create fresh bytes under an already consumed
permit. A claim without sufficient evidence remains blocked for explicit
resolution. The business-operation fingerprint remains separate from the permit
nonce, so a fresh permit does not bypass an existing ambiguous transaction.
Audit retries inside the signer methods as well as the RPC handler: an internal
nonce retry must not treat one consumed permit as blanket permission to create
new signed transactions.

For returned approval signatures, persist the exact signature before returning
it. For non-EVM transaction paths, equivalent durable submission evidence and
reconciliation are an enabling requirement, not an assumed property of EVM state.
Do not claim exactly-once execution across SQL, private-key use and chain RPC.

Retain spent nonce identities durably. After database restore or loss of claim
history, stop protected execution, reconcile outstanding transaction evidence and
rotate the authorization epoch before accepting new permits. Every signer instance
sharing keys must use the same claim ledger and active epoch. Revocation requires
stopping admission on every signer, updating the issuer set and epoch, and
verifying configuration convergence before resuming. Updating only one process
does not revoke another. Reading a previously recorded result is a separate
authenticated operation and does not authorize another dispatch.

## Implementation and acceptance

Deliver this as small changes while #64 stays Draft:

1. Add fixed typed schemas, the separate issuer trust configuration and a CLI
   preparation/approval workflow. The trusted approval tool displays every
   economic field and constructs the exact typed data to be signed. No runtime
   operation is enabled in this change.
2. Add durable claiming, audit and recovery/evidence linkage. Define and test
   the consumed-without-result resolution path before enabling financial work.
3. Integrate callers and enable explicitly reviewed variants with valid permits.
   Keep all other guarded variants denied. Assess complete manager workflows,
   not only individual RPC successes, before production rollout.

Required tests include all 16 variants; every request-field mutation; wrong
issuer, caller, SP, chain, scope, mode and domain; malformed/malleable signatures;
height and fee limits; revoked keys; concurrent claims; database failures and
ambiguous commits; restart/restore; lost responses; and recorded-byte-only EVM
recovery. Exercise real signature verification and disposable MySQL, plus caller
and end-to-end lifecycle tests. Add cross-tool golden vectors for each schema.

Stop old signer instances before switching the key owner to an enforcing version.
There must be no legacy caller fallback that bypasses permits. Roll back by
denying protected operations, not by restoring unconditional signing. The new
issuer enrollment, epoch ownership, operator approval process, claim database
backup policy and complete workflow acceptance must be agreed before #64 is ready.

## Evidence and remaining decisions

Source review confirmed the existing mTLS URI check, all 16 guarded variants,
the current proto fields and the separate EVM operation fingerprint. A native
Go spike using the resolved existing dependency verified EIP-712 hashing, issuer
recovery, and rejection after nonce or request-field changes. It validates the
library seam only, not a finished permit protocol, atomic claim or all schemas.

The deployment owner still needs to name the separate approval-key custodian and
enrollment/revocation owner. The recommendation is an operator-controlled approval
wallet first; unattended policies require their own reviewed issuer design.
No approval key, trust entry, schema, SQL table or production signing behavior
has been installed by this documentation change.
