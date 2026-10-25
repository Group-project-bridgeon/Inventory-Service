# Stock Foundation specification

Status: DOMAIN SLICE 1, SCHEMA/SQL SLICE 2 AND INTERNAL POSTING SLICE 3 IMPLEMENTED
(sections 13 and 15; verification details in section 15).
Stock tables are defined by migration 000002 and tested only in disposable PostgreSQL.
Internal posting is available only through injected dependencies; no stock endpoints,
new permissions/grants or production integration are wired.
Inspected 2026-10-22. CONFIRMED means supplied business requirements; OBSERVED
means source evidence; PROPOSED means a recommendation requiring review.
Finalization review: 2026-10-23; local source baselines rechecked, no live acceptance run.
Domain slice implementation authorized 2026-10-23; R1 is confirmed below.
Schema/SQL slice 2 authorized 2026-10-23; warehouse migration 000001 is unchanged.
Internal posting slice 3 authorized 2026-10-23. Historical baseline/review sections
below retain their inspection-time evidence; section 15 records the current implementation.

## 1. Scope, baseline and evidence

The foundation is an internal posting engine plus balance/history reads. Opening
stock, adjustments, reservations, transfers and Fulfillment/Delivery remain later
business workflows. Fulfillment/Delivery is planned as a separate internal Inventory
module. There is no unrestricted public set-stock, receive, issue or mutation API
in this proposal. Internal receipt/issue commands are exercised by tests, not by
adding demonstration endpoints. A zero balance is not opening stock.

### 1.1 Actual checkout state

| Repository | Local baseline inspected | State at inspection |
| --- | --- | --- |
| Inventory-Service | 97445fbd5cc23fe06313e9274813d6c1dcf037c8 | docs/stock-foundation-spec; pre-existing edits to .gitignore, internal/integration/warehouse_test.go and both specification documents |
| Catalog-Service | b60cb583f5009fe0272013e80ac8704500aa77fb | development, clean |
| Authentication | 3490657647869f5b34c63b47ab74a950850b3bc7 | development, clean |
| ERP-Platform | 08baa65181756bd17153dcf9bf6c1767c4aa62f2 | development, clean |

Inventory documentation branch: docs/stock-foundation-spec, created at the same
HEAD. SHA256 hashes of both pre-existing edited files matched before/after branch
creation. This finalization rechecked the same HEADs and preserved both code-file
hashes; the specification was already untracked and inventory-rules.md modified.
No checkout of another commit, stash, reset or discard was used. Remote
URLs were inspected locally; no remote fetch or runtime acceptance check is claimed
for this specification task. No applicable on-disk AGENTS.md was found in the
searched ancestors/repositories; the user's supplied instructions apply.

### 1.2 Catalog findings (repository-relative paths)

| Evidence | Observed behavior and implication |
| --- | --- |
| Catalog-Service/db/migrations/000001_create_catalog_schema.up.sql: catalog_products, catalog_product_variants, catalog_units | Product owns category/brand/name/product_code. Variant owns product_id, sku, unit_id and optional selling price. Product and unit FKs are local to Catalog. Units have ID/code/name/active/timestamps, no precision or conversion factor. |
| Same migration: uq_catalog_product_variants_product_sku | SKU uniqueness is within a product, not global. Stock identity must use variant UUID, never a naked SKU. |
| Catalog-Service/internal/service/product.go: ProductService.Create; db/queries/products.sql: CreateProduct | Product creation inserts only a product. No default variant is created or required. A product can have zero variants; no stockable flag or guarantee every item has a variant exists. |
| Catalog-Service/internal/domain/product_variant.go: ProductVariant; internal/repository/postgres/catalogdb/models.go: CatalogProductVariant, CatalogUnit | UUID relationships match schema. Generated pgtype.Numeric is for selling price, not physical quantity. It does not justify decimal stock quantities. |
| Catalog-Service/internal/service/product_variant.go: CreateProductVariantInput, UpdateProductVariantInput; db/queries/product_variants.sql: UpdateProductVariant | Create needs product_id and unit_id; update changes SKU/price, not those references. SKU is mutable. Active flags are separate for product, variant and unit. |
| Catalog-Service/internal/service/unit.go: UnitService.Update; db/queries/units.sql: UpdateUnit | Unit code/name may be edited. A string such as BOX or PCS is a label, not a proven quantity-kind or fixed factor. |
| Catalog-Service/db/queries/product_details.sql; internal/repository/postgres/product_detail_repository.go: ProductDetailRepository.Get | Detail combines product/variants/attribute values/images/prices, not unit definitions or pack semantics. It uses separate queries without an explicit shared snapshot transaction. |
| Catalog-Service/db/migrations/000002_catalog_performance.up.sql | Adds product and variant keyset/read indexes and image ordering normalization; it adds no stock, piece precision or conversion model. |
| Catalog-Service/internal/router/{product,product_variant,product_detail,unit}.go; permissions.go: PermissionMatrix | Only existing secured business routes below. No dedicated internal stock-reference endpoint. Product/variant/unit CRUD has status PATCH, no hard-delete route; their SQL has no corresponding delete operation. Database administrators remain outside that API guarantee. |
| Catalog-Service/internal/handler/product_variant_test.go: TestProductVariantHandlerList, TestProductVariantHandlerBatchList; internal/service/product_test.go: TestProductServiceCreate | Tests substantiate separate product creation and normal/batch handler shapes; they do not establish a pack model. Inspected, not rerun in this documentation task. |

No product/variant-specific packs, universal conversion table, piece classification,
client-credentials flow or service-identity bypass was found in the inspected schema,
domain, service and router surfaces. Absence is a source finding, not a live DB audit.

### 1.3 Existing Catalog API contracts

All routes require a valid Auth RS256 access token. Every named permission below is
an exact GLOBAL grant; all grants listed for a route must be present.

| Existing route | Actual response | Required permission |
| --- | --- | --- |
| GET /api/v1/catalog/product-variants/:id | Direct variant object | catalog.product-variants:read |
| GET /api/v1/catalog/product-variants?product_id=UUID&unit_id=UUID&is_active=true&limit=50&cursor=... | {items:[variant],next_cursor:string-or-null} | catalog.product-variants:read |
| GET /api/v1/catalog/product-variants?ids=UUID,UUID | {items:[variant]}; max 100 IDs; missing IDs absent, not implicit success for every requested ID | catalog.product-variants:read |
| GET /api/v1/catalog/products/:id/variants | Paginated variant collection; does not by itself prove parent active | catalog.product-variants:read |
| GET /api/v1/catalog/products/:id | Direct product object including is_active | catalog.products:read |
| GET /api/v1/catalog/units/:id | Direct unit object | catalog.units:read |
| GET /api/v1/catalog/units | Raw [unit] JSON array; current list is not paginated | catalog.units:read |
| GET /api/v1/catalog/products/:id/detail | {product,variants,variant_values,images,prices}; no units/packs collection | catalog.products:read, catalog.product-variants:read, catalog.variant-values:read, catalog.product-images:read, catalog.prices:read |

Variant DTO (internal/handler/product_variant.go: productVariantResponse) contains
id, product_id, sku, optional default_selling_price as a decimal string, unit_id,
is_active, created_at, updated_at. Unit DTO contains id/code/name/is_active and
timestamps. Product DTO contains id/category_id, optional brand_id, name,
product_code, optional description, is_active and timestamps. Detail is not a
single variant DTO and needs five permissions; do not use it merely to fetch stock
metadata. Batch mode takes precedence when ids is supplied; do not assume combining
ids with active filters validates all active references. SQL GetByID/ListByIDs
returns inactive rows too; validate flags explicitly. Raw error bodies differ from
Inventory's stable {code,error,request_id}; the adapter must normalize them safely.

### 1.4 Historical Inventory and integration findings (pre-slice baseline)

- Inventory-Service/db/migrations/000001_create_warehouses.up.sql is the only up
  migration. It creates warehouses and the exactly-one-active-default constraints.
  db/queries/warehouse.sql and sqlcdb/models.go describe only warehouse persistence.
- internal/router/router.go: NewWithBusiness and internal/handler/warehouse.go
  implement create/list/detail/update/status. internal/service/warehouse.go provides
  UUIDv7, validation and 50/default, 100/max keyset pagination. No hard-delete API.
- internal/repository/postgres/warehouse.go: Create/Update/SetActive use a pgx
  transaction, advisory lock 782341092, decisions and writes, and check Commit.
  mapErr matches exact known SQLSTATE/constraint pairs; unknown errors stay safe
  internal diagnostics. Warehouse deactivation currently does not inspect stock.
- Source inventory of internal/ and db/ contains no stock balance/movement,
  posting-idempotency, reservation, Kafka, Outbox or Inbox implementation. No stock
  table, router policy or generated model exists. Examples in Platform's generic
  database-migrations.md are conventions, not implemented Inventory features.
- internal/security/{jwt,middleware}.go: Claims, Verifier.Verify, Protect, Identity
  enforce RS256, issuer/audience, numeric times, identity, token_version and exact
  GLOBAL permissions, max 15-minute lifetime. token_version is contract validation,
  not a live Auth revocation lookup. Only exact GET health/ready is public.
- internal/platform/logger/{failure,http}.go: Failure, Classify, DiagnoseHTTP,
  Respond; docs/logging.md define safe classified diagnostics and correlation IDs.
- Existing integration evidence includes warehouse_concurrency_test.go:
  TestIntegrationConcurrentFirstCreation/TestIntegrationConcurrentNormalizedCode;
  warehouse_integration_test.go: TestIntegrationWarehousePersistenceConstraintsAndDefaultSerialization;
  router/warehouse_security_test.go: TestWarehouseRoutesRequireExactPermissionBeforeHandlers;
  integration/warehouse_test.go: TestIsolatedAuthInventoryHTTP/Gateway. The latter
  has a pre-existing local edit and is not changed by this documentation task.
- Authentication/db/migrations/000006_inventory_warehouse_permissions.up.sql adds
  warehouse read/create/update definitions only. Auth router supports user login
  and /me, not a machine/client-credentials endpoint. JWT permissions contain only
  GLOBAL grants (Authentication/internal/helper/jwt.go: Claims).
- ERP-Platform/docker-compose.yml and infrastructure/kong/kong.yml configure
  Inventory at inventory-service:8082, preserve the Inventory prefix, mount only
  Auth's public key and run a separate migration container. This is not a stock
  mutation permission or an internal-authentication bypass.

Regular authorized warehouse 200 acceptance: NOT VERIFIED / still pending in the
inspected record. ERP-Platform/docs/inventory-docker-results.md records isolated
login/authorized warehouse success and explicitly says normal deployment/readiness
was not performed. Inventory/docs/verification.md describes expected fixture
statuses, not a completed normal acceptance run. README introductory text still
calls Docker/Kong a later milestone despite merged Docker documentation; prefer
actual configuration and the attributed verification record. Do not edit these
unrelated historical documents or claim new runtime evidence during this task.

## 2. Confirmed business rules and proposal boundaries

CONFIRMED by this request:

1. The stock identity is warehouse UUID + Catalog variant UUID.
2. Stock is whole physical pieces. Fractions are rejected, including via packs.
3. Boxes/packs convert into pieces using an item-specific valid factor.
4. Stock cannot become negative, including concurrent deductions.
5. Posted movements are immutable. Corrections append compensating/reversal
   movements; old history and conversion snapshots are not edited.
6. Entries, corresponding balances and successful idempotency records commit
   atomically. A duplicate logical request cannot apply twice; persistence mechanics
   are proposed below.
7. Catalog owns master data; Inventory does not read its DB or add external FKs.
8. Opening/adjustment/reservation/transfer/fulfillment workflows are later work.
9. No public unrestricted mutation endpoint is introduced.
10. Catalog owns piece-base-unit semantics and variant-specific box/pack definitions.
11. Inactive warehouses, products, variants or base units block ordinary new receipts
    and issues. Historical reads remain available; controlled corrections are later work.

Slice 1 implements int64 quantities, exact string quantity serialization and
single-warehouse commands of up to 100 distinct variants. Remaining PROPOSED
engineering defaults include BIGINT persistence, 50/100 read pagination,
per-balance versions, permanent successful-operation deduplication, bounded Catalog
calls, GLOBAL read permissions and the schema/lock strategy. They are not already
implemented or implicitly approved by confirmed quantity rules.

### 2.1 Confirmed ownership and inactive-reference decisions

| Decision | Agreed behavior | Status |
| --- | --- | --- |
| D1: piece semantics and pack ownership | Catalog owns piece units and variant-specific box/pack definitions. Exact immutability, revision and API mechanics remain proposed below. | CONFIRMED by user, 2026-10-22 |
| D2: inactive references | Block ordinary new receipts and issues if warehouse/product/variant/base unit is inactive; history remains readable; design controlled corrections later. | CONFIRMED by user, 2026-10-22 |
| R1: remote active-check timing | Require fresh active status at the coherent Catalog check and retain the revision and validation time. Catalog can deactivate afterward before Inventory commits; active-at-commit is not guaranteed. | CONFIRMED by user, 2026-10-23 |

D1/D2/R1 are confirmed. Domain slice 1 and schema/SQL slice 2 are authorized and implemented.
Remaining technical slices and companion contracts require separate approval.
Later workflow questions include partial/full reversals,
effective/backdated business dates, approval roles/reasons, and inactive-reference
recovery; those do not authorize generic corrections now.

## 3. Ownership and Catalog integration (PROPOSED)

### 3.1 Master data and immutable posting evidence

Catalog continues to own product/variant identities, mutable labels and active
state. Confirmed D1 places piece-base semantics and pack definitions there. Proposed fields are:
pack UUID, variant UUID, positive integer pieces_per_pack, label, active flag and an
immutable revision. A shared label BOX never means every item contains six pieces.
Variant A may have BOX6, variant B BOX12; a variant may have several packs. Start
with variant-specific definitions; shared product defaults/inheritance are a later
Catalog decision, not a global box conversion. Do not reinterpret an existing BOX
unit as PCS or rewrite existing unit assignments automatically.

Inventory owns on-hand pieces, append-only postings, operation/source identity,
idempotency, and immutable snapshots sufficient to explain each posting. It stores
external UUID references plus product/variant/base-unit/pack IDs, relevant labels,
revision, submitted count, factor and resolved pieces as posting evidence. These
snapshots are not editable Catalog master data and are not a general Catalog cache.
Changing a pack from six to twelve pieces affects new validated revisions only.

### 3.2 Existing usable reads and missing contract

Existing variant + product + unit GETs can establish existence and individual
active flags using three independently secured reads. They cannot establish pack
factor, explicit piece semantics or one coherent stock-reference revision. A list
response omitting a requested ID is missing-reference failure, never factor=1.
A timestamp from the variant alone does not version changes to product/unit/packs.

Before production posting, recommend a Catalog-owned contract, for example
GET /api/v1/catalog/stock-references/:variant_id (PROPOSED, not present). It returns
variant/product/base-unit identities and active flags, explicit quantity_kind=PIECE,
stock_reference_revision, and requested pack definition/revision/factor. A bounded
batch equivalent may handle <=100 distinct variants. Catalog must return a coherent
snapshot and change its revision whenever contributing state changes; existing
separate queries are not proof of that guarantee. Every missing requested item is
explicit. Authorization is a proposed dedicated catalog.stock-references:read grant.
The current units list uses repository/cache/unit_repository.go: UnitRepository.List
and handler/reference_cache.go: writeReferenceJSON
(ETag plus public max-age/stale-while-revalidate); UnitRepository.GetByID delegates
to the underlying repository. Do not use a cached list as fresh posting validation.
The proposed resolver must define explicit no-stale validation semantics.
Catalog schema/contract, migrations and revision semantics require a companion
review; they are not implemented by this Inventory document.

