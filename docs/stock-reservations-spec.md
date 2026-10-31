# Milestone 8: Inventory Reservations

Status: **IMPLEMENTED AND DISPOSABLY VERIFIED; NORMAL ENABLEMENT / REAL SALES INTEGRATION PENDING**. Updated 2026-10-27.
The Sales-facing contract below is **PROPOSED**, pending acceptance by the Sales
service owner. There is no real Sales Service or team agreement evidenced here.
The user authorizes end-to-end implementation after the material choices below;
R8-A/B/D are confirmed as recommended; R8-C selects Sales-ordered reserve before cancellation.

## Evidence, preservation and boundaries

Inspected Inventory `feat/stock-adjustments` at 9fbc818, Auth on the same branch at
55aa66f, Platform on that branch at 8ffc85f, Catalog development at 8fea2f2. Existing
uncommitted adjustment code, Inventory migration 000005 and Auth 000010 are dependencies,
not discarded work. Continue these checkouts without switching to an older baseline.
Inventory backups/ and Catalog go.mod are unrelated and remain untouched. A current
hash/status baseline is saved under review-artifacts/stock-reservations/.

The inspected pre-milestone source had human-only posting principals, command/fingerprint
v1/v2, stock result v1 and no reserved quantity. This milestone adds explicit machine
actors, consume command/fingerprint v3 and reservation command/result v1 while retaining
legacy bytes and stock result v1. Auth now has a separate exact reservation allowlist;
Catalog authority remains unchanged and cannot authorize Sales mutations.
Warehouse writes take advisory lock 782341092 then their warehouse row; posting
claims idempotency, locks warehouse FOR SHARE, then balance rows in UUID-byte order.

Read [rules](inventory-rules.md), [foundation](stock-foundation-spec.md),
[adjustments](stock-adjustments-spec.md), [Opening Stock](opening-stock-spec.md),
[reads](stock-read-api.md) and [Catalog integration](catalog-reference-integration.md).
Historical reports establish past evidence only; no previous runtime test is counted
as a fresh reservation test. Normal databases were previously reported Auth 9 /
Catalog 3 / Inventory 4; no normal database check or mutation is performed here.
Normal Opening Stock posting/replay and adjustment enablement remain separate pending work.

No commits, pushes, PRs, merges, resets, stashes, normal migrations/data/grants,
provisioning, deployment, restarts, credential/TLS replacement or normal stock posting.
No fake production Sales Service, public provisioning endpoint, transfers, advanced
delivery, valuation, physical counts, Kafka, Outbox, Inbox, Saga or full Postman work.

## User-confirmed decisions (2026-10-26)

| ID | Confirmed choice | Rejected/deferred alternative |
| --- | --- | --- |
| R8-A lifecycle | Whole-order all-or-nothing reserve, explicit release only, full consume/release; immutable lines. Changes require release and a new generation. First contract uses direct whole pieces, one warehouse per generation, 1–100 distinct variants with distinct order-line IDs. | Partial workflows or expiry require additional states, quantity accounting and business rules; exclude them unless explicitly selected. |
| R8-B authority | Machine-only reservation mutations/status for a dedicated Sales identity. Exact audience inventory-reservations; machine permissions inventory.reservations:reserve, inventory.reservations:release, inventory.reservations:consume, inventory.reservations:read. Owner is verified machine UUID. No human reservation access in this MVP. | Human-only postpones the actual machine contract; both requires explicitly defining human ownership/override authority rather than impersonation. |
| R8-C sequencing | Sales must confirm reserve before cancellation; release-before-reserve rejects out-of-order and creates no record. Generations start at 1 and only the immediate successor after RELEASED may open with matching predecessor/head version. A confirmed terminal generation cannot be recreated; CONSUMED closes that order identity in this MVP. | Durable pre-reserve cancellation tombstones were not selected. Sales needs durable cancellation intent, ordered delivery and explicit reconciliation of unknown outcomes. |
| R8-D inactive behavior | New reserve and consume require active warehouse and fresh current active/eligible Catalog references. Release of an existing owned hold remains possible despite deactivation, without Catalog. Status/completed replay require current authority, not fresh references. | Allowing consume despite inactivity changes the current new-issue rule and needs explicit approval. Blocking release can strand holds. |

