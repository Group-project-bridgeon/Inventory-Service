# Opening Stock contract

Status: implemented behind a default-off flag, 2026-10-26; disposable verification
results are recorded in the task report. Normal enablement/deployment is separate.
The original specification was prepared 2026-10-25 and implementation was explicitly
authorized afterward. Inventory Service is not complete.

## 1. Purpose, scope and evidence

Record physical stock already present when adopting the system: 100 plates in a
warehouse become one opening receipt of 100 pieces for the plate variant. This
is not a generic receive/set-balance endpoint. The implemented workflow: a verified
human prepares references, reviews quantities, and posts one atomic operation for
one warehouse with 1–100 distinct variants.

Out of scope: valuation/accounting entries, effective-date backdating, import files,
draft persistence, approval queues, adjustments/reversals, reservations, transfers,
delivery, messaging, ordinary receipt/issue APIs and Catalog management APIs.
Date/approval scope is confirmed by D2 below.

Source inspected: Inventory `internal/stock/{command,quantity,fingerprint,posting,
durable_result}.go`, `internal/repository/postgres/stock.go`, migrations 000001–000003,
`db/queries/stock.sql`, `internal/security/{jwt,middleware}.go`, router/app wiring,
Catalog client and relevant posting/concurrency/recovery/read tests. See
[foundation](stock-foundation-spec.md), [rules](inventory-rules.md),
[read API](stock-read-api.md), and [reference integration](catalog-reference-integration.md).
Historical foundation checkpoints retain their inspection-time limitations; later
checkpoints and current source take precedence for implemented capability.

Current source wires preparation and posting through the human authorizer, shared
Catalog adapter and freshness-aware repository when OPENING_STOCK_ENABLED=true.
The default is false; disabled deployments do not register either route.
Normal rollout previously verified Auth 8, Catalog 3, Inventory 3. These database
versions are prior rollout evidence, not re-queried during this implementation task.
The user reports successful authenticated empty-list responses from both stock
read routes through normal Kong; this is user-reported evidence, not an agent-run
Opening Stock test. Normal live secured references remain disabled.

## 2. Confirmed rules and decision register

Confirmed, not reopened: identity is warehouse + variant; exact whole pieces;
Catalog owns piece units and versioned packs; no negative balances; immutable
posted facts; balance/movements/operation/completed idempotency result commit
atomically. New posting requires an active warehouse and active, eligible Catalog
references at the coherent check. Maximum check age is 5 seconds, future allowance
1 second, checked again after pool acquisition before BEGIN. R1 permits Catalog
deactivation after validation but before Inventory commit. Unknown commit never
causes an automatic mutation retry. Corrections must append separately authorized
facts rather than edit/delete history.

The user explicitly confirmed D1–D3 on 2026-10-25 during this specification task.
The alternatives below document the tradeoffs; they were not selected.

| Decision | Confirmed choice | Alternative / tradeoff |
| --- | --- | --- |
| D1: eligibility and zero | Opening allowed only before ANY posted movement for the pair; reject zero opening quantities | Allow zero as an explicit completed opening: requires a separate durable eligibility marker because current movement quantities must be positive; expands schema/replay design |
| D2: human authority and posting date | Exact GLOBAL `inventory.opening-stock:post`, immediate server-time posting, no backdating or second-person approval in this slice | Require effective dates or approvals: specify audit, authority and lifecycle before implementation |
| D3: obtain reviewed references | Add secured non-posting preparation endpoint using the same opening permission | Defer preparation transport: Opening Stock stays blocked until a supported caller can obtain real revisions; never invent revisions or expose machine credentials |

D1 applies separately to each warehouse/variant, not once per warehouse. A pair
with prior activity remains ineligible even after its balance returns to zero.
Rejecting zero leaves a never-used pair eligible for a later positive opening; it
does not record that an operator counted zero. Silently ignoring zero lines is not
recommended because it makes the submitted operation differ from its posted facts.

The rest of this document specifies the selected branch for technical review. Routine
engineering choices (strict parsing, lock order, existing v1 encoding, fail-closed
configuration) implement these policies; they do not create additional authority.

## 3. HTTP and human authority