New commands carry the expected stock-reference and selected pack revision from
preparation. Resolve them again before posting; stale expectations return conflict,
not silently a different quantity. Once posted, replay uses stored results even if
Catalog later changes. A new stock reference must have a stable piece base unit;
conflicting base-unit UUID for an established balance is rejected, never relabeled.
Current Catalog variant updates already exclude unit_id; new master-data features
must preserve this physical-unit invariant or require an approved migration/new ID.

The proposed Catalog invariant is global for the variant, across warehouses: once
made stock-eligible, its physical piece meaning and base-unit binding cannot be
reinterpreted under the same identity. Code/name edits are labels, not unit changes.
Inventory's per-pair mismatch check alone cannot protect the first posting into a
different warehouse, or detect a meaning change under the same unit UUID. Catalog
must enforce the invariant without querying Inventory to ask whether history exists;
freezing it at stock eligibility is the recommended companion design. Existing
BOX-like unit assignments must be reviewed explicitly for eligibility, never
automatically mapped to pieces or given an inferred factor. A new physical meaning
needs a new identity or a separately approved conversion/migration workflow.

### 3.2.1 Catalog prerequisite delivery contract (not implemented)

| Required change | Proposed Catalog locations and acceptance |
| --- | --- |
| Explicit piece eligibility | Candidate db/migrations/000003_stock_reference_metadata.{up,down}.sql; internal/domain/unit.go and product_variant.go; db/queries/units.sql and product_variants.sql. Distinguish piece meaning from mutable labels; define immutable variant binding, safe legacy eligibility and explicit rejection of unsupported units. No automatic conversion/backfill from labels. |
| Variant-specific packs | Proposed internal/domain/variant_pack.go, db/queries/variant_packs.sql and a Catalog-local pack/version schema. Catalog allocates pack IDs and immutable revisions; each version binds variant, positive int64 factor, label and status. Factor changes create new versions, never overwrite historical versions. Local Catalog FKs are allowed; no Inventory FK. |
| Coherent reference resolution | Proposed internal/service/stock_reference.go, internal/repository/postgres/stock_reference_repository.go, internal/handler/stock_reference.go and router/permissions.go. A reviewed endpoint/batch contract supplies complete requested identities, active flags, exact string factors and bounded opaque revisions from one coherent snapshot. Fail closed on missing or unsupported data; no stale-positive cache. |
| Revision and authorization contract | Changes to any contributing active state, binding, selected pack or snapshot field invalidate the applicable reference revision. Define current selectable pack revision versus historical versions, exact no-stale response behavior and a dedicated least-privilege read permission in the companion design. Existing endpoints/permissions do not provide this guarantee. |

These are required capabilities and proposed file locations, not claims that tables,
routes, credentials or consumers already exist. Inspect migration history again
before assigning 000003. Auth definitions and machine identity are separate work.

### 3.3 Security and failure policy

Recommended integration uses an explicit least-privilege service principal and
short-lived Catalog-readable tokens through a reviewed Auth machine-auth contract.
Neither that issuance flow nor a service principal is currently implemented. Current
Catalog Claims.Validate requires nonzero user_id matching sub, token_version and
GLOBAL permissions: a new client_id-only token would not work. The companion
Auth/Catalog design must explicitly preserve a compatible principal contract or
version both verifiers; do not assume a generic machine token already passes. Define
key/credential rotation and Catalog verifier compatibility in that companion work;
do not mount a private signing key into Inventory or manually mint production JWTs.
A metadata service grant never grants a human permission to post stock.

An incoming Inventory token does NOT imply catalog.product-variants:read,
catalog.products:read and catalog.units:read. Explicit user-token forwarding is an
alternative only after approving those exact user grants and delegating that read
behavior; no silent fallback or admin-account login. Private Docker networking and
Kong do not bypass Catalog's middleware. No direct Catalog DB pool or external FK.
Production posting remains blocked until a supported reference/authentication path
exists; tests may inject a validated reference-provider fake but cannot claim live
integration from it.

Recommended defaults: respect caller cancellation; total reference resolution budget
2s, per attempt <=1s, bounded concurrency 4, max 100 references and 1 MiB decoded
response. At most one retry for transient transport/502/503 failures within that
budget; no retry for invalid data, 401/403/404. Validate UUIDs, full requested-ID
coverage, kind, revisions, factors and flags; reject malformed/oversized responses.
Catalog 401/403 is a service/delegation configuration failure, not permission to
continue. Missing references are distinguished from timeout/unavailable responses.
Never serve stale-positive cached metadata to authorize a new posting by default.

All HTTP validation completes before taking stock or warehouse row locks. A valid
remote observation is not a distributed lock: Catalog can deactivate immediately
afterward. Record reference_checked_at/revision; the guarantee is valid-at-check,
not Catalog-active-at-Inventory-commit. R1 confirms this interpretation of the
inactive rule. The existing proposed 2s observation-age value in section 6.3 is
preserved for integration review, not implemented or silently promoted to a
confirmed freshness policy. Pure snapshot validation only requires a nonzero check
time and coherent-reference revision; it does not establish observation age or
trustworthy provenance. Do not pretend a distributed transaction
exists. Kafka + Outbox/Inbox and a versioned Inventory reference projection remain
the planned later consistency direction, not existing infrastructure. Historical
reads/replays do not depend on current Catalog availability.

## 4. Quantities (domain implemented; persistence proposed)

Use PostgreSQL BIGINT and Go int64 for persisted counts, factors, resolved pieces
and per-balance versions. Allowed stock range is 0..9,223,372,036,854,775,807;
movement input/count/factor range is 1..that maximum. No float64 and no NUMERIC
stock-quantity columns. Catalog decimal prices are unrelated to whole-piece stock.
Use decimal strings at all future JSON boundaries, including versions, to avoid
JavaScript's exact-integer limit of 9,007,199,254,740,991. Clients parse BigInt or an
exact integer library; never JSON.parse into Number then stringify a rounded value.

Quantity grammar: canonical [1-9][0-9]* for inputs; 0 or that grammar for outputs.
Reject signs, fractions (including "2.0"), exponent notation, numeric JSON literals,
blank values, leading zeros and values exceeding int64. Internal types remain int64
and never pass through map[string]any numeric float conversion. JSON responses use
"on_hand_pieces":"12", not a JSON number. Strict DTOs reject unknown/trailing JSON.

Pieces use factor 1 and no pack. Packs require a valid Catalog definition for that
variant, matching immutable revision and positive integer pieces_per_pack. Validate
count <= MaxInt64 / factor before multiplying. For an IN posting, validate current
<= MaxInt64 - pieces before addition. For OUT, require current >= pieces; reject
zero/negative input and encode direction separately. Never negate MinInt64 or use
an unchecked sum while aggregating multiple lines.

Examples:

- 2 boxes * 6 pieces = 12 pieces. Store input "2", pack revision, factor "6", and
  pieces "12". A future factor "12" does not turn this old receipt into 24.
- 3 pieces = 3 pieces with factor "1". "1.5" pieces or "1.5" boxes is rejected even
  if multiplication could produce an integer; inputs are whole counts only.
- On-hand 10, two simultaneous issues of 7: one may post; the other is insufficient.
  The final balance is 3, never -4. Neither request may approve from an unlocked read.
- Receiving 1 into MaxInt64, or count MaxInt64 with factor 2, fails without writes.
- Reversing an earlier receipt after its pieces were consumed cannot bypass the
  nonnegative rule. The later correction workflow must resolve that conflict.

Under confirmed D2, new operations require local warehouse active plus remote
product/variant/base unit active. The proposed pack-validation rule also requires
the selected pack to be active. Existing history
and balances remain readable when references become inactive or disappear remotely.
No silent reactivation, default-warehouse substitution or fallback variant is allowed.

## 5. Schema, constraints and reconciliation (slice 2 implemented)

This design is implemented in db/migrations/000002_create_stock_foundation.{up,down}.sql,
after inspecting the existing version-1 warehouse baseline. Warehouse 000001 is
unchanged. See section 13 for exact query/result contracts and integrity ownership. Schema has four
related tables; no reservation, delivery, pack-master or workflow draft tables.
The operation header is justified by shared actor/source/idempotency, atomic
multi-line results, and line completeness. For this slice an operation covers one
warehouse, one direction, and 1..100 distinct variants; transfers need later design.

### 5.1 stock_balances

| Column | Type/constraint | Purpose |
| --- | --- | --- |
| warehouse_id | UUID NOT NULL, local FK warehouses(id), RESTRICT | Warehouse owned locally |
| variant_id | UUID NOT NULL, nonzero, external reference | Catalog variant; no cross-service FK |
| base_unit_id | UUID NOT NULL, nonzero, external reference | Stable Catalog piece-unit binding |
| on_hand_pieces | BIGINT NOT NULL, CHECK >=0 | Materialized on-hand pieces only |
| version | BIGINT NOT NULL, CHECK >=0 | Per-balance committed movement sequence, initially 0 |
| created_at, updated_at | TIMESTAMPTZ NOT NULL, DB assigned | Creation and last balance update |

Primary key (warehouse_id,variant_id). No separate surrogate balance ID is needed.
No reserved/available/valuation quantities. A new posting may initialize a row to
0/version 0 inside its transaction; a rolled-back first issue leaves no row.
Once initialized successfully, retaining a zero row preserves its movement version.
Reads do not create rows. Existing balances must keep their base_unit_id; reject a
mismatch rather than converting history. Catalog remains owner of that ID/meaning.

### 5.2 stock_operations

| Column | Type/constraint | Purpose |
| --- | --- | --- |
| id | UUID PK, stable nonzero logical operation UUID | Trusted workflow allocates once, not on each retry |
| producer | TEXT bounded 64, server allowlist | Stable workflow/module namespace, not a user-supplied authority |
| source_event_id | UUID NOT NULL, nonzero | Durable source action identity, UNIQUE(producer,source_event_id) |
| warehouse_id | UUID NOT NULL, local FK warehouses(id), RESTRICT | Single warehouse for the operation |
| direction | TEXT CHECK IN ('IN','OUT') | All lines share direction; input quantities stay positive |
| actor_user_id | UUID NOT NULL, nonzero external Auth reference | Derived from verified principal, never arbitrary request body |
| line_count | SMALLINT CHECK BETWEEN 1 AND 100 | Completeness at commit |
| command_version | SMALLINT, currently CHECK =1 | Versioned canonical intent/result contracts; future versions need review |
| idempotency_key | UUID NOT NULL | Together with producer references the idempotency record |
| posted_at | TIMESTAMPTZ NOT NULL, DB assigned | Posting observation, not proof of global commit order |
| first_request_id | TEXT NULL, bounded 64 | Optional safe tracing correlation; no deduplication meaning |

UNIQUE(id,warehouse_id) supports a composite local movement FK that prevents lines
from pointing to the wrong warehouse. UNIQUE(producer,idempotency_key) and a deferred
FK to stock_idempotency tie one successful request to its operation. Source IDs are
stable for one logical business action across HTTP keys, contract versions and
message redelivery; they are not arbitrary names reused for all lines/documents.
A source document with several actions needs distinct source event IDs.

Header rows are inserted as final posted facts inside the transaction. No public
editable draft header/status lifecycle. System/machine actors for future background
work require a reviewed principal contract; do not put a fake human UUID or accept
actor_user_id from an untrusted caller. Workflow-specific reasons/backdating and
reversal linkage belong to their later approved workflow design.

### 5.3 stock_movements

| Column group | Fields | Constraints/use |
| --- | --- | --- |
| Identity | id UUIDv7 PK, operation_id UUID, line_number SMALLINT | All nonnull/nonzero; UNIQUE(operation_id,line_number) |
| Balance identity | warehouse_id UUID, variant_id UUID | Local composite FK to stock_balances; external variant UUID |
| Operation linkage | (operation_id,warehouse_id) | Local composite FK stock_operations(id,warehouse_id), RESTRICT |
| Per-balance sequence | balance_version BIGINT >0 | UNIQUE(warehouse_id,variant_id,balance_version) |
| Quantities | direction IN/OUT, input_count BIGINT >0, pieces_per_input BIGINT >0, pieces BIGINT >0, before_pieces BIGINT >=0, after_pieces BIGINT >=0 | Exact conversion and before/after invariant; direction matches header |
| Base references | product_id UUID, base_unit_id UUID | Nonzero external IDs, not Catalog FKs |
| Pack evidence | pack_id UUID NULL, pack_revision TEXT NULL bounded 128 | Both null for pieces; both present for packs; pack belongs to this variant |
| Snapshots | product_code, product_name, sku, base_unit_code, optional pack_label | Bounded to agreed Catalog contract; current product_code 50/name 200/SKU 100/unit code 30 |
| Validation evidence | stock_reference_revision TEXT bounded 128, reference_checked_at TIMESTAMPTZ | Required validated observation; not an editable reference cache |

UNIQUE(operation_id,variant_id) rejects duplicate balance identities in an operation;
callers must prepare one line per variant. No mixed IN/OUT netting that could hide
an insufficient issue. Posted time/actor/source are obtained from the immutable
header, avoiding disagreeing copies. Pack labels/factors/quantities are copied into
the movement; reads do not join current Catalog to reconstruct historical meaning.

Check input_count * pieces_per_input = pieces using numeric casts in the CHECK
expression to avoid intermediate BIGINT overflow from a malformed direct insert;
all stored quantities are still BIGINT integers. Likewise validate before/after
using exact widened arithmetic: IN adds pieces, OUT subtracts. Pack null requires
factor 1; selected pack requires nonempty revision. JSON strings are only API
serialization; BIGINT fields are the authoritative persisted count.

### 5.4 stock_idempotency

| Column | Type/constraint | Purpose |
| --- | --- | --- |
| producer, key | TEXT bounded 64 + UUID, composite PK | Stable producer namespace and nonzero idempotency key |
| fingerprint_version | SMALLINT, currently CHECK =1 | Canonical encoding/hash algorithm version |
| request_hash | BYTEA length 32 | SHA-256 canonical intent, not raw request storage |
| actor_user_id | UUID nonzero external reference | Replay ownership and authorization binding |
| operation_id | UUID NOT NULL UNIQUE, deferred FK stock_operations(id) | Preallocated logical operation; link completed result |
| state | TEXT CHECK IN ('IN_PROGRESS','COMPLETED') | IN_PROGRESS exists only within an uncommitted transaction |
| result_version, result | SMALLINT positive + JSONB | Minimal immutable successful result with IDs, quantities and versions |
| created_at, completed_at | TIMESTAMPTZ; completed_at required at completion | DB times; no automatic expiry |

result_version/result/completed_at may be null only while IN_PROGRESS; COMPLETED
requires all three and an object-shaped versioned result. No raw request body is stored.
Deferrable reciprocal references and commit-time checks require exactly one matching
operation/header and COMPLETED record at commit. A crash cannot leave a committed
IN_PROGRESS row under this design. Permit only the single in-transaction transition
to COMPLETED, then prevent update/delete/truncate of completed records. No worker
lease, recovery sweeper or permanently visible pending-job state is needed.

Across these four tables all required identifiers, quantities, versions, status,
revision, timestamps and required snapshots are NOT NULL, unless explicitly marked
optional above. In stock_idempotency only result_version/result/completed_at may be
temporarily null in IN_PROGRESS; require them when COMPLETED. first_request_id,
pack_id/pack_revision/pack_label are the other intentional nullable fields. Enforce
nullability explicitly: CHECK expressions alone accept NULL and do not replace
NOT NULL constraints. No active flag belongs on an immutable posted operation.