R8-A includes a deliberately narrow proposed Sales line contract: Sales must present
at most one line per variant, each with its own order_line_id. Do not silently merge
different Sales order-line identities. If Sales needs repeated variants or packs,
settle the alternative contract before dependent implementation. No current piece,
negative-stock, ledger immutability, freshness or uncertain-commit rule is reopened.

## Business identities, states and proposed sequencing

Authority is the verified service identity, never a caller-supplied order ID or actor.
The order namespace is (owner_machine_id, order_id), independent of warehouse, so
changing the route warehouse cannot bypass a confirmed terminal generation. A reservation has
its own immutable reservation_id, warehouse, generation and ordered immutable lines
(order_line_id, variant_id, positive pieces, Catalog/unit evidence). Reserve/release/
consume each have command_id and an independent Idempotency-Key. IDs are canonical
nonzero UUIDs; command/key IDs use the existing v4/v7 contract. Generations/versions
and all BIGINT quantities serialize as canonical decimal strings.

Approved states under R8-A/C (Sales adoption remains proposed):

| Current | Action | Result |
| --- | --- | --- |
| No generation, expected predecessor 0 | Reserve generation 1 | ACTIVE; increment reserved only |
| No generation | Release/cancel | Reject out-of-order; no writes, no cancellation acknowledgement |
| ACTIVE | Full release | RELEASED; remove that hold only |
| ACTIVE | Full consume | CONSUMED; atomically remove that hold and issue its pieces |
| RELEASED | Reserve exact next generation with matching predecessor/head version | New immutable ACTIVE generation |
| RELEASED or CONSUMED | Delayed reserve/consume/release for that generation | Terminal conflict; no new effect |
| CONSUMED | Any later generation for that order identity | Closed-order conflict under the approved MVP |

Future/gapped commands reject as out-of-order; they are not buffered by Inventory.
An early release is a known out-of-order rejection, not a successful cancellation. Sales must durably retain cancellation intent, reconcile the same original reserve identities after uncertainty, then submit/confirm release before marking its order cancellation complete.
A cancellation for generation n+1 cannot skip an active predecessor or release its
stock implicitly. Sales must reconcile and sequence its commands. A cancelled
generation cannot become active again even with different IDs/keys. A later valid
generation requires an explicit new reserve and matching predecessor facts.
Reservation identity reuse against another owner/order/warehouse/generation conflicts.
Release/consume cannot move ownership or target another order's reservation.

Same-key matching completed replay returns its immutable command result, even if
the reservation subsequently changed state; GET status gives current state. A new
key for an already performed business action does not add another transition/effect;
return a typed business conflict with status reconciliation, not an invented retry.
Unknown commit retains the original identities/key/body; no automatic mutation retry,
replacement IDs, compensation or assumption that missing lookup proves rollback.

## Required local invariants and lock order

available_pieces = on_hand_pieces - reserved_pieces.
Both quantities are exact nonnegative int64 integers; reserved <= on_hand. Positive
line counts are 1..9223372036854775807, with checked per-pair arithmetic and version-exhaustion
rejection; there is no invented sum limit across unrelated variants. Reserve/release create no physical ledger movements. Consume produces
one OUT movement per distinct variant and completes the reservation transition,
reserved decrement, on-hand decrement, operation and durable result in one transaction.

All ordinary OUT adjustments and decreasing reversals must check available stock,
not only on-hand. Consuming a hold may spend its own held pieces, never another
reservation's stock. Warehouse deactivation rejects stock or outstanding holds.
Existing Opening first-movement eligibility is not reset by reserve/release; consume
is posted movement history. Consumption is not an Opening/Adjustment producer and
cannot be reversed through the existing correction endpoint's approved producer allowlist.

Consistent order across workflows: claim the workflow idempotency key; lock warehouse
FOR SHARE (including release, without the active check); lock the order head then
reservation/generation; acquire balance rows by sorted raw UUID bytes. Existing
warehouse lifecycle keeps advisory-lock -> warehouse-row order and must not acquire
an order lock after locking balances. Ordinary posting has no order lock and proceeds
warehouse -> balances. No path holds balances then waits for a reservation/order row.
Resolve Catalog and check freshness before database locks, including pool-wait checks.

Preserve physical balance version as the ledger chain counter. Reservation-only
changes advance reservation_version; they must not manufacture
physical movements or advance a ledger version with no matching movement. Consume
updates both effects atomically. Deferred SQL checks must validate reserved totals
against ACTIVE reservation lines, complete immutable transition history, per-order
generation/head correspondence, ownership, key/result correspondence and consume
ledger linkage. Protect audit/lines from edits/deletes and guard essential invariants
against direct SQL misuse in disposable tests.

