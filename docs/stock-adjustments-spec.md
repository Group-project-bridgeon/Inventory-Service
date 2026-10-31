# Stock Adjustments and Corrections

Status: **IMPLEMENTED — DISPOSABLE VERIFICATION PASSED**. D1, D2 and D3 confirmed by the user
on 2026-10-26. This document records the approved scope; implementation and runtime
verification evidence are recorded separately below. No normal operational changes
are authorized.

## Purpose, current evidence and boundaries

Record explained stock increases/decreases and append a linked inverse
of an eligible earlier operation. Physical-count reconciliation means comparing a
count with an observed balance/version; it is not interchangeable with an explicit
delta. Never implement a count as an unconditional overwrite of the current balance.

Inspected baseline: Inventory development 9fbc818, Authentication 55aa66f,
Catalog 8fea2f2 and Platform 8ffc85f. Inventory backups/ and Catalog go.mod are unrelated
and preserved. New work is on feat/stock-adjustments in Inventory/Auth/Platform;
Catalog is read-only. No repeated fetch/pull is needed to establish this baseline.

Existing source includes command/fingerprint v1, durable result v1, checked int64
quantities, a READ COMMITTED posting transaction, warehouse-before-balance locking,
ordered balance initialization/locking, authorized completed replay before Catalog,
and bounded authoritative-primary uncertain-commit reconciliation. Opening Stock,
human JWT verification, secured Catalog preparation/client and stock read APIs are
implemented. At that baseline SQL accepted only command/fingerprint version 1. Movement and
operation history are immutable; all movements in an operation share its direction.

See [rules](inventory-rules.md), [foundation](stock-foundation-spec.md),
[Opening Stock](opening-stock-spec.md), [reads](stock-read-api.md), and
[Catalog integration](catalog-reference-integration.md). Older documentary checkpoints
retain historical limitations; actual source and newer checkpoints take precedence.
Normal enablement/login/preparation/read results are prior operational evidence, not
new acceptance for this feature. Normal Opening Stock posting/replay remains pending.
Inventory PR #6's reported description mismatch is a non-blocking metadata follow-up;
no GitHub change belongs to this task.

No normal database writes, grants, provisioning, TLS/credential replacement, service
restarts, stock posting, commits, pushes, PR work or merges are authorized here.
Reservations, transfers, delivery, Kafka and financial/accounting compensation are
outside scope. The complete Inventory Postman deliverable remains later work.

## Decision register — D1/D2/D3 confirmed

| ID | Confirmed decision | Rejected or deferred alternatives |
| --- | --- | --- |
| D1 workflow | Explicit positive IN/OUT delta, one direction per atomic operation, 1–100 distinct variants; plus one-time whole-operation reversal of Opening Stock or Adjustment operations. No partial reversal or reversal-of-reversal. Any replacement is a separate explicit adjustment. | Delta only: linked reversals explicitly deferred. Adding physical counts requires observed balance/version checks and stale-count rejection, plus decisions on zero-delta counts and mixed-direction batches. |
| D2 authority/audit/date | Exact GLOBAL inventory.stock-adjustments:increase, inventory.stock-adjustments:decrease, inventory.stock-corrections:reverse. A reversal needs reverse plus the permission for its derived direction; it may target an eligible operation by another actor. Require one reason code and a trimmed 1–500-character explanation. Immediate server time; no approval queue/backdating. | Broader single adjustment permission or an approval workflow changes the security/lifecycle contract. Such changes require a separate user decision. |
| D3 inactive correction — CONFIRMED | User selected active warehouse and fresh active/eligible current variant/product/base-unit checks for adjustments and reversals. Reversal conversion comes from original facts, including an obsolete original pack. | Inactive-reference bypass was not selected. No inactive-warehouse exception is authorized. |

D1–D3 were explicitly confirmed by the user on 2026-10-26. Physical-count reconciliation is deferred and unimplemented.