### 5.5 Database integrity and indexes

- CHECK nonnegative balances and positive count/factor/pieces; named constraints
  for known input/duplicate conflicts. A violated balance/ledger invariant from
  malformed persistence is an internal defect, not automatically a client 400.
- Deferred checks verify header line count/direction/warehouse, completed
  idempotency, and affected balance's final version/quantity against its last
  movement, including matching base_unit_id. Validate each movement's predecessor: version 1 starts at zero;
  version N's before equals version N-1's after. Reject missing predecessors.
- Enforce line numbers 1..header.line_count, positive/nonzero references, and
  immutability of balance identity/base_unit_id. An initialized balance must have
  version >=1 and its corresponding movement at commit; version 0 exists only
  inside a posting transaction. Reject committed orphan zero rows. Reject balance
  DELETE/TRUNCATE as well as movement/header/idempotency deletion so history and
  sequence identity cannot be reset.
- Reciprocal FKs alone do not prove correspondence: the deferred checks must
  verify that the referenced key points back to this exact operation and that
  producer/key, operation_id and actor agree on both rows. Verify stored result
  operation/line IDs, counts, after quantities and versions against the ledger,
  not merely that result is a JSON object. Result replay must not invent facts.
- Install deferred checks on every relevant INSERT/UPDATE/DELETE path, not just
  balance updates: movement-only inserts, incomplete operation/key rows and
  balance-only changes must all fail at commit. Inspect final persisted state for
  each affected pair/header/key, rather than relying on an obsolete trigger NEW
  image. Use indexed correspondence/predecessor lookups; no full-history scan per post.
- Guard UPDATE/DELETE/TRUNCATE of immutable headers/movements and completed
  deduplication records; deny direct ledger edits. Balance changes without a
  matching movement/version fail the deferred check. These are backstops, not
  a substitute for the locked posting service.
- Runtime warehouse deletion must not cascade-delete stock; use RESTRICT FKs.
  External product/variant/unit/actor IDs have no cross-service FK.
- Balance PK supports warehouse list ordered by variant_id and pair detail.
  The implemented (variant_id,warehouse_id) index supports variant-only filtering.
  No partial nonzero index is added without measured need.
- Movement unique balance/version index supports backward keyset history and
  predecessor validation without a second duplicate index. Operation/line unique
  index supports result replay and per-operation reads. Add an index for the
  referencing movement operation/warehouse FK if not covered by the chosen
  operation/line index; check plans rather than duplicating indexes blindly.
- Successful-key and source-event unique indexes enforce deduplication. Local FKs
  and immutable rows are retained together; no TTL job deletes historical keys.

Full reconciliation computes exact SUM(IN pieces)-SUM(OUT pieces) per pair and
compares with balance and contiguous versions. PostgreSQL SUM(bigint) uses widened
numeric accumulation; a Go reconciliation tool may use big.Int for this aggregate.
That avoids overflow from lifetime turnover exceeding int64 even though each
balance is in range; it does not introduce fractional or decimal stock storage.
Use a consistent DB snapshot for reconciliation, not independent pages racing
with live writes. A mismatch is an alert and investigated repair procedure, never
silently fixing balances or editing movements. Full history scans are an offline
check; per-post deferred checks use indexed last/predecessor rows, not a full sum.

Operational limit: current inventory_service owns its migrated tables. Owners/DB
administrators can disable triggers or alter data; this is application-level
immutability with DB backstops, not tamper-proof/WORM storage. Separate migration
ownership/runtime privileges, audited break-glass repair and backup restore need
an explicit operational follow-up. Do not change normal roles in this task.

## 6. Exact posting transaction and concurrency (implemented internally; section 15)

### 6.1 Before the transaction: no stock locks across HTTP

1. A future workflow adapter authenticates and authorizes its own business action
   with a verified principal and allowed producer. Internal method visibility or
   a private network is not HTTP authentication. No public posting route exists.
2. Validate UUIDs, stable logical operation/source event/key, positive whole counts,
   command version, one warehouse/direction and <=100 distinct variant lines.
   Reject duplicate variants and unknown fields. Capture actor from security.Identity.
3. Compute canonical intent fingerprint (section 7). Read any completed key from
   the primary database. Check current authorization/ownership and hash, then replay
   without Catalog access when matched. Never bypass current authorization on retry.
4. For a new key, resolve and validate all Catalog references/revisions/factors
   under the bounded HTTP budget. Convert with overflow checks. Do not hold a
   warehouse advisory lock, warehouse row lock or stock row lock while doing so.
   Concurrent new-key requests may duplicate this read-only work safely.

   If reference resolution fails, perform a bounded fresh completed-key lookup on
   the primary before returning that error, subject to the original caller deadline
   and authorization. Another same-key request may have committed since step 3;
   replay an authorized matching result even if the reference is now inactive or
   Catalog is unavailable. Without a visible completed match, return the validation
   error with outcome explicitly unconfirmed for a competing request; do not claim
   that the logical action did not post. A later retry keeps the same IDs/key.
   Never enter the write path with failed validation. No mutation or unbounded
   lookup continues after caller cancellation.

### 6.2 One local READ COMMITTED transaction

5. Begin pgx transaction using caller cancellation. Insert stock_idempotency with
   IN_PROGRESS and intended operation UUID, ON CONFLICT(producer,key) DO NOTHING.
   A competing insert waits on the unique key, bounded by the lock deadline. If
   another transaction commits first, reread in a fresh READ COMMITTED statement;
   compare owner/hash and replay its completed result or return conflict, without
   rechecking active flags in this replay branch. End the read-only losing
   transaction before returning. If it
   rolls back, this transaction can claim the key. Do not use a broad ON CONFLICT
   clause that silently absorbs unrelated operation/source uniqueness failures.
6. Lock the single warehouse row FOR SHARE; check it exists and is active under
   confirmed D2. Do this before inserting its referencing header, so a missing local
   warehouse produces the intended not-found result rather than an FK failure.
   FOR KEY SHARE is insufficient: it can allow a non-key active-status update.
   FOR SHARE allows unrelated stock postings to share this warehouse lock while
   conflicting with a warehouse UPDATE. No warehouse-wide advisory lock.
7. Insert the immutable operation header and enforce stable source-event/operation
   identity uniqueness. A new HTTP key for an already used logical action cannot
   post again; return stock_operation_conflict and direct caller to its original
   key/result. Any conflicting transaction must be resolved before proceeding.
8. Sort balance keys lexicographically by UUID bytes. For each in that order,
   INSERT zero/version-0 balance ON CONFLICT(warehouse_id,variant_id) DO NOTHING.
   Then acquire FOR UPDATE on each balance in the same order. Safe initialization
   lets concurrent first receipts converge on one row instead of losing an insert.
9. From the locked state, verify base-unit binding, enforce OUT >= required pieces,
   IN <= MaxInt64-pieces,
   and version < MaxInt64. Validate every line before any durable result. An issue
   must not succeed from a balance read performed before these locks.
10. Insert one immutable movement per line with before/after, version+1 and snapshots.
    Update each balance to that after value/version and updated_at in this same
    transaction. Check exactly one affected row per expected update. No partial
    line success. Deferred constraints check final correspondence at Commit.
11. Store immutable typed result, transition idempotency to COMPLETED, and commit.
    Only after confirmed commit return the successful result. Every statement and
    Commit can fail; pass errors through safe classification and named mappings.
12. On any pre-commit error roll back: no balance initialization, header, movement
    or successful-key record remains. A failed whole operation cannot post some lines.

Example for two deductions from 10: both may validate Catalog concurrently; after
one locks balance and commits 7, the other sees 3 under its lock and rejects 7.
No lock-free check-then-write, no negative result, no ledger without matching balance.
An SQL conditional update can provide a second quantity predicate, but does not
replace locked before/after/version logic or checked IN overflow.

### 6.3 Status synchronization, deadlocks and recovery

Recommended companion behavior at implementation: warehouse deactivation acquires
its warehouse row FOR UPDATE before testing for any nonzero balance, then rejects
closure with warehouse_has_stock. The posting FOR SHARE lock makes this local
check race-safe. Current SetActive lacks this stock check; adding it is a proposed
behavior change requiring spec approval, not an implemented rule. Nonzero-stock
closure/recovery policy must be reconciled with D2. Inactive Catalog state can
still race after a remote check; local locks cannot prevent that distributed race.

Current warehouse metadata mutations retain advisory key 782341092. Do not reuse
it for stock: it would serialize unrelated variants and warehouses. Future
multi-warehouse operations must acquire all warehouse locks in sorted UUID order,
then all balance keys in sorted order. Review/align metadata default-switch lock
ordering before introducing those operations; current metadata may touch the old
and new defaults in a different order. Transfers are not enabled by this document.

Original proposed budgets: total posting deadline <=5s, reference resolution <=2s,
all local transaction attempts <=2s, lock wait <=500ms. Slice 3 uses a configurable
3s local transaction budget instead, within the same overall 5s deadline, because
the expanded disposable benchmark observed approximately 2s for 100 lines. This
is an internal execution budget, not a production SLA. Retry only the complete local
transaction for SQLSTATE 40001/40P01, at most three attempts with bounded jitter and
remaining context budget. Do not retry one statement from an aborted transaction.
Slice 3 reacquires references outside locks on every new attempt. A mandatory
FreshnessPolicy checks each observation batch; there is no default age policy.
The proposed 2s observation age remains unapproved and is not enforced implicitly.
Business rejection, different payload and unknown invariants are not automatic
retries. A known idempotency-key wait timeout may return retryable in-progress;
other lock saturation is a safe temporary-unavailable failure. Never parse DB text.

Set transaction-local lock_timeout and statement_timeout from the remaining
budget; caller cancellation alone is not the complete lock-budget mechanism.
SQLSTATE 55P03 is in-progress only when the failing statement is the known key
gate; other lock timeouts map to 503 stock_busy. SQLSTATE 57014 follows the
deadline/cancellation mapping unless commit outcome is uncertain. A timed-out SQL
statement aborts its transaction: roll back before any primary recheck, and retry
only in a fresh transaction. Defer bounded rollback/connection cleanup on every
path, including a canceled caller; a short cleanup-only context may be used, never
for continuing posting work. Do not return a pooled connection with an open transaction.

A connection failure during Commit creates an UNKNOWN outcome: the server may have
committed even if the response was lost. Do not report definitely rolled back, use
a fresh key, or compensate blindly. Query the primary using the same producer/key
and logical/source IDs. A completed match replays; an absent result may still be
an uncommitted competing transaction, so re-entry must use the unique-key gate.
Return a safe retryable outcome if certainty cannot be obtained within budget.
Subsequent retries must keep the same command and IDs. No background unbounded retry
or continued mutation after caller cancellation is introduced.

## 7. Idempotency contract (internal implementation in section 15; HTTP proposed)

- Future workflow HTTP adapters require exactly one Idempotency-Key header, a
  canonical nonzero UUID (v4 or v7), max 36 characters. The internal method receives
  the same typed key even without HTTP. Reads do not require this header.
- Unique scope is (server-approved producer,key) within this Inventory deployment.
  Do not scope only by route, actor, warehouse or contract version and accidentally
  allow a logical action to be repeated by changing one of those values. The
  independent operation/source-event uniqueness is an additional safety boundary.
- Canonical fingerprint uses versioned typed encoding and SHA-256. Include logical
  operation UUID, producer/source event, authenticated actor, warehouse, direction,
  command version, sorted line numbers/variant UUIDs, canonical counts, selected
  pack UUID/revision and expected stock-reference revision. Include future
  effect-changing workflow fields only through a reviewed command version.
- Normalize UUID/count representation and reject duplicate lines before hashing.
  Preserve semantic line numbers; changing JSON property order is not new intent.
  Do not hash raw JSON text, auth token, request_id, transient timestamps or current
  Catalog labels. Resolved factors/snapshots are stored separately; never obtain
  a different hash merely because Catalog changed after a successful request.
- Same committed key + same fingerprint + authorized owner: return the stored
  original operation/movement IDs, after quantities, versions and result version.
  Set replayed=true in the current envelope; do not reread current balance as the
  original result. A future workflow HTTP adapter keeps its documented original
  success status and may set Idempotency-Replayed:true. Core has no HTTP status.
- Same committed key + different fingerprint: 409 idempotency_key_reused, no writes.
  Different actor is denied without disclosing another user's stored result.
- Check owner/current authorization before exposing hash-conflict details. Keep
  fingerprint/result decoders versioned for retained history; deployment of a new
  command version must not make old successful commands unreplayable. Never
  silently rehash an old command using a new canonicalization algorithm.
- Simultaneous same-key requests serialize on the insert; successful requests have
  one durable operation. When waiting exceeds budget return retryable 409
  stock_operation_in_progress with Retry-After:1 in a future HTTP adapter, not an
  unbounded request or fabricated success.
- Scope of persistence is SUCCESSFUL postings. Validation, dependency failures and
  insufficient-stock attempts do not commit a key record and leave all stock tables
  unchanged. Therefore an uncommitted/rolled-back attempt has no durable key binding;
  clients must use a new key for changed intent, but permanent different-payload
  rejection is guaranteed once a successful key exists. Sticky rejected outcomes
  would require a separate approved durable-claim/error-result protocol; do not
  silently promise it while rolling back every write.
- Reauthenticate/reauthorize every attempt, including replay. Existing stateless
  revocation limits still apply. No Catalog revalidation is needed for completed
  replay; a new effect must pass new reference validation.
- Retain successful keys, hashes, owner and operation linkage permanently with the
  ledger by default. There is no expiry after which a key is treated as new.
  If large response bodies are later compacted, immutable header/movements must
  reconstruct the original result version; a permanent key/source tombstone remains.
  Backup/restore/archival must preserve these boundaries together. A finite TTL
  without a durable uniqueness boundary would re-enable duplicate effects.
- request_id is tracing per attempt; Idempotency-Key identifies durable intent.
  Neither a successful HTTP response nor an incoming request ID is evidence of
  exactly-once network delivery. The guarantee is one committed local stock effect
  per durable logical action under this contract.

### 7.1 Implemented fingerprint v1 contract

CommandVersion=1, ResultVersion=1 and FingerprintVersion=1 are explicit independent
versions. Unsupported command versions fail structural validation; they are never
silently interpreted as v1. Future versions require their own reviewed encoding and
retained-version replay support. Fingerprint returns {Version, Hash [32]byte}; it
does not mint, replace or persist the caller's idempotency key.

SHA-256 input is exactly this unambiguous binary sequence:

1. ASCII domain tag inventory.stock.request followed by a zero byte.
2. Big-endian uint16 fingerprint version, then uint16 command version.
3. Raw 16-byte operation UUID; producer text; source-event UUID; authenticated
   actor UUID; warehouse UUID; direction text; uint16 line count.
4. Lines ordered by semantic LineNumber, each containing uint16 line number,
   raw variant UUID, uint64 positive input count, one-byte pack-presence marker
   (0/1), selected pack UUID and revision text only when present, then expected
   stock-reference revision text.

Every text field has a big-endian uint32 UTF-8 byte-length prefix. UUID values are
already parsed, so equivalent spelling produces identical bytes. Text is preserved
exactly; opaque revisions are not case-folded, trimmed or numerically ordered.
Producer/revision validation requires nonblank valid UTF-8 within the existing
64/128-character bounds; producer authorization remains the future adapter's job.
Input slices are copied before sorting and are never reordered in place.

