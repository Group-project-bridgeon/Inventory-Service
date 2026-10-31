# Milestone 9 — Minimal Fulfillment / Delivery

Status: **IMPLEMENTED AND DISPOSABLY VERIFIED; NORMAL ENABLEMENT / REAL SALES INTEGRATION PENDING.**
Prepared 2026-10-27. The user confirmed the recommended R9-A/B/C/E policies. The user instructed continuation after the exact Resource/Action matrix was displayed; implementation uses those exact pairs.
The Sales-facing contract remains **PROPOSED pending Sales-owner acceptance** even
after local implementation approval. No real Sales integration is evidenced.

## 1. Purpose, inspected evidence and preservation

Add a small dispatch/delivery module inside Inventory, on top of existing immutable
whole-order reservations. A draft is an intention to dispatch, not a stock issue.
Confirming dispatch consumes the entire held reservation once. Delivery evidence
after dispatch changes no physical stock.

Current inspected branches/HEADs: Inventory feat/stock-adjustments at 9fbc818,
Authentication feat/stock-adjustments at 55aa66f, Platform feat/stock-adjustments at
8ffc85f, Catalog development at 8fea2f2. Adjustments and Reservations remain
uncommitted dependencies. Inventory migrations 000005/000006 and Auth 000010/000011
were preserved byte-for-byte. This milestone adds Inventory 000007 and Auth 000012.
No branch switch or fetch was performed.

Read [reservations](stock-reservations-spec.md), [adjustments](stock-adjustments-spec.md),
[current rules](inventory-rules.md), and actual stock/reservation service, repository,
fingerprint, recovery, JWT, SQL and fixture sources. The current consume path in
internal/service/stock_reservation.go calls the posting engine; at the initial inspection the PostgreSQL
StockRepository.attempt owned BEGIN/COMMIT and atomically wrote ledger, balances,
reservation terminal state and durable results. Calling that service and saving a
dispatch afterward would create two commits and is prohibited.

The preserved physical consume v3 requires a verified machine owner as acting principal;
SQL reservation_assert_consume requires MACHINE actor and the reservation producer.
Human dispatch now uses an explicit new actor-aware/versioned workflow, not
a constructed machine identity or fake ReservationAccess. At initial inspection, Auth issued
only exact Catalog and reservation machine audiences; the approved fulfillment
audience/permissions are added by Auth 000012, with no normal provisioning.

Baseline source/status/protected hashes are saved under the workspace
review-artifacts/inventory-fulfillment/ directory (current.txt locates this run).
Preserve Catalog go.mod, Inventory backups/ (contents not enumerated), security-work,
normal env/credentials/certificates/TLS configuration and running services. Prior
milestone reports are historical evidence, not fresh Milestone 9 test results.

No normal database changes, provisioning, grants, migrations, stock acceptance,
credential rotation, TLS replacement, service restart or deployment. No staging,
commits, pushes, PRs, merges, resets, stashes or history rewriting. Catalog is
read-only unless a demonstrated contract gap requires separate review.

## 2. Confirmed requirements and exclusions

Already confirmed and not reopened: warehouse + variant stock identity; exact whole
pieces; immutable posted facts; on_hand >= reserved >= 0; available = on_hand - reserved;
1–100 distinct immutable reservation variants/order lines; full reserve/release/consume;
active warehouse and fresh current active/eligible Catalog checks for new consumption;
maximum observation age 5s / future allowance 1s; active-at-check, not at commit;
completed replay with current authority before fresh dependencies; no automatic
mutation retry after uncertain commit; no remote calls while holding database locks.

Required new scope: create a linked draft, confirm full dispatch, record minimal
delivery outcome, detail/list reads, immutable lifecycle audit, exact authority,
durable idempotency and local concurrency protection. Use default-off wiring and
existing Go/Gin/pgx/sqlc/zerolog conventions. Do not create a Delivery microservice.

Excluded: partial dispatch/delivery, warehouse transfers, courier/GPS/route integration,
shipping costs, invoicing/payments, returns/restocking, automatic expiry/schedulers,
physical counts, Kafka/Outbox/Inbox/Saga implementation and full Postman delivery.
Failed delivery or cancellation after physical issue must never implicitly restock.

## 3. Decision register — R9-A/B/C/E confirmed; R9-D confirmed