Approved reason codes: DAMAGE, LOSS, FOUND, DATA_CORRECTION, OTHER. They are audit
labels, not implicit permissions or restrictions on direction. All require explanation
under the approved contract. Reason/explanation and correction linkage are immutable,
fingerprinted intent, and become visible with authorized movement history. No free
text is logged. Existing human tokens are stateless and may retain signed authority
until expiry; no immediate grant-revocation claim is introduced.

Already confirmed and not reopened: whole pieces; warehouse+variant identity;
nonnegative stock under concurrency; immutable facts; local atomic persistence;
Catalog-owned units/packs; no network calls under locks; maximum reference age 5s,
future allowance 1s; active-at-check, not active-at-commit; exact human GLOBAL authority;
no authority inferred from Opening Stock/read permissions; no automatic mutation retry
after uncertain commit. An insufficient-stock reversal therefore fails as a whole;
there is no forced negative balance, partial fallback or automatic compensating receipt.

## API and trusted boundaries

All paths are below /api/v1/inventory/warehouses/:id. Approved routes:

- POST /stock-adjustments/prepare: selected quantities/packs, intended direction,
  reason/explanation; real Catalog validation and converted-piece preview, no writes.
- POST /stock-adjustments: stable operation_id, exactly one Idempotency-Key,
  direction, reason_code, explanation and lines with reviewed Catalog revisions.
- POST /stock-corrections/prepare: original_operation_id, reason/explanation;
  derive whole-operation inverse from immutable facts and preview it, no reservation.
- POST /stock-corrections: stable operation_id/key and original_operation_id plus
  approved audit/reference expectations. Client cannot choose reversal quantities,
  direction, historical factors or a subset of the original movements.

Strict application/json object, no query parameters, at most 64 KiB / 100 lines;
reject unknown/duplicate/case-ambiguous fields, null required fields, trailing JSON,
malformed/noncanonical IDs, duplicate variants and invalid line sequences. Operation
and idempotency IDs follow the existing canonical v4/v7 contract. Quantities and
BIGINT versions use canonical decimal strings, never floats or truncation. New delta
counts and converted pieces are positive and at most 9223372036854775807. Packs use
current explicit Catalog IDs/revisions; factor/actor/producer/source are server-owned.

Direct-piece adjustment example (synthetic IDs/revision):

```json
{
  "operation_id":"01935f00-0000-7000-8000-000000000011",
  "direction":"OUT",
  "reason_code":"DAMAGE",
  "explanation":"Two shirts were physically damaged during handling.",
  "lines":[{
    "line_number":1,
    "variant_id":"01935f00-0000-7000-8000-000000000002",
    "input_count":"2",
    "expected_stock_reference_revision":"cat-stock-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  }]
}
```

Server-owned producers: inventory.stock-adjustments.v1 and inventory.stock-corrections.v1.
Actor originates only from verified human claims. SourceEventID=OperationID is bound
inside the service. Correction direction and every reversed piece/conversion fact
are derived from the original, never current pack factors. Preparation is a preview,
not a reservation, completed opening or promise of eligibility/sufficient balance.

New confirmed completion returns 201; matching authorized completed replay returns
200 with the same durable IDs/time and no new effect. Retain existing explicit result
DTO fields and decimal strings. Durable result v1 need not include newly added audit
metadata; authoritative immutable metadata is stored atomically and exposed through
the documented movement/read extension. No invented operation-detail URL.

## Versioning and additive persistence design

Migrations: Inventory `000005_stock_adjustment_audit`, Authentication
`000010_inventory_adjustment_permissions`. Auth adds definitions only, no grants.

Keep command/fingerprint v1 byte-for-byte and preserve all old replay/result decoding.
Introduce explicit command/fingerprint v2 for approved new producers. Include actor,
warehouse, operation/source identity, producer, semantic direction/lines, reason,
explanation, original target and any requested expected revision/version. Idempotency
key and volatile validation/result/request IDs remain excluded. Reordering equivalent
lines does not alter intent. SQL must allow the new versions only with their proper
producer/audit contract; do not weaken old invariants indiscriminately.