Excluded: idempotency key, request ID, token/credentials, Catalog labels, resolved
factors, observation time/revision (distinct from the requested expected revision),
posting time, generated movement/result IDs and replay flags. ReferenceSnapshot
and PostedResult are separate arguments/types, not fields in Command. Changing
expected reference/pack revision DOES change intent; changing a later observation
does not change the fingerprint of an already requested operation.

Golden fixture: synthetic-test producer, fixture IDs/count/revisions in
internal/stock/fingerprint_test.go. Literal canonical bytes yield SHA-256
f00e106d983db48d9947f3bcc18de8f379cc23db93b4a0dfca8fe35e4773f786,
independently checked using .NET SHA256 over those literal bytes.

## 8. PROPOSED read APIs and internal posting interface

These routes and Auth definitions are not present/applied today. Keep existing
warehouse paths, DTOs and permissions intact. Require exact GLOBAL grants through
Inventory's existing RS256 middleware; default-deny unmapped routes. No wildcard,
WAREHOUSE-scope interpretation or arbitrary stock-write grant is introduced.

| Proposed route | Permission | Contract |
| --- | --- | --- |
| GET /api/v1/inventory/stock-balances | inventory.stock-balances:read | Optional warehouse_id, variant_id, nonzero_only; bounded keyset list |
| GET /api/v1/inventory/warehouses/:id/stock-balances/:variant_id | inventory.stock-balances:read | One materialized pair and version; :id is warehouse UUID |
| GET /api/v1/inventory/warehouses/:id/stock-movements?variant_id=UUID | inventory.stock-movements:read | Required variant filter; history of exactly one pair; :id is warehouse UUID |

Use :id at the shared /warehouses/:id branch, matching existing Gin registration
and exact permission-policy templates. Gin v1.12.0 rejects a differently named
wildcard at that same branch (dependency tree.go: addRoute). Register existing
warehouse GETs and proposed stock GETs together in a router-construction regression
test; assert no panic and an exact policy entry for each registered business route.

All UUIDs are nonzero; boolean filters are strict. Default limit 50, allowed 1..100.
Opaque base64url JSON cursor <=1024 bytes carries cursor schema version, normalized
filter fingerprint and last key. Reject malformed/mismatched-filter cursors with
400. Cursor contents never grant authority or override supplied route filters.
Version cursor fields use canonical int64 decimal strings; reject negatives,
overflow, before_version above the captured upper bound, and unknown cursor schema
versions. A client-lowered upper bound only narrows its authorized history window;
it cannot expose rows outside route/filter authorization. The page size is not an
identity filter and may change within 1..100 without changing that window.

Balance list orders (warehouse_id,variant_id) ascending and uses a strict greater-than
last key; a variant-only filter uses the proposed variant/warehouse index. Return
only stored balances, including zero by default. A valid local warehouse with no
balance for a variant returns 404 stock_balance_not_found, not a fabricated zero
or a claim the Catalog variant does not exist. A missing warehouse is 404
warehouse_not_found. Reads never initialize a row or require Catalog availability.
Historical or inactive references remain readable from local rows/snapshots.

Balance lists are live keyset reads, not repeatable multi-page snapshots: values
may change, and newly initialized lower-sorting keys may be absent from a traversal.
Do not claim stable cross-page totals or use that traversal for reconciliation.
Use returned versions and refresh the list for current UI state. Reconciliation
uses its own consistent database snapshot. Names/SKUs never determine list ordering.

History orders balance_version descending. Capture snapshot_upper_version from the
same first-page DB snapshot; retain it plus before_version in subsequent cursors.
Query versions <=upper and <before. Per-balance locking/version sequencing means
later committed posts have greater versions and do not shift this fixed history
window. No global timestamp/UUID cursor is advertised as commit-order stable: a
late-committing transaction can have an earlier UUIDv7 or transaction timestamp.
History for an existing zero balance may be nonempty; an uninitialized pair is 404.

Illustrative response contracts, not executed data:

```json
{
  "items": [{
    "warehouse_id": "01935f00-0000-7000-8000-000000000001",
    "variant_id": "01935f00-0000-7000-8000-000000000002",
    "base_unit_id": "01935f00-0000-7000-8000-000000000003",
    "on_hand_pieces": "12",
    "balance_version": "1",
    "updated_at": "2026-10-22T08:00:00Z"
  }],
  "next_cursor": null
}
```

The detail route returns the item directly. A provided nonexistent warehouse filter
returns warehouse_not_found; a variant filter with no local rows gives an empty
list, without claiming whether the Catalog variant exists. A history read uses a
single query or short read-only consistent-snapshot transaction to capture the
upper version and first page. It does not imply live Catalog labels
or a computed available/reserved quantity.

```json
{
  "items": [{
    "id": "01935f00-0000-7000-8000-000000000006",
    "operation_id": "01935f00-0000-7000-8000-000000000005",
    "warehouse_id": "01935f00-0000-7000-8000-000000000001",
    "variant_id": "01935f00-0000-7000-8000-000000000002",
    "direction": "IN",
    "input_count": "2",
    "pack_id": "01935f00-0000-7000-8000-000000000004",
    "pack_revision": "1",
    "pieces_per_input": "6",
    "pieces": "12",
    "before_pieces": "0",
    "after_pieces": "12",
    "balance_version": "1",
    "sku_snapshot": "SKU-A",
    "base_unit_code_snapshot": "PCS",
    "pack_label_snapshot": "Box of six",
    "stock_reference_revision": "opaque-reference-revision",
    "actor_user_id": "01935f00-0000-7000-8000-000000000007",
    "posted_at": "2026-10-22T08:00:00Z"
  }],
  "snapshot_upper_version": "1",
  "next_cursor": null
}
```

History DTO additionally exposes bounded source producer/event and the immutable
product/base-unit reference fields where needed for audit. Do not expose raw
idempotency records, fingerprints, credentials or mutable personal actor names.

### 8.1 Internal contract, not an HTTP route

Slice 1 implements Command, Principal, CommandLine, ReferenceSnapshot/PackSnapshot,
PostedResult/PostedLineResult and sentinel errors in internal/stock. Quantity is an
int64-based scalar with string-only JSON serialization; these other structs are
internal contracts, not HTTP DTOs. Future adapters must serialize versions as
decimal strings and enforce complete strict request decoding as specified above.
No PostingService, provider interface, HTTP parser or persistence is implemented.

Principal validation checks a nonzero user UUID only; it does not authenticate or
authorize. Command validation checks identifiers, v4/v7 RFC4122 idempotency UUID,
v1 command, IN/OUT, 1..100 distinct variants and unique contiguous line numbers,
positive counts and bounded expected/pack revisions. Reference validation checks
PIECE kind, identity/revision match, nonzero observation time, active flags and
selected pack factor/conversion. It does not check clock age, warehouse activity,
Catalog provenance, coherent remote reads or globally immutable unit meaning.
PostedResult.ValidateFor verifies structural operation/line/movement correspondence,
nonnegative after counts and positive balance versions; it cannot prove a commit,
actual ledger state or mathematically correct balance changes without posting state.

Deferred integration contracts: the exact producer allowlist and its authorization
binding; the authenticated Catalog contract and machine identity; final metadata
label/snapshot DTO bounds; and observation-age enforcement. The reference type here
contains only identity/active/revision/factor/time evidence, not complete persisted
historical labels. No inferred pack, default producer, fake authentication or
freshness duration is introduced to fill those gaps. None blocks pure slice 1.

Proposed internal/stock PostingService.Post(ctx, principal, command) returns a typed
PostedResult or typed error. Keep it internal to the Go service. Command contains:

- stable OperationID, SourceEventID, server-approved Producer;
- WarehouseID, Direction IN/OUT, CommandVersion, typed IdempotencyKey;
- 1..100 lines with LineNumber, VariantID, InputCount int64, optional PackID and
  required matching PackRevision when selected, ExpectedStockReferenceRevision;
- no client-provided signed delta, on-hand value, factor, actor identity or bypass.

LineNumber is unique and covers 1..line_count. ExpectedStockReferenceRevision and
PackRevision are bounded nonempty opaque strings; no numeric ordering is inferred.
Any JSON adapter uses explicit decimal strings for int64 fields; typed Go command
fields do not authorize ordinary JSON numeric decoding. Proposed versioned hash
encoding uses fixed-width UUID bytes and unambiguous length-prefixed fields, with
lines ordered by LineNumber; sort balance locks separately by UUID bytes. Document
golden vectors before implementation so equivalent inputs hash identically.

Principal comes from verified middleware/approved future worker authentication.
The future business adapter checks its own action permission, binds allowed producer
and source event, and maps to this typed command. The core validates the trusted
context again at its boundary; metadata-service credentials cannot authorize a
human stock effect. A test-only fake does not create an externally callable route.
Result contains OperationID, movement IDs and original after quantities/versions,
result version, posted time and replayed flag. No arbitrary SetBalance method.
A future in-process fulfillment module can use this boundary once its workflow is
approved. Cross-service workflow requests/events need their later authenticated
API or Inbox design; a Go interface alone is not cross-service communication.

### 8.2 Errors

Public error envelope stays {code,error,request_id} with fixed generic text. New
names below are proposals; exact final sentinels/mappings are implementation work.
Dependency/insufficient-stock errors apply to internal posting/future authorized
workflow adapters, not a newly exposed generic write API.

| Condition | Proposed HTTP mapping where an adapter exists | Stock effect |
| --- | --- | --- |
| Invalid quantity/UUID/cursor, fraction/overflow, invalid pack relationship | 400 invalid_input | None |
| Missing/invalid token | 401 unauthorized | None, do not reach posting |
| Missing exact grant or replay ownership | 403 forbidden | None |
| Missing local warehouse/materialized balance | 404 warehouse_not_found / stock_balance_not_found | None |
| Missing Catalog reference for a new post | 404 catalog_reference_not_found | None; no silent creation |
| Insufficient locked on-hand | 409 insufficient_stock | Full rollback |
| Inactive reference (confirmed D2), changed reference revision/base-unit binding | 409 stock_reference_inactive / stock_reference_changed | None |
| Different fingerprint for completed key | 409 idempotency_key_reused | None |
| Same logical operation/source action under another key | 409 stock_operation_conflict | None |
| Known bounded same-key contention | 409 stock_operation_in_progress, Retry-After:1 | No second effect |
| Other local lock saturation | 503 stock_busy, Retry-After:1 | Roll back this attempt; retry same IDs/key |
| Catalog unavailable/malformed/service-auth failure | 503 catalog_unavailable | None, safe diagnostic reason distinguishes cause |
| Catalog time budget expired | 504 catalog_timeout | None |
| Caller cancellation/deadline | Existing 408 request_canceled / 504 request_timeout convention | Rollback before commit; uncertainty handled separately |
| Unknown commit result | 503 stock_outcome_unknown, retry same key | May already be committed; do not claim rollback |
| Unknown DB constraint or ledger invariant | 500 internal_error | Rollback where outcome is known |

Stock-effect entries describe this attempt, not proof that another same-key request
did not commit. Apply the completed-key recheck in section 6.1 before a reference
failure response. Unknown commit outcome takes precedence over generic timeout or
cancellation mapping once COMMIT may have reached the server. A known server
rejection (including a deferred constraint failure) is a failed commit, distinct
from a transport failure with unknown outcome. Adapters must preserve that distinction.

Use errors.As on pgconn.PgError with exact SQLSTATE plus reviewed known constraint
names. Known code/source/key uniqueness can map to their conflicts. Do not turn all
23514, all stock_* constraints or raw message substrings into validation errors.
Check Commit errors, including deferred constraints; preserve SQLSTATE safely.
Proposed named boundaries: stock_operations_source_event_unique and
stock_idempotency_operation_unique (23505) map to stock_operation_conflict.
The successful-key primary-key race is resolved by the explicit insert/recheck gate.
A collision in stock_movements_balance_version_unique or a violation of
stock_balances_nonnegative after locked validation signals an invariant defect,
not automatic client duplicate/insufficient-stock mapping.

## 9. Diagnostics and operational boundaries

Reuse internal/platform/logger Failure/Classify/DiagnoseHTTP/Respond and existing
request middleware. Required safe diagnostic fields: service, component (stock,
postgres, catalog_client), operation, stable reason, request_id for HTTP; valid
SQLSTATE when present. Safe operation/warehouse/variant/source UUIDs may be included
where useful and authorized. Use fixed summaries and typed causes, not raw errors.

Business rejection (insufficient_stock, reference_inactive, idempotency conflict)
is a normal 4xx result, not repeated Error logging. Unexpected DB/dependency failure
has one classified diagnostic at the owning boundary; wrapping must not log twice.
Suggested stable reasons include catalog_timeout, catalog_unavailable,
catalog_credentials_rejected, reference_invalid, stock_overflow,
ledger_balance_mismatch, transaction_retry_exhausted and commit_outcome_unknown.
Do not expose service-auth configuration details to callers.

Preserve bounded probe logging and slow-request behavior. Do not log JWTs, secrets,
Authorization/cookies, HTTP bodies, raw Catalog error bodies, SQL parameters,
connection URLs or PgError Message/Detail/Hint. Avoid logging raw idempotency keys;
operation IDs and bounded reason fields suffice. Canary tests inspect rendered JSON
and public errors, including wrapped DB/client errors with malicious values.

## 10. Verification plan (slice-specific actual evidence in sections 13-15)

| Test group | Meaningful acceptance evidence |
| --- | --- |
| Whole pieces | Accept 1/MaxInt64 as valid counts where conversion/balance permits; reject 0, negatives, 1.5, 2.0, exponents, numeric JSON, leading zeros and out-of-range strings without repository writes |
| Packs | 2 * 6 = 12; reject zero/negative/missing/mismatched factors/revisions/variant, inactive packs under proposed pack validation; check MaxInt64/factor boundary and historical replay after factor revision |
| Core receipt/issue | Internal receipt creates ledger/balance/header/key together; issue uses locked current stock; no public mutation endpoint exists |
| Insufficient stock | Compare all four tables before/after, including absent-row initialization and idempotency; no partial lines or successful-key record remain |
| Concurrent deductions | Barrier-start two issues of 7 from 10 with real independent DB connections: one success, one insufficiency, balance 3, one new movement |
| Concurrent first receipt | Barrier-start different operations for the same absent pair; one balance, both committed movements, exact sum, contiguous versions |
| Same key | Barrier-start identical keys: one effect, stable result/IDs on replay; concurrent/successful different-payload conflicts and different-actor denial; timeout path bounded |
| Independent source dedupe | A fresh key/operation ID with the same source action cannot post twice; collision is not absorbed by a broad ON CONFLICT |
| Rollback | Inject failures after header/movement insertion, balance update and before completion; every transactional table returns to its prior state |
| Commit uncertainty | Simulate lost acknowledgement plus actual commit and actual rollback; retry same key proves no double effect; missing lookup alone is not proof of rollback |
| DB backstops | Direct invalid writes fail known named checks; orphan version-0 balances, forged results, mismatched reciprocal key/header/actor, movement-only/balance-only changes and incomplete line sets fail at Commit; unknown constraints remain internal; identity/base-unit edits and DELETE/TRUNCATE of balances or immutable facts rejected |
| Lock ordering/retries | Opposite input line orders acquire the same sorted locks; 40001/40P01 retry the whole transaction under finite budget; cancellation/lock timeout releases resources |
| Warehouse concurrency | FOR SHARE blocks deactivation until posting ends; proposed FOR UPDATE/nonzero guard prevents closure with stock. Existing default-switch and warehouse API behavior remains covered |
| Reconciliation | Sum signed ledger with widened exact accumulator in a consistent snapshot; compare versions/balances, including huge lifetime turnover; detect corruption without silent repair |
| Pagination | History upper-version cursor excludes later posts without duplicate/skipped preexisting movements; filter-mismatch cursor rejected; balance list tests its documented live-page limits |
| Security | Anonymous 401/missing grant 403 on each proposed read; no mutation repository call on denial; no WAREHOUSE-to-GLOBAL promotion; actor cannot be supplied/spoofed via body |
| Catalog | Timeout/unavailable/401/403/malformed/oversized/missing/inactive/stale metadata produces safe failure; expected-ID completeness; no stock locks held during a blocked HTTP fake; shared unit meaning frozen across warehouse first posts; reproduce Catalog deactivation between validation and commit and assert the explicitly approved R1 guarantee |
| Real integration | Disposable Catalog/Auth/Inventory contract with explicit service identity; actual authorized token issuance, correct keys, exact least-privilege grants. Mocks alone do not establish this |
| Historical independence | Inactive/renamed/removed Catalog item does not erase ledger or change old pack conversion; successful replay works during Catalog outage after current authorization, including a commit between initial lookup and failing validation; old fingerprint/result versions remain replayable after upgrade |
| Router registration | Construct existing warehouse plus proposed stock routes in one Gin engine; no wildcard panic; exact FullPath policies, anonymous 401 and missing-grant 403 for every GET |
| Migration/tooling | New migration up/down/up through pinned migrate CLI in a uniquely named disposable project; version/dirty=false, FK/trigger cleanup/reapply; no normal DB rollback |
| Existing regressions | Warehouse normalization/default/status rules, JWT contract, safe logs, health/readiness and gateway prefix stay intact; normal authorized acceptance remains separately attributed |

