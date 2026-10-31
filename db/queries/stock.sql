-- All mutation queries below require ONE caller-owned transaction. No query
-- commits. Claim key first, lock warehouse FOR SHARE/check active, insert header,
-- initialize/lock balance keys in sorted UUID-byte order, write all lines,
-- complete key, then check Commit. Never hold locks across Catalog HTTP calls.

-- name: InitializeStockBalance :exec
INSERT INTO stock_balances (warehouse_id,variant_id,base_unit_id)
VALUES ($1,$2,$3) ON CONFLICT (warehouse_id,variant_id) DO NOTHING;

-- name: GetStockBalance :one
SELECT * FROM stock_balances WHERE warehouse_id=$1 AND variant_id=$2;

-- name: LockStockWarehouse :one
SELECT id,is_active FROM warehouses WHERE id=$1 FOR SHARE;

-- name: LockStockBalance :one
SELECT * FROM stock_balances WHERE warehouse_id=$1 AND variant_id=$2 FOR UPDATE;

-- name: ReceiveStockPieces :one
UPDATE stock_balances SET on_hand_pieces=on_hand_pieces+sqlc.arg(pieces)::bigint,
    version=version+1,updated_at=now()
WHERE warehouse_id=sqlc.arg(warehouse_id) AND variant_id=sqlc.arg(variant_id)
  AND sqlc.arg(pieces)::bigint>0 AND version<9223372036854775807
  AND on_hand_pieces::numeric+sqlc.arg(pieces)::numeric<=9223372036854775807
RETURNING *;

-- name: IssueStockPieces :one
UPDATE stock_balances SET on_hand_pieces=on_hand_pieces-sqlc.arg(pieces)::bigint,
    version=version+1,updated_at=now()
WHERE warehouse_id=sqlc.arg(warehouse_id) AND variant_id=sqlc.arg(variant_id)
  AND sqlc.arg(pieces)::bigint>0 AND on_hand_pieces-reserved_pieces>=sqlc.arg(pieces)::bigint
  AND version<9223372036854775807
RETURNING *;

-- name: InsertStockOperation :one
INSERT INTO stock_operations (id,producer,source_event_id,warehouse_id,direction,actor_user_id,
    line_count,command_version,idempotency_key,first_request_id,actor_kind,actor_machine_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,COALESCE(NULLIF(sqlc.arg(actor_kind)::text,''),'HUMAN'),sqlc.narg(actor_machine_id)::uuid) RETURNING *;

-- name: InsertStockMovement :one
INSERT INTO stock_movements (id,operation_id,line_number,warehouse_id,variant_id,balance_version,
    direction,input_count,pieces_per_input,pieces,before_pieces,after_pieces,product_id,base_unit_id,
    pack_id,pack_revision,product_code,product_name,sku,base_unit_code,pack_label,
    stock_reference_revision,reference_checked_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
RETURNING *;

-- name: ClaimStockIdempotency :execrows
INSERT INTO stock_idempotency (producer,key,fingerprint_version,request_hash,actor_user_id,operation_id,actor_kind,actor_machine_id)
VALUES ($1,$2,$3,$4,$5,$6,COALESCE(NULLIF(sqlc.arg(actor_kind)::text,''),'HUMAN'),sqlc.narg(actor_machine_id)::uuid) ON CONFLICT (producer,key) DO NOTHING;

-- name: GetStockIdempotency :one
SELECT * FROM stock_idempotency WHERE producer=$1 AND key=$2;

-- Only a previously claimed key may transition. Caller checks affected rows = 1.
-- Durable result is computed once from immutable facts; deferred checks validate it.
-- name: CompleteStockIdempotency :execrows
UPDATE stock_idempotency SET state='COMPLETED',result_version=1,
    result=stock_expected_result(operation_id),completed_at=now()
WHERE producer=$1 AND key=$2 AND operation_id=$3 AND state='IN_PROGRESS';

-- name: ListStockBalances :many
SELECT * FROM stock_balances
WHERE (warehouse_id,variant_id)>(sqlc.arg(after_warehouse_id)::uuid,sqlc.arg(after_variant_id)::uuid)
  AND (sqlc.narg(warehouse_filter)::uuid IS NULL OR warehouse_id=sqlc.narg(warehouse_filter)::uuid)
  AND (sqlc.narg(variant_filter)::uuid IS NULL OR variant_id=sqlc.narg(variant_filter)::uuid)
  AND (NOT sqlc.arg(nonzero_only)::boolean OR on_hand_pieces>0)
ORDER BY warehouse_id,variant_id LIMIT LEAST(GREATEST(sqlc.arg(page_size)::int,1),101);

-- name: ListStockHistory :many
SELECT m.*,o.posted_at,o.actor_user_id,o.producer,o.source_event_id
FROM stock_movements m JOIN stock_operations o ON o.id=m.operation_id
WHERE m.warehouse_id=sqlc.arg(warehouse_id) AND m.variant_id=sqlc.arg(variant_id)
  AND m.balance_version<=sqlc.arg(upper_version)::bigint
  AND (sqlc.narg(before_version)::bigint IS NULL OR m.balance_version<sqlc.narg(before_version)::bigint)
ORDER BY m.balance_version DESC LIMIT LEAST(GREATEST(sqlc.arg(page_size)::int,1),101);

-- Live warehouse history: immutable unique movement ID is the ordering key,
-- not per-variant balance_version or transaction/commit time.
-- name: ListWarehouseStockMovements :many
SELECT m.*, o.posted_at,a.reason_code,a.explanation,a.original_operation_id
FROM stock_movements m JOIN stock_operations o ON o.id=m.operation_id
LEFT JOIN stock_operation_audit a ON a.operation_id=o.id
WHERE m.warehouse_id=sqlc.arg(warehouse_id)::uuid
  AND (sqlc.narg(variant_filter)::uuid IS NULL OR m.variant_id=sqlc.narg(variant_filter)::uuid)
  AND (sqlc.narg(before_id)::uuid IS NULL OR m.id<sqlc.narg(before_id)::uuid)
ORDER BY m.id DESC LIMIT LEAST(GREATEST(sqlc.arg(page_size)::int,1),101);

-- name: HasStockMovements :one
SELECT EXISTS(SELECT 1 FROM stock_movements WHERE warehouse_id=$1 AND variant_id=$2);

-- name: InsertStockAudit :exec
INSERT INTO stock_operation_audit(operation_id,reason_code,explanation,original_operation_id) VALUES($1,$2,$3,$4);

-- Original facts are immutable; bounded service calls read only the eligible
-- warehouse header and at most 100 lines. No Catalog call or database lock here.
-- name: GetOriginalStockOperation :one
SELECT * FROM stock_operations WHERE id=$1 AND warehouse_id=$2;

-- name: GetOriginalStockLines :many
SELECT * FROM stock_movements WHERE operation_id=$1 ORDER BY line_number;

-- Atomic own-hold release plus physical issue; never a separate release commit.
-- name: ConsumeReservedStock :one
UPDATE stock_balances SET reserved_pieces=reserved_pieces-sqlc.arg(pieces)::bigint,
on_hand_pieces=on_hand_pieces-sqlc.arg(pieces)::bigint,reservation_version=reservation_version+1,
version=version+1,updated_at=now()
WHERE warehouse_id=sqlc.arg(warehouse_id) AND variant_id=sqlc.arg(variant_id)
AND sqlc.arg(pieces)::bigint>0 AND reserved_pieces>=sqlc.arg(pieces)::bigint AND on_hand_pieces>=sqlc.arg(pieces)::bigint
AND version<9223372036854775807 AND reservation_version<9223372036854775807 RETURNING *;
