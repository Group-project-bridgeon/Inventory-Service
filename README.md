# Inventory Service

Inventory service for the team ERP project. Owns inventory_db; product and variant master data belongs to Catalog Service.

## Implemented scope

The service provides warehouse create/list/detail/update/status endpoints, UUIDv7 identifiers, cursor pagination, normalized unique codes, and an atomic single-default rule. A partial unique index and deferred trigger enforce exactly one active default at commit whenever warehouses exist. Warehouse writes use the inventory-owned PostgreSQL database through pgx and sqlc. Exact GET /health and GET /ready stay public; business routes require RS256 Auth access tokens and exact GLOBAL permissions. Stock balances, movements, reservations, transfers, bins/racks, Docker/Kong and deployment integration are later milestones. See [inventory-rules.md](docs/inventory-rules.md) and the [warehouse specification](docs/warehouse-implementation-spec.md).

## Prerequisites and conventions

- Go 1.25.4 (matches the inspected Catalog go.mod, Dockerfile and installed toolchain).
- Gin v1.12.0, pgx/v5 v5.11.0, godotenv v1.5.1 and zerolog v1.35.1 match Catalog direct dependencies. go.sum records resolved checksums.
- Catalog uses zerolog, not slog; this bootstrap follows that existing JSON logging convention.
- A provisioned PostgreSQL inventory_db and inventory_service login. Platform infrastructure/postgres/init/01-create-service-databases.sh configures these, but inspecting that script does not prove it has run. This service creates no schemas or records.

## Responsibilities

- cmd/server/main.go: invoke application and exit nonzero after cleanup on failure.
- internal/app: dotenv, startup wiring, signal handling, HTTP timeouts and shutdown.
- internal/config: load and validate typed environment settings once.
- internal/platform/database: validate database identity, configure pool and bounded startup ping.
- internal/platform/logger: JSON logs with service identity.
- internal/router: probes using a small injectable Ping(context.Context) interface.
- internal/middleware: safe recovery, request read limits, failure/slow-request logs.
- docs/inventory-rules.md: confirmed scope and unresolved business decisions.

## Configuration

Environment variables take precedence over optional .env defaults. No real .env is supplied. Explicitly empty numeric/duration settings are invalid. Errors name configuration keys but do not echo values or connection URLs.

| Variable | Default | Meaning |
| --- | --- | --- |
| DATABASE_URL | required | Must use inventory_service and inventory_db |
| JWT_PUBLIC_KEY_PATH | required | Path to Auth's public RSA PEM; Inventory never accepts a private key |
| JWT_ISSUER | required | Expected JWT issuer; currently `erp-auth` |
| JWT_AUDIENCE | required | Expected JWT audience; currently `erp-api` |
| PORT | 8082 | Integer 1-65535 |
| DB_MAX_CONNS | 10 | Positive int32 |
| DB_MIN_CONNS | 2 | 0 through maximum |
| DB_MAX_CONN_LIFETIME | 30m | Positive duration |
| DB_MAX_CONN_IDLE_TIME | 5m | Positive duration |
| DB_STARTUP_TIMEOUT | 5s | Connection/startup ping bound |
| DB_STATEMENT_TIMEOUT | 5s | PostgreSQL statement timeout, 1ms through 2147483647ms |
| READINESS_TIMEOUT | 2s | Per-request readiness ping bound |
| HTTP_MAX_BODY_BYTES | 1048576 | Positive bytes |
| HTTP_SLOW_REQUEST_THRESHOLD | 500ms | Positive slow-request threshold |
| HTTP_READ_HEADER_TIMEOUT | 5s | Header read timeout |
| HTTP_READ_TIMEOUT | 15s | Whole request read timeout |
| HTTP_WRITE_TIMEOUT | 15s | Response write timeout |
| HTTP_IDLE_TIMEOUT | 60s | Keep-alive idle timeout |
| SHUTDOWN_TIMEOUT | 10s | HTTP graceful drain deadline |

All durations must be positive Go duration strings. Statement timeout is sent as integer milliseconds (sub-millisecond remainder is truncated); values below 1ms are rejected so they cannot silently disable the timeout. Keep READINESS_TIMEOUT below HTTP_WRITE_TIMEOUT so clients can receive the probe response.

Port 8082 is proposed because Auth uses 8080 and Catalog uses 8081 in the inspected platform configuration. Port availability is not reserved and must be checked when starting. The HTTP server binds all interfaces, matching Catalog's convention.