Reuse stock_balances, stock_operations, stock_movements and stock_idempotency.
Add narrowly scoped immutable operation audit/correction metadata linked to the
operation and optional original operation. Persist it in the posting transaction.
A unique original correction target plus deferred checks must prevent different-key
duplicate/excess reversal. Check original workflow, same warehouse, opposite direction,
all original lines and exact historical pieces/conversions. Do not permit a reversal
of a reversal or reopen Opening Stock eligibility. Preserve all prior SQL integrity
and immutability checks. Parameterized query sources precede pinned sqlc regeneration;
generated bindings are never hand edited.

For reversal, separate current active-reference evidence from immutable original
conversion provenance. A current variant/base-unit validation must not require an
obsolete original pack to become current or substitute its latest factor. The current reference revision/check time belongs to the direct-piece check;
the stored pack ID/revision/factor and labels belong to the original facts. No cross-service DB queries or foreign keys.

Down migration must preserve posted facts/audit and state its limitations; it must
not silently erase populated correction history. Writers must be disabled before
any reviewed rollback of their integrity backstops. No normal migration is applied.

## Transactions, errors and concurrency

Authorize every attempt/replay before disclosing a result. Completed matching replay
precedes new eligibility/active/reference checks and must survive Catalog outage.
New requests resolve coherent references outside locks. Keep bounded timeouts,
cancellation, primary recovery and cleanup; check freshness again after pool wait.
Claim key, lock active warehouse FOR SHARE, insert operation and audit (the unique
original target serializes corrections), then initialize/lock deterministically ordered balance rows, validate every line and metadata relationship, then mutate all
facts and complete the durable key. Failure rolls back initialized rows and metadata.
Different-key decreases cannot overdraw; target uniqueness prevents double reversal.

Safe structured error contract includes 400 invalid_input, 401 unauthorized, 403
forbidden, 404 warehouse/reference/original-operation not found, 409 insufficient_stock,
operation_not_reversible/already_reversed, reference/warehouse conflicts, key/operation conflict
or in-progress; 503 dependency/busy/outcome_unknown and existing bounded timeout errors.
Physical counts and stale-count/version contracts are unimplemented. Outcome-unknown must
win over joined cancellation/deadline causes. A missing lookup is not proof of rollback;
never generate replacement IDs, auto-repeat an uncertain mutation or delete history.

Use one secret-safe zerolog diagnostic at the owning boundary with component,
operation, stable reason, request_id, safe operation_id and valid SQLSTATE. No tokens,
credentials, reason text, raw bodies or infrastructure messages are logged. Helpers
return recognizable errors without duplicate logs. Responses are no-store.

## Files, implementation and verification plan

Approved implementation files and verification plan:

1. internal/stock/adjustment.go plus command/fingerprint version support and tests.
2. db/migrations/000005_*.sql, db/queries/stock.sql (or focused adjustment query file),
   internal/repository/postgres/stock.go and generated sqlcdb files: atomic metadata,
   correction guards and persistence/recovery tests.
3. internal/service/stock_adjustment.go, internal/handler/stock_adjustment.go and
   preparation/authority/strict transport tests, sharing existing helpers.
4. internal/router/router.go, internal/app/app.go, internal/config/config.go and
   .env.example: explicit default-off STOCK_ADJUSTMENTS_ENABLED, real shared adapter
   and freshness-aware repository; preserve existing constructors/routes where useful.
5. Authentication/db/migrations/000010_* definitions only for approved permissions.
6. Extend existing disposable PostgreSQL and ERP-Platform/scripts/test-inventory-gateway.ps1
   fixtures; isolated HTTPS machine issuance/Catalog references, real human login/Kong,
   post/read/replay reconciliation. No normal overlay/credential/wrapper edits.
7. Update this specification, inventory-rules.md and stock-read-api.md with actual
   implemented behavior, then one report under review-artifacts/stock-adjustments/.