## Implemented schema, actor and compatibility

Source migrations are Inventory 000006 and Auth 000011, after the preserved
uncommitted 000005/000010. Never edit those existing migrations.

Additions: reserved_pieces and reservation_version on balances;
order heads, immutable reservation identities/lines, state projection, immutable
transition audit and reservation command/idempotency results. Business uniqueness
is separate from key uniqueness. No Catalog DB access or remote foreign keys.

The previous physical ledger required actor_user_id. Migration 000006 adds an
explicit actor kind and machine UUID, allowing exactly one appropriate identity;
legacy rows remain HUMAN with their original UUID. Do not put a machine UUID into
actor_user_id or invent a Sales user. Correlate both actor kind/identity in durable
key validation. Preserve human v1/v2 fingerprints byte-for-byte and stock durable
result v1. A versioned consume command/fingerprint must bind machine identity,
order/reservation/generation, command/operation identity and requested action.
Reservation fingerprints use an explicit separate versioned domain and canonical
representation, never raw JSON or incidental map ordering. Include caller intent;
exclude request_id, validation time, refreshed tokens and generated result metadata.

For the proposed direct-piece Sales contract, Catalog observations are server execution
evidence, not a quoted pack factor or caller-supplied revision. Validate through real
typed direct-piece selections. Define an explicit new consume version for that policy;
never pass fake expected revisions or weaken existing human command versions.
Store coherent revision/checked_at and retain confirmed 5s age/1s future allowance.
Catalog may still deactivate before Inventory commits (existing R1).

Extend PostingStore's single transaction for consume; do not call independent release
and posting transactions. Keep safe primary recovery and outcome-unknown precedence.
Reservation-only transactions need equivalent bounded recovery and immutable results.
Use pinned sqlc 1.31.1 and generated bindings, not handwritten generated code.

Down migrations must refuse when reservation/machine-posted history requires the new
schema; no audit/ledger/hold deletion as rollback. Verify empty down/up and populated
preservation. Deploy required schema before any new reader/binary, even if mutation
flags remain off; define recovery for a dirty refused-down marker. No normal migration
or service is changed by this task.

## Proposed HTTP and authority matrix

Default-off STOCK_RESERVATIONS_ENABLED requires STOCK_REFERENCE_ENABLED, the separate
approved machine verifier and real reference dependencies. The routes below are wired
only with that gate. These are implemented Inventory contracts proposed to Sales. Human routes retain their existing verifier;
Catalog machine tokens must fail reservation authentication and vice versa.

| Route | Exact approved machine permission |
| --- | --- |
| POST /api/v1/inventory/warehouses/:id/reservations | inventory.reservations:reserve |
| POST /api/v1/inventory/warehouses/:id/reservations/:reservation_id/release | inventory.reservations:release |
| POST /api/v1/inventory/warehouses/:id/reservations/:reservation_id/consume | inventory.reservations:consume |
| GET /api/v1/inventory/reservations/:reservation_id | inventory.reservations:read |
| GET /api/v1/inventory/reservations | inventory.reservations:read |

All reads/mutations are scoped to the verified owner. Status reads do not call Catalog.
The bounded owner-only list of ACTIVE holds supports abandoned-hold review, with
creation time and order/generation identities; no invented automatic expiry threshold.
Use existing 50/default, 100/max cursor conventions and document live pagination.
Sales keeps durable IDs, compares active holds with its own order state, then submits
explicit release. Credential rotation preserves the same machine owner. Ownership
transfer/admin overrides and identity replacement recovery are outside this MVP;
authorized operators recover that principal's issuance through existing reviewed
lifecycle tooling rather than impersonating a human or adding a public admin endpoint.

Suggested reserve body (synthetic IDs; Idempotency-Key header required):

```json
{
  "command_id": "01935f00-0000-7000-8000-000000000011",
  "reservation_id": "01935f00-0000-7000-8000-000000000012",
  "order_id": "01935f00-0000-7000-8000-000000000013",
  "generation": "1",
  "expected_previous_generation": "0",
  "expected_order_version": "0",
  "lines": [{
    "order_line_id": "01935f00-0000-7000-8000-000000000014",
    "variant_id": "01935f00-0000-7000-8000-000000000002",
    "pieces": "10"
  }]
}
```