Routes, both protected by exact GLOBAL `inventory.opening-stock:post`:

- `POST /api/v1/inventory/warehouses/:id/opening-stock/prepare`
- `POST /api/v1/inventory/warehouses/:id/opening-stock`

Use existing RS256 human verification with configured issuer/audience, subject
matching nonzero user_id, token_version and expiry checks. Machine tokens cannot
post or prepare. Warehouse-read, stock-read, wildcard or WAREHOUSE-only permissions
do not imply this permission. A fresh token after a grant change is necessary;
rechecking signed authority is not a live Auth revocation lookup. Existing human
tokens can retain authority until their maximum 15-minute expiry.

Obtain actor only from `security.Identity`; never from JSON, user headers or a
request_id. A workflow-specific ProducerAuthorizer verifies trusted claims,
unexpired authority, exact namespace and IN direction on every attempt/replay.
No permissive default authorizer and no machine-principal fallback.

Server-owned producer constant: `inventory.opening-stock.v1`, server-owned forever
for this contract. CommandVersion and FingerprintVersion remain 1. Client supplies
one stable operation_id and one Idempotency-Key, canonical RFC4122 v4/v7 UUIDs.
Map SourceEventID = OperationID inside the workflow; no separate user-supplied
producer/source_event_id/direction/actor fields. IDs identify intent, not authority.
Warehouse comes only from the path. Movement IDs are server UUIDv7 values.

Strict application/json object, no query parameters, maximum 64 KiB per request,
1–100 lines, unique variants, explicit unique contiguous numeric line_number
1..N. Reject unknown/duplicate/case-ambiguous keys, null required fields, invalid
UTF-8, trailing JSON, noncanonical UUIDs, and caller-supplied trusted metadata.
The body cap may reject a request even below 100 lines. Do not sum counts across
unrelated variants. Preserve explicit line numbers when array order changes.
Posting requires exactly one Idempotency-Key header; preparation does not claim
or reserve a key. Success and error responses are no-store.

## 4. Preparation contract

Preparation accepts `{ "lines": [...] }`, with line_number, variant_id, input_count
and optional paired pack_id/pack_revision. It does not accept an expected revision.
An omitted pack means direct pieces; no implicit pack discovery or newest-revision
substitution. A known selected pack revision must be current/active. Pack selection
UX/management remains separate; direct pieces also require Catalog eligibility.

Preparation example (synthetic IDs):

```json
{"lines":[{"line_number":1,"variant_id":"01935f00-0000-7000-8000-000000000002","input_count":"100"}]}
```

Return 200 only if every line can be validated, otherwise one safe error and no
partial prepared operation. Return warehouse_id, checked_at and lines containing
line_number, variant_id, product_id, base_unit_id, product_code, product_name, sku,
base_unit_code, input_count, pieces_per_input, pieces, expected_stock_reference_revision
and optional pack_id/pack_revision/pack_label. Counts/factors/revisions are strings.
No credentials, JWTs, internal machine identity or raw Catalog payload is exposed.
Eligibility prechecks may reject known prior activity but never promise a reservation.

Illustrative preparation response (synthetic revision, not live Catalog evidence):

```json
{"warehouse_id":"01935f00-0000-7000-8000-000000000001","checked_at":"2026-10-25T11:59:00Z","lines":[{"line_number":1,"variant_id":"01935f00-0000-7000-8000-000000000002","product_id":"01935f00-0000-7000-8000-000000000003","base_unit_id":"01935f00-0000-7000-8000-000000000004","product_code":"PLATE","product_name":"Plate","sku":"PLATE-WHITE","base_unit_code":"PC","input_count":"100","pieces_per_input":"1","pieces":"100","expected_stock_reference_revision":"cat-stock-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}
```

`catalogclient.Adapter.Resolve` still requires a fully valid stock.Command
and its expected revision. `Adapter.Prepare` accepts explicit selection
input, sending no expected_revision to Catalog's existing optional-expectation batch
contract. Share authenticated transport and response validation; do not fabricate
a stock.Command/hash or weaken Resolve's required comparison. Validate Catalog
status, identities, active/PIECE eligibility, pack currentness, coherence and freshness
before returning reviewed labels/factors/revision. No Inventory writes/locks while
waiting for Catalog; no durable preparation token/table/cache is proposed.