Keep barriers/channels rather than timing sleeps as concurrency coordination.
Tests use temporary keys, identities, databases and explicit fixture roles; inspect
only safe diagnostics. Production DBs and normal credentials are never test fixtures.
The proposed new stock harness owns a uniquely named disposable container/project,
uses inventory_stock_test (not inventory_db), generated fixture credentials/keys
and dedicated test variables, and verifies container identity plus host/port/database
before migrations or cleanup. It must not load local/production .env files, use
normal ERP containers, run compose down -v or remove existing volumes. Only resources
created and recorded by that run are removed. Existing warehouse harness database
names are not permission to reuse a normal database. Ungated DB tests must skip
explicitly, and skipped tests never count as persistence/concurrency evidence.
Run targeted tests per slice, then gofmt changed Go files, go test -count=1 ./...,
go vet, go build, sqlc twice with output comparison, and whitespace/untracked review.
Recheck migration numbers against current history. A disposable down migration test
is not approval to delete posted data in normal environments.

## 11. Ordered implementation roadmap (PROPOSED, after approval)

| Slice | Proposed locations/responsibility | Dependencies and acceptance |
| --- | --- | --- |
| 0. Approve the selected implementation slice | This spec; later companion Catalog stock-reference/pack design and Auth machine-identity design | D1/D2/R1 confirmed; slice 1 authorized. Further slices require approval. Slices 2-3 can use test reference providers without completed companion delivery; live contracts remain production gates |
| 1. Exact quantity/domain types (implemented) | internal/stock/{quantity,command,result,errors,reference,fingerprint}.go and focused tests | Checked int64 parsing/conversion/addition/subtraction, string quantity JSON, command/result/reference validation, versioned canonical fingerprint; no posting or I/O |
| 2. Schema and SQL (implemented) | db/migrations/000002_create_stock_foundation.{up,down}.sql, db/queries/stock.sql; existing sqlc output package | Four-table constraints/immutability/correspondence guards; isolated migration/query tests; warehouse migration unchanged; see section 13 |
| 3. Atomic posting (implemented; section 15) | internal/stock/posting.go + reference-provider/posting repository interfaces; internal/repository/postgres/stock.go | Synthetic provider tests and real PostgreSQL locks/idempotency/rollback tests; simulated transport/transaction faults explicitly identified; no HTTP write adapter |
| 4a. Catalog prerequisites | Catalog candidate db/migrations/000003_stock_reference_metadata.{up,down}.sql and domain/query/service/handler/router locations in section 3.2.1 | Companion schema/API approval; explicit eligibility, immutable physical meaning, Catalog-owned versioned packs, coherent complete reference snapshot, exact string factors and no-stale behavior. Safe legacy treatment; no shared DB or sibling internal imports |
| 4b. Auth/service communication prerequisites | Authentication internal/router/router.go, internal/helper/jwt.go and reviewed service-principal issuance/domain/storage locations; Catalog internal/security/jwt.go compatibility | Separate security specification/approval before code. Define identity, token issuance, scope, rotation and verifier compatibility; actual least-privilege service token issuance plus Catalog validation tests. User login or permission definitions alone do not satisfy this slice |
| 4c. Inventory Catalog adapter | Proposed internal/platform/catalog/client.go and focused contract tests; configuration/wiring locations approved with companion contract | Depends on 4a/4b and R1. Enforce budgets, complete-ID validation and exact conversions; no locks over HTTP, no stale/admin/user-token fallback. Absent provider fails closed; never wire a test fake into production |
| 5. Warehouse status integration | Existing repository/postgres/warehouse.go plus a new named query and tests | Approved nonzero-stock closure guard, row-lock sequencing compatible with posting/default switching; no warehouse-wide stock advisory lock |
| 6. Permission definitions | Authentication candidate db/migrations/000007_inventory_stock_read_permissions.{up,down}.sql after current 000006; Catalog stock-reference definition as companion | Approved stock balances/movements read definitions only, no automatic role/user grants. Reallocate if machine-auth changes consume this number first; definitions alone do not create a service identity |
| 7. Read handlers/security | internal/handler/stock.go, stock read service/repository; internal/router/router.go | Depends on schema and reviewed definitions/policy contract, independent of live Catalog for historical reads. Only three approved GET routes; matching :id wildcard, DTO/cursor/error/history-window tests and current warehouse regressions |
| 8. Reconciliation/integration acceptance | internal/stock reconciliation service or bounded operator tool; scripts/test-stock-foundation.ps1 proposed; docs | Consistent-snapshot reconciliation, disposable real-identity HTTP/reference checks, SQLSTATE/canary tests, reproducible sqlc, normal 200 acceptance recorded only when actually executed |

Slices 1-2 implement pure domain types plus schema/SQL/generated bindings and
isolated integrity tests. Slice 3 was subsequently explicitly authorized and
implemented (section 15): internal/stock/posting.go and narrow provider/repository interfaces,
internal/repository/postgres/stock.go and focused tests. It must compose existing
queries in one transaction, authorize workflow/principal boundaries, acquire stable
ordered locks, validate references outside locks, resolve completed-key retries,
roll back all failed effects, classify known constraints and handle uncertain commits.
Acceptance includes opposite-order multi-line contention, concurrent same-key replay,
commit acknowledgement loss, fault injection at each stage and bounded cleanup.
For every line, after InitializeStockBalance and LockStockBalance and before any
quantity mutation, compare the locked row's base_unit_id with the validated Catalog
snapshot's base_unit_id. A mismatch returns ErrReferenceChanged (future adapter:
stock_reference_changed), rejects the ENTIRE operation and rolls back all staged
headers, claims, balance initialization, movements and balance changes. Never
overwrite, relabel, convert or silently reuse the old binding to accept a mismatch.
Posting acceptance must assert unchanged balances/ledger/operation/key tables for
a mismatch on any line, including after earlier lines were prepared. Existing SQL
tests prove initialization is non-overwriting and direct unit mutation is denied;
the slice 3 repository tests additionally verify the application comparison using synthetic observations.
Use test-only reference providers first. No public stock mutation endpoint or live
Catalog/machine-auth assumption is authorized by the internal slice. Catalog/Auth
production prerequisites remain separate, and no normal database changes occurred.

Slices 2 and 3 are implemented with isolated PostgreSQL and injected test-only references.
The next roadmap slice is 4a: separately specify/approve Catalog eligibility,
immutable piece-unit binding, versioned packs and coherent stock-reference reads.
Read SQL/DTO work can also proceed without live Catalog once its local schema is
approved. ALL production new posting, including direct pieces with factor 1, stays
disabled until Catalog proves explicit piece eligibility and active references,
the Auth communication contract works, confirmed R1 is honored, and local warehouse status
synchronization is accepted. There is no automatic pieces-only fallback. Opening
stock, adjustments, reversal approvals, reservations, transfers and fulfillment
remain separately specified later workflows; foundation tests are not those workflows.

Candidate numbers are not reservations. If another migration lands first, allocate
the next number; never rewrite an applied migration. Migration down would remove
posted facts/deduplication and is destructive; normal rollback favors compatible
application rollback and backups, not casually running down.

No standalone transaction-runner abstraction, stock valuation/accounting, delivery,
reservation or transfer schema, Kafka implementation, audit-personal-data expansion
or direct Catalog DB integration is justified by this slice. Minimum posting actor
and immutable source/snapshot attribution are integral to the new ledger, not a
retrofit of created_by/updated_by columns across unrelated warehouse/master tables.

## 12. Review gates and checks performed for this document

Historical specification-review verdict: DOMAIN SLICE 1 AND SCHEMA/SQL SLICE 2 IMPLEMENTED.
Section 15 supersedes this status with the authorized internal slice 3. R1 is confirmed. The review findings
below remain design guidance for subsequent slices, not evidence of persistence
or production readiness; further implementation requires approval.

### 12.1 Findings, ordered by severity

| Severity | Finding and disposition | References |
| --- | --- | --- |
| High, resolved decision | A remote active check cannot prove Catalog remains active at Inventory commit. R1 now explicitly accepts fresh valid-at-check semantics with retained revision/time; active-at-commit is not guaranteed. | Sections 3.3, 6.3, 12.2 |
| High, implementation prerequisite | Catalog lacks piece eligibility, pack/version data and coherent stock-reference reads; Auth lacks machine issuance. Existing user tokens/grants cannot be presumed sufficient. Required companion schema/API/auth acceptance is now explicit, with production posting disabled until delivered. | Sections 3.2.1, 3.3, 11 slices 4a-4c |
| High, corrected | Per-warehouse base-unit checks do not protect another warehouse's first posting or a semantic change under the same UUID. Require Catalog-global immutable physical meaning/binding and explicit legacy eligibility; do not infer pieces from labels. | Sections 3.2, 3.2.1 |
| High, corrected | Reciprocal FKs and object-shaped result JSON alone do not prove ledger/result/key correspondence. Added exact reciprocal actor/operation/key checks, result/ledger validation, orphan initialization rejection and complete mutation-path coverage. | Section 5.5 |
| High, corrected | Proposed :warehouse_id conflicts with existing Gin :id in the same GET tree and can panic during registration. Reused :id and added router construction/policy acceptance. | Section 8; Inventory-Service/internal/router/router.go; Gin v1.12.0 tree.go: addRoute |
| Medium, corrected | Concurrent completion after the initial key lookup could be masked by Catalog failure/inactivation. Added a bounded completed-key recheck, replay before active validation at the gate, and no false claim that another attempt rolled back. | Sections 6.1, 6.2, 8.2 |
| Medium, corrected | Lock budgets, canceled cleanup and unknown commit mappings lacked implementation precision. Added transaction-local deadlines, rollback before recheck, safe cleanup, 55P03 mapping and commit-uncertainty precedence. | Sections 6.3, 8.2 |
| Medium, corrected | Roadmap grouped Catalog/Auth/adapter work and made independent foundation work appear to require completed integration. Split the slices and named the next pure-domain scope and acceptance. | Section 11 |

### 12.2 Confirmed, proposed and unresolved decisions

D1/D2/R1 and all eleven request rules are CONFIRMED. Integer/string representation
and domain/fingerprint contracts are implemented under slice 1 authorization.
The four-table schema and successful-only permanent-key persistence contract are
implemented in slice 2. GLOBAL read permissions, production pack validation,
production freshness and remote authorization remain future integration work.
Internal bounded retries and transaction-service behavior are implemented in section 15.

| Unresolved item | Options and recommendation | Gate |
| --- | --- | --- |
| Companion contract approval | Recommended: explicit Catalog eligibility/frozen meaning/versioned packs plus coherent fresh resolver and reviewed Auth service identity. A separately approved user-delegation path is an alternative but requires exact user grants and does not eliminate missing Catalog metadata. | Security/architecture approval and live contract evidence before production posting; proposed endpoint names are not existing routes. |

Warehouse nonzero-stock closure protection is already a future requirement in
inventory-rules.md. Its FOR UPDATE synchronization, error mapping and recovery
behavior remain proposed mechanics for slice 5; this review does not change current
warehouse behavior. Controlled correction on inactive references remains later work,
not an implicit exception allowing ordinary OUT to clear stock.

### 12.3 Scope and verification record

| Mistake | Cause | Prevention | Verification at implementation |
| --- | --- | --- | --- |
| Rounding a whole count or overflowing conversion | Generic JSON numbers lose precision above 2^53; unchecked int64 arithmetic wraps | Quantity accepts exact canonical decimal strings; check operands before add/subtract/multiply | Slice 1 tests cover 2^53+1, MaxInt64, MinInt64 rejection, overflow and no receiver mutation on failed decode |
| Changing a retry fingerprint without changing intent | Raw JSON, slice order, observation time or generated IDs get hashed | Frozen v1 binary framing, sorted copy of semantic lines, separate execution evidence and key | Slice 1 literal-byte/hash golden vector, reordered-line/equivalent-UUID tests and meaningful/volatile field tests |
| Treating a retry failure as proof the stock action failed | Another request can commit between the initial key lookup and Catalog failure, or COMMIT acknowledgement can be lost | Recheck completed keys with current authorization, keep original IDs/key, distinguish unknown outcome | Barrier-controlled replay race and lost-acknowledgement tests with committed and rolled-back outcomes |
| Reinterpreting pieces in another warehouse | A per-pair unit check cannot enforce global variant meaning | Catalog freezes stock-eligible physical meaning and unit binding; no label-based factors | First-post tests across two warehouses and same-ID meaning-change rejection |
| Accepting malformed ledger/result rows despite FKs | Links establish existence, not exact correspondence; CHECK allows NULL | Explicit NOT NULL, exact deferred correspondence, immutable result/identity and mutation-path guards | Direct writes and Commit assertions for forged results, mismatched keys/actors and orphan balances |
| Running isolated tests against a normal DB | A database name or environment variable alone does not prove fixture ownership | Unique run-owned container, dedicated inventory_stock_test, generated credentials and identity checks before changes | Harness refusal tests for normal DB names/ports/container IDs and cleanup restricted to recorded resources |

D1/D2/R1 are confirmed and slices 1-2 are implemented. Before further implementation,
approve posting behavior, read permissions, local warehouse
closure behavior and Catalog/Auth prerequisites. Later reversal/date/approval
workflows get their own specifications. A future business change from pieces to
fractional stock requires a separate migration/API review; do not quietly switch
these columns to NUMERIC or round values.

The earlier finalization was documentation-only. Slice 1 added pure internal/stock
code; slice 2 subsequently adds migrations, queries, bindings and isolated SQL tests.
No application posting repository/service, endpoints, grants or deployment changes
were added. No normal database or service was changed and no secrets were printed.
Production posting remains NOT IMPLEMENTED. See section 13 for current SQL evidence.

### 12.4 Slice 1 verification (2026-10-23)