Tests: valid IN/OUT, reasons, exact/overflow inputs, 1/100/101 and duplicate lines;
full rollback; simultaneous decreases; key conflicts on changed reason/target/version;
historical conversion stability; approved inactive policy; same/different-key reversal
and overlapping requests; replay owner/authority, machine separation,
401/403/success, no-store and safe logs; uncertain acknowledgement and later same-key
reconciliation; Opening Stock/warehouse/read regressions. Counts remain deferred; no count/version overwrite fields are accepted.

Use generated secrets/keys/certs, unique projects, DB markers, owned tmpfs data and
environment restoration. Distinguish real DB/HTTPS/Kong runs from synthetic snapshots
and simulated acknowledgement faults. Run focused tests, appropriate final regression,
gofmt, vet/build, sqlc determinism where changed, script/config and whitespace checks.
No verification is claimed until actually executed. Reservations do not exist; later
available-to-promise/reservation rules require a separate agreed design.

## Implemented API details and examples

| Endpoint suffix (POST) | Exact GLOBAL authority |
| --- | --- |
| /stock-adjustments/prepare and /stock-adjustments | increase for IN, decrease for OUT |
| /stock-corrections/prepare and /stock-corrections | reverse plus decrease for an original IN; reverse plus increase for an original OUT |

Preparation accepts the adjustment example above with `operation_id` and each
`expected_stock_reference_revision` omitted, and no Idempotency-Key header. Posting
requires exactly one canonical v4/v7 key header. Preparation is read-only and may
return a preview that later fails for insufficient stock or a prior reversal.

Whole reversal preparation:

```json
{
  "original_operation_id": "01935f00-0000-7000-8000-000000000011",
  "reason_code": "DATA_CORRECTION",
  "explanation": "The earlier receipt was recorded in error."
}
```

Whole reversal posting (Idempotency-Key required):

```json
{
  "operation_id": "01935f00-0000-7000-8000-000000000012",
  "original_operation_id": "01935f00-0000-7000-8000-000000000011",
  "reason_code": "DATA_CORRECTION",
  "explanation": "The earlier receipt was recorded in error.",
  "references": [{
    "variant_id": "01935f00-0000-7000-8000-000000000002",
    "expected_stock_reference_revision": "cat-stock-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  }]
}
```

`references` must contain each original variant exactly once. Preparation returns
`warehouse_id`, `original_operation_id`, derived `direction`, `checked_at` and the
standard prepared `lines`: original input_count, pieces_per_input and pieces,
original pack metadata, and reviewed **current direct-piece** reference revisions.
A new direct revision is required even if the original used a pack; an obsolete
pack is never looked up or assigned a new factor during reversal.

Both posting endpoints return the existing explicit result DTO, for example:

```json
{
  "operation_id": "01935f00-0000-7000-8000-000000000012",
  "result_version": "1",
  "posted_at": "2026-10-26T12:00:00Z",
  "replayed": false,
  "lines": [{
    "line_number": 1,
    "variant_id": "01935f00-0000-7000-8000-000000000002",
    "movement_id": "01935f00-0000-7000-8000-000000000013",
    "after_pieces": "98",
    "balance_version": "2"
  }]
}
```

The stored result stays v1. Authorized movements additionally expose optional
`reason_code`, `explanation`, `reverses_operation_id`; old history omits these.
An explanation is trimmed before validation/fingerprinting, then must have 1–500
Unicode characters and no control characters. Reasons are exact case-sensitive
approved enum values. No free text is written to diagnostics.

Fingerprint v2 retains the v1 domain tag and explicitly changes both encoded
versions to 2. It uses the same actor, operation, producer, source, warehouse,
direction and sorted numbered line representation, then length-prefixed reason,
trimmed explanation and raw original-operation UUID (zero for ordinary adjustments).
The immutable original ID binds historical conversion provenance. Current direct
reference expectations are included per line. Key, first request_id, observation
check time, live labels, movement IDs and result timestamps are excluded. V1's
canonical bytes/golden hash and all result v1 decoding stay unchanged.