| ID | Policy and approval status | Rejected / deferred alternative |
| --- | --- | --- |
| R9-A lifecycle — CONFIRMED | DRAFT -> DISPATCHED -> DELIVERED; DRAFT -> CANCELLED. FAILED is immutable delivery-outcome evidence while state stays DISPATCHED; a later explicit DELIVERED outcome is allowed. Each outcome increments dispatch version. No scheduled retry or stock compensation. | Terminal DELIVERY_FAILED adds a terminal state; later successful delivery/recovery needs a future approved workflow. |
| R9-B stock / prior consume — CONFIRMED | Draft changes no stock. Confirm dispatch and consume the full ACTIVE reservation in one transaction. Reject prior CONSUMED/RELEASED reservations; never attach unrelated historical consumes. Standalone consume rejects while a live draft claims that reservation; legacy consume remains for unlinked reservations. | Historical attachment needs an explicit audited association/eligibility contract and must never issue again; it is not silently included. |
| R9-C cancellation — CONFIRMED | Owning Sales cancels the draft only, leaving its hold unchanged; Sales explicitly releases through the existing owned reservation flow. A new draft can claim the same reservation if still ACTIVE and no live dispatch exists. No post-dispatch cancellation. | Atomically cancel and release couples workflows and needs explicit release authority; it must not be implemented as two commits. |
| R9-D authority — CONFIRMED | Sales machines create/cancel/read owned drafts with a new exact fulfillment audience. Verified humans dispatch, record outcomes and read under exact GLOBAL permissions, explicitly across Sales owners/warehouses. Human actor is distinct from machine owner. | Owner-machine-only for every action avoids a human warehouse queue but excludes human fulfillment operations; add only its explicitly approved exact machine scopes. |
| R9-E evidence — CONFIRMED | Optional trimmed external_reference <=100 Unicode characters, optional trimmed note <=500; valid UTF-8 and control-character rejection. Immediate server-recorded times, no caller occurrence timestamp. No recipient personal fields or signatures. | Recipient data needs fields/purpose, access/retention and privacy decisions before implementation. |

Draft creation requires an existing owned ACTIVE reservation and an
active warehouse, without a new Catalog call; confirmation performs the approved
fresh current Catalog check. Cancellation/read and post-dispatch outcome recording
need no active-reference check or Catalog availability because they issue no stock.
These rules preserve R8: draft is non-posting; dispatch confirmation applies the
existing active/fresh consumption policy. Metadata-only cleanup/outcomes issue no stock.

## 4. Approved lifecycle, identities and races

Immutable dispatch identity: dispatch_id, owner_machine_id, reservation_id, order_id,
warehouse_id, reservation generation, fixed direct-piece line identity from Inventory.
The machine owner is derived from trusted identity/reservation, not supplied as an
authority-bearing body field. Humans resolve the existing dispatch owner server-side.
No request may replace lines, quantities, warehouse, owner or reservation after create.

One non-CANCELLED dispatch per reservation, enforced by a unique partial index.
Cancelled drafts remain immutable historical records; a replacement gets a new ID
and command/key, only if the original reservation remains ACTIVE. A dispatched or
delivered record permanently occupies its reservation's dispatch association.
Dispatching an already consumed reservation without an existing completed matching
fulfillment result rejects. A duplicate ID/key cannot attach historical stock.

Each mutation has its own command_id and Idempotency-Key (canonical UUID v4/v7),
expected_dispatch_version, and applicable expected reservation/order versions.
Creation starts dispatch version 1. Dispatch advances once and pins its operation ID;
later outcome entries advance dispatch version only. Versions/quantities are int64
decimal JSON strings; timestamps are server UTC. No client backdating or approvals.

Existing reservation release remains usable for cleanup. If release wins a race
against dispatch, dispatch rejects without issue or partial fulfillment writes;
the draft may remain DRAFT but its released reservation makes it non-dispatchable.
The owner can explicitly cancel it. Do not auto-cancel/compensate in an unrelated
release transaction. Sales should cancel its draft and then release, reconciling
each uncertain result before claiming completion. The reserve-before-cancel protocol
still applies; a draft request or cancellation never creates a reservation tombstone.