Terminal commands bind command_id, order_id and generation plus expected reservation/
order version; they never accept replacement quantities, variant subsets, actor,
producer or source. Release-before-reserve rejects out-of-order without creating a tombstone. Both terminal actions require expected_reservation_version and expected_order_version. Sales must confirm reserve first; terminal commands never act on a missing generation.

Strict JSON, no unknown/duplicate/case-ambiguous/null fields, bounded media/body and
1–100 lines; decimal string BIGINTs only, canonical UUIDs and exactly one key header.
Return explicit DTOs, no-store and safe {code,error,request_id}. Proposed success is
201 for a new reserve, 200 for terminal action/status and completed replay;
reserve/release result version is separate from the preserved stock result v1 embedded
for consumption. Typed 400 malformed/overflow, 401 wrong principal/token, 403 missing
authority, 404 unknown owned resource, 409 insufficient_available_stock, key/identity
conflict, stale_generation/version, terminal/out-of-order conflicts; 503 dependencies,
busy or outcome_unknown, existing 408/504 deadlines. Unknown wins over joined timeout.

Existing stock balance reads will expose reserved_pieces, available_pieces and the
separate reservation revision as decimal strings; old quantities/physical version
keep their meaning. Update documentation/tests for that additive contract.

## Auth extension and future transport

R8-B confirmed: extend Auth's exact allowlist for inventory-reservations and the four
approved scopes, preserving the Catalog contract unchanged. Requested scopes must
be a canonical unique subset of allowed scopes and all explicitly granted to that
machine; no default all-scopes issuance or cross-audience token. Retain HTTP Basic
over TLS, strict form credentials, 256-bit hashed expiring credentials and existing
rotation/revocation policy. Tokens keep explicit machine kind/subject and <=5m issued
lifetime; <=30s tolerance must not extend issued lifetime. Revocation stops future
issuance; already-issued tokens can survive expiry plus tolerance. No human grants,
normal machine provisioning or new public lifecycle endpoint is authorized.

Transport-independent workflow methods take verified service identity and typed
commands. Proposed Milestone 10 event envelope: schema/version, event_id,
correlation/request_id, order_id, reservation_id, generation, command_id, action and
typed payload, plus independently verified producer identity from transport security.
event_id is dedupe identity, never authority. Preserve order/generation ordering,
business uniqueness and original idempotency intent across delivery/retries; per-order
partitioning alone is not a substitute for Inventory checks. Sales owns order state;
Inventory owns holds/stock/results. No event consumer, Outbox, Inbox or Saga is built now.

## Implementation blast radius and verification plan

After decisions, complete all layers without another routine approval stop:

1. internal/stock/reservation.go (or focused internal domain package), command/
   fingerprint/principal extensions: exact lifecycle, identities and pure validation.
2. db/migrations/000006_stock_reservations.{up,down}.sql; db/queries/reservations.sql
   and stock.sql/warehouse.sql; generated internal/repository/postgres/sqlcdb/.
3. ReservationStore in internal/stock/reservation.go and internal/repository/postgres/reservation.go
   plus reservation_transaction.go;
   integrate consume into stock.go, OUT availability guards and warehouse status.
4. internal/service/stock_reservation.go, internal/handler/stock_reservation.go,
   internal/security/reservation_machine.go, router/router.go, app/app.go and config/.
   Extend domain/stock_read.go and read mappings/DTOs without changing old meanings.
5. Authentication/db/migrations/000011_reservation_machine_authority.{up,down}.sql,
   domain/service/repository/query/handler machine authority and actual issuance tests;
   preserve existing Catalog clients, operator commands and grant behavior.
6. scripts/test-reservations-postgres.ps1 and narrowly extend ERP-Platform/scripts/
   test-inventory-gateway.ps1 plus disposable-only overlay; no normal Compose edit.
7. Current rules/read/operational documentation, this spec, one timestamped report.

Risks: shared posting fingerprints/decoders, immutable SQL guards and actor migration;
reservation-only balance version updates; availability protection for existing OUT
adjustments/reversals; lock ordering and default warehouse lifecycle; machine token
audience separation; rolling reader/schema compatibility. Keep source changes scoped,
existing adjustment migrations intact and unrelated edits preserved. Security
authority/actor changes implement the explicit R8-B approval, with no normal grants.

