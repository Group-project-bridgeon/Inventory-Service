# Warehouse verification

From Inventory-Service, alongside the actual Authentication checkout:

```powershell
go test -count=1 ./...
go vet ./...
go build ./...
git diff --check
pwsh -NoProfile -File ./scripts/test-warehouse-postgres.ps1 -IncludeHTTP
```

Requires PowerShell 7.4+, Go and Docker Desktop. The harness pins
postgres:17-alpine and migrate/migrate:v4.18.3 (the existing ERP convention).
Without -IncludeHTTP, it runs only migration and repository checks. Ordinary Go
runs skip database/HTTP tests without their dedicated fixture environment.
Never point those test variables at normal ERP databases.

## Isolation and expected results

A unique inventory-warehouse-test-<UUID> container uses tmpfs data, generated
credentials and a random localhost port. No .env, existing data mount, Compose,
existing volume removal, account change or regular database is involved. The HTTP
fixture uses inventory_db/inventory_service inside its new container to satisfy
the existing application identity check; repository cases use inventory_warehouse_test.
Credentials pass through process environments, not CLI arguments. The migrate
CLI uses a password-free URL and lib/pq reads PGPASSWORD. Readiness requires
`psql -X -q -t -A -v ON_ERROR_STOP=1 ... -c 'SELECT 1'` against the intended
database, not merely pg_isready.

Expected assertions:

- Inventory up: version=1, dirty=false. Complete down: zero schema_migrations rows
  and no warehouse table/function/trigger. Reapply: version=1, dirty=false.
- Auth prerequisites and warehouse migration: version=6, dirty=false; exactly
  create/read/update definitions; no users or automatic grants.
- Auth down one: version=5, dirty=false, definitions intentionally retained.
  Reapply is idempotent. Complete down removes application tables and version row;
  full reapply ends at version=6, dirty=false.
- Repository persistence, duplicate-code enforcement and Commit-time deferred
  trigger mapping. Separate empty schemas and synchronization barriers verify
  eight distinct-code first creations persist with one active default, and eight
  normalized same-code creations produce one success/seven duplicate errors,
  one persisted row and one active default. Later concurrent promotions are tested.

With -IncludeHTTP, actual Auth and Inventory executables run from a temporary
working directory with fresh RSA keys and an explicit clean environment. Fresh
identities and least-privilege roles exist only in the disposable Auth database.
Successful tokens come from real login; the test locally verifies signatures and
the full Inventory claim contract. Expected HTTP statuses:

- Real login and authenticated /me: 200.
- Public /health and /ready on both services: 200.
- Missing token: 401; no grants or WAREHOUSE-only grants: 403.
- Reader list/detail: 200; writes: 403.
- Editor create: 201; update/status: 200.
- Default deactivation and inactive promotion: 409.

Denied writes must leave a full database snapshot unchanged. Default promotion
and former-default deactivation must persist with exactly one active default.
This is direct HTTP Auth/Inventory integration, **not Kong verification**.
Only generated service processes/container/temp files are removed, including all
fixture data, keys and binaries. Previous environment values are restored.
Cleanup failure fails the run.

## Generated-code reproducibility

Run pinned sqlc twice, checking each exit code. Compare SHA256 hashes before and
after the second run; expected comparison output is empty.

```powershell
docker run --rm -v "${PWD}:/src" -w /src sqlc/sqlc:1.31.1 generate
if ($LASTEXITCODE -ne 0) { throw 'sqlc failed' }
$before = Get-ChildItem internal/repository/postgres/sqlcdb/*.go | Get-FileHash
docker run --rm -v "${PWD}:/src" -w /src sqlc/sqlc:1.31.1 generate
if ($LASTEXITCODE -ne 0) { throw 'sqlc failed' }
$after = Get-ChildItem internal/repository/postgres/sqlcdb/*.go | Get-FileHash
Compare-Object $before $after -Property Path,Hash
```

Format only changed Go files. Review untracked files and existing line-ending
noise as well as git diff. A successful generator exit alone is not proof of
reproducibility.

## Archive hygiene

.gitignore does not exclude secrets from manually created ZIPs. Use:

```powershell
pwsh -NoProfile -File ./scripts/export-source.ps1 -Destination <new-output-path>.zip
```

The exporter includes current tracked/untracked nonignored source, explicitly
excludes .env variants except placeholder-only .env.example, keys, binaries/logs
and nested archives, and refuses to overwrite a destination. Inspect entries
before sharing. Never put real credentials in .env.example. Existing archives
are preserved. If a credential-bearing archive was shared, coordinate rotation
and recipient removal; committed credentials also need coordinated history review.

## Unverified and future boundaries

Disposable results do not establish regular ERP deployment, Kong behavior or
live token revocation. token_version is contract validation only; Inventory makes
no live Auth revocation lookup. created_by/updated_by, full audit history, stock
workflows and a transaction-runner abstraction are separate design follow-ups.
No applied migration is edited by this hardening task.
