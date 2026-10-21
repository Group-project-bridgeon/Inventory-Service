# Inventory decisions

## Confirmed scope

Inventory owns inventory_db and connects as inventory_service. Product and variant master data belongs to Catalog. This milestone implements warehouse management only; stock balances, movements, reservations, transfers, bins/racks, and Kafka remain later work. Exact GET /health and GET /ready remain public.

## Confirmed warehouse decisions

- Support multiple warehouses and track future stock by warehouse only; no rack/bin tables.
- Required normalized warehouse code and name; address is optional.
- Code is trimmed and uppercased on create, unique in PostgreSQL, and immutable after creation. Name/address can be changed.
- There is one active default warehouse after the first warehouse is created. First create sets it. A deferred database constraint trigger and unique index enforce exactly one at commit; a transaction lock serializes writes. Promotion of another active warehouse switches the default atomically; the current default cannot be demoted or deactivated until another warehouse is selected.
- No hard deletion. Deactivation does not check stock; when stock workflows arrive, prevent deactivating a warehouse with stock or reservations unless a defined transfer/closure workflow handles them.
- API uses cursor pagination (default 50, maximum 100), UUIDv7 IDs, and the exact GLOBAL permissions documented in README.
- Permission scope is GLOBAL only. No warehouse-specific authorization is implemented. Stateless tokens can continue to authorize up to 15 minutes after a grant changes or is revoked.

Code may contain uppercase ASCII letters, digits, underscore and hyphen (1-50 chars, starts with a letter/digit); names are 1-100 Unicode characters; addresses are optional and at most 500 characters. Inputs are trimmed and control characters are rejected.

Important decisions (Malayalam): code മാറ്റാനാകാത്തതാക്കിയത് integrations-ൽ സ്ഥിരമായ തിരിച്ചറിയൽ നിലനിർത്താനാണ്; name/address തിരുത്താം. ആദ്യ warehouse default ആകും; മറ്റൊന്ന് default ആക്കുമ്പോൾ ഒരേ transaction-ൽ മാറ്റുന്നതിനാൽ രണ്ട് defaults ഉണ്ടാകില്ല. Stock checks ഇപ്പോൾ സാധ്യമല്ല; stock workflow വരുമ്പോൾ deactivation നിയന്ത്രണം ചേർക്കണം.

## Observed compatibility constraints

Catalog internal/domain/unit.go defines ID, Code, Name and active status; product_variant.go references UnitID. These models do not establish inventory quantity precision, conversions, or fractional quantity policy.
Authentication internal/helper/jwt.go explicitly defines token Permissions as GLOBAL grants only. Its integration/catalog_security_test.go checks that WAREHOUSE grants are excluded. Warehouse-scoped authorization needs a future agreed contract; existing tokens must not be interpreted as warehouse grants.

## Unresolved: ask before schema or business implementation

- Quantity units, precision, fractional quantities and conversions.
- Whether negative physical stock is allowed.
- Opening stock posting, corrections, dates and audit history.
- Adjustment reasons and approval requirements.
- Reservation, release and transfer semantics when introduced.

Quantity units, precision/fractional policy, negative stock, opening stock, corrections/audit, adjustments/approval, and reservation/release/transfer semantics remain unresolved for their later milestones. Ask before implementing those workflows.

## Transaction and token boundaries

All warehouse mutations acquire transaction-scoped advisory lock 782341092 before reading or deciding. It serializes first creation, code conflicts, default promotion and activation updates. Keep the same key and read/decide/write/commit in one transaction. Reads remain unlocked. The unique index and deferred trigger are database backstops, including at Commit.

Inventory validates token_version as part of the signed token contract only. It performs no live Auth revocation lookup. Grant/revocation changes take effect when existing access tokens expire (maximum 15 minutes).
