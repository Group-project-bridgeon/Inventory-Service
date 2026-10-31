# Inventory decisions

Milestone 9 proposal: [Minimal Fulfillment / Delivery](fulfillment-spec.md).
R9-A through R9-E are confirmed for implementation: exact Sales create/cancel/read
and human GLOBAL dispatch/update/read pairs are recorded in the specification.
Fulfillment is implemented and disposably verified; normal enablement and real
Sales acceptance/integration remain pending.
The existing Milestone 8 checkpoint and confirmed rules below remain unchanged.

## Current Milestone 9 checkpoint

Sales machines own create/cancel/read under audience inventory-fulfillment; verified
humans use corresponding exact GLOBAL dispatch/update/read grants across Sales owners
and warehouses. Actual human actor is separate from Sales owner; no impersonation,
unauthorized replay or implied permissions. Draft cancellation leaves the reservation
unchanged and requires a separate owned Sales release. FAILED outcome stays DISPATCHED;
DELIVERED is terminal. Neither outcome restores or deducts stock.

Dispatch composes the existing consume work and fulfillment audit/result in one
caller-owned transaction; it deducts the full hold once. Standalone consume cannot
bypass a live draft. Inventory000007 / Auth000012 are additive; old migrations and
replay formats remain preserved. Stock human consume v4 and internal reservation
intent v2 are explicit new versions. Fulfillment result/fingerprint use v1.

Schema7 must precede compatible binaries using new reservation columns/guards;
Auth12 is required for fulfillment authority. Default STOCK_FULFILLMENT_ENABLED=false;
no normal enablement, migrations, grants or service changes occurred. Kafka/Outbox/
Inbox/Saga remains Milestone10; final Inventory Postman delivery stays deferred.
Earlier checkpoints below record their historical milestone scope.

Milestone 8 proposal: [Inventory Reservations](stock-reservations-spec.md).
R8-A/B/D are confirmed; R8-C requires Sales to confirm reserve before cancellation.
The approved lifecycle/security contract is recorded there; implementation and disposable verification are complete. Sales adoption remains
proposed; normal enablement and real Sales integration are pending.
The existing adjustment implementation and historical checkpoints below are preserved.

[Stock Adjustments and Corrections](stock-adjustments-spec.md): D1–D3 confirmed on
2026-10-26. Positive IN/OUT deltas and one-time whole-operation reversals require
exact direction authority, mandatory reason and trimmed explanation. Reversals also
require reverse authority and current active/eligible references, while preserving
original quantities/factors. Physical-count reconciliation remains unimplemented.
Earlier checkpoints below retain their historical implementation/operational status.

## Current Milestone 8 checkpoint — implemented / disposable-verified

Reservations are machine-owned, direct-piece, all-or-nothing holds. available equals
on-hand minus reserved; reserve/release change holds only, while consume atomically
clears its hold and writes the physical OUT ledger, operation, audit and both durable
results. Ordinary OUT adjustments/decreasing reversals cannot spend held stock.
Warehouse deactivation respects stock and outstanding holds. Physical balance
`version` and `reservation_version` remain separate; compare both for availability.

Sales must confirm reserve before cancellation. Early release rejects with no writes
or tombstone. Terminal generations cannot reopen; only the next generation after a
confirmed release may open with matching head versions. Consumed orders are closed
in this MVP. Sales must retain cancellation intent and reconcile uncertain reserve
outcomes with original IDs/key before confirming release. No automatic mutation
retry after uncertain commit, implicit expiry or human reservation override exists.

New reserve/consume require active warehouse and fresh current active/eligible
Catalog references under R1 (5s maximum age / 1s future allowance, active-at-check
only). Owned release, authorized status and completed replay need no fresh Catalog
check. Exact reservation machine audience/scopes are approved in the linked spec;
Catalog and human JWTs remain separate. Revocation prevents future issuance; signed
machine JWTs can survive expiry plus <=30s tolerance, issued lifetime <=5 minutes.

Inventory 000006 must precede this binary even when STOCK_RESERVATIONS_ENABLED is
false; Auth 000011 is needed for the new exact machine grant pairs. The flag defaults
off. No normal migrations, data, grants, provisioning, services or TLS are changed.
Normal enablement, normal Opening/adjustment acceptance and real Sales integration
remain pending. Earlier checkpoints below preserve the historical scope of each
slice; their former pending statements do not override this current checkpoint.

## Previous implementation checkpoint — Stock Adjustments and Corrections

The approved positive IN/OUT adjustments and one-time whole-operation reversals are
implemented and disposable-verified. New command/fingerprint v2 preserves v1 replay
and durable result v1. Inventory migration 000005 adds immutable audit/linkage and
whole-reversal guards; Auth 000010 adds the three approved definitions only.
The new `STOCK_ADJUSTMENTS_ENABLED` gate defaults off. Migration 5 must precede this
application release even when posting is off, because movement history joins audit.
No normal data/configuration, grants, credentials or services were changed. The real
disposable Auth/Kong/HTTPS Catalog flow and PostgreSQL regressions passed; normal
Opening Stock posting/replay remains pending. Physical counts and the final Inventory
Postman deliverable remain unimplemented. See the linked specification for contracts,
rollout prerequisites, verification limits and protected cleanup evidence.