If dispatch wins, reservation becomes CONSUMED; release/cancel cannot succeed.
Two dispatch attempts, even with different keys/IDs, can produce only one consume.
Delivery success is terminal; fresh keys cannot mutate a delivered record. Same-key
completed replay always returns its historical result rather than current projection.
FAILED outcomes append evidence; identical-key replay adds none. A new explicit FAILED
report can append another audited observation with the current dispatch version;
there is no automatic retry, partial delivery or carrier attempt scheduler.

## 5. Exact authority and actor separation (R9-D — CONFIRMED)

The user confirmed continuation after the exact matrix was displayed with separate
Resource and Action columns. Implementation checks these exact pairs; no action
spellings were renamed during implementation.
The exact identifiers below are those specified by the user: machine actions
create/cancel/read and human actions dispatch/update/read. They replace the prior
proposed create-draft/cancel-draft/confirm-dispatch/record-outcome spellings. No earlier spelling is an approved alias. No action-less resource name,
wildcard, prefix match or implicit permission is accepted.

| Workflow | Principal / audience | Resource | Action | Exact permission | Access limit |
| --- | --- | --- | --- | --- | --- |
| Create draft | Sales machine / inventory-fulfillment | inventory.dispatches | create | inventory.dispatches:create | Own ACTIVE reservation only; no human access |
| Cancel draft | Sales machine / inventory-fulfillment | inventory.dispatches | cancel | inventory.dispatches:cancel | Own DRAFT only; no human access; hold unchanged |
| Confirm dispatch | Human / erp-api | inventory.dispatches | dispatch | inventory.dispatches:dispatch | Corresponding exact GLOBAL permission; across owners/warehouses; no Sales machine access |
| Record delivery outcome | Human / erp-api | inventory.deliveries | update | inventory.deliveries:update | Corresponding exact GLOBAL permission; across owners/warehouses; no Sales machine access |
| Read detail/list/audit | Sales machine / inventory-fulfillment | inventory.dispatches | read | inventory.dispatches:read | Verified machine owner only |
| Read detail/list/audit | Human / erp-api | inventory.dispatches | read | inventory.dispatches:read | Corresponding exact GLOBAL permission; across owners/warehouses |

The repeated read string is intentional: the resource/action is the same, while
the trusted principal, audience, grant store and query scope differ. Machine grants
live in machine_grants under inventory-fulfillment; human read comes from an explicit
GLOBAL identity permission in the human erp-api token. There is no authority implied
between these domains. Create/cancel/dispatch/update do not imply read or each other.
A machine owner ID or human UUID supplied in a request body grants no access.

"No cross-principal access" means no impersonation, token-kind/audience confusion or
unauthorized replay of another actor's command. It does not restrict the explicitly
approved human operations/reads across Sales owners and warehouses when the human
has the corresponding exact GLOBAL permission. Human access is checked as that
human, never by substituting the Sales owner machine. Machine reads remain restricted
to the verified machine owner. Draft cancellation changes no reservation quantity/state;
Sales release remains an independent explicitly authorized reservation action.

Approved human permission definitions in Auth: (inventory.dispatches, dispatch),
(inventory.deliveries, update), (inventory.dispatches, read). No human create/cancel
definition or grant is proposed. Approved machine allowlist: only create, cancel and
read for inventory-fulfillment. No machine dispatch/update grants or human override
of Sales draft cancellation.

Human dispatch permission explicitly authorizes physical consumption through this
approved fulfillment boundary only. It does not grant direct reservation-machine
access, adjustment permissions, credential/grant control or machine impersonation.
Ledger/audit must record the actual HUMAN user UUID; the Sales owner remains a
separate machine UUID. The owner machine is never substituted for the acting human.
No token receives cross-audience scopes or permissive default authority.

Auth 000012 adds required human permission definitions only and extends the
exact machine grant allowlist for the selected fulfillment audience/scopes. No normal
grants/identities. Preserve Catalog and reservation authority and existing <=300s
machine lifetime, <=30s clock tolerance, hashed 256-bit expiring credentials and
rotation/revocation policy. Already-issued JWTs may remain valid until expiry plus
tolerance. Human GLOBAL grants are not warehouse-scoped membership checks.

Use distinct machine/human route protection rather than accepting whichever token
decoder happens to parse a UUID. Reauthorize every attempt and completed replay.
Bind idempotency to actual actor kind/ID as well as immutable owner/resource intent;
another human or machine cannot replay someone else's command result by guessing IDs.
Read permission may expose current authorized detail, never another actor's raw key,
request hash or durable replay payload. Machine lookups remain owner-filtered SQL.