- gofmt check: all eleven new Go files formatted.
- go test -count=1 ./internal/stock: passed.
- go test -count=1 -cover ./internal/stock: passed, 100.0% statement coverage,
  using a run-created workspace GOTMPDIR removed afterward. The initial default
  Windows sandbox temp path denied a coverage-report rename; no test assertion
  failed in that coverage attempt. The first golden-test authoring run used a
  placeholder expected hash; the independently checked fixed vector replaced it
  before the passing runs.
- go vet ./internal/stock, go build ./internal/stock and go build ./...: passed.
  The full build emitted a nonfatal sandbox module stat-cache write warning and
  exited 0. go list confirmed only standard-library imports plus existing google/uuid
  in the new package; go.mod/go.sum are unchanged.
- Document JSON examples, fences, links, formatting and preserved code-file hashes
  checked. Scoped git diff --check passed; the full check still reports pre-existing
  .gitignore whitespace. That file was deliberately preserved, not reformatted.
- No broad application test suite, database test, live Catalog/Auth check or runtime
  posting acceptance was performed or implied by these domain tests.

## 13. Implemented schema/SQL slice 2 (2026-10-23)

### 13.1 Files, objects and review decisions

- db/migrations/000002_create_stock_foundation.{up,down}.sql: four tables,
  twelve constraint/query indexes, seven functions and ten non-internal triggers.
  No warehouse migration edits, extensions or external-service FKs.
- Tables: stock_balances, stock_operations, stock_movements, stock_idempotency.
- Functions: stock_reject_mutation, stock_guard_balance_update,
  stock_guard_key_update, stock_expected_result, stock_assert_balance,
  stock_assert_operation, stock_check_deferred.
- Four deferred constraint triggers cover balance INSERT/UPDATE, operation INSERT,
  movement INSERT and key INSERT/UPDATE. Four statement guards deny deletion or
  truncation of the stock tables and update of posted headers/movements; two row
  guards protect balance identity/version transitions and claim-to-completion.
- db/queries/stock.sql and generated sqlcdb/{stock.sql.go,models.go} implement the
  query contracts below. sqlc v1.31.1 is pinned to the existing generator version;
  no generated code was hand-edited. Existing warehouse generated API content and
  bytes were preserved after normalizing its generator-only LF/CRLF difference.
- internal/repository/postgres/stock_integration_test.go contains test-only staged
  SQL with synthetic Catalog IDs. scripts/test-stock-postgres.ps1 owns a unique
  labelled tmpfs PostgreSQL container, generated credentials and inventory_stock_test.
  No normal DATABASE_URL/.env fallback or production/local secret reuse exists.
- scripts/test-warehouse-postgres.ps1 now pins its Inventory migration to version 1,
  preserving its original milestone assertions after adding 000002. This is a
  necessary harness compatibility change, not a warehouse behavior/schema change.

Review decisions: schema number 000002 was unused; current command, fingerprint
and result versions are constrained to 1 to match domain contracts. Adding future
versions requires an explicit compatible migration/decoder review. pack_label stays
nullable TEXT without an invented length limit; its final bound remains a Catalog
adapter-contract prerequisite. Required product/name/SKU/unit snapshots use the
already documented limits. Existing owner/runtime roles are unchanged.

Durable result v1 is stored exactly once in stock_idempotency.result. It is derived
by stock_expected_result from immutable facts and checked again at commit:

```json
{
  "operation_id": "01935f00-0000-7000-8000-000000000005",
  "result_version": "1",
  "posted_at": "2026-10-23T00:00:00.000000Z",
  "lines": [{
    "line_number": 1,
    "variant_id": "01935f00-0000-7000-8000-000000000002",
    "movement_id": "01935f00-0000-7000-8000-000000000006",
    "after_pieces": "12",
    "balance_version": "1"
  }]
}
```

Lines are ordered by semantic line_number. All int64 values and result_version
are decimal strings; bounded smallint line_number is numeric. posted_at has a
fixed UTC microsecond format, independent of session timezone. request_id and the
per-attempt replayed flag are not persisted in this result. This is an internal
durable representation, not a new HTTP response contract.

### 13.2 Integrity ownership

| Invariant | Database enforcement | Future application/reconciliation responsibility |
| --- | --- | --- |
| Exact pieces and overflow | BIGINT, NOT NULL and named positive/nonnegative checks; numeric-widened conversion/delta CHECKs; conditional receive/issue predicates prevent overflow/overdraw/version overflow | Validate canonical quantities before SQL; interpret conditional no-row using locked state, not a blanket insufficient-stock mapping |
| Pair and unit identity | PK warehouse/variant, local warehouse FK, nonzero IDs, immutable pair/base-unit/created_at; version strictly advances on updates | Catalog owns global unit meaning, piece eligibility and variant binding; no external FK can prove these |
| Operation and key identity | Unique operation/source-event/key, valid v4/v7 RFC4122 keys, 32-byte v1 fingerprint, reciprocal deferred references and matching actor/producer/key/operation | Authenticate actor, bind server-allowed producer/source IDs, compute and compare canonical fingerprint; DB does not recompute SHA-256 intent or authorize replay |
| Complete facts at commit | Exact operation line count, distinct variant and contiguous 1..line_count numbers, matching direction/warehouse/base unit, contiguous per-pair predecessor before/after chain, final balance equals latest ledger version/quantity | Caller must append all facts in one transaction and handle every statement/Commit error; unknown commit outcome requires same-key resolution |
| Successful replay result | Claimed IN_PROGRESS cannot commit; only one completion transition; exact immutable result equality to operation/ledger-derived v1 JSON | Current authorization/owner check before replay and conflict handling, no revalidation of completed effect against inactive Catalog |
| Immutability | Headers/movements reject UPDATE/DELETE/TRUNCATE; balances reject identity changes/DELETE/TRUNCATE; completed keys cannot change or disappear | Owner/admin can disable/alter guards; no tamper-proof/WORM or new privilege separation is claimed |
| Historical audit | Required immutable product/unit labels, IDs, revisions, counts/factor/pieces and checked-at evidence | Adapter must provide trustworthy coherent snapshots and final label bounds; SQL cannot authenticate Catalog or enforce a freshness clock policy |
| Reconciliation | Indexed final/predecessor checks on affected operations/pairs, not a full ledger scan on each post | Controlled single-snapshot test sums widened numeric lifetime turnover and compares balances/versions; future bounded operator reconciliation/repair is not implemented |

The active warehouse check, remote validation, R1 observation age, authorization,
timeouts, stable lock sequencing and uncertain-commit recovery remain future posting
service obligations. SQL permits a syntactically valid synthetic snapshot; passing
these tests is not evidence of Catalog eligibility, pack APIs or machine identity.
No new freshness value is approved or enforced in this slice.

BIGINT guarantees stored integers, not detection of an explicitly coerced SQL
fraction: PostgreSQL can round a numeric value before assigning it to BIGINT.
Fractional/malformed caller inputs must be rejected by domain parsing and typed
int64 bindings before SQL. The schema's widened numeric expressions are only
overflow-safe integrity calculations, not fractional quantity storage or approval
to cast arbitrary decimal input into a stock amount.

### 13.3 Query contracts and transaction boundary

| Queries | Contract |
| --- | --- |
| ClaimStockIdempotency / GetStockIdempotency | Claim uses ON CONFLICT(producer,key) DO NOTHING and returns rows affected. Never overwrites hash/actor/result; source/operation conflicts are not absorbed. Look up committed result on the primary; only a successful completed key can exist durably. |
| LockStockWarehouse | FOR SHARE returns local active state. Caller validates it; shared metadata lock is not the global warehouse advisory lock. |
| InitializeStockBalance / LockStockBalance / GetStockBalance | Initialize zero/version 0 in the transaction; a fresh orphan cannot commit. ON CONFLICT does not change a unit binding. Lock/read the actual row and compare unit before mutation; ordinary reads create nothing. |
| ReceiveStockPieces / IssueStockPieces | Positive int64 amount only, row-level conditional atomic update, version+1 and DB timestamp; no row on absent/insufficient/overflow/exhausted version. Must be followed by matching movement/key completion in that same transaction; not standalone autocommit writes. |
| InsertStockOperation / InsertStockMovement | Parameterized immutable header/snapshot rows. Use header before movements and balances before referencing lines; no hidden transaction/commit or idempotent upsert of ledger facts. |
| CompleteStockIdempotency | Complete only the exact producer/key/operation in IN_PROGRESS, returns affected count; result is derived from immutable lines/header. Caller requires one affected row, then checks Commit/deferred constraints. |
| ListStockBalances | Optional warehouse/variant/nonzero filters; ascending warehouse/variant UUID tuple keyset; initial cursor is two non-null all-zero UUIDs (not SQL NULL). Query bounds page rows to 1..101 for 100+lookahead; future API validates limit 1..100. Live pages, not a repeatable snapshot. |
| ListStockHistory | One warehouse/variant, descending version; supplied fixed upper bound and nullable exclusive before-version; 1..101 bounded rows. Caller captures initial upper bound/page in one snapshot. Joins immutable header for actor/source/time; no current Catalog join. |

Mutation composition must use sqlcdb.New(tx) or WithTx(tx), not separate pool calls.
Stable order: key claim, warehouse FOR SHARE/active check, header, then balance
initialization/row locks sorted lexicographically by UUID bytes; append lines and
update balances, complete key, commit. Never use metadata advisory key 782341092 to
serialize stock. Resolve Catalog outside locks. No query starts/commits a transaction,
and no application repository/posting service/startup wiring was added in slice 2.

Down migration is destructive to new stock facts and permanent deduplication.
It removes only the stock tables/indexes/constraints/triggers/functions, explicitly
breaks reciprocal dependencies and uses no CASCADE. Warehouse table/function/trigger
and pre-existing rows survive. Normal rollback still needs a reviewed backup/recovery
procedure; this work ran down only in its labelled disposable database.

### 13.4 Verification evidence and limits

Pinned sqlc generation twice produced identical SHA256 output. The final disposable
harness verified 1:false baseline, 2:false up, 1:false down, 2:false reapply;
warehouse sentinel data survived; all stock table/index/function/trigger objects
were removed by down. Populated stock down/reapply also ended at 2:false.

Seven SQL test groups passed: complete boundary/replay, invalid constraints,
deferred correspondence, reference/uniqueness/pack snapshots, synchronized concurrent
deduction/initialization, pagination/reconciliation and valid multi-line transaction
(including session-timezone-independent durable results). Concurrent tests hold two
independent transactions at a channel barrier before balance initialization/locking.
No timing sleeps coordinate assertions. Every run removed its own container/tmpfs
data and workspace temp files and restored its changed environment variables.

Initial failures were test-fixture issues: lowercase warehouse codes, assertions
that assumed one of multiple violated constraints would fire first, FK rejection of
TRUNCATE before custom triggers, and a manual test transaction not cleaned up on a
fatal assertion. Corrected fixtures/assertions and registered bounded rollback on
every transaction; the stalled run-owned test executable was stopped once so its
harness finally cleanup could run. No application rule was weakened to pass tests.

Broader Go/static/formatting and final preservation checks are reported with their
actual final results. Ordinary go test without fixture gates skips DB/HTTP cases;
those skips do not substitute for the passing disposable evidence above. Existing
warehouse milestone testing remains separate. No normal DB migration, role/account,
key, service, deployment or sibling repository change occurred.

At the slice 2 checkpoint the next slice was the transactional posting
service/repository; its subsequent implementation is recorded in section 15.
Catalog piece eligibility/versioned packs, coherent authenticated reference reads,
service authentication, freshness enforcement, Auth permissions and stock HTTP APIs
remain unimplemented. The whole Stock Foundation milestone is not complete.

### 13.5 Final checks

- Pinned sqlc/sqlc:1.31.1 generate ran twice; generated SHA256 sets matched.
- Focused domain/repository Go tests passed; the seven stock SQL groups passed
  under the disposable harness with native PostgreSQL 17 and migrate v4.18.3.
- go test -count=1 ./... first hit Windows linker resource and existing localhost
  shutdown-test failures in the sandbox. go test -p 1 -count=1 ./... then passed
  outside the sandbox with dedicated fixture gates cleared/restored; DB/HTTP cases
  skip in that ordinary run. No existing test was weakened or changed.
- go vet ./... and go build ./... passed. A sandbox module stat-cache write warning
  was nonfatal. Existing warehouse migration/repository tests passed in their
  separate disposable harness after the version-1 pin, without HTTP/service startup.
- PowerShell parsing, gofmt, document JSON examples/fences/links and slice whitespace
  checks passed. Full git diff --check still reports only preserved pre-existing
  .gitignore whitespace; scoped checks cover the new/modified slice files.
- Twenty-one protected baseline hashes match: all domain code/tests, warehouse
  migration/query/generated API files, manifests, sqlc config and the pre-existing
  .gitignore/warehouse integration-test edits. The warehouse model itself is
  unchanged; generated models.go only adds the four stock model structs.
- All run-owned containers and temporary directories were cleaned up; changed
  environment variables were restored. Sibling repositories stayed clean. No
  normal DB migration, deployment, commit, push, merge or PR occurred.

## 14. Slice 2 review follow-up (2026-10-23)

Verdict: REVIEW FINDINGS ADDRESSED. This follow-up changes only test code and the
relevant specification/rules documentation. Both migration files, stock.sql,
generated bindings, domain code and the disposable harness are unchanged.
Normal database state and whether 000002 has been deployed/shared are NOT VERIFIED;
no normal database was accessed to establish that. No migration rewrite is needed
for this correction. Any future integrity/performance SQL change must establish
deployment/shared status first and use a forward migration if already applied.

### 14.1 Concurrent failure handling: finding confirmed and corrected

The old worker path called stage/stockOK/finish and could invoke t.Fatal/t.Fatalf
from worker goroutines. Its WaitGroup.Wait had no bound. The corrected path uses
error-returning test-only workers, structured terminal results and main-test-only
assertions. It does not introduce an application repository or posting service.

- Each worker sends exactly one result to a channel buffered for all workers.
  A panic is converted to a type-only diagnostic; no raw panic/error arguments
  or credentials are printed. Workers do not use testing.T or fatal helpers.
- The coordinator handles readiness and terminal results together. Failure before
  the barrier cancels the peer instead of waiting for impossible readiness. Context
  bounds execution to 10s; canceled result collection has an independent 3s bound.
  These are test safety deadlines, not production timeouts or freshness values.
- SQL workers own their transactions and defer rollback on every unsuccessful path
  using a fresh context with a 2s cleanup bound. Terminal reporting follows cleanup.
  Cancellation can release workers blocked at readiness/release and SQL uses that
  same request context. There is no unbounded WaitGroup or unbuffered final send.
- Healthy concurrency still synchronizes two actual PostgreSQL transactions before
  balance initialization/locking. Main-thread assertions verify distinct nonzero
  backend PIDs, one accepted/one rejected deduction, and both first receipts.
- TestStockWorkerFailureBeforeBarrier needs no DB and injects early failure; it
  verifies both terminal results and peer cancellation. The SQL counterpart waits
  until both claims/headers exist, injects failure before readiness, verifies both
  independent transactions rolled back and no stock facts/orphan claims remain.

### 14.2 Deferred-check cost: repeated work confirmed, guards retained

Source analysis: for one valid N-line operation with fresh balances, each movement,
the operation header and the key's insert/update queue operation checks (N+3).
Each successful check validates the complete line set and reconstructs the result;
completion constructs the result once more (nominal N+4 result reconstructions).
Balance insert/update/movement events also queue indexed final-state checks.
This is repeated work with approximately quadratic line traversal/result assembly
within the maximum 100-line operation. These are source-derived call counts, not
instrumented PostgreSQL function-call measurements.