## Confirmed scope

Inventory owns inventory_db and connects as inventory_service. Product and variant master data belongs to Catalog. Application routes implement warehouse management and authenticated stock balance/movement reads. Stock domain types, migration 000002, generated queries and the internal transactional posting service/repository are implemented. Posting has no production wiring; stock mutation HTTP routes remain absent. See [stock-read-api.md](stock-read-api.md) and [stock-foundation-spec.md](stock-foundation-spec.md), sections 15 and 18. Reservations, transfers, bins/racks, fulfillment and Kafka remain later work. Exact GET /health and GET /ready remain public.

## Confirmed warehouse decisions

- Support multiple warehouses and track future stock by warehouse + product variant; no rack/bin tables.
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

## Confirmed Stock Foundation business decisions

- Identity is warehouse UUID + Catalog product variant UUID; product/variant master data stays in Catalog.
- Stock quantities are whole physical pieces only. Reject fractional input; box/pack counts convert into pieces using item-specific factors, never a universal box size.
- Catalog owns piece-base-unit semantics and variant-specific box/pack definitions.
- Block ordinary new receipts and issues if the warehouse, product, variant or base unit is inactive. Historical reads remain available; controlled corrections require a later workflow design.
- R1 (confirmed 2026-10-23): require fresh active status at the coherent Catalog check and retain its revision and validation time for later posting. Catalog may deactivate an entity before Inventory commits; active-at-commit is not guaranteed. Slice 4c approval (2026-10-24) sets maximum observation age 5 seconds and maximum future-clock allowance 1 second, independent of JWT tolerance. The earlier two-second proposal was not adopted.
- Block negative stock even under concurrent requests.
- Posted movements are immutable. Corrections append compensating/reversal movements; historical conversions do not change when pack definitions change.
- Stock movement entries, corresponding balance changes and successful idempotency records commit atomically. Duplicate requests must not change stock twice.
- Inventory must not query Catalog's database or add cross-service database foreign keys.
- No unrestricted public stock mutation endpoint is justified by the foundation.
- Opening stock, adjustments, reservations, transfers and fulfillment remain later workflows. Fulfillment/Delivery is planned as a separate internal Inventory module.

These are confirmed requirements. Domain slice 1, schema/SQL slice 2 and internal posting slice 3 are implemented. Migration 000002 adds four stock tables, exact BIGINT constraints, deferred ledger/balance/result/key correspondence and immutable-fact guards. StockRepository composes the existing queries in a caller-bounded transaction. Permissions and production integration remain later work. See sections 13 and 15 of [stock-foundation-spec.md](stock-foundation-spec.md) for invariant ownership and verification.

Domain slice 1, schema/SQL slice 2 and internal posting slice 3 are authorized and implemented. The normal database has not been migrated by this work. Catalog prerequisites, coherent authenticated stock-reference APIs and machine-token issuance remain unimplemented by these slices. Ordinary production posting, including direct pieces, stays disabled until those prerequisites and approved freshness enforcement exist. Synthetic references/authorization/freshness appear only in tests. Further slices require approval.

## Unresolved: ask before schema or business implementation

- Opening Stock technical implementation/release review (business decisions now recorded below); correction/reversal workflow and operational audit/recovery details for later workflows.
- Adjustment reasons and approval requirements.
- Reservation, release and transfer semantics when introduced.

Whole pieces, Catalog ownership of piece/pack metadata, pack-to-piece conversion, inactive-reference blocking at check time (R1), nonnegative balances, immutable posted movements, atomic posting and duplicate prevention are confirmed above. Opening Stock decisions are recorded below; correction workflows, their dates/approvals, reservations and transfers require later decisions. Remaining technical slices require review before implementation.

## Transaction and token boundaries

All warehouse mutations acquire transaction-scoped advisory lock 782341092 before reading or deciding. It serializes first creation, code conflicts, default promotion and activation updates. Keep the same key and read/decide/write/commit in one transaction. Reads remain unlocked. The unique index and deferred trigger are database backstops, including at Commit.

Inventory validates token_version as part of the signed token contract only. It performs no live Auth revocation lookup. Grant/revocation changes take effect when existing access tokens expire (maximum 15 minutes).

Internal posting compares each locked balance's base_unit_id with its validated reference snapshot before changing quantities. InitializeStockBalance never overwrites an existing binding. Any mismatch rejects the entire operation with ErrReferenceChanged and rolls back staged balances, movements, headers and idempotency claims. Disposable repository tests exercise this application-level comparison with synthetic observations; real Catalog provenance remains unverified.