## 6. Implemented HTTP contracts (approved split)

All paths below start /api/v1/inventory. Separate Sales routes allow exact machine
middleware while retaining the existing human namespace and :id warehouse wildcard.
The implemented split uses distinct human and machine routes; do not register two
meanings for the same principal boundary. The exact displayed matrix is confirmed for implementation.

| Method/path | Permission | Success |
| --- | --- | --- |
| POST /sales/warehouses/:id/dispatches | machine inventory.dispatches:create | 201 new / 200 replay |
| POST /sales/warehouses/:id/dispatches/:dispatch_id/cancel | machine inventory.dispatches:cancel | 200 |
| GET /sales/dispatches/:dispatch_id | machine inventory.dispatches:read | 200 own detail |
| GET /sales/dispatches/:dispatch_id/transitions | machine inventory.dispatches:read | 200 own paginated audit |
| GET /sales/dispatches | machine inventory.dispatches:read | 200 own list |
| POST /warehouses/:id/dispatches/:dispatch_id/dispatch | human GLOBAL inventory.dispatches:dispatch | 200 |
| POST /warehouses/:id/dispatches/:dispatch_id/delivery-outcomes | human GLOBAL inventory.deliveries:update | 200 |
| GET /dispatches/:dispatch_id | human GLOBAL inventory.dispatches:read | 200 detail |
| GET /dispatches/:dispatch_id/transitions | human GLOBAL inventory.dispatches:read | 200 paginated audit |
| GET /dispatches | human GLOBAL inventory.dispatches:read | 200 operational list |

Strict JSON: bounded 64KiB body; application/json; no unknown, duplicate, null or
case-ambiguous fields; no query on mutations; exactly one Idempotency-Key. Existing
UUID/quantity helpers enforce canonical text and bounds. Text normalizes before
fingerprinting; omitted/empty optional text has one explicit canonical empty form.
Caller actor/producer/owner fields, lines, quantities and occurrence dates reject.

Synthetic create body (header Idempotency-Key is a distinct persisted UUID):

```json
{
  "command_id": "01935f00-0000-7000-8000-000000000031",
  "dispatch_id": "01935f00-0000-7000-8000-000000000032",
  "reservation_id": "01935f00-0000-7000-8000-000000000012",
  "order_id": "01935f00-0000-7000-8000-000000000013",
  "generation": "1",
  "expected_reservation_version": "1",
  "expected_order_version": "1",
  "external_reference": "EXAMPLE-SHIPMENT"
}
```

Synthetic dispatch confirmation body:

```json
{
  "command_id": "01935f00-0000-7000-8000-000000000033",
  "expected_dispatch_version": "1",
  "expected_reservation_version": "1",
  "expected_order_version": "1"
}
```

Cancellation: command_id, expected_dispatch_version and optional note; no physical
quantity or implicit release fields under R9-C's recommendation. Synthetic delivery:

```json
{
  "command_id": "01935f00-0000-7000-8000-000000000034",
  "expected_dispatch_version": "2",
  "outcome": "FAILED",
  "external_reference": "EXAMPLE-ATTEMPT",
  "note": "Delivery could not be completed; no goods returned into stock."
}
```

Mutation result version 1: dispatch_id, reservation_id, order_id, warehouse_id,
generation, state, version, changed_at, nullable operation_id, nullable delivery_outcome,
nullable stock_result, and replayed execution flag. The dispatch-confirmation result
embeds the validated durable stock result v1 in stock_result; metadata-only results
use null there. A later outcome still retains the pinned operation_id. It must never
fabricate a second operation. SQL expected-result construction and strict decoder
tests enforce the complete DTO. Example draft result:

```json
{
  "result_version": "1",
  "dispatch_id": "01935f00-0000-7000-8000-000000000032",
  "reservation_id": "01935f00-0000-7000-8000-000000000012",
  "order_id": "01935f00-0000-7000-8000-000000000013",
  "warehouse_id": "01935f00-0000-7000-8000-000000000001",
  "generation": "1",
  "state": "DRAFT",
  "version": "1",
  "changed_at": "2026-10-27T00:00:00Z",
  "operation_id": null,
  "delivery_outcome": null,
  "stock_result": null,
  "replayed": false
}
```

