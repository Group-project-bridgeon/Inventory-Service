# Stock Foundation slice 4c: authenticated Catalog references

Implemented checkpoint: 2026-10-24. This describes optional transport and internal
components, not deployment or a production stock workflow. The approved freshness
decision is maximum age **5 seconds**, with **1 second** of future-clock allowance.

## Flow and authority

Inventory reads an explicitly configured credential file and obtains a machine
JWT from Auth over verified HTTPS. The exact form is grant_type=client_credentials,
audience=catalog-stock-references and scope=catalog.stock-references:read. HTTP
Basic uses the canonical machine UUID as username and credential-ID.secret as
password. Credentials never come from user tokens, URL parameters or anonymous
fallbacks. Auth owns machine identities, hashed credentials and grants.

Inventory sends POST /api/v1/catalog/stock-references/batch with the machine JWT.
Catalog verifies RS256, issuer, the dedicated audience/subject/token_kind, lifetime
and permission using the slice 4b verifier. Human JWTs cannot enter this route;
machine JWTs cannot enter existing human business routes. Only direct TLS counts
as encryption; forwarded-proto headers are not trusted. Public probes retain their
existing routing rules.

Catalog uses its existing coherent repository/service without the ordinary
reference cache. The batch has one explicit result per requested variant, bounded
to 100 unique variants and 64 KiB. JSON rejects duplicate/case-ambiguous/unknown
fields, null request values, invalid UTF-8 and trailing data. Successful int64
factors and versions remain canonical decimal strings. Responses are no-store.

Inventory validates all identities, counts, statuses, required fields, active and
PIECE eligibility, selected-pack ownership/current revision/factor, metadata,
opaque cat-stock-v1 revision and coherent checked_at. It does not reproduce
Catalog's revision hash. Invalid selected packs cannot fall back to pieces.
Reference results are never cached. Pure helpers return typed errors without
logging remote bodies or secrets; the future authenticated workflow owns its
single failure diagnostic. Catalog transport failures use structured zerolog
component/operation/reason/request_id and safe SQLSTATE where available.

## Configuration and TLS trust

Inventory defaults to disabled. These names are process configuration; no real
credential file or environment file is supplied by this slice.

| Variable | Contract/default |
| --- | --- |
| STOCK_REFERENCE_ENABLED | Absent/false disables; true enables; other values fail |
| STOCK_AUTH_TOKEN_URL | HTTPS, exact /api/v1/auth/service-token path |
| STOCK_CATALOG_BATCH_URL | HTTPS, exact /api/v1/catalog/stock-references/batch path |
| STOCK_MACHINE_ID | Canonical, nonzero machine UUID |
| STOCK_CREDENTIAL_SECRET_FILE | Required explicit credential JSON path |
| STOCK_AUTH_ISSUER | Exact trusted Auth issuer |
| STOCK_CA_FILE | Optional PEM bundle added to system trust; at most 1 MiB |
| STOCK_TOKEN_TIMEOUT | Default 2s; greater than zero and at most 5s |
| STOCK_REFERENCE_TIMEOUT | Default 2s; greater than zero and at most 5s; includes token wait and one possible retry |
| STOCK_TOKEN_MAX_BYTES | Default 16384; allowed 1024..65536 |
| STOCK_REFERENCE_MAX_BYTES | Default 1048576; allowed 1024..2097152 |

Secret JSON has exactly two string fields: credential_id (canonical credential
UUID) and secret (canonical unpadded base64url of 32 random bytes). Its bounded
size is 4 KiB. Provision it separately, restrict OS permissions to the service
identity, and replace it atomically during approved rotation. No sample usable
secret is included. Construction checks file shape; each token issuance rereads
the same path, so replacement takes effect at the next refresh/invalidation,
without restarting. Existing cached JWTs are not invalidated by a file change.

Both outbound endpoints reject URL credentials, query strings, fragments,
plaintext and redirects. TLS validates hostname and CA with minimum TLS 1.2;
there is no InsecureSkipVerify or environment-proxy fallback. Dial/TLS handshake
budgets are 2s, response headers at most 5s/16 KiB, eight connections per host,
idle expiry 30s, and bounded decoded body reads. Caller and component deadlines
bound all work. Response bodies close on every response path. Optional startup
construction validates configuration/trust without a network probe.

Catalog enables the route only with CATALOG_STOCK_REFERENCES_ENABLED=true plus
paired TLS_CERT_PATH/TLS_KEY_PATH and its existing JWT_PUBLIC_KEY_PATH/JWT_ISSUER.
Its default disabled configuration keeps the existing HTTP listener behavior.
Enabling TLS changes the listener for all routes; it requires a separately
reviewed deployment/probe/Kong plan. No normal upstream was changed here.

