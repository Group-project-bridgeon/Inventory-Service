# Non-posting local Catalog probe

```text
reference-local-check --confirm-local --variant <confirmed-variant-uuid>
```

Uses the existing configured authenticated HTTPS adapter and its time/body/trust
budgets. Requires STOCK_REFERENCE_ENABLED plus the normal STOCK_* client settings
and protected credential/CA mounts. It accepts the Compose auth-service/catalog-service
DNS names or loopback destinations, never an arbitrary remote deployment hostname.
The explicit local acknowledgement is mandatory before credential/network use.

It prepares one direct piece and prints only variant/unit/revision/check-time/kind.
It does not connect to Inventory PostgreSQL, reserve stock, claim idempotency, post
or replay an operation. Machine credentials and access tokens never go to output.
This does not replace secured human preparation acceptance or authorize real counts.

See ERP-Platform/docs/opening-stock-local-operations.md in the adjacent checkout for
portable configuration. Normal local preparation/read verification and previous
disposable posting tests are separate evidence. Normal posting/replay remains pending;
the complete Inventory Postman collection is a later deliverable.