Tests before claiming completion: domain transitions and malformed/overflow bounds;
same-key replay/changed intent/different-key business duplicates; sufficient/insufficient
multi-line reserve and rollback; races (reserve/reserve, consume/release, consume/OUT,
reversal/holds); both terminal actions and out-of-order/generation/reserve-before-cancel cases;
owner isolation and human/Catalog-token rejection; actual Auth issuance and HTTPS
Catalog checks; approved inactive/replay policy; all quantity/ledger/reserved/audit
correspondence at Commit; simulated unknown-commit recovery and later same-key
reconciliation; cleanup/deadlines; empty/populated migrations; stock reads, Opening,
adjustment/correction and warehouse regressions. Use unique owned tmpfs PostgreSQL,
generated credentials/keys/certs, marked databases, safe logs and environment restoration.
No normal env/credentials/volumes fallback. Run heavy verification sequentially with
bounded Go concurrency to avoid earlier Windows resource failures; never loosen
application budgets to make fixtures pass. Run formatting, vet/build, pinned sqlc
determinism, script syntax and scoped/new-file whitespace checks.

Pure helpers return errors without duplicate logging. Boundary zerolog includes
component, operation, stable reason, request_id, safe reservation/order/operation IDs
and SQLSTATE where relevant. Never emit tokens, secrets, raw payloads, explanations
or raw infrastructure errors. Runtime evidence must distinguish synthetic snapshots,
simulated faults, real disposable integration, normal enablement and real Sales use.

## Delivery status

Reservation domain, schema, transactions, machine security, HTTP wiring and test
fixtures are implemented. Final verification evidence and any remaining limitations
are recorded in the single report under review-artifacts/stock-reservations/ (see
current.txt). Normal enablement and real Sales integration remain unperformed.
Normal Opening posting/replay and adjustment acceptance remain separate pending work.
Full Inventory Postman collection/environment, automation, examples and tests remain
a final deliverable after the planned milestones; milestone verification is independent.


## Exact transport examples and response semantics

All examples are synthetic. Send a verified machine Bearer token for the exact
route permission; retain TLS certificate and hostname verification. Every mutation
requires exactly one canonical v4/v7 `Idempotency-Key`. The caller persists the
complete body, route, key and IDs before sending. Never copy a real token into source.

Release or consume body for generation 1 (different command IDs/keys for each
new action; reservation/warehouse IDs come from the route):

```json
{
  "command_id": "01935f00-0000-7000-8000-000000000015",
  "order_id": "01935f00-0000-7000-8000-000000000013",
  "generation": "1",
  "expected_order_version": "1",
  "expected_reservation_version": "1"
}
```

New reserve response example (201); same-key replay returns 200 and `replayed: true`:

```json
{
  "result_version": "1",
  "reservation_id": "01935f00-0000-7000-8000-000000000012",
  "order_id": "01935f00-0000-7000-8000-000000000013",
  "warehouse_id": "01935f00-0000-7000-8000-000000000001",
  "generation": "1",
  "state": "ACTIVE",
  "version": "1",
  "order_version": "1",
  "changed_at": "2026-10-27T00:00:00Z",
  "operation_id": null,
  "stock_result": null,
  "replayed": false
}
```

Release returns state RELEASED, reservation version 2 and next order version;
operation_id/stock_result stay null. Consume returns CONSUMED, version 2, the next
order version, operation_id equal to its command_id, and stock_result containing
the existing full durable stock result v1 (see stock-foundation-spec.md). No raw
SQL row or actor credential is serialized. Replaying reserve after consume returns
its original ACTIVE result; GET status reports the current CONSUMED state.

GET `/api/v1/inventory/reservations/:reservation_id` accepts no query parameters.
It returns reservation_id, order_id, warehouse_id, generation, state, version,
order_version, created_at, updated_at and immutable lines (line_number, order_line_id,
variant_id, pieces, product_id, base_unit_id). GET `/api/v1/inventory/reservations`
accepts only limit/cursor and returns `items`, `next_cursor`, `state_filter: "ACTIVE"`.
List items omit lines, retain creation time and use UUID ascending live keyset
pagination; membership may change between pages. Refresh for reconciliation; pages
are not a frozen snapshot. Cursors are versioned, canonical and owner/state-bound;
none of their contents establish authority. Terminal history is read by known ID.

