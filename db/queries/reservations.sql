-- All mutation queries require the one bounded caller-owned transaction.
-- Claim key, warehouse, order head/reservation, then sorted balance UUIDs.
-- name: ClaimReservationCommand :execrows
INSERT INTO reservation_commands(owner_machine_id,key,command_id,reservation_id,order_id,warehouse_id,generation,action,fingerprint_version,request_hash,actor_user_id,dispatch_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(owner_machine_id,key) DO NOTHING;

-- name: GetReservationCommand :one
SELECT * FROM reservation_commands WHERE owner_machine_id=$1 AND key=$2;

-- name: CompleteReservationCommand :execrows
UPDATE reservation_commands SET state='COMPLETED',result_version=1,result=reservation_expected_result(owner_machine_id,key),completed_at=now()
WHERE owner_machine_id=$1 AND key=$2 AND command_id=$3 AND state='IN_PROGRESS';

-- name: EnsureReservationOrder :exec
INSERT INTO reservation_orders(owner_machine_id,order_id) VALUES($1,$2) ON CONFLICT DO NOTHING;

-- name: LockReservationOrder :one
SELECT h.*, COALESCE((SELECT r.state FROM stock_reservations r WHERE r.id=h.current_reservation_id),'NONE')::text AS current_state
FROM reservation_orders h WHERE h.owner_machine_id=$1 AND h.order_id=$2 FOR UPDATE OF h;

-- name: AdvanceReservationOrder :one
UPDATE reservation_orders SET generation=sqlc.arg(generation),version=version+1,current_reservation_id=sqlc.arg(reservation_id)
WHERE owner_machine_id=sqlc.arg(owner_machine_id) AND order_id=sqlc.arg(order_id)
AND version=sqlc.arg(expected_version) AND version<9223372036854775807 RETURNING *;

-- name: InsertReservation :one
INSERT INTO stock_reservations(id,owner_machine_id,order_id,warehouse_id,generation,state,version,order_version,line_count)
VALUES($1,$2,$3,$4,$5,'ACTIVE',1,$6,$7) RETURNING *;

-- name: GetReservationOwned :one
SELECT * FROM stock_reservations WHERE owner_machine_id=$1 AND id=$2;

-- name: LockReservationOwned :one
SELECT * FROM stock_reservations WHERE owner_machine_id=$1 AND id=$2 FOR UPDATE;

-- name: GetReservationLines :many
SELECT * FROM reservation_lines WHERE reservation_id=$1 ORDER BY line_number;

-- name: InsertReservationLine :exec
INSERT INTO reservation_lines(reservation_id,line_number,order_line_id,variant_id,pieces,product_id,base_unit_id,
product_code,product_name,sku,base_unit_code,stock_reference_revision,reference_checked_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13);

-- name: AddReservedPieces :execrows
UPDATE stock_balances SET reserved_pieces=reserved_pieces+sqlc.arg(pieces)::bigint,reservation_version=reservation_version+1,updated_at=now()
WHERE warehouse_id=sqlc.arg(warehouse_id) AND variant_id=sqlc.arg(variant_id) AND sqlc.arg(pieces)::bigint>0
AND on_hand_pieces-reserved_pieces>=sqlc.arg(pieces)::bigint AND reservation_version<9223372036854775807;

-- name: ReleaseReservedPieces :execrows
UPDATE stock_balances SET reserved_pieces=reserved_pieces-sqlc.arg(pieces)::bigint,reservation_version=reservation_version+1,updated_at=now()
WHERE warehouse_id=sqlc.arg(warehouse_id) AND variant_id=sqlc.arg(variant_id) AND sqlc.arg(pieces)::bigint>0
AND reserved_pieces>=sqlc.arg(pieces)::bigint AND reservation_version<9223372036854775807;

-- name: SetReservationTerminal :one
UPDATE stock_reservations SET state=sqlc.arg(state),version=version+1,order_version=sqlc.arg(order_version),
operation_id=sqlc.narg(operation_id)::uuid,updated_at=now()
WHERE id=sqlc.arg(id) AND owner_machine_id=sqlc.arg(owner_machine_id) AND state='ACTIVE' AND version=sqlc.arg(expected_version) RETURNING *;

-- name: InsertReservationTransition :exec
INSERT INTO reservation_transitions(id,reservation_id,owner_machine_id,order_id,command_id,action,state,reservation_version,order_version,operation_id,first_request_id,actor_user_id,dispatch_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13);

-- Owner-only live keyset listing for explicit abandoned-hold reconciliation.
-- name: ListActiveReservations :many
SELECT * FROM stock_reservations WHERE owner_machine_id=sqlc.arg(owner_machine_id) AND state='ACTIVE'
AND (sqlc.narg(after_id)::uuid IS NULL OR id>sqlc.narg(after_id)::uuid)
ORDER BY id LIMIT LEAST(GREATEST(sqlc.arg(page_size)::int,1),101);