Posting requires explicit ProducerAuthorizer, ReferenceProvider and FreshnessPolicy dependencies; nonzero actor UUIDs and producer strings alone grant no authority. Reauthorize every attempt/replay. Completed results replay without Catalog. New attempts reacquire observations outside locks and retain coherent revision/check time under confirmed R1; active-at-commit is not guaranteed. Slice 4c enforces the approved 5-second age/1-second future allowance at response acceptance and before each new transaction, including after pool acquisition.

Internal defaults: overall 5s, reference resolution 2s per attempt, cumulative store-call budget 3s, lock wait/primary lookup 500ms each, independent rollback/connection cleanup 2s. At most three attempts, only for known rolled-back 40001/40P01, with cancellable 10-20ms jitter. Unknown Commit never automatically re-mutates: a fresh physical primary lookup can prove success; otherwise ErrOutcomeUnknown requires retrying the same identities/key. The 3s local budget reflects disposable 100-line cost observations, not an SLA. See specification section 15 for precise coverage and remaining production gates.

## Slice 4c current integration checkpoint (2026-10-24)

The earlier slice 1-3 prerequisite statements describe their historical scope.
Catalog piece/pack/coherent reads (4a), separate machine issuance/verification (4b)
and optional secured transport/Inventory adapter (4c) now exist in source. Real
disposable Auth HTTPS -> Catalog HTTPS -> Inventory PostgreSQL posting has passed;
the earlier synthetic-only evidence does not describe this additional test.
Normal infrastructure and data were not changed. Production posting remains
unwired and stock mutation HTTP APIs remain absent, pending an approved workflow
and producer/actor permission contract. Future wiring must use the approved
freshness policy and NewStockRepositoryWithFreshness to include pool wait time.
See [catalog-reference-integration.md](catalog-reference-integration.md) and
specification section 17 for the current contract and deployment prerequisites.

## Stock Read APIs checkpoint (2026-10-25)

Warehouse-scoped balance and movement GET routes now use the user-approved exact
GLOBAL inventory.stock-balances:read and inventory.stock-movements:read permissions.
No authority is inferred from warehouse-read grants; machine tokens remain separate.
Reads expose stored balances and immutable snapshots, including inactive historical
references, without Catalog access or balance initialization. All BIGINT quantities
and versions serialize as strings. Live keyset pages are not frozen reconciliation
snapshots; movement UUID order is not commit order. Auth migration 000008 adds only
definitions and Inventory 000003 adds read indexes, applied only in disposable
verification here. See [stock-read-api.md](stock-read-api.md) for exact filters,
cursors, examples and manual checks. This does not enable production stock mutations.

## Opening Stock specification checkpoint (2026-10-25)

See [opening-stock-spec.md](opening-stock-spec.md): documentation only, ready for
technical review. Opening Stock records physical pieces present at adoption.
The user confirmed during specification preparation:

- A warehouse/variant may be opened only before any posted movement. Returning
  to zero after activity never restores eligibility.
- Reject zero opening quantities; a never-used pair remains eligible for a later
  positive opening. No zero-count completion marker is created.
- Exact GLOBAL `inventory.opening-stock:post` authority for a verified human;
  immediate server-time posting, no backdating or approval workflow in this slice.
- Include a secured non-posting preparation endpoint under that same permission
  so the caller can review real Catalog revisions before submitting intent.

API details, server producer/identity mapping, transaction and SQL guard changes,
warehouse deactivation guard, audit metadata and acceptance plan are proposals for
implementation review, not implemented behavior. No new permission definition or
grant exists by virtue of this document. Corrections remain later append-only work.
The previously confirmed warehouse rule preventing deactivation with stock becomes
a release prerequisite; current warehouse code does not yet enforce that stock check.

Later operational evidence supersedes the historical rollout limitations above:
the local rollout verified Auth 8, Catalog 3 and Inventory 3. The user now reports
successful authenticated empty-list responses from both stock read routes through
normal Kong; this is user-reported evidence, not a new agent-run acceptance test.
Live secured Catalog references remain disabled and normal stock mutation routes
remain unwired. TLS/trust, machine provisioning and explicitly authorized human
grants remain prerequisites for enabling Opening Stock.

The complete Inventory Postman collection/environment, token automation, examples
and tests will be finalized after planned Inventory milestones. Each milestone's
verification remains independent of that later deliverable.

## Opening Stock implementation checkpoint (2026-10-26)

The authorized [Opening Stock contract](opening-stock-spec.md) now has preparation
and posting routes behind default-off `OPENING_STOCK_ENABLED`. Exact GLOBAL
`inventory.opening-stock:post` and a verified human are required. Earlier checkpoints
above describe their historical state; they do not imply the new source is unwired.
Inventory migration 000004 supplies the SQL integrity backstop; Auth 000009 adds only
the permission definition. Positive-stock warehouse deactivation is rejected under
locks conflicting with posting. Completed authorized replay avoids Catalog; unknown
commit never causes an automatic mutation retry. Confirmed D1-D3 and R1 are unchanged.
Normal enablement, migrations, grants, machine provisioning and TLS changes remain
separately authorized operational work. Disposable verification is not deployment.