## PostgreSQL host versus future container execution

Platform docker-compose.yml publishes ${POSTGRES_PORT:-5432}:5432; its .env.example sets POSTGRES_PORT=5432. Catalog's example uses host port 5433, so do not copy it without checking your actual Platform host mapping. Never commit real credentials or .env files.

For host execution, use 127.0.0.1 and the actual published host port. For a future container on the Platform network, use postgres:5432. Container wiring is not implemented here. sslmode=disable in the example is for local development; use suitable TLS settings for remote deployments.

## PowerShell setup and run

Run from `feat/warehouse-management`; never paste credentials into chat or commit them. Replace PASSWORD with the URL-encoded Inventory password locally, set JWT_PUBLIC_KEY_PATH to the Auth public key supplied through your secret/configuration workflow, and adjust the host port to the actual mapping. Do not copy Auth's private key.

```powershell
Set-Location <workspace>/Inventory-Service
git branch --show-current
# Expected: feat/warehouse-management

go mod download
# Only create a local .env if one does not already exist:
if (-not (Test-Path -LiteralPath '.env')) { Copy-Item -LiteralPath '.env.example' -Destination '.env' }
notepad .env
# Edit DATABASE_URL locally and save before proceeding.
Get-NetTCPConnection -LocalPort 8082 -State Listen -ErrorAction SilentlyContinue
go run ./cmd/server
```

Alternatively set DATABASE_URL in the process environment through your local secret-management workflow; it overrides .env. The program fails startup if PostgreSQL cannot be reached. Use Ctrl+C to shut down. Do not restart other ERP services for this bootstrap.

In a second PowerShell terminal:

```powershell
Invoke-RestMethod 'http://localhost:8082/health'
Invoke-RestMethod 'http://localhost:8082/ready'
```

/health returns 200 with {"status":"ok","service":"inventory"} without querying PostgreSQL. /ready returns 200 with status ready, or 503 with status not_ready. Readiness uses a request-derived timeout, propagating client cancellation. Neither probe exposes database details. After startup, a database outage affects readiness but not liveness.

## Warehouse API

All routes below are under `/api/v1/inventory/warehouses`. JSON responses use explicit snake_case fields; errors contain `code`, safe `error`, and `request_id`.

| Method and path | Permission | Result |
| --- | --- | --- |
| POST `/` | `inventory.warehouses:create` | Create; 201; first record becomes default |
| GET `/` | `inventory.warehouses:read` | `{items, next_cursor}`; `limit` defaults to 50 and is bounded to 1-100; optional opaque `cursor` |
| GET `/:id` | `inventory.warehouses:read` | Detail by UUID |
| PUT `/:id` | `inventory.warehouses:update` | Replace name/address; optional `is_default: true` promotes an active warehouse |
| PATCH `/:id/status` | `inventory.warehouses:update` | Set required `is_active` boolean |

Create body: `{"code":"WH-01","name":"Main","address":"Optional"}`. Code is trimmed and uppercased; updates reject unknown fields (including code), so it cannot change. PUT requires name; omitted address clears it; omitted `is_default` preserves current state. To switch defaults, PUT another active warehouse with `is_default:true`; switching is atomic. The current default cannot be demoted/deactivated until another is selected. Duplicate code and default conflicts return 409. There is no hard-delete endpoint.

Authorization is GLOBAL-only; warehouse-scoped grants are not accepted. A token's grant state can remain effective for at most its 15-minute lifetime. Permission definitions are an additive Authentication migration; explicit role grants are a separate deployment action and are not made by this service.

Apply Inventory migrations only through the approved deployment procedure after backup and review. Never apply them to a regular database as part of local code verification. The down migration drops the warehouse table; recovery requires restoring the pre-migration backup. The sqlc generation command is in `docs/verification.md`.

## Verification

```powershell
gofmt -w cmd internal
docker run --rm -v "${PWD}:/src" -w /src sqlc/sqlc:1.31.1 generate
go test -count=1 ./...
go vet ./...
go build ./...
git diff --check
git status --short
```

Unit tests do not require PostgreSQL. They cover validation, pagination, JWT claims and permissions, request correlation, safe diagnostics, probes, recovery, body limits and shutdown. Isolated PostgreSQL integration checks are required to verify actual constraints, transactions and persistence; unit tests do not prove live database, Kong or deployment behavior. See [verification.md](docs/verification.md) for isolated checks and [logging.md](docs/logging.md) for the diagnostics contract.