Measurements used one run-owned tmpfs PostgreSQL 17.11 container, x86_64 Linux musl
(Alpine GCC 15.2), shared_buffers=128MB, JIT on. Docker Engine 29.5.3 exposed 12
logical CPUs and 3,746,779,136 bytes of engine memory, without a dedicated per-test
CPU quota. Client: Windows/amd64, Go 1.25.4, 12 logical CPUs; SQL travelled over the
random localhost Docker port. Image ID:
sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24.

One warmup plus seven measured valid, single-threaded transactions per size, using
fresh synthetic variants. Warehouse/fixture creation is excluded. Setup includes
ID allocation, command validation/hash and line sorting; statements include Begin,
claims, locks, inserts, conditional balance changes and key completion. Commit
includes all deferred work plus client/network/server scheduling. Values are
milliseconds; each cell is median [minimum,maximum].

| Lines | Setup ms | Statements ms | Commit ms | End-to-end ms |
| --- | --- | --- | --- | --- |
| 1 | 0.000 [0.000,0.000] | 26.185 [16.199,33.786] | 7.219 [5.448,12.084] | 34.530 [23.240,44.003] |
| 10 | 0.000 [0.000,0.512] | 111.412 [101.443,123.927] | 18.233 [15.975,23.452] | 131.484 [117.418,142.612] |
| 100 | 0.000 [0.000,0.545] | 911.545 [850.125,1374.028] | 316.115 [295.716,376.669] | 1219.443 [1165.306,1697.941] |

Setup zeroes reflect observed Windows clock resolution for short intervals, not
proof of zero CPU cost. End-to-end is the sum of the three measured intervals;
fixture creation is outside that sum. Samples and safe runtime/plan summaries are emitted by
TestStockSQLDeferredCost; no wall-clock threshold determines test success.

Separate EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) probes after fixture ANALYZE examined
line count, result-line aggregation, latest balance movement and predecessor joins.
For 1/10-line probes, the small tables favored sequential/hash plans. At 100 lines,
count/result aggregation used a bitmap scan on
stock_movements_operation_variant_unique, while the latest-movement lookup used
stock_movements_balance_version_unique. The predecessor probe used hash joins and
sequential scans over the small fixture tables. No index hint/forced plan was used.
At 100 lines, representative execution times were count 0.128ms, result-line
aggregation 1.241ms, latest movement 0.069ms and predecessor join 1.218ms.
These probes do not expose every cached PL/pgSQL plan or measure the total deferred
execution; the Commit wall time above is the relevant inclusive client observation.

Decision: keep current invariant coverage and the existing migration. Commit cost
is visible at 100 lines, but this local sample did not demonstrate a production
latency/correctness failure that justifies migration or trigger-deduplication
machinery. The measured 100-line commit is about 0.3s, with local end-to-end samples
below 1.7s. This is NOT a production SLA or validation under contention, cold cache,
large history, other hardware or operational load. Posting/deployment work must
budget for Commit and revisit the repeated-work cost with realistic load; do not
assume all calls will fit the proposed transaction budget because this fixture did.

### 14.3 Base-unit boundary: requirement confirmed and made explicit

The retained SQL tests prove InitializeStockBalance does not overwrite an existing
base_unit_id and direct unit changes fail stock_balance_transition. Those primitive
tests cannot prove the future repository compares a Catalog snapshot to a locked
balance. Section 11 now explicitly requires that comparison before mutation and
full-operation rollback (balances, movements, headers and idempotency) on mismatch.
There is no fallback conversion/rebinding and no implemented production comparison.

### 14.4 Verification and remaining scope

The disposable harness passed all nine SQL groups, including failure handling and
cost measurements, plus up/down/reapply and populated rollback. Both concurrent
failure tests passed; the no-DB helper additionally passed 25 repeated runs.
Existing domain tests, focused vet/build, gofmt and scoped whitespace checks passed.
The full diff check still flags only preserved pre-existing .gitignore whitespace.
Twenty-five baseline files are unchanged; only stock_integration_test.go and the
specification/inventory-rules documents changed in this review. SQL and bindings did not change, so
sqlc regeneration is unnecessary for this follow-up. Fixture cleanup removed only
the labelled run-owned container/tmpfs data and temporary directory and restored
the harness environment.

At this historical review checkpoint, slice 3 could be proposed next, subject to its explicit implementation approval and
the base-unit/failure/Commit acceptance criteria above. This review does not start
that slice. Catalog eligibility/packs/reference authentication, service identity,
freshness enforcement, permissions, stock HTTP APIs and production posting remain
unimplemented. No normal databases, existing services/accounts/credentials/keys or
sibling repositories were changed; no commit, push, PR, merge or deployment occurred.

## 15. Internal posting slice 3 (implemented 2026-10-23)

Verdict: SLICE 3 COMPLETE for the authorized internal service/repository scope.
This is not completion of the Stock Foundation or Inventory Service and does not
enable authenticated production stock operations. No normal deployment occurred.
Migration 000002, SQL queries and generated bindings are unchanged in this slice.

### 15.1 Entry point and trust boundaries

`internal/stock/posting.go`: `NewPostingService(store, authorizer, references,
freshness, options)` constructs the internal `PostingService.Post(ctx, principal,
command) (PostedResult, error)`. All four dependencies are mandatory. There is no
startup construction, HTTP route, fake production provider, token fallback or
implicitly authorized producer. The verified calling workflow must supply a
trusted principal; a UUID by itself is not authentication.

- ProducerAuthorizer.Authorize must establish trusted identity, current workflow
  authority and the exact producer namespace. It runs before lookup, before each
  effect attempt and again before returning replay. No production allowlist,
  permission or service-principal policy is invented here.
- ReferenceProvider.Resolve must provide authenticated coherent Catalog evidence.
  Observation embeds ReferenceSnapshot plus product code/name, SKU, base-unit code
  and optional pack label. These historical labels satisfy existing SQL bounds;
  pack label has no invented length limit. Prepare matches the complete variant
  set, validates IDs/revisions/active PIECE semantics and selected pack, copies
  optional metadata and computes exact overflow-checked pieces. Request metadata
  is never accepted as proof of Catalog provenance.
- FreshnessPolicy.Check is mandatory, with no production implementation/default
  age. The policy and reference acquisition run outside locks. Each retry obtains
  new observations; the proposed two-second observation age remains unapproved.
- PostingStore.Lookup/Apply is a trusted internal persistence boundary, not a
  publicly callable authorization API. StockRepository uses the authoritative
  primary pool supplied to NewStockRepository. Replica pools are unsupported.

R1 remains confirmed: required Catalog entities must be active at the coherent
check, with reference revision and CheckedAt retained in each immutable movement.
Catalog may deactivate after that observation and before the Inventory commit;
active-at-commit is not guaranteed. The tests use explicitly synthetic references,
authorization and freshness policies; they do not prove actual Catalog integration.

### 15.2 Execution and lock order

1. Clone command lines, validate existing command/quantity contracts, calculate
   unchanged canonical fingerprint v1, authorize the actor/producer.
2. Look up the completed primary key and check owner before fingerprint/version/
   operation identity. Matching completed results replay without reference calls.
3. Resolve, validate and convert references outside any transaction. If resolution
   or validation fails, make one bounded fresh completed-key lookup before returning
   a safe reference error. A peer may have committed during the redundant lookup.
   A reference failure with no visible result does not prove a peer rolled back.
4. Reauthorize and begin READ COMMITTED. Set transaction-local lock_timeout and
   statement_timeout with remaining caller budget. Use one transaction-bound
   sqlc Queries instance for every stock statement.
5. Claim key. A losing insert rereads with a fresh READ COMMITTED statement, verifies
   the durable binding, and rolls back its read-only transaction before replay.
6. Lock warehouse FOR SHARE; distinguish missing from inactive; insert header.
7. Sort balance identities by UUID bytes, then initialize/lock each in that order.
   Preserve semantic line numbers. Compare every locked base-unit binding and
   validate all arithmetic/versions before any quantity mutation.
8. Apply each conditional change, requiring the returned row/version. Insert the
   matching UUIDv7 movement and complete exactly one key. SQL constructs the durable
   result; decode and validate it before Commit. Only confirmed Commit/recovery
   returns success. All pre-commit failures roll back the whole operation, including
   newly initialized zero rows. No warehouse-wide stock advisory lock is used.

Application checks include authorization, observation provenance/eligibility via
the injected boundary, locked base-unit agreement, exact conversion, arithmetic,
version exhaustion, stable lock order and stage-specific error classification.
SQL enforces uniqueness, local FKs, arithmetic, immutable facts and deferred final
balance/movement/operation/key/result correspondence. Neither layer replaces the
other; SQL has no cross-service knowledge of Catalog active state or user authority.

### 15.3 Replay, safe errors and uncertainty

The existing v1 fingerprint/quantity serialization is unchanged. The caller's key
is distinct from the fingerprint. DecodePostedResult accepts exactly the existing
stock_expected_result v1 fields, including decimal-string result version,
after_pieces and balance_version, and numeric smallint semantic line_number.
Unknown versions, malformed/trailing/unknown-field data, null quantities or wrong
operation/line correspondence fail with ErrStoredResult. Replayed is an envelope
flag, never part of the durable JSON. Replay returns the original result, not a
fresh balance read.

Recognizable errors: ErrForbidden, ErrKeyConflict, ErrOperationConflict,
ErrWarehouseNotFound, ErrWarehouseInactive, ErrReferenceChanged,
ErrReferenceInactive, ErrReferenceUnavailable, ErrInsufficientStock,
ErrQuantityOverflow, ErrVersionExhausted, ErrInProgress, ErrBusy,
ErrStoredResult, ErrInternal and ErrOutcomeUnknown. Existing invalid-input and
context cancellation/deadline errors remain recognizable with errors.Is.

Different owner is denied before hash details. Different stored fingerprint/version
or operation binding is a key conflict. Different keys colliding on operation or
producer/source identity are operation conflicts, never equivalent replay. Only
the exact named 23505 constraints receive this business mapping. An absent balance
after initialization or rejected conditional update after locked predicates is an
internal invariant failure, not generic insufficiency. Unknown/deferred integrity
violations remain internal. 55P03 at claim is in-progress; elsewhere busy. 57014
respects caller cancellation/deadline, otherwise a safe busy database diagnostic.

Normal PostgreSQL Commit rejection/ErrTxCommitRollback is definite failure.
Transport loss or fatal connection termination during Commit is conservative
UNKNOWN. After original transaction cleanup, Apply attempts one bounded lookup
using a new physical connection to the configured primary. Only a matching owner,
hash, version, operation and validated durable result proves success. An absent,
unavailable or mismatching observation does not authorize mutation retry:
ErrOutcomeUnknown instructs the caller to preserve all identities and the key.
No automatic new key, compensation or mutation retry follows an uncertain Commit.

The service/domain helpers do not log repeatedly. Repository infrastructure causes
are wrapped with existing logger.Classify safe Failure metadata (component,
operation, stable reason, SQLSTATE); raw causes are not rendered. The future
authenticated workflow boundary owns one zerolog event with request_id and safe
operation_id after cleanup. It must not log raw causes, commands or snapshots.
Provider/authorization unknown errors are converted to fixed safe errors.

### 15.4 Bounded work and retry configuration

Defaults are explicit constructor options, not final production/SLA approval:

| Budget | Internal default |
| --- | --- |
| Overall normal posting work | 5 seconds, bounded further by caller deadline |
| Reference resolution and validation | 2 seconds per attempt |
| Cumulative Apply call budget | 3 seconds; earlier proposal was 2 seconds |
| Transaction-local lock wait | 500ms, capped by remaining budget |
| Each primary lookup, including new-connection recovery | 500ms within parent deadline |
| Independent rollback/connection cleanup | 2 seconds per cleanup; may extend wall time beyond normal-work deadline |
| Attempts | At most 3, only known rolled-back 40001/40P01 |
| Backoff | Cancellable jitter in [10ms,20ms); no backoff for business/unknown outcomes |

Retries preserve command IDs/key/fingerprint, roll back first, and reacquire fresh
observations outside locks. Exhaustion returns busy; caller cancellation wins.
Rollback uses an independent bounded context; pgx discards failed rollback sessions.
No detached reference fetch, continued posting or unbounded pool wait is introduced.

The expanded first fixture benchmark observed a maximum 100-line SQL transaction
around 2.008 seconds; hence the explicit 3s internal local default. A later run
under concurrent Go compilation exceeded that deadline. This is expected bounded
failure under load, not evidence of a production latency guarantee. The maximum-
line functional test now explicitly uses 15s total/10s local fixture budgets,
separate from operational defaults and dedicated timeout tests. Successful local
100-line observations were about 1.146s (default budget) and 1.456s (final functional
run). Repeated deferred trigger work remains a load-testing/optimization follow-up;
no integrity checks were weakened to improve timing.

### 15.5 Actual verification and limitations

- Focused stock domain/service/decoder tests passed, including retry boundaries,
  fresh observations per retry, failed-reference replay, replay reauthorization,
  cancellation and safe dependency errors.
- Full disposable harness passed ten posting groups and all nine retained SQL
  groups, migration up/down/up, populated rollback and reapply.
- Final `pwsh -NoProfile -File ./scripts/test-stock-postgres.ps1 -PostingOnly`
  passed eleven posting groups, including explicit UUID lock tracing and R1.
  Container/tmpfs/temp files were removed and prior environment restored.
- `go vet ./...`, `go build ./...` and serial `go test -p 1 -count=1
  -timeout=120s ./...` passed. Ordinary DB-gated tests skipped without fixture
  variables; disposable runs, not skipped tests, establish persistence evidence.
  Build emitted a nonfatal sandbox module-cache stat-write warning and exited 0.
- Formatting and scoped whitespace checks cover new/untracked source as well as
  tracked edits. Full Git whitespace findings are preserved pre-existing .gitignore
  whitespace; no unrelated cleanup is authorized.

Real PostgreSQL evidence: receipt 100/issue 10 leaves 90; pack conversion;
multi-line atomic success; later-line insufficiency/unit mismatch rollback; no
first-issue orphan; int64 quantity boundary; inactive/missing warehouse; synthetic
reference rejection; sequential/concurrent same-key replay; identity conflicts;
concurrent deduction/first initialization; reversed semantic input order; claim
and balance lock timeouts; cancellation cleanup; reconciliation; 100-line posting;
real deferred Commit rejection after an injected ledger mismatch.

Simulated seams around real transactions: stage failures (including after a first
movement), 40001/40P01 responses with known rollback, actual Commit followed by
lost acknowledgement, definite rollback and unresolved transport outcome. These
are not real TCP disruption/deadlock-generation tests. Version exhaustion is
verified directly against synthetic locked state; no infeasible MaxInt64-length
production movement history is claimed. Mutated replay records test version/
owner/hash decoding in memory without bypassing SQL immutability.

One initial deferred-error fixture was rejected by result validation before Commit;
the seam was corrected to inject the mismatch at Commit and passed. A contended
100-line deadline failure is retained in local evidence, not concealed as a pass.
Evidence directory (workspace-relative): review-artifacts/stock-slice3-2026-10-23.

### 15.6 Remaining gates and next milestone

Next roadmap slice: 4a, Catalog-owned explicit PIECE eligibility, immutable global
unit meaning/binding, versioned variant-specific packs and coherent stock-reference
reads. It needs a separately reviewed Catalog specification, safe legacy treatment
and authorization to modify that repository; none is implied by this slice.
Slice 4b separately defines/approves real machine identity, producer authority and
least-privilege token issuance. Slice 4c implements the authenticated Inventory
adapter plus finalized freshness policy/enforcement and live contract tests.

