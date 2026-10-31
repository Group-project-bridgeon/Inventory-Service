# Stock Read APIs

Status: implemented and verified with disposable PostgreSQL/HTTP. Contract approved
2026-10-25, including the user's selected
inventory.stock-balances:read balance permission.
This milestone completes existing read helpers; it does not enable stock mutations.

## Endpoints and approved authority

| GET endpoint | Exact GLOBAL permission | Query filters |
| --- | --- | --- |
| /api/v1/inventory/warehouses/:id/stock-balances | inventory.stock-balances:read | variant_id, nonzero_only, limit, cursor |
| /api/v1/inventory/warehouses/:id/stock-movements | inventory.stock-movements:read | variant_id, limit, cursor |

This replaces the older proposed read route/permission matrix in stock-foundation-spec.md
section 8 for this milestone only. The existing proposed inventory.stock-balances:read
name is now approved; the required variant history filter is replaced by an optional filter. No
inventory.warehouses:read implication, wildcard, WAREHOUSE grants or machine-token
access is introduced. Permission definitions alone assign no users/roles/grants;
authorized administrators must grant separately, and human users need a fresh login
JWT after changes. Existing JWT expiry/revocation limitations remain unchanged.

## Request and response contract

Nonzero canonical UUIDs are required for :id and any variant_id. Each supported
query key may occur at most once; unknown keys, empty explicit values, invalid URL
encoding and malformed UUID/boolean/limit/cursor return 400. nonzero_only is exactly
true or false (default false). limit defaults to 50 and accepts decimal digits 1..100.
Cursors are at most 1024 ASCII characters, unpadded base64url of canonical JSON,
versioned and bound to endpoint kind, warehouse, variant filter and nonzero flag.
Page size may change between pages. They carry only the last returned key, never
the look-ahead row, and confer no authority; editing a valid key only changes the
traversal start inside the independently authorized warehouse/filter.

Both return {"items":[],"next_cursor":null} when no stored rows match. A nonexistent
warehouse returns 404 after authentication/authorization and input validation.
Inactive warehouses and historical inactive Catalog references remain readable.
Balances enumerate stored pairs only; absence does not enumerate Catalog variants,
prove eligibility or fabricate zero stock. Reads never contact Catalog or initialize
balances. Missing/invalid/expired human authentication returns existing 401; missing
exact permission returns 403. Infrastructure errors use generic existing errors,
with request_id; cancellation/timeout follow existing 408/504 behavior, other
unexpected failures 500. Successful read responses use Cache-Control: no-store.

Balance example (illustrative UUIDs):

```json
{"items":[{"warehouse_id":"01935f00-0000-7000-8000-000000000001","variant_id":"01935f00-0000-7000-8000-000000000002","base_unit_id":"01935f00-0000-7000-8000-000000000003","on_hand_pieces":"12","version":"1","updated_at":"2026-10-25T00:00:00Z"}],"next_cursor":null}
```

Movement DTO: id, operation_id, warehouse_id, variant_id, direction, input_count,
pieces_per_input, pieces, before_pieces, after_pieces, balance_version, posted_at,
product_id, base_unit_id, nullable pack_id/pack_revision/pack_label, product_code,
product_name, sku, base_unit_code, stock_reference_revision and reference_checked_at.
All BIGINT quantities/versions are decimal strings. pack_revision preserves recorded
revision text; real Catalog pack revisions use canonical decimal strings, but reads
do not rewrite older recorded text. Null pack values mean direct pieces.
No generated database struct, actor claims, request metadata, idempotency hash or
replay JSON is exposed. Recorded snapshots preserve posting-time labels/factors;
they do not claim current Catalog state. There is no invented business reason.
Direction and operation identity do not establish a user-facing reason.

## Pagination and database changes

Balances sort variant_id ascending within the required warehouse and use > after
variant. Movement history sorts unique movement id descending and uses < before
movement id across all variants, optionally filtered. posted_at is displayed,
not used as commit order; balance_version repeats across variants and is not a
warehouse ordering key. Existing ListStockHistory and its callers remain intact.

Both are ordinary live keyset reads, not a frozen multi-page snapshot. Values can
change between balance pages, nonzero membership can change, and lower-sorting
new balance keys may be missed. An insertion whose movement ID sorts behind the
current descending cursor may appear later; a newly committed ID above that
cursor is missed until refresh. UUIDv7 allocation and posted_at do not establish
commit order: a transaction that began earlier can commit later. Static datasets
traverse without duplicates/gaps. Do not use live pages as a consistent reconciliation
snapshot; refresh the first page for new history.