Preparation time is not a five-second user-entry deadline. Posting resolves fresh
observations again and compares the preserved expected revision. An older prepared
revision is usable only if Catalog confirms it still describes current state.
If it changed, require review/preparation again; never silently replace caller intent.

## 5. Posting request and durable response

Path warehouse UUID is canonical/nonzero. Example header:
`Idempotency-Key: 01935f00-0000-7000-8000-000000000010`.
Revision below is synthetic and must be obtained from preparation in real use.

```json
{
  "operation_id":"01935f00-0000-7000-8000-000000000011",
  "lines":[{
    "line_number":1,
    "variant_id":"01935f00-0000-7000-8000-000000000002",
    "input_count":"100",
    "expected_stock_reference_revision":"cat-stock-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  }]
}
```

For packs, additionally send pack_id and pack_revision (e.g. `"2"`). input_count
then counts whole packs; Catalog alone supplies factor, never a caller factor.
Example 10 packs × 6 pieces = 60 pieces. Unknown, inactive, mismatched or obsolete
packs reject the whole request without falling back to direct pieces.

Return 201 for confirmed new completion; 200 for a matching completed replay,
including recovery proven through a durable lookup. Use an explicit DTO, not Go
field-name defaults or a generated SQL struct:

```json
{
  "operation_id":"01935f00-0000-7000-8000-000000000011",
  "result_version":"1",
  "posted_at":"2026-10-25T12:00:00.000000Z",
  "replayed":false,
  "lines":[{
    "line_number":1,
    "variant_id":"01935f00-0000-7000-8000-000000000002",
    "movement_id":"01935f00-0000-7000-8000-000000000012",
    "after_pieces":"100",
    "balance_version":"1"
  }]
}
```

Same persisted facts/time/IDs on replay; only replayed changes. posted_at is the
database transaction timestamp, not a guaranteed wall-clock commit order or an
operator-specified effective date. Current balances may later differ. No invented
Location URL to an unimplemented operation-detail endpoint.

## 6. Eligibility, transaction and concurrency

Never implement eligibility as a preflight read, balance == 0, or a uniqueness
check only on request IDs. Existing generic first-receipt tests intentionally
allow two different operations to add to the same virgin pair; that is not opening.

Implemented engine rule: bind the exact opening producer to an explicit
internal opening policy. Inside StockRepository's existing transaction, after
claim/replay handling and warehouse FOR SHARE, initialize and lock balance rows
in UUID-byte order. For EACH line, before any quantity writes, require version=0,
on_hand_pieces=0 and no posted movement for the pair. An observed existing movement
or positive version means `opening_not_eligible`; contradictory state is an internal
invariant error. Reject the entire operation. Uses a bounded EXISTS query using the
existing warehouse/variant movement index; no aggregate history scan needed.

The balance primary key serializes absent-row initialization. A competing insert
waits for the first transaction; after it commits, SELECT FOR UPDATE and the next
READ COMMITTED history query see prior activity. If it rolls back, the waiting
request may proceed. Hold every selected balance lock through writes and commit.
All posting producers must preserve this shared lock protocol. A generic receipt
winning first makes opening ineligible; opening winning first permits later ordinary
receipts under their future workflow rules. No network calls under these locks.

Inventory migration 000004 extends the existing deferred operation integrity check for this exact producer:
require IN direction, SourceEventID=OperationID, before_pieces=0 and balance_version=1
for every opening movement. Existing unique (warehouse,variant,balance_version),
nondeletable history and contiguous-version correspondence make a second opening
impossible, including direct SQL bypass of the application check. It is not sufficient
to add a producer-specific unique constraint only on operation IDs. Preserve all
existing function checks; never rewrite migration 000002. No new opening ledger,
completion marker or duplicate balance store is needed for positive-only D1.
The implementation includes application checks and this SQL backstop. Database owners can still disable guards; no WORM claim.