Real Catalog integration, machine authentication, freshness-policy approval and
production enforcement, new stock permissions and HTTP APIs remain incomplete.
Warehouse closure rules, read adapters, operator reconciliation, later workflows
and operational ownership/recovery remain separate milestones. No normal databases,
regular services, project volumes or sibling repositories were changed. No commit,
push, merge, PR or deployment occurred. Existing unrelated edits were preserved.

## 16. Focused review: request-bounded uncertain-commit recovery

This review retains the existing runtime policy. Automatic recovery is best effort
within the transaction/caller deadline, not a guaranteed post-cancellation
reconciliation step. No reserved recovery interval or detached recovery context is
introduced. An unknown outcome is not a duplicate-write defect by itself: later
authorized retries must retain operation ID, source event ID, producer, actor,
intent and idempotency key, and use the permanent unique-key gate.

### 16.1 Actual deadline and release sequence

1. PostingService.Post derives its normal-work context from the caller with the
   configured TotalTimeout (default 5s). Caller cancellation/deadline and context
   values propagate. Reference work has a child limit of 2s per attempt.
2. Each Apply call gets a child context bounded by the remaining cumulative
   TransactionBudget (default 3s) and the overall caller deadline. Begin, stock
   statements and Commit receive that same transaction context. Apply includes
   attempted recovery; it does not receive an extra 500ms after this deadline.
3. Before Apply can recover, attempt returns and its deferred rollback executes.
   Rollback has an independent bounded cleanup context (default 2s), even if the
   normal-work context was canceled. A completed pgx transaction normally returns
   ErrTxClosed immediately here. pgxpool.Commit/Rollback release the pooled
   connection; failed rollback invalidates/discards the underlying session.
4. Only an uncertain Commit enters recovery. Recovery uses a fresh physical
   connection from the authoritative primary configuration, not a pooled possibly
   broken original session or a replica. Its context is a child of the same Apply
   transaction context, limited further by LookupTimeout (default 500ms). This
   covers connection establishment and the one completed-key query together.
   Context values are preserved. If the parent is already expired/canceled, no
   successful recovery query can run, even if the overall 5s budget still has time.
5. If a fresh connection was established, its deferred Close uses another
   independent bounded cleanup context (default 2s). No normal mutation/reference
   work is detached from cancellation; no background recovery goroutine is started.

Recovery normal work never extends its parent normal-work deadline. The
conservative sum of independent cleanup allowances on an Apply path is 2s rollback
plus 2s fresh-connection Close (4s); these are sequential cleanup caps, not a latency
SLA or a promise of zero scheduler/OS overhead. When cancellation precedes recovery
connection creation, no fresh connection Close is needed and only original
transaction cleanup may extend beyond the deadline. In common already-committed
cases, the original rollback returns immediately. The 500ms recovery allowance is
not an extra post-deadline budget. The overall 5s plus both cleanup allowances is a
conservative 9s accounting bound for such a normal request, not a strict stopwatch
guarantee for the process or external infrastructure.

### 16.2 Outcome classification and retry contract

When recovery proves a matching completed operation, Apply returns its original
decoded result with Replayed=true. PostingService checks current authorization
again before disclosing that result. A later completed-key replay also authorizes
before lookup and again before returning it, with no Catalog call or new stock
transaction. The primary result must match owner, canonical fingerprint/version,
operation identity, durable result version and expected lines.

If Commit is uncertain and no completed match can be confirmed, Apply returns
ErrOutcomeUnknown. A transaction/caller cancellation or expiry is joined to that
error when the Apply parent context itself has ended. Therefore errors.Is may be
true for BOTH ErrOutcomeUnknown and context.Canceled/context.DeadlineExceeded.
Consumers must check ErrOutcomeUnknown FIRST; treating the joined error as only a
generic timeout could encourage an unsafe new-key attempt. The service preserves
this error unchanged and does not automatically retry a mutation. A recovery-only
500ms child timeout while the Apply parent remains live still returns
ErrOutcomeUnknown, not a claim of definite rollback or a generic caller timeout.

A PostgreSQL Commit rejection/ErrTxCommitRollback is definite failure and does not
enter uncertain recovery. An unavailable connection, failed/timed-out query or
absent completed row cannot establish definite rollback of an uncertain operation.
A competing original transaction may still be committing; a same-key retry must
serialize at the existing unique claim gate or return bounded ErrInProgress.
If a genuine rollback released the claim, a later identical retry may post once.

Authorization failure never discloses a recovered/stored result. It cannot undo
an operation that was already authorized and committed before authority changed.
This is not a new revocation or rollback policy; it protects result disclosure.

### 16.3 Policy alternatives considered

- Reserve part of the existing request budget for recovery: keeps cancellation
  semantics but reduces time available for a valid transaction and still cannot
  guarantee recovery if the caller cancels. It would require an explicit split
  and further load/deadline evidence; this review does not introduce that policy.
- Use a separately bounded, value-preserving reconciliation context: could improve
  acknowledgement recovery after cancellation, but extends response/shutdown
  work and consumes a fresh primary connection after the caller has left. It
  requires a reviewed cancellation/shutdown/resource policy. No unbounded
  Background context or detached mutation is justified.
- Retain the current request-bounded lookup and require later same-key retry:
  selected for this review. It preserves the existing contract, avoids new
  post-cancellation normal work and keeps permanent deduplication intact.

### 16.4 Regression coverage and review boundary

The new repository tests are in internal/repository/postgres/stock_recovery_test.go;
the existing guarded posting fixture now accepts test-only pgx driver hooks.
TestPostingPreservesUncertainCommitContextErrors in internal/stock/posting_test.go
checks that the service preserves uncertainty and its joined context cause without
retrying. Repository stock.go changes only the explanatory recovery comment.

The targeted cases use real disposable PostgreSQL transactions and synthetic
authorization/references. Acknowledgement loss, connection failure/timeout and
client query failure use explicit driver/transaction seams; no real network loss
is claimed. The blocked recovery-query timeout and concurrent unique-key wait use
real PostgreSQL locks. Deadline expiry waits for context.Done; cancellation and
concurrent attempts use bounded channels, with worker results asserted on the main
test goroutine. No coordination sleeps or worker fatal assertions are introduced.

Coverage includes: live acknowledgement-loss recovery; success followed by expired
transaction context and later same-key replay; explicit caller cancellation;
definite rollback; failed/timed-out recovery connections and queries; absence
remaining unknown; in-flight original Commit with a same-key retry receiving
ErrInProgress; and denial of recovered/later replay after current authority fails.
Successful issue scenarios check one operation, one movement, one completed key,
balance 100->90 and balance version 1->2, plus stable later replay without a new
mutation transaction. Probes check original cleanup before the fresh connection,
distinct physical backend identity, context-value preservation, bounded live cleanup,
closed recovery connections and zero outstanding acquired pooled connections.

Actual commands/results and this focused review's verdict are recorded separately
in review-artifacts/stock-commit-timeout-review-20261023T162057Z-cd06d070.
Closing this timeout gate is not full slice 3 approval and does not start slice 4a.
Slice 4a remains the next separately specified/approved Catalog milestone, subject
to any other open reviews. No Catalog integration, permissions, HTTP API,
credentials, normal infrastructure changes or broader completion claim is added.

### 16.5 Verified outcome of this focused gate (2026-10-23)

Verdict: VERIFIED AS DESIGNED. Runtime behavior and production timeouts are
unchanged; this is regression coverage/documentation, not a recovery-policy fix.

- The disposable -PostingOnly harness passed all 15 posting groups (15.092s),
  including all A-G edge cases and the existing 11 posting groups. Fixture
  migrations/up/down/reapply and populated rollback/reapply passed. The run-owned
  container/tmpfs/temp files were removed and process environment restored.
- Focused service/decoder tests passed, including all three uncertainty/context
  propagation cases. Without fixture variables the repository DB tests skip;
  only the disposable run establishes actual PostgreSQL behavior here.
- Focused go vet and go build for internal/stock and internal/repository/postgres
  passed, as did formatting and scoped whitespace checks. Broader suites were not
  rerun because no runtime logic or shared contracts changed.
- Baseline hashes confirm 78 of 82 existing repository files unchanged. Only the
  specification, explanatory stock.go comment, posting fixture hook and service
  regression tests changed; stock_recovery_test.go is the one new source file.
  In particular, domain posting.go, SQL/migrations/generated bindings, harness,
  unrelated .gitignore/warehouse edits and dependency manifests are preserved.
- The specific timeout gate is closed. Slice 4a may be considered as the next
  separately specified/approved milestone; it was not started or authorized by
  this focused review. This is not full slice 3 approval or completion of the
  Stock Foundation/Inventory Service.

## 17. Slice 4c implemented checkpoint (2026-10-24)

This checkpoint supersedes earlier prerequisite/policy status statements for the
scope delivered here; historical slice reports above remain unchanged.

User-approved freshness decision: maximum observation age **5 seconds**, maximum
future-clock allowance **1 second** (inclusive boundaries). The proposed two-second
age was not adopted. This is separate from the machine JWT verifier's maximum
30-second clock tolerance and does not extend the maximum 300-second issued JWT
lifetime. R1 remains confirmed: active at the coherent Catalog check, with revision
and checked_at retained; deactivation before Inventory commit is accepted and
active-at-commit is not guaranteed.

Catalog now optionally registers POST /api/v1/catalog/stock-references/batch with
direct TLS, the dedicated machine verifier/permission and coherent reference
service. It preserves decimal-string counts and per-item statuses, bounds inputs
and rejects ambiguous JSON. Existing human routes keep their human verifier.
Inventory internal/catalogclient implements verified HTTPS, explicit secret-file
credentials, bounded/coalesced in-memory token refresh, generation-safe one-time
401 refresh, strict complete reference validation and typed safe failures.
Catalog revision hashes remain an opaque versioned contract. No reference cache
or anonymous/human-token fallback is introduced.

Freshness is checked on response acceptance, after preparation and immediately
before every new Apply attempt. NewStockRepositoryWithFreshness also checks after
pool acquisition immediately before BEGIN, so a connection wait cannot consume
the age window unnoticed. Retries reacquire references outside locks; no remote
call occurs while stock locks are held. Future production wiring must select that
constructor and the approved policy. The older repository constructor retains the
explicit synthetic contract used by low-level tests. Completed operations still
replay without Catalog, subject to current producer authorization.

Optional startup configuration validates/builds real dependencies without creating
a production PostingService or granting posting authority. Disabled integration
preserves warehouse behavior. Enabled invalid configuration fails safely. There
are still no stock mutation HTTP routes or production ProducerAuthorizer.

Actual disposable Auth HTTPS issuance -> Catalog HTTPS validation -> Inventory
internal posting and PostgreSQL persistence passed: 10 pieces + 2 packs of 6 - 5
pieces = 17 pieces, with three movements. Revoked credentials cannot issue anew,
while the cached issued JWT remains usable; missing/inactive/changed/obsolete
references reject. Stopped dependencies allow same-key replay without another
effect and block new posting. A real pool-wait test rejects observations that age
past 5s before BEGIN. The existing Auth disposable harness also passed lifecycle,
expiry, grant, disable and concurrency checks. Controlled HTTPS fixtures and clocks
cover token expiry/cache/outage, trust, redirects, cancellation, malformed responses,
401/403 limits and stale/future observations. Real packet loss and normal deployment
are not tested. Detailed commands, limitations and baseline preservation are in
workspace review-artifacts/stock-slice4c-20261024/report.md.

See [catalog-reference-integration.md](catalog-reference-integration.md) for exact
configuration, trust/rotation/cache/error behavior and deployment order. No SQL,
migration or generated binding changed in slice 4c; sqlc was not needed. Auth
000007, Catalog 000003 and Inventory 000002 are prerequisites in each owning
service, applied only to disposable fixtures here. Normal TLS/probes/upstreams,
least-privilege provisioning, protected credentials and normal Catalog eligibility
classification require a separately authorized deployment/management plan.

Next milestone: specify and approve the first production stock workflow and its
actor/producer permissions, input/error and audit contract; then wire the real
components behind that boundary. Opening stock/adjustments require explicit
business rules. Production stock operations, stock HTTP APIs, normal provisioning
and deployment, reservations/transfers/Kafka and the entire Stock Foundation
remain incomplete. No implicit authorization for a further slice is created.

## 18. Stock Read APIs checkpoint (2026-10-25)

The user approved completing the existing read implementation, selecting exact
GLOBAL inventory.stock-balances:read for balances and inventory.stock-movements:read
for movements. This supersedes section 8's proposed route shapes and single-variant
history requirement for this milestone. Historical implementation checkpoints above
remain unchanged; read HTTP APIs are now implemented while mutation APIs remain absent.

- GET /api/v1/inventory/warehouses/:id/stock-balances lists stored rows only, with
  optional variant_id/nonzero_only. GET on the same :id branch at /stock-movements
  lists warehouse-wide immutable history, with optional variant_id.
- Both preserve existing human RS256/GLOBAL-only security and require their exact
  separate permission; warehouse-read authority and machine tokens grant no access.
  Auth migration 000008 defines the permissions without users/roles/grants. Fresh
  human login tokens are needed after separately authorized grant changes.
- Default page size 50, API maximum 100, SQL look-ahead maximum 101. Strict query
  parsing rejects repeated/unknown/empty/malformed keys and invalid cursors.
  Canonical base64url JSON cursors are at most 1024 bytes, versioned and bound to
  endpoint/warehouse/filters; next keys use the last returned row, not look-ahead.
- Balance order is variant_id ascending within the warehouse. History order is
  unique movement id descending. Per-variant balance_version is not a global key;
  neither UUIDv7 nor posted_at is advertised as commit order. Ordinary live keyset
  limitations include changing nonzero membership and late commits sorting above
  a traversed cursor. Static datasets traverse without gaps/duplicates; no frozen
  snapshot or consistent cross-page reconciliation claim is made.
- Explicit DTOs serialize all BIGINT quantities/versions as decimal strings and
  expose recorded product/pack snapshots, not idempotency/replay/request/actor
  metadata. There is no invented business reason. Missing warehouse is 404;
  existing empty warehouse/filter is 200/items:[]. Inactive and historical rows
  remain readable without Catalog access or any writes/balance initialization.
- Inventory migration 000003 adds two complementary movement indexes for warehouse
  and warehouse/variant keyset access. Existing migrations/ListStockHistory and
  callers are preserved. Normal index creation can block writes while building;
  deployment/maintenance planning remains separate. Index down drops indexes only.

Source and full HTTP/filter/cursor/error/examples/manual instructions are in
[stock-read-api.md](stock-read-api.md). Tests include a read-only PostgreSQL pool,
int64/zero values, multi-variant/equal-time ordering, live commit barriers, exact
human permissions and preserved historical snapshots after actual Catalog fixture
rename/inactivation/outage. Actual commands/results, fixture cleanup, original-file
hash preservation and full handwritten code are recorded in workspace
review-artifacts/stock-read-api-5ea469c9f3d343469294e83f58484ec3.

Only disposable migrations and generated synthetic fixtures are authorized here.
No normal migration, accounts/grants, environment/key changes, service restart,
commit/push/PR/merge/deploy or stock mutation workflow is included. Gateway runtime
coverage and normal rollout remain separately unverified. Next task: specify and
approve the first production stock workflow and its actor/producer authorization,
reference-acquisition, error and audit contracts before implementation. Opening
Stock, adjustments, reservations, delivery and messaging patterns remain out of scope.