Detail includes immutable reservation line facts, current status and an audit-resource
link; transitions are separately paginated with server timestamps, versions and
evidence. No raw infrastructure, claims, secret or idempotency data. List and audit
return {items,next_cursor}; limit defaults 50 / max100. Necessary list filters:
state, warehouse_id, order_id, reservation_id; operational humans may additionally
filter owner_machine_id, while Sales owner is always trusted identity. Unique ID
descending keyset pagination, bound to principal/read scope and all normalized
filters. Audit sorts immutable transition IDs descending with a cursor bound to
dispatch, principal/read scope and endpoint. Live pages are not a frozen reconciliation
snapshot. Detail lines are bounded by the existing 100-line reservation contract;
audit always uses its paginated transitions resource, never an unbounded history array.

Errors follow safe {code,error,request_id}: 400 invalid input/overflow; 401 invalid or
wrong token kind/audience; 403 missing exact authority; 404 missing owned/authorized
resource; 409 key/command identity conflict, stale version, wrong lifecycle, prior
consumption, claimed reservation or insufficient availability; 503 dependency/busy/
outcome_unknown; existing 408 cancellation/504 deadline behavior. Unknown outcome
wins over joined timeout/cancellation. Reads/outcomes/cancel and matching completed
replay require no fresh Catalog response. Cache-Control: no-store on all responses.

## 7. Implemented atomic stock boundary and narrow repository refactor

Reuse the existing prepared direct-piece consume calculation and ConsumeReservedStock
SQL once per line. Extract only the posting work needed inside a caller-owned pgx
transaction; keep existing standalone PostingStore as a bounded BEGIN/COMMIT wrapper
with its current recovery seam. A helper must not commit, open a second transaction,
call a remote client or perform independent uncertain-commit retry.

Fulfillment confirmation owns the transaction and durable fulfillment key. In one
commit: verify current dispatch/reservation versions and eligible state; issue its
immutable held lines; write stock operation/movements/key/result; finish reservation
state/head/audit/result with explicit actual actor evidence; update dispatch state
and physical operation linkage; append fulfillment audit; complete fulfillment result.
Any statement/deferred guard failure rolls back everything. No consume-first/save-later.

Human support requires a new physical command/fingerprint version (v4,
producer inventory.dispatches.v1) and explicit owner-versus-actor linkage. Extend
reservation terminal audit/command evidence through an explicit new version where
needed; do not submit a human action as legacy machine-owned fingerprint v1 or stock
consume v3. Existing reservation machine API, v1/v2/v3 stock fingerprints, stock result
v1 and legacy replays remain byte-compatible. New fulfillment canonical intent binds
actual actor, owner, dispatch/reservation/order/warehouse/generation, command/action,
expected versions, outcome and normalized evidence fields. Keys, request_id, refreshed
tokens, server timestamps/results and fresh Catalog observations are execution data.
Never hash raw JSON or map ordering. Exact canonical version tests accompany code.

Implemented lock order: workflow idempotency claims, including derived reservation
and stock claims, before business rows; warehouse FOR SHARE -> order head -> reservation
-> dispatch/association -> balances in sorted raw UUID order. Creation/cancellation
also lock the reservation before claiming/changing its live dispatch association.
Legacy consume checks that association after its reservation lock before touching
balances, preventing a bypass or lock inversion. Existing release takes warehouse ->
order -> reservation and can win before dispatch; it never waits on a balance then
an order/dispatch. Preserve existing warehouse advisory-lock -> warehouse-row order.

Dispatch references resolve outside locks, with fresh active/eligible current Catalog
evidence, immutable product/unit binding and approved 5s/1s freshness rechecked after
pool acquisition. Default budgets remain overall5s, reference2s/attempt, cumulative
transaction3s, lock/primary lookup500ms, independent cleanup2s, at most3 attempts for
known rolled-back 40001/40P01 only. Do not relax budgets to make a fixture pass.

After commit acknowledgement uncertainty, bounded fresh physical-primary lookup
must establish the complete authorized fulfillment durable result, not just that a
stock operation exists. Missing lookup is not rollback proof. Never rerun the mutation
automatically or rotate identities. Reconcile later using original IDs/key/body.
Include the concurrent-completion replay recheck before dependency/state rejection.

## 8. Implemented additive schema and invariants