Two openings with different keys/IDs for the same pair: at most one commits; the
other returns eligibility conflict or a bounded busy response requiring same-key
reconciliation. Overlapping multi-line requests lock consistently and either post
all lines or none. Duplicate variants reject before I/O, even with different packs.
Rollback includes newly initialized rows, headers, movements and key claims.

SetActive now enforces the documented prohibition
on deactivation with positive stock. It acquires a warehouse row lock conflicting with
posting's FOR SHARE BEFORE testing stock, retaining existing warehouse advisory-lock
order, then check positive balances in a fresh statement and update in one transaction.
Do not lock warehouse after balance rows. If deactivation wins, posting observes
inactive; if posting wins, deactivation observes positive stock and rejects. Preserve
default-warehouse rules. Reservations do not exist and are not added here.

## 7. Replay, IDs and uncertainty

Reuse canonical fingerprint v1: command/fingerprint versions, operation ID,
server producer, source event ID, authenticated actor, warehouse, direction and
semantic lines (number, variant, count, pack presence/ID/revision, expected reference
revision). Binary framing and sorted line-number encoding stay frozen. Exclude
idempotency key, request_id, observed check time, resolved labels/factor and generated
movement IDs. No raw-JSON hashing. Changing body array order alone is equivalent.

| Situation | Result |
| --- | --- |
| Same key, actor, IDs and intent, completed | Authorized durable replay before new eligibility/Catalog checks; no writes |
| Same key, changed intent/operation | 409 idempotency_key_reused |
| Same key, different actor | 403 without disclosing stored result or hash |
| Different key, same operation/source identity | 409 operation_conflict; not a second replay identity |
| Different IDs/key, previously opened or otherwise used pair | 409 opening_not_eligible; zero balance does not reopen it |
| Same-key peer still holding claim | Bounded 409 operation_in_progress or completed replay |
| Definitive rollback | No durable key/effect; same intent may retry with the same identities |
| Uncertain commit | 503 outcome_unknown; no automatic mutation retry, compensation or new key |

Completed replay remains allowed after warehouse/reference deactivation or Catalog
outage, subject to valid current request authority and original actor ownership.
Do not place a new active/eligibility preflight before completed lookup. Existing
reference-failure path checks for a peer's completed result once more. Replays must
also survive temporary reference-client unavailability; keep lookup capability
available and make unavailable references fail only a new effect attempt.

On lost acknowledgement, preserve exact operation ID/key/body/actor, and explicitly
resubmit the same request to reconcile. An absent primary lookup is not proof of
rollback. A later explicitly requested same-key call may safely replay or contend
on the original unique claim; the failed call itself never re-mutates. Error mapping
must check ErrOutcomeUnknown BEFORE joined deadline/cancellation errors. Existing
automatic retries remain restricted to known rolled-back 40001/40P01, at most three,
with original identities and fresh references. No browser/proxy mutation retry loop.

## 8. Quantities and error contract

Reuse Quantity/int64 and checked PackToPieces/AddPieces. Input and converted piece
counts: 1..9223372036854775807; balance 0..9223372036854775807; positive pack factor.
Multiplication overflow rejects before writes. Reject negatives, fractions, numeric
JSON quantities, exponents, plus signs, whitespace, leading zeroes and out-of-range
strings. No float conversion, rounding or lower business cap is invented. Existing
100-line bound and bounded execution budgets apply; maximum batch is not an SLA.

Errors reuse `{ "code": "...", "error": "safe message", "request_id": "..." }`.
The implemented transport mappings are:

```json
{"code":"outcome_unknown","error":"Posting outcome is unknown; reconcile using the same operation, key and request.","request_id":"opening-review-example"}
```