The error body is `{code,error,request_id}` with safe generic wording. In addition
to existing stock/reference errors, concrete reservation codes are:

| Status | Code | Caller action |
| --- | --- | --- |
| 400 | invalid_input | Correct malformed/overflow/duplicate/unsupported fields before submitting a new intent. |
| 401 / 403 | unauthorized / forbidden | Obtain the correct current machine authority; never switch to a human or Catalog token. |
| 404 | reservation_not_found | No owned resource is exposed; another machine's reservation is not readable. |
| 409 | insufficient_available_stock | No partial hold or issue was committed. |
| 409 | reservation_exists | Same business generation/action cannot be duplicated with a new key. |
| 409 | reservation_out_of_order | Confirm/reconcile reserve before cancellation; no tombstone or successful cancellation is recorded. |
| 409 | reservation_terminal / stale_reservation / order_consumed | Reconcile status and sequence against current order/generation versions. |
| 409 | idempotency_key_reused / operation_conflict | Preserve original identities; do not change the body under the same key. |
| 503 | outcome_unknown | Outcome is uncertain: retain original IDs/key/body, reconcile status/same-key replay; no automatic mutation retry. |
| 503 | reference_unavailable / stock_busy | Dependency or bounded contention failure; follow the original identity protocol. |

Exact shared code spellings are defined by internal/handler/opening_stock.go. The
unknown-outcome classification takes precedence over a joined cancellation/deadline.

## Canonical forms, revisions and operational prerequisites

Reservation fingerprint v1: domain `inventory.reservation.request\0`, big-endian
uint16 version 1; raw owner, command, reservation, order and warehouse UUIDs; action
as uint32 byte-length-prefixed UTF-8; uint64 generation, expected_previous_generation,
expected_order_version, expected_reservation_version; uint16 line count; lines sorted
by raw order_line_id UUID, each raw order-line UUID, variant UUID and uint64 pieces.
SHA-256 hashes that input. Key and first_request_id are excluded. Business command
uniqueness is independently enforced by (owner, command_id); it is not the key.

Stock consume fingerprint v3: stock domain, fingerprint and command versions 3,
length-prefixed MACHINE kind, raw owner UUID, producer, reservation request hash,
uint16 line count and sorted derived lines (uint16 line number, raw variant UUID,
uint64 pieces). Catalog checks and validation timestamps are execution evidence;
no fake quoted revision is supplied. Human canonical v1/v2 and stock result v1 are
unchanged. Immutable direct lines bind product/unit identity on later consume.

`version` on stock balances remains the physical ledger chain counter;
`reservation_version` counts hold transitions for the pair. A client comparing
availability must compare the pair `(version,reservation_version)`. A physical
adjustment changes availability without changing reservation_version. Reserve and
release change no physical version/movement; consume advances both counters.

Before deploying this binary, apply Inventory 000006 (including prerequisite 5)
even if the new flag stays false: shared readers/posting queries use the new columns.
Auth 000011 permits exact machine grant pairs but provisions nobody and defines no
human reservation permission. New Inventory machine access requires a separately
reviewed explicit Sales principal/grants/expiring credentials, Auth TLS issuance,
trusted matching public key/issuer, normal Catalog TLS/trust and working Inventory
Catalog machine credentials. Existing local admin tooling remains Catalog-specific;
scoped grant lifecycle exists internally, with no new public provisioning endpoint.
Normal Compose and TLS overlay are not altered here.

A populated down migration intentionally refuses. Preserve the database and use
reviewed forward recovery; do not delete reservation or ledger facts. A refused
migrate down may leave a dirty version marker despite its SQL transaction rollback.
Inspect schema and data, verify backup/recovery, and obtain a target-specific plan
before any force-marker repair. Empty down/up is supported and preserves legacy
human history. This task authorizes only run-owned disposable migration tests.

Abandoned holds have no time-based expiry. Sales must monitor its owned ACTIVE list,
compare with durable order state, and explicitly release under the original machine
identity. Identity replacement/ownership transfer and human override are deferred.
Future Milestone 10 consumers must preserve verified producer identity, order
sequencing and idempotency through the same transport-independent workflow boundary.


## Proposed future message example (Milestone 10; no consumer implemented)