Inventory 000007: dispatch projection/immutable identity, immutable transitions and
evidence, actor-scoped durable commands/results; indexed association to reservation
and physical operation; explicit real-actor evidence/linkage additions needed by the
consume extension. No copied caller-owned mutable stock lines and no remote FK.

Constraints/guards should enforce one non-cancelled dispatch per reservation, immutable
identity/audit, legal versioned transitions, contiguous audit/projection/result
correspondence, actual actor and owner distinction, exact consume operation/lines/
quantities and complete local results. Post-dispatch outcomes cannot alter the pinned
operation, reservation identity, pieces or ledger. Cross-table checks must be backed
by consistent row locking; uniqueness alone does not replace transition validation.

Use parameterized db/queries/fulfillment.sql and shared stock/reservation queries;
regenerate pinned sqlc/sqlc:1.31.1, never hand-edit bindings. Preserve prior migration
files exactly. Down supports empty fulfillment schema, preserving earlier stock and
reservation facts; it refuses if fulfillment/new-version history needs the schema.
Auth down must preserve needed exact grants/definitions; no destructive cleanup of
normal identity/history. A refused down can leave a dirty migrate marker: inspect,
verify backup and use reviewed forward recovery, never blind force/delete.

Default STOCK_FULFILLMENT_ENABLED=false. Enable dependency: Reservations
and secured references enabled. Schema must precede the binary even when route flag
is off if shared queries/guards reference new columns/tables. Disabling HTTP routes
does not permit legacy consumption to bypass an existing draft association; plan
operational draining/recovery for in-flight drafts. No normal rollout is authorized.

## 9. Verification plan, gotchas and blast radius

Affected: Inventory stock/consume transaction boundary, reservation linkage/actor audit,
new fulfillment module, additive SQL/migration, exact route middleware/config/app,
human and machine Auth authority, disposable Platform harness. No normal deployment
or Catalog master-data change. Main risks are double issue, partial commit, lock
inversion, impersonation, replay compatibility and a schema/binary mismatch.

Verification plan (completed results recorded in section 12):

- Pure domain/HTTP tests: state/versions, exact piece bounds, malformed bodies/UUIDs,
  bounded text/filters, canonical intent stability/changed fields and execution exclusions.
- Real owned PostgreSQL: draft leaves stock unchanged; confirmation consumes once and
  persists dispatch/audit/results atomically; delivery/FAILED changes status/evidence only;
  same-key and different-key races; dispatch versus release/cancel; no historical attach;
  legacy consume cannot bypass a live draft; replacement draft uniqueness.
- Inject failures after partial SQL work and before/during commit; verify rollback,
  bounded primary recovery, no automatic retry after unknown, canceled recovery still
  unknown and later same-key reconciliation. Label faults simulated, not real partitions.
- Real authorization: current authority before replay, actor/owner isolation, human
  versus machine/audience separation, exact scopes/GLOBAL permissions, no UUID-only
  authorization. Actual Auth issuance/login and HTTPS Kong/Catalog fixtures.
- Inactive/outage policy: new dispatch blocked; reads/cancel/post-dispatch evidence and
  completed replay work without Catalog; no automatic restock on failure.
- Migration empty up/down/reapply and populated refusal preserving stock/reservation/
  fulfillment facts; old human and machine fingerprint/result replay remains valid.
- Existing Opening, adjustment/reversal, reservation, availability, warehouse lifecycle
  and stock read regressions. Heavy suites sequential with GOMAXPROCS=2/-p=1.
- gofmt, focused tests, vet/build, pinned sqlc twice/hash equality, PowerShell parse,
  scoped/new-file whitespace, UTF-8/Markdown/JSON/local links and protected hash checks.

Specific mistake prevention: never call committed consume then save dispatch; never
negate/restore stock after failed delivery; never substitute owner machine for acting
human; never use a new key to repair uncertainty; never let a generic flag bypass
persisted associations. Detection: durable correspondence guards, concurrent/fault
tests, old replay golden tests and authorized real transport acceptance.

Zerolog boundary diagnostics only: service/component/operation/stable reason/request_id,
safe business UUIDs and SQLSTATE where relevant. Pure helpers return typed errors.
Never log raw errors, tokens, credentials, URLs, evidence notes or recipient data.

## 10. Implementation files and delivery

The approved module is implemented in these layers:

