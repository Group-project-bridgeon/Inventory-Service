# Warehouse management specification

## Confirmed business rules

- Multiple warehouses; warehouse management only. No bins, stock, reservations, transfers, or Kafka.
- Required code and name; optional address. Code is immutable after creation.
- Exactly one active default once any warehouse exists. The first warehouse becomes default automatically.
- Switching default to another active warehouse is atomic. Deactivation of the current default returns a conflict until another warehouse is selected.
- No hard deletion. Deactivation does not yet check stock; this is a future stock-workflow invariant.

## Proposed API and validation

Base path: `/api/v1/inventory/warehouses`.

| Method/path | Behavior | GLOBAL permission |
| --- | --- | --- |
| POST / | Create active warehouse; first is default | inventory.warehouses:create |
| GET / | Cursor-paginated list | inventory.warehouses:read |
| GET /:id | Detail | inventory.warehouses:read |
| PUT /:id | Replace name/address; optionally promote with is_default=true | inventory.warehouses:update |
| PATCH /:id/status | Set required is_active boolean | inventory.warehouses:update |

Use Catalog UUIDv7 IDs, direct entity responses, 201 on creation and 200 on reads/updates. List uses items and next_cursor with default limit 50, maximum 100, stable code/ID ordering and an opaque cursor. PUT requires name; omitted address clears it. Code in an update is rejected rather than silently ignored. is_default=false cannot demote the current default; promote another active warehouse instead. Omitted is_default preserves its designation.

Proposed validation defaults: trim and uppercase code, 1-50 ASCII letters/digits/underscore/hyphen, beginning with a letter or digit; trim name, 1-100 Unicode characters; trim optional address, maximum 500 Unicode characters. Reject control characters. UUIDs must be nonzero. Public errors have stable code, safe error message and request_id: 400 validation, 401 invalid credentials, 403 permission denied, 404 missing warehouse, 409 duplicate code/default conflict, 500 unexpected failures; cancellation and deadline failures have distinct safe classifications.

## Data and architecture

Inventory migration 000001 creates warehouses with UUID primary key, normalized unique code, name, address, is_active, is_default, created_at and updated_at. Database checks enforce normalized code and default implies active; a partial unique index enforces at most one default, and a deferred constraint trigger requires one default at transaction commit whenever warehouses exist. A transaction-scoped advisory lock serializes warehouse mutations, including first creation and default switching. Reads remain unlocked. This intentionally favors simplicity for low-volume warehouse metadata writes.

Use existing domain/service/repository/handler layers and sqlc-generated pgx/v5 queries. Preserve request cancellation. No cross-service database access. Authentication migration 000006 adds three permission definitions without grants on its own feature branch; its rollback retains definitions as Catalog does, to avoid deleting grants or pre-existing rows. Warehouse down migration is destructive and is tested only in disposable infrastructure; regular database rollback requires a backup/export before removing the table.

## Security and diagnostics

Port the inspected Catalog public-key-only RS256 contract into Inventory without cross-service internal imports: verify algorithm, signature, issuer, audience, numeric times, subject/user ID, token version and permissions; enforce maximum 15-minute lifetime. Exact GLOBAL permission matching, fail closed for unmapped routes, only exact GET health/ready public. Stateless revocation can take up to 15 minutes.

Extend the existing zerolog helpers: validated/generated X-Request-ID, request-scoped logger, matched route templates or fixed unmatched label, safe classified causes, one diagnostic per unexpected failure, sanitized panic stack. Routine successful requests and repeated client failures do not generate Info/Warn floods; slow requests use the configured threshold. Probe failure diagnostics are rate bounded. Never serialize raw errors, tokens, bodies, SQL values or credentials.

## Blast radius and verification

Inventory app/config/router/middleware/logger wiring, new warehouse layers, SQL bindings/migrations and documentation/tests; Authentication receives only an additive migration. ERP-Platform, actual keys, .env, accounts/grants and running services remain untouched.

Verify validation, immutable code, cursor bounds, error mapping, every route's authorization before mutation, request correlation and secret canaries. Use isolated disposable PostgreSQL for migration up/down, persistence, actual unique constraint behavior and concurrent default transitions. Run formatting, reproducible sqlc generation, go test -count=1 ./..., go vet ./..., go build ./... and whitespace checks including new files. Report real exit codes and skips. Live deployment/Kong checks remain out of scope.

## Delivery stages

1. Approve this specification and validation defaults.
2. Implement schema/generated queries, warehouse layers and atomic default transitions.
3. Wire authentication, authorization and correlated safe diagnostics; add companion permission migration.
4. Complete focused tests, isolated database checks and documentation; review branch diffs.

No commits, pushes, PRs, merges, real database migrations or service restarts.