## Token cache, errors and retry

Tokens exist only in memory. The client checks the response and JWT claim
consistency using authenticated Auth HTTPS as provenance; Catalog performs actual
RSA signature verification. The client does not claim a separate JWT signature
verification step. exp-iat must equal expires_in and be at most 300s. Cache expiry
is the earlier of JWT exp and issuance-start plus expires_in. A refresh margin
of min(15s, TTL/5) never extends expiry using JWT clock tolerance.

One bounded refresh is shared by concurrent callers. Caller cancellation stops
that caller's wait; shared work has its own token deadline and component-close
cancellation. Failed refresh has a 1s cooldown, preventing a storm. A transient
Auth outage may reuse only an unexpired cached token. Credential, malformed or
denied issuance does not receive transient fallback. Expired cache fails closed.
Credential revocation/expiry, disabled machine and removed grants stop issuance;
an already issued JWT can remain accepted by Catalog until exp plus its maximum
30s tolerance. The Inventory cache itself stops at actual expiry.

A Catalog 401 invalidates only the rejected token generation and allows at most
one refresh/retry within the same reference budget. A 403 never refreshes. There
is no automatic retry of arbitrary remote failures. Typed errors distinguish
not-found, inactive, ineligible, changed references, unauthorized credentials,
denied permission, malformed response, stale observation and unavailable service;
context cancellation/deadline remain recognizable. No remote error body escapes.

## R1 and transaction timing

The approved bounds are inclusive: observations older than 5s or more than 1s in
the future fail. This clock policy is independent of JWT's 30s verifier tolerance.
Checks run on response acceptance, after preparation, immediately before every
new Apply attempt, and after pool acquisition immediately before BEGIN through
NewStockRepositoryWithFreshness. The last check covers pool contention. A retry
reacquires references outside database locks; there are no network calls while
Inventory holds stock locks. Future production wiring must use that repository
constructor and the same policy; the legacy constructor remains available for
explicit synthetic low-level repository tests.

R1 guarantees active status at the coherent Catalog observation, retaining its
revision and checked_at in the immutable posting snapshot. Catalog can deactivate
an entity before Inventory commits. This is **not active-at-commit**, and the age
limit does not create a cross-service transaction or lock Catalog entities.
Completed operations replay without Catalog access, subject to current posting
authorization, even during dependency outage.

## Verification and deployment prerequisites

scripts/test-reference-e2e.ps1 creates only a uniquely labelled tmpfs PostgreSQL
fixture with three separately owned databases. It applies Auth through 000007,
Catalog through 000003 and Inventory through 000002, creates only synthetic
identities/grants/eligible records, and uses temporary signing keys and trusted
TLS certificates. Actual Auth/Catalog subprocess handlers feed the real Inventory
HTTPS adapter, internal posting service and PostgreSQL repository. Test-only
authorizer permits the synthetic actor/producer only. No production authorizer
or stock mutation route is installed.

The verified reconciliation is 10 pieces + 2 packs * 6 - 5 pieces = 17 pieces,
three immutable movements; failed lookups and same-key outage replay add no
effects. Real cases include credential revocation with cached-token reuse,
pack revision obsolescence, product revision change/inactivity, missing variant,
stopped HTTPS hosts and stale-after-pool-wait rejection. The Auth disposable
harness separately checks credential expiry, disable, grants and lifecycle races.
Controlled TLS fixtures/clocks simulate cache expiry, Auth 503, malformed/oversized
responses, 401/403 behavior, refresh coalescing, cancellation, stale/future clocks,
bad trust/hostname and redirects. These do not simulate real packet loss or
prove production network behavior. Detailed actual results live in workspace
review-artifacts/stock-slice4c-20261024/report.md.

Deployment order, when separately authorized: review/merge prerequisite branches;
apply service-owned Auth 000007, Catalog 000003 and Inventory 000002 with their
reviewed recovery plans; provision TLS trust and listener/probe/upstream changes;
provision a least-privilege machine/grant and protected credential file; classify
normal Catalog units/variants/packs through authorized management; then enable
optional transport/client configuration and verify it in the target environment.
No normal migrations/provisioning/deployment occurred in this slice.

Next milestone: specify and approve the first production stock workflow and its
actor/producer permissions, input/error API, reference acquisition flow and audit
contract, then wire these components behind that boundary. Opening stock and
adjustments require their own business decisions. Stock HTTP APIs, production
posting authorization/wiring, management APIs, reservations/transfers/Kafka and
normal deployment remain incomplete; the entire Stock Foundation is not complete.
