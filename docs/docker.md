# Inventory container

The Dockerfile builds cmd/server with Go 1.25.4 and verified modules using BuildKit
caches, then copies only its static Linux binary into Alpine. CA certificates
and BusyBox wget are available; USER app runs non-root. Environment files, keys,
test fixtures and development artifacts are excluded from the build context.

The image listens on PORT=8082 and probes /health. Normal ERP Compose uses /ready,
runs migration 000001 first, mounts only Auth's public RSA key read-only, and
allows 15s for the configured 10s HTTP drain. Exec-form ENTRYPOINT preserves
SIGTERM delivery. Existing JSON logging and JWT/permission enforcement remain
inside Inventory.

See [Platform flow, isolated tests, troubleshooting and proposed deployment](../../ERP-Platform/docs/inventory-docker-integration.md).
Run the isolated gateway harness from ERP-Platform:

```powershell
pwsh -NoProfile -File ./scripts/test-inventory-gateway.ps1
```

Clients reach /api/v1/inventory/warehouses through Kong on localhost:8000. Inventory
has no default host port. Inside Compose its database is postgres:5432; host tools
use the published POSTGRES_PORT. The password-free DATABASE_URL selects
inventory_db/inventory_service; pgx reads the raw PGPASSWORD separately. Keep
JWT_ISSUER/JWT_AUDIENCE aligned with Auth. Inventory receives the public key only,
not Auth's private signing key. No runtime Auth dependency is needed for local
JWT verification.

This Docker/gateway milestone covers existing warehouse management only.
Fulfillment/Delivery is planned as a separate internal Inventory module; stock
ledger, reservations, transfers and delivery workflows remain later work.