Live database startup, actual provisioning, database outage/recovery and real OS signal behavior require local runtime verification. Configuration files and passing unit tests do not establish these facts.

## Operational gotchas and troubleshooting

- Missing DATABASE_URL: startup intentionally fails. Configure it locally; validate by restarting and checking /ready. Database URLs are never included in errors.
- Wrong database/user: startup rejects them before connecting; use only inventory_db/inventory_service. Unit tests verify identity rejection. This configuration check does not replace database-side privileges.
- Unreachable PostgreSQL: inspect the actual host port, provisioning and credentials locally. Startup is bounded and releases the pool on failure; no migrations or repairs run automatically.
- Occupied HTTP port: select an available PORT and update probe URLs. The listener failure returns nonzero and releases the pool. Do not stop another service automatically.
- Streamed bodies: MaxBytesReader only discovers overflow when read. Known oversized Content-Length is rejected with 413; future body-reading handlers must map http.MaxBytesError to 413. Tests cover known length, streaming reads, exact limit and unread streams. Wrapping alone does not guarantee a JSON 413.
- Secret leakage: recovery logs panic type and stack, not panic values; request logs use route templates and omit query strings, bodies and headers. Tests inject a marker secret and check it is absent. Probes skip routine request logs.
- Shutdown ordering: HTTP drains before the pool closes. If draining expires, Server.Close cancels remaining request contexts. Future handlers must honor context cancellation and release acquired connections; pgxpool.Close waits for borrowed connections. Lifecycle tests verify both drain and forced cancellation.


## Safe startup diagnostics

Fatal failures are logged once by main after app cleanup. JSON fields include service,
component, operation, reason, and message (a safe summary). A valid five-character
sqlstate is included only for PostgreSQL server errors. HTTP responses remain generic.
The diagnostic payload retains no raw cause. Never log a raw pgx error or connection URL.

| Reason | Meaning / next check |
| --- | --- |
| invalid_connection_configuration | Check URL syntax and inventory_db/inventory_service identity locally |
| authentication_failed | SQLSTATE 28P01 or 28000; verify configured credentials and server authentication policy |
| database_missing | SQLSTATE 3D000; verify provisioning of inventory_db |
| insufficient_privileges | SQLSTATE 42501; inspect the Inventory role's permitted access |
| connection_refused | Check PostgreSQL listener and actual published host port |
| dns_lookup_failed | Check configured hostname and DNS |
| timeout | Check connectivity and startup timeout; distinct from cancellation |
| context_canceled | Startup/request context was canceled, for example by a shutdown signal |
| postgres_server_error | Other server failure; use the SQLSTATE for investigation |
| unknown_connection_failure | Cause is unclassified; do not infer a password failure |
| invalid_configuration | Configuration key/constraint is included without its value |
| invalid_dotenv / permission_denied | Check local dotenv syntax or OS access permissions |
| address_in_use / listen_failed | Check the HTTP port and local listener configuration |

Database operations are parse_config, validate_identity, create_pool and startup_ping.
Application operations include load_dotenv, load_config, listen, serve and shutdown.
Unclassified runtime HTTP failures use serve_failed/shutdown_failed; an unexpected error
at the boundary uses unknown_failure and omits its raw message.

Illustrative outputs (these do not identify the cause of a particular local failure):

```json
{"level":"error","service":"inventory","component":"database","operation":"startup_ping","reason":"authentication_failed","sqlstate":"28P01","message":"PostgreSQL authentication failed"}
{"level":"error","service":"inventory","component":"database","operation":"startup_ping","reason":"connection_refused","message":"PostgreSQL connection refused; check listener and host port"}
```

Run `go run ./cmd/server` again from this repository with your existing local configuration.
Use the resulting reason to choose the next investigation. A healthy container, reachable
host port, or a successful separate login does not establish that the application's URL
and credentials are correct. Diagnostics tests inject synthetic failures and a no-network
pgx dialer; they do not prove successful live database startup.

Warehouse hardening verification and safe source archives: see [docs/verification.md](docs/verification.md). Inventory validates token_version as a signed contract field only; no live Auth revocation lookup occurs. JWT public-key validation runs before database initialization.

## Docker and Kong

See [container instructions](docs/docker.md) and the linked Platform integration guide. Inventory is internal on port 8082; clients use Kong at localhost:8000/api/v1/inventory/warehouses.