This illustrative envelope is a proposal for later transport agreement, not a live
API or accepted Sales contract. The independently authenticated transport principal
establishes the machine owner; producer labels and IDs in the message do not.
The payload retains the exact reserve intent. Delivery retry must retain event,
command, reservation, order/generation, key and payload; correlation/request metadata
is for tracing and excluded from fingerprints. Ordering and cancellation obligations
remain the same as HTTP. Outbox/Inbox/Saga ownership and event-to-command mapping
must be agreed before implementing transport.

```json
{
  "schema_version": "1",
  "event_type": "inventory.reservation.reserve.requested",
  "event_id": "01935f00-0000-7000-8000-000000000020",
  "producer": "sales",
  "correlation_id": "01935f00-0000-7000-8000-000000000021",
  "request_id": "synthetic-trace",
  "warehouse_id": "01935f00-0000-7000-8000-000000000001",
  "idempotency_key": "01935f00-0000-7000-8000-000000000022",
  "payload": {
    "command_id": "01935f00-0000-7000-8000-000000000011",
    "reservation_id": "01935f00-0000-7000-8000-000000000012",
    "order_id": "01935f00-0000-7000-8000-000000000013",
    "generation": "1",
    "expected_previous_generation": "0",
    "expected_order_version": "0",
    "lines": [{
      "order_line_id": "01935f00-0000-7000-8000-000000000014",
      "variant_id": "01935f00-0000-7000-8000-000000000002",
      "pieces": "10"
    }]
  }
}
```

Inventory validates stale/gapped versions even with ordered partitions; it does not
buffer future commands. Sales retains durable cancellation intent, confirms an
uncertain reserve using original identities, then explicitly releases and confirms
that terminal result before marking cancellation complete. No message transport can
silently add release-before-reserve tombstones, expiry, partial fulfillment or
compensation to the approved MVP.


## Verification checkpoint — 2026-10-27

Real disposable PostgreSQL passed legacy human v1 replay across migration 6,
empty down/up and populated refusal, reserve/terminal/same-key races, no oversell,
consume with concurrent available OUT, multi-line rollback, immutable SQL guards,
inactive cleanup and simulated uncertain-commit/caller-cancellation recovery.
The current Auth scoped lifecycle services passed against an isolated real database.
Real HTTPS Auth issuance through Kong into Inventory and secured Catalog passed
all reservation actions/replay, machine/human/audience/scope/owner separation, held
OUT/reversal protection and Catalog-outage replay/status/release. Existing warehouse,
Opening, adjustment/correction and read gateway regressions passed. Fixture cleanup
retained all 36 pre-existing containers unchanged.

Final review added a same-key consume completion recheck when a hold becomes terminal
between lookup and status read. It performs authorized read reconciliation only,
not mutation retry. A forced-interleaving unit test and subsequent PostgreSQL
concurrent same-key tests for reserve/release/consume passed. The gateway acceptance
run preceded this narrow reconciliation refinement; the final refinement was verified
with those unit/PostgreSQL checks, focused service/stock tests, vet and build.

Both repositories passed full standalone Go tests, vet/build; DB/network-gated
standalone skips are not runtime evidence. Inventory's post-refinement focused tests,
vet/build also passed. Stock SQL and read HTTP/PostgreSQL harnesses passed; SQL
negative-boundary expectations were updated for migration 6's earlier availability
constraint. Real pool-wait tests reject stale posting/reservation observations before
writes. Pinned sqlc 1.31.1 generated Inventory's 5 and Auth's 9 files twice with equal
normalized hashes. Checked scripts parse, scoped/new-file whitespace and formatting
pass; protected-file hashes match the baseline. Detailed commands, limits, preservation
and final Git status are in the single workspace reservation report. Simulated fault
tests are not claims of a real network partition. No normal service/data was changed.


## Milestone 9 fulfillment checkpoint (2026-10-27)

[Minimal Fulfillment / Delivery](fulfillment-spec.md) now composes a full consume and
dispatch audit/result in one transaction. Legacy owner-machine consume v3 remains
available for unlinked reservations and rejects a live dispatch association. Human
confirmation uses explicit stock v4 / internal reservation fingerprint v2 with actual
human actor evidence separate from owner. Machine reservation fingerprints v1 and
stock results v1 remain preserved. Draft cancellation leaves holds unchanged; Sales
release is still explicit and reserve-before-cancel sequencing remains unchanged.
Schema7 precedes the compatible binary, even if the fulfillment flag is off; new
fulfillment authority needs Auth12. The normal stack/real Sales remain separate.
