# Inventory decisions

## Confirmed scope

This task implements the Go operational foundation only. Inventory owns inventory_db and connects as inventory_service. Product and variant master data belongs to Catalog. No business routes, migrations, JWT enforcement, Docker changes, or Kong changes are included. The public health/readiness routes do not require JWT.

## Observed compatibility constraints

Catalog internal/domain/unit.go defines ID, Code, Name and active status; product_variant.go references UnitID. These models do not establish inventory quantity precision, conversions, or fractional quantity policy.
Authentication internal/helper/jwt.go explicitly defines token Permissions as GLOBAL grants only. Its integration/catalog_security_test.go checks that WAREHOUSE grants are excluded. Warehouse-scoped authorization needs a future agreed contract; existing tokens must not be interpreted as warehouse grants.

## Unresolved: ask before schema or business implementation

- One warehouse or multiple warehouses.
- Warehouse-only balances versus rack/bin tracking.
- Quantity units, precision, fractional quantities and conversions.
- Whether negative physical stock is allowed.
- Opening stock posting, corrections, dates and audit history.
- Adjustment reasons and approval requirements.
- GLOBAL versus warehouse-scoped permissions.
- Reservation, release and transfer semantics when introduced.

No recommendation is a user-approved business rule. These questions do not block this bootstrap. At the next milestone ask focused questions in simple Malayalam with technical terms, 2-3 clickable choices, a recommendation and tradeoff, and a warehouse example when useful. Allow custom answers and wait before implementing dependent behavior.