Reuse ListStockBalances. Add parameterized ListWarehouseStockMovements with limit
clamped to 101 and warehouse/optional variant filters. Proposed next Inventory
migration 000003 adds (warehouse_id,id DESC) and (warehouse_id,variant_id,id DESC) indexes;
verify their plans for bounded filtered/unfiltered traversal on realistic synthetic
data. No stock facts or existing migrations change. Normal index creation can block
writes while building; a target-environment rollout/maintenance plan is required.
Rollback drops only these indexes; existing data remains. Auth migration 000008 defines only the approved
read permissions; its down migration retains definitions to preserve pre-existing
rows/grants, matching existing permission migration conventions. Never apply either
to normal databases during this task.

## Layer flow and verification plan

handler -> service -> repository -> generated SQL -> PostgreSQL -> explicit DTO.
Handler authenticates through existing human middleware, strictly parses queries,
and serializes BIGINTs as strings. Service validates/binds cursors before repository
access, checks local warehouse existence (including inactive), fetches at most
limit+1, and creates the next cursor from the last returned row. Repository maps
pgtype values and classifies errors with existing safe helpers. Existing pool
statement deadlines plus a bounded read context cap DB work; no external HTTP or
write transaction is involved. Preserve router constructors/callers deliberately.

Tests: service validation/missing/empty/inactive/look-ahead/filter binding/errors/
cancellation; actual disposable PostgreSQL warehouse/variant/nonzero filters,
int64 boundaries, equal timestamps and multi-variant ordering/static pagination,
documented concurrent-insert behavior, immutable historical snapshots and no
writes; HTTP 401/403/200/400/404, string BIGINTs/empty arrays, human/machine separation,
warehouse routes/probes and Gin wildcard coexistence. Run index migration up/down/
reapply, EXPLAIN, pinned sqlc twice with hash equality, formatting, scoped whitespace,
Go tests/vet/build. Gateway runtime verification is pending unless a safe disposable
Kong harness can be used. Never restart the normal stack.

## Request flow and code locations

1. internal/router/router.go registers both GET routes under the existing :id
   wildcard and exact human GLOBAL policies. internal/app/app.go injects a local
   StockReadService using the configured DB_STATEMENT_TIMEOUT. Existing constructors
   remain compatible through an optional read-service parameter.
2. internal/handler/stock_read.go strictly parses the route/query, invokes the
   service using the request context, and constructs explicit DTOs. For example,
   strconv.FormatInt(b.OnHandPieces, 10) preserves 9223372036854775807 exactly.
3. internal/service/stock_read.go binds canonical version-1 cursors, checks local
   warehouse existence, and requests Limit: int32(in.Limit+1). For the next cursor
   it uses page.Items[in.Limit-1], never the look-ahead item.
4. internal/repository/postgres/stock_read.go reuses the supplied balance mapping
   and generated ListStockBalances; its new movement method maps the generated
   ListWarehouseStockMovements rows and nullable pack snapshots into domain views.
5. db/queries/stock.sql owns the SQL; sqlc/sqlc:1.31.1 generates stock.sql.go.
   Existing ListStockHistory is unchanged. No generated database struct reaches HTTP.

Actual complete handwritten code and per-file explanations are delivered in the
workspace review-artifacts/stock-read-api-5ea469c9f3d343469294e83f58484ec3/implementation-code.md.

Movement response example (illustrative durable facts, not a live response):

```json
{
  "items": [{
    "id": "01935f00-0000-7000-8000-000000000004",
    "operation_id": "01935f00-0000-7000-8000-000000000005",
    "warehouse_id": "01935f00-0000-7000-8000-000000000001",
    "variant_id": "01935f00-0000-7000-8000-000000000002",
    "direction": "IN",
    "input_count": "2", "pieces_per_input": "6", "pieces": "12",
    "before_pieces": "0", "after_pieces": "12", "balance_version": "1",
    "posted_at": "2026-10-25T00:00:00Z",
    "product_id": "01935f00-0000-7000-8000-000000000006",
    "base_unit_id": "01935f00-0000-7000-8000-000000000003",
    "pack_id": "01935f00-0000-7000-8000-000000000007",
    "pack_revision": "1", "pack_label": "Box of six",
    "product_code": "EXAMPLE", "product_name": "Recorded product name",
    "sku": "EXAMPLE-PC", "base_unit_code": "PC",
    "stock_reference_revision": "cat-stock-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "reference_checked_at": "2026-10-25T00:00:00Z"
  }],
  "next_cursor": null
}
```

## PowerShell 7 verification

1. For reproducible local evidence without normal services or credentials, run:

```powershell
Set-Location 'C:\Users\abdum\OneDrive\Desktop\New folder\Coding\Group Project\Inventory-Service'
pwsh -NoProfile -File .\scripts\test-stock-read-postgres.ps1
if ($LASTEXITCODE -ne 0) { throw 'Disposable stock read verification failed' }
```

The harness owns its generated database/credentials and destroys its fixture on
exit. HTTP tokens are signed with a temporary test key; this test does not log a
real user in. The separate existing test-reference-e2e.ps1 verifies unchanged
history after real Catalog fixture changes and dependency outage.