| Status | Code / condition | Client action |
| --- | --- | --- |
| 400 | invalid_input: malformed body/IDs/header, duplicates, zero, fractional or overflow count | Correct request; no write |
| 401 | unauthorized: missing/invalid/expired human token, machine token | Authenticate as human |
| 403 | forbidden: no exact GLOBAL permission or different replay owner | Obtain separately authorized access |
| 404 | warehouse_not_found / reference_not_found | Review selection |
| 409 | opening_not_eligible, warehouse_inactive, reference_inactive, reference_ineligible, reference_changed | Review state; never force/reset |
| 409 | idempotency_key_reused / operation_conflict | Reconcile original request; do not blindly rotate IDs |
| 409 | operation_in_progress | Preserve same request and reconcile explicitly |
| 409 | version_exhausted | Operational review; no retry loop |
| 413 / 415 | request_too_large / unsupported_media_type | Respect body/media contract |
| 503 | reference_unavailable (including stale/malformed/denied machine response), stock_busy | Safe generic dependency/configuration error; same identities for a retry |
| 503 | outcome_unknown | Original effect may exist; reconcile only with same identities/body |
| 408 / 504 | request_canceled / timeout, only absent unknown-commit classification | Preserve IDs if response/effect status is uncertain |
| 500 | internal_error: corrupt durable result or integrity failure | Stop and investigate; never treat corrupt history as a new request |

No remote 401/403 is forwarded as a human authorization failure; distinguish human
authority from machine configuration internally. Changed/obsolete selected pack is
reference_changed. Stale server observations fail safe; do not loosen freshness.
Disconnected clients may receive no response. Never assert rollback solely from an
HTTP timeout or network disconnect. Do not expose raw database/network errors.

## 9. Audit, operational wiring and compatibility

Reuse operation actor, producer, source identity, posted_at; movements retain
historical Catalog labels, base unit, selected pack/revision/factor, revision and
checked_at. No free-text memo/business reason or effective date is proposed.
InsertStockOperation receives bounded first_request_id execution metadata through
the internal call boundary, sourced from existing request middleware, and stores it
on the first successful posting.
Exclude metadata from v1 fingerprints/durable result. Never overwrite it on replay;
each retry has its own request_id diagnostic. Request IDs are correlation only.

Use one owning-boundary zerolog diagnostic with component, operation, stable reason,
request_id, safe operation_id and SQLSTATE when available. No tokens, Basic headers,
secret paths/connection strings, raw bodies, snapshots or duplicate helper logs.
Preserve existing logger.Classify/DiagnoseHTTP conventions and generic client errors.

The explicit OPENING_STOCK_ENABLED flag defaults off; enabled startup requires real
authorizer, posting store with freshness, Catalog adapter and valid machine/TLS
configuration. Invalid enabled configuration fails startup; transient upstream
outage must not prevent durable replay. Disabled routes stay unregistered, preserving
current reads. Retain/close one shared client instance correctly at app lifecycle
boundaries. Do not fake references, use admin human tokens or read sibling databases.

Before normal enablement: retain confirmed D1–D3 and review the implementation;
apply Inventory migration 000004 and Auth permission definition 000009 with
verified backups and separate operational authorization. Definition only; no automatic role/user grants.
Obtain separately authorized least-privilege human grants and fresh human login.
Provision real Catalog piece eligibility/packs through an approved management process;
migration 3 did not classify legacy units. Never infer piece semantics from names.

Live reference prerequisite: valid direct HTTPS Auth and Catalog listeners, trusted
CA/hostname verification from Inventory, correctly scoped readable keys/certificates,
matching issuer/dedicated audience, enabled Catalog reference route, separate machine
UUID/grant/credential with secure file distribution and rotation. Retain approved
5-minute machine JWT maximum and independent 30-second clock tolerance. Align Kong
upstreams, trust and health probes if listener protocol changes; no spoofable forwarded
headers or TLS bypass. Ensure safe human bearer transport outside the established
local loopback development exception. TLS and normal provisioning are separate
operational approvals, outside this implementation task. Current HTTP read-only
rollout cannot serve as proof of live opening readiness.

On rollback disable the opening route first; retain immutable stock history. A
narrow guard-function down migration would remove only its added enforcement, not
delete facts, and must not be run while an opening writer remains active. Permission
down should preserve definitions/grants as existing migrations do. No change to
foundation fingerprint/result versions is needed on the recommended design.

## 10. Implementation paths

Paths below are repository-relative. Inventory migration 000004 and Authentication
migration 000009 are implemented; Catalog source remains unchanged. Normal
application of migrations and permission grants is not authorized by this work.

1. Review the technical contract under confirmed D1–D3 and authorize implementation.
   Keep this file and inventory-rules.md aligned.