1. Inventory internal/fulfillment/command.go and command_test.go (command, fingerprint
   and result contracts kept together rather than split into trivial helpers):
   small typed state/actor/command contracts, no generic workflow framework.
2. Inventory db/migrations/000007_fulfillment.{up,down}.sql,
   db/queries/fulfillment.sql and narrowly shared stock/reservation queries;
   generated internal/repository/postgres/sqlcdb/.
3. Inventory internal/repository/postgres/fulfillment.go, fulfillment_test.go;
   stock.go transaction-local extraction and explicit reservation terminal linkage
   in reservation.go/reservation_transaction.go; dedicated compatibility/fault tests.
4. Inventory internal/service/fulfillment.go, internal/handler/fulfillment.go,
   internal/security/fulfillment_machine.go and focused tests; router/router.go,
   app/app.go, config/config.go/.env.example default-off wiring.
5. Auth db/migrations/000012_inventory_fulfillment_authority.{up,down}.sql,
   internal/domain/machine_authority.go, exact issuance/handler validation as needed,
   lifecycle/SQL/tests; keep existing human/Catalog/reservation contracts unchanged.
6. Inventory scripts/test-fulfillment-postgres.ps1 and internal/integration/
   fulfillment_gateway_test.go; Platform disposable-only fulfillment overlay and
   scripts/test-inventory-gateway.ps1 extension. Generate owned fixture material only.
7. Update this specification, current rules, relevant README/read/operational docs;
   save one report at review-artifacts/inventory-fulfillment/<run>/report.md.

Final report distinguishes implemented, real disposable tests, simulated faults,
skipped/unverified checks, normal enablement and real Sales integration. List exact
files, migrations, failures/resolutions, observed quantities, protected/cleanup hashes
and final Git status versus baseline. No normal data or service changes permitted.

Normal prerequisites: approved exact grants/principals, reviewed schema-before-binary
rollout with verified backups, working Auth/Catalog TLS/trust and immutable actor
contracts. Sales contract acceptance and real Sales use remain pending independently.
Kafka integration remains Milestone 10. Full Inventory Postman collection/environment,
token automation/examples/tests remains a final release deliverable, independent of
these disposable milestone tests. Inventory Service and ERP are not complete.


## 11. Canonical versions and actual DTO details

Fulfillment fingerprint v1 uses domain `inventory.fulfillment.request\0`; uint16
version1; uint32-byte-length-prefixed actor kind; raw actor UUID; raw owner, command,
dispatch, reservation, order, warehouse UUIDs; action text; uint64 generation, expected
dispatch/reservation/order versions; outcome, external reference and note text. Hash:
SHA256. Text is UTF-8 with uint32 byte-length prefixes. Trusted immutable owner/order/
reservation/generation context is loaded by Inventory for post-create actions. Key,
request_id, refreshed JWTs, server result IDs/times and Catalog execution observations
are excluded. Optional evidence is trimmed before hashing; empty and omitted normalize
to one empty value. Create accepts external_reference but no note; cancellation accepts
note; delivery accepts external_reference and note; confirmation accepts neither.

Internal human consume reservation fingerprint v2 preserves the old field layout with
version2, then appends actual human UUID, dispatch UUID, expected dispatch version and
fulfillment intent hash. Machine reservation fingerprint v1 remains unchanged.
Stock consume v4 uses the frozen stock domain, versions4/4, HUMAN kind, actual human
UUID, separate Sales owner UUID, dispatch producer, internal reservation v2 intent hash
and sorted derived direct-piece lines. Legacy stock fingerprints v1/v2/v3 and durable
stock result v1 are unchanged; an independently calculated v3 golden test verifies
byte compatibility. Version4 uses producer inventory.dispatches.v1 and cannot enter
the existing Opening/Adjustment-only reversal allowlist.

Actor-specific fields on reservation commands/transitions are actor_user_id and
dispatch_id; legacy machine rows retain nulls and infer their actual machine from
owner_machine_id. New v2 terminal commands require explicit actual human and dispatch
linkage. No actor_user_id is filled with a machine UUID. Deferred SQL guards correlate
reservation, stock, fulfillment commands/audit/projection/results before commit.