Errors use the existing safe `{code,error,request_id}` envelope (`error` is a generic message;
no raw database/client error). Exact new codes: 409
`insufficient_stock`, `operation_not_reversible`, `already_reversed`; 404
`original_operation_not_found`. Existing 400, 401, 403, 409 reference/key/operation,
408 canceled, 504 timeout and 503 reference/busy/unknown codes remain. An unknown
outcome takes precedence over a joined timeout: retain the same actor, operation,
key and full request for a later reconciliation attempt; do not generate new IDs.

## Operational prerequisites and deliberately deferred work

The feature is default-off with `STOCK_ADJUSTMENTS_ENABLED=false`. Enabling it
requires `STOCK_REFERENCE_ENABLED=true`, Inventory migration 5, Auth migration 10,
approved human GLOBAL grants, and existing HTTPS machine issuance/Catalog batch
configuration with verified trust chains and hostname checks. Machine identities
and expiring credentials must already be provisioned through reviewed tooling;
permission definitions do not grant access. No normal configuration was changed.
Apply Inventory migration 5 **before deploying this application release**, even
with the new posting flag off: movement reads now join the additive audit table.
For an empty-history downgrade, restore a compatible older application before
removing that table; turning the posting flag off alone does not change read SQL.
Older application releases can ignore the additive audit table; roll forward for
new v2 history. The down migration refuses populated v2/audit history atomically;
a failed migrate downgrade can leave its version marker dirty and requires reviewed
forward recovery. Disable writers and take verified backups before any later
normal rollout. No automatic audit destruction is a rollback option.

Physical counts, partial reversals, reversal-of-reversal, replacement-in-one-call,
backdating, approvals, reservations, transfers, delivery, messaging and financial
compensation remain unimplemented. Future reservations must define available stock
and correction interactions before extending these checks. Complete Inventory
Postman collection/environment, token automation, examples and tests remain a later
deliverable; milestone verification does not depend on that final collection.

## Verified implementation checkpoint — 2026-10-26

D1–D3 are implemented for the approved explicit delta/whole-reversal scope.
Full Inventory standalone tests, vet/build, changed-file formatting, pinned sqlc
regeneration and whitespace checks passed. Disposable PostgreSQL verified both
inverse directions, multi-line rollback, concurrent decreases/reversals, v1 replay
compatibility, v2 audit/fingerprints, SQL guards and simulated commit-recovery
failures. Empty audit downgrade/reapply passed; populated downgrade refused while
preserving ledger/audit. Disposable read regression verified HTTP, read-only queries,
pagination, warehouse indexes and index rollback/reapply.

Real disposable Auth login → Kong → HTTPS machine issuance/Catalog references →
preparation/posting/read/replay passed: IN 100+60, OUT 1, explicit IN 1, linked whole
OUT 100+60, final zero; four operations/six movements. Reason changes conflict with
reused keys; a different actor with reverse+derived authority can reverse, while only
the reversing owner can replay. Obsolete/inactive old pack conversion stays six;
current inactive product blocks new correction while completed replay succeeds.
The fixture checked 36 pre-existing containers remained unchanged and removed only
its owned project, images/network, tmpfs data and temporary key/credential material.
Twenty-two protected files matched their initial hashes.

Failed attempts are not counted as passes: initial nullable UUID mapping and a
configuration-test setup were fixed; Windows linking/vet resource errors and timing
failures during overlapping builds were followed by sequential green verification.
An existing one-second expired-token fixture became a clearly expired token without
changing production validation. One new gateway conflict expectation received 504;
safe step diagnostics were added and the full rerun passed. Its exact transient
cause was not established. No application timeout was loosened.

Database snapshots are synthetic Catalog evidence; the gateway used real isolated
HTTPS service integration and Auth login. Acknowledgement loss/recovery transport
faults are simulated over real PostgreSQL, not real network fault injection.
Machine-shaped signed JWT rejection is covered by unit tests; a real issued machine
JWT was not separately submitted to the human Kong endpoint. No normal stock posting,
normal migration/grant/provisioning, restart, TLS change or Git publication occurred.
Normal Opening Stock posting/replay and final Postman delivery remain separate work.