2. Narrow engine guard: `internal/stock/opening.go` (producer/policy/error),
   `internal/repository/postgres/stock.go`, `db/queries/stock.sql`, generated
   `internal/repository/postgres/sqlcdb/stock.sql.go`,    `db/migrations/000004_opening_stock_guard.up.sql` and `.down.sql`.
   Bind the producer policy in the store as well as the workflow; no caller switch
   capable of bypassing it. Add execution metadata without changing frozen intent.
3. Warehouse prerequisite: `internal/repository/postgres/warehouse.go`,
   `db/queries/warehouse.sql`, `internal/repository/postgres/sqlcdb/warehouse.sql.go`
   and existing warehouse service/handler error mapping; reject positive
   stock deactivation atomically. No new reservation model.
4. Preparation: extend `internal/catalogclient/reference.go` and add
   `internal/catalogclient/preparation_test.go`; reuse `internal/strictjson/`.
   New `internal/service/opening_stock.go` and `internal/handler/opening_stock.go`
   implement preparation/posting DTOs and trusted actor binding.
5. Auth definitions-only migration in Authentication,    `db/migrations/000009_inventory_opening_stock_permission.up.sql` / `.down.sql`.
   Add policy and dependency wiring in Inventory `internal/router/router.go`,
   `internal/app/app.go`, `internal/config/config.go`.
   Preserve all existing read/warehouse routes.
6. Tests: `internal/stock/opening_test.go`, `internal/service/opening_stock_test.go`,
   `internal/repository/postgres/opening_stock_test.go`,
   `internal/handler/opening_stock_test.go`, `internal/router/opening_stock_test.go`,
   `internal/integration/opening_stock_gateway_test.go`; guarded disposable runner
   `scripts/test-stock-postgres.ps1 -OpeningOnly`. Extend existing recovery/freshness
   regressions only where the new boundary requires it.
7. Authorized disposable cross-service acceptance in ERP-Platform
   `scripts/test-inventory-gateway.ps1` and `docker-compose.opening-test.yml` (overlay on the existing fixture),
   with real temporary HTTPS reference services and machine issuance. Then a
   separately reviewed normal enablement plan; no automatic normal grant/config.

## 11. Acceptance matrix and gotchas

| Layer | Required checks before claiming completion |
| --- | --- |
| Domain/service | positive/max quantities, zero/negative/fraction/malformed rejection, pack overflow; 1/100/101 lines; duplicate variant/number; trusted actor/producer binding; stable fingerprints; no volatile metadata in hash |
| Preparation | direct pieces/packs, all-or-nothing validation, malformed/oversized request, no synthetic expected revision, no writes; real coherent check; later re-resolution rather than reusing prepared timestamps |
| PostgreSQL | first opening success; prior ordinary receipt rejects; receipt/issue-to-zero still rejects; same-key replay; changed body/actor/key/operation conflicts; multi-line failure rolls back all five categories of state including initialized rows |
| Concurrency | same pair different keys exactly one opening; same-key race one effect; overlapping/reversed-order batches; generic receipt versus opening; rollback winner releases eligibility; warehouse deactivation versus posting using deterministic barriers |
| SQL backstop | direct SQL opening at version>1 rejected, including after zero balance; wrong direction/source rejected; existing generic receipt/issue integrity tests remain valid; guard migration up/down/reapply on populated disposable history |
| Recovery | lost acknowledgement with successful lookup; lookup deadline/cancellation/unavailability yields unknown; mapping checks unknown before timeout; no Apply retry after uncertain commit; later original request replays; simulated faults explicitly labeled |
| Catalog | inactive/ineligible/missing/obsolete references, changed revision, stale>5s, future>1s, pool-wait freshness, R1 deactivation-after-check; completed replay works during outage/deactivation without new network calls |
| HTTP | exact 201/200 DTOs and string BIGINTs; strict JSON/media/body/header parsing; 401 human/machine separation; 403 wrong permission/owner; no authority from caller metadata; no-store and safe errors/log capture |
| Disposable Kong | actual Auth human login and /me; exact GLOBAL posting permission success; stock-read-only and WAREHOUSE-only denied; real TLS machine issuance/Catalog validation; post then read matching history/balance, replay no delta; warehouse/read regressions |