2. For interactive checks, use a separately authorized test environment with
   Inventory migrations through 000003, Auth definitions through 000008, and an
   explicitly granted human account. This task did not apply normal migrations,
   grant an account or start normal services. Login again after an approved grant
   change; a previously issued JWT does not gain new permissions.
3. Set placeholders and read the human token without echoing it:

```powershell
$base = 'https://<approved-test-host>'
$warehouse = '<existing-test-warehouse-uuid>'
$variant = '<stored-test-variant-uuid>'
$token = Read-Host 'Human access token for the approved test environment' -AsSecureString
$path = "$base/api/v1/inventory/warehouses/$warehouse"
$balances = Invoke-RestMethod "$path/stock-balances?limit=2&nonzero_only=true" -Authentication Bearer -Token $token
$balances.items
if ($balances.next_cursor) {
    $cursor = [Uri]::EscapeDataString($balances.next_cursor)
    $next = Invoke-RestMethod "$path/stock-balances?limit=2&nonzero_only=true&cursor=$cursor" -Authentication Bearer -Token $token
    $next.items
}
$history = Invoke-RestMethod "$path/stock-movements?variant_id=$variant&limit=2" -Authentication Bearer -Token $token
$history.items
```

4. Expect 401 with no/invalid/expired token, 403 with only warehouse read or the
   other stock-read permission, 400 with limit=0/nonzero_only=1/repeated keys/wrong
   filter cursor, and 404 with a nonexistent warehouse UUID and correct permission.
   Existing empty warehouses return 200 with items: []; inactive warehouses stay
   readable. Preserve filters when following a cursor. An unknown variant filter
   returns an empty list rather than Catalog validation or a fabricated balance.
5. Check all quantities/versions are JSON strings; ensure no internal idempotency,
   request or actor metadata appears. Never paste real tokens/cursors into logs,
   screenshots, source files or exported Postman collections.

## Postman

Use a private test environment with base_url, warehouse_id and optional variant_id.
Configure GET {{base_url}}/api/v1/inventory/warehouses/{{warehouse_id}}/stock-balances,
Bearer Token {{human_token}}, and query limit=2, nonzero_only=true. For history,
replace the final path with stock-movements and omit nonzero_only. Copy next_cursor
into a cursor parameter on the same route with the same filters. Keep human_token
in a private/local secret field and do not export it. Test each permission separately
and use the negative cases above; do not create grants or mutate stock through these
read requests. TLS verification must stay enabled.

Production mutations remain unwired. Opening Stock, adjustments, reservations,
delivery, Kafka, Outbox, Inbox and Saga are outside this milestone. The next task
is a separately approved production stock workflow/actor-producer authorization
specification, not an arbitrary mutation endpoint. Normal rollout and gateway runtime
verification are separate from completing the direct authenticated read paths.


## Adjustment/correction audit extension

After Inventory migration 000005, movement items may additionally include
`reason_code`, `explanation`, and `reverses_operation_id`. These are immutable
operation metadata, available under the existing exact GLOBAL
`inventory.stock-movements:read` permission; legacy movement items omit them.
The reversal's pack conversion/labels come from the original posted facts, while
its reference revision and validation time describe a fresh current direct-piece
eligibility check. See [Stock Adjustments and Corrections](stock-adjustments-spec.md).
No Catalog lookup is performed by stock reads. BIGINT response fields remain strings.


## Reservation availability extension (Milestone 8)

Inventory 000006 adds decimal-string `reserved_pieces`, `available_pieces` and
`reservation_version` to balance items. available_pieces is on_hand_pieces minus
reserved_pieces, with 0 <= reserved <= on_hand. Existing `version` still counts
physical movements; availability changes on physical movements or hold transitions,
so compare `(version,reservation_version)`. Reserve/release create no movement rows.
`nonzero_only` continues to mean nonzero **on-hand**, including fully held stock.
Human balance/movement authority is unchanged; Sales machines use separate owner-only
reservation status routes, never these human routes. No actor secrets are exposed.
See [reservation contracts, rollout and sequencing](stock-reservations-spec.md).

Current binary prerequisite: Inventory schema through 000006 even when reservation
posting is off, because shared generated/read queries reference the new columns.
Earlier migration numbers above describe the original Stock Read milestone.


## Milestone 9 compatibility checkpoint

Current binary prerequisites advance to Inventory schema7 for the shared reservation/
fulfillment guards and query bindings. Balance/movement read permissions and payload
meanings remain unchanged. Drafts/delivery metadata create no movement; confirming
full dispatch consumes its hold once and records a HUMAN actor separately from the
Sales machine owner. Dispatch operations are not eligible for the existing Opening/
Adjustment-only correction endpoint. See [fulfillment](fulfillment-spec.md).