Detail includes owner_machine_id, immutable identity, current state/version/operation,
creation external_reference, created_at/updated_at, up to100 immutable lines and
transitions_url. The transitions endpoint returns paginated action/state/version,
delivery_outcome, reference/note and server changed_at. Actual actor UUIDs are retained
in protected database audit; no human email/name or raw replay keys/claims are exposed.
Audit UUID cursor order is traversal order, not a commit/causal-order guarantee;
version defines the per-dispatch lifecycle chain. Refresh to see new entries.

Concrete new error codes: dispatch_not_found (404), dispatch_state_conflict,
stale_dispatch, reservation_has_dispatch (409). Existing shared invalid_input (400),
idempotency_key_reused (409), version_exhausted (409), stock_busy/reference_unavailable/
outcome_unknown (503), timeout (504), request_canceled (408), unsupported_media_type
(415), request_too_large (413), unauthorized (401) and forbidden (403) remain exact.
A stopped dependency can cause 503/reference_unavailable or 504/timeout; neither is
success, neither writes stock, and unknown-commit precedence stays distinct.
Human mutation responses return their authorized action result without implying GET
read permission. Human tokens remain stateless with existing <=15-minute lifetime;
a grant change is reflected on renewed tokens, not immediate live revocation.

## 12. Verification checkpoint (2026-10-27)

Real owned PostgreSQL passed draft/no-stock, full human consume once, FAILED then
DELIVERED/no-stock, historical replay/changed intent, cancellation leaving holds,
replacement drafts, terminal/prior-consume rejection, owner/permission checks,
same-key/different-key dispatch and release/cancel races, SQL immutable guards,
inactive/dependency policy, partial audit and second-movement rollback, simulated
lost acknowledgements/unknown/canceled recovery and later same-key reconciliation.
Inventory7 empty down/up preserved legacy human history; populated down refused
without deleting fulfillment/stock facts. Legacy Opening/adjustment/reversal/reservation,
stock SQL, freshness-after-pool-wait and actual HTTP/read-only PostgreSQL regressions
passed in the broad fixture. Auth12 lifecycle tests used application services and
real PostgreSQL; machine grants/issuance, requested subsets and actor separation passed.

Actual HTTPS Auth issuance/human login -> Kong -> Inventory -> HTTPS Catalog passed.
The observed primary case reserved40 from100, draft left100 on-hand/40 held, confirmation
left60 on-hand/0 held once. Another owner/warehouse confirmed under explicit human
GLOBAL authority. Machine owner isolation and scope/audience/WAREHOUSE-vs-GLOBAL
negatives passed. With fixture Catalog stopped, replay/read, delivery and draft-only
cancellation worked; new dispatch timed out with504 and wrote nothing. Fresh human
login after dispatch-grant removal denied completed replay with403, with no writes.
Previously issued JWT validity is not online-revoked. All36 pre-existing containers
retained IDs/start times/states/restart counts during fixture cleanup.

Both repositories passed full standalone tests, vet/build. Standalone gated skips
are not database/network proof; runtime evidence above comes from owned fixtures.
Simulated acknowledgement-loss tests are not real network-partition testing. No
normal enablement, Sales service integration, production load or mixed-version
rollout is verified. Maximum-size fulfillment throughput is not a production SLA.
Pinned generation/formatting/documentation/preservation evidence is recorded in the
single workspace review-artifacts/inventory-fulfillment delivery report.

Fixes found by verification: cursor actor keys now use strict lowercase JSON; the
shared transaction wrapper preserves replay flags returned by concurrent lookup;
wrong-owner draft creation maps missing owned context to404; outage acceptance checks
the documented503/504 status/code pairs instead of assuming an immediate503.
No timeout budgets were relaxed. An interrupted fixture rerun was explicitly cleaned
only after validating its unique ownership label and temporary directory boundary.

Deploy Inventory schema7 before compatible binaries, even with the fulfillment gate
off when reservation queries/guards use the new columns/tables. Auth schema12 must
precede fulfillment machine grants and enablement. Use coordinated compatible
binaries/feature gates; mixed-version or older-binary rollback after v4 history is
not certified. Populated downgrade refuses and requires reviewed forward recovery.
STOCK_FULFILLMENT_ENABLED defaults to false. Enabling it requires
STOCK_RESERVATIONS_ENABLED=true and working secured references; human/Sales grants, TLS/trust and backups require their
own normal rollout authorization. No normal data, accounts, credentials or services
were changed. Sales contract acceptance and real integration remain pending.