Use isolated generated credentials/keys, unique projects and database markers; no
normal .env/volumes fallback. Verify cleanup and unchanged unrelated containers.
Run focused Go tests, required PostgreSQL/HTTP/Kong suites, gofmt, vet/build, sqlc
deterministic regeneration and git diff --check as implementation scope warrants.
Do not count gated skips as runtime acceptance or simulated failures as real network
fault injection. Keep pure helpers free of logging and preserve bounded cleanup.

Key mistakes prevented: using zero balance instead of movement history (post-issue
reopening); checking eligibility outside the transaction (different-key races);
checking Catalog before durable replay (outage breaks completed replay); replacing
reviewed revision on retry (changes intent); treating timeout as rollback (duplicates);
locking warehouse after balances (deadlocks); enabling references over HTTP (credential
exposure). Each has an explicit acceptance case above.

Complete Inventory Postman collection/environment, token automation, examples and
tests will be finalized after planned Inventory milestones. This is a later deliverable;
each milestone's automated verification must stand independently of that collection.

## 12. Configuration, rollout and manual verification

Inventory schema **4**, Authentication schema **9**, Catalog schema **3** are required.
Auth 000009 creates only the permission definition; its down migration intentionally
retains definitions/grants. Inventory 000004 replaces the existing deferred integrity
function with the narrow opening backstop and preserves every prior check. Its down
migration restores the old function without deleting facts: disable opening writers
before rollback, since older code cannot enforce the complete opening policy.

`OPENING_STOCK_ENABLED=false` is the default. True requires
`STOCK_REFERENCE_ENABLED=true` and the existing validated reference configuration
in [catalog-reference-integration.md](catalog-reference-integration.md). Startup
rejects invalid local TLS/credential settings; it does not require remote services to
be currently reachable. Thus a transient Catalog outage does not remove durable replay.
The app shares and closes its reference adapter. No normal configuration was changed.

Before separately authorized normal enablement: review/apply migrations, provision
an expiring machine credential and exact Catalog audience/permission, configure
verified HTTPS/CA trust for Auth and Catalog, enable eligible PIECE units and current
packs, assign the exact GLOBAL human permission, and obtain a fresh human token.
Never disable TLS verification or reuse disposable fixture credentials. User-reported
normal empty read responses do not prove this mutation workflow is enabled.

PowerShell 7 / Postman manual sequence (only against an explicitly authorized target):

1. Obtain a fresh human token through the normal login flow. In PowerShell use
   `Read-Host -AsSecureString` for passwords/tokens and `Invoke-RestMethod -Authentication
   Bearer -Token $token` with HTTPS. Do not paste tokens into scripts/history or print
   login responses. In Postman keep secrets in local sensitive variables, not exports.
2. Send the preparation example above to the prepare route with Content-Type exactly
   `application/json`, no idempotency header. Review every returned identity, count,
   conversion and revision. Preparation is neither a reservation nor an eligibility
   guarantee; it performs no history precheck. Posting enforces eligibility atomically.
3. Generate an operation ID and independent idempotency key once (PowerShell
   `[guid]::NewGuid().ToString()`). Retain the original path, IDs and exact semantic body
   securely. Copy only accepted request fields and each reviewed revision from the
   preparation response; do not submit labels/factors/checked_at.
4. Submit once. Expect 201 and decimal-string results. Read the balance and movements
   with separately granted read permissions. An intentional identical replay returns
   200 with the same durable IDs/time and no balance change.
5. On `outcome_unknown`, retain the original operation/key/body; reconcile that same
   request. Never automatically create a new key, retry a mutation with altered
   intent, or compensate. A confirmed changed reference requires a new operator review;
   it must not silently overwrite an uncertain original operation.
6. Keep response bodies/tokens out of shared logs. Record safe request_id/operation_id
   and status only. Refresh expired human authority without changing original intent.

Automated milestone acceptance remains independent of the later complete Inventory
Postman collection/environment, token automation, examples and tests. Exact executed
checks, simulated fault boundaries, cleanup and remaining gaps belong to the final
report under `review-artifacts/opening-stock/<timestamp>/report.md`.
