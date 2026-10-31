-- name: GetDispatch :one
SELECT * FROM dispatches WHERE id=sqlc.arg(id) AND(sqlc.narg(owner)::uuid IS NULL OR owner_machine_id=sqlc.narg(owner)::uuid);
-- name: LockDispatch :one
SELECT * FROM dispatches WHERE id=$1 FOR UPDATE;
-- name: HasLiveDispatch :one
SELECT EXISTS(SELECT 1 FROM dispatches WHERE reservation_id=$1 AND state<>'CANCELLED');
-- name: InsertDispatch :one
INSERT INTO dispatches(id,owner_machine_id,reservation_id,order_id,warehouse_id,generation,state,version,external_reference)
VALUES($1,$2,$3,$4,$5,$6,'DRAFT',1,$7) RETURNING *;
-- name: ChangeDispatch :one
UPDATE dispatches SET state=sqlc.arg(state),version=version+1,operation_id=sqlc.narg(operation_id)::uuid,updated_at=now()
WHERE id=sqlc.arg(id) AND version=sqlc.arg(expected_version) AND version<9223372036854775807 RETURNING *;
-- name: ClaimFulfillment :execrows
INSERT INTO fulfillment_commands(actor_kind,actor_id,key,command_id,dispatch_id,owner_machine_id,action,fingerprint_version,request_hash)
VALUES($1,$2,$3,$4,$5,$6,$7,1,$8) ON CONFLICT(actor_kind,actor_id,key) DO NOTHING;
-- name: GetFulfillmentCommand :one
SELECT * FROM fulfillment_commands WHERE actor_kind=$1 AND actor_id=$2 AND key=$3;
-- name: CompleteFulfillment :execrows
UPDATE fulfillment_commands SET state='COMPLETED',result_version=1,result=fulfillment_expected_result(actor_kind,actor_id,key),completed_at=now()
WHERE actor_kind=$1 AND actor_id=$2 AND key=$3 AND state='IN_PROGRESS';
-- name: InsertFulfillmentTransition :exec
INSERT INTO fulfillment_transitions(id,dispatch_id,command_id,actor_kind,actor_id,action,state,version,operation_id,outcome,external_reference,note,first_request_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13);
-- name: ListDispatches :many
SELECT * FROM dispatches WHERE
(sqlc.narg(owner)::uuid IS NULL OR owner_machine_id=sqlc.narg(owner)::uuid)
AND(sqlc.narg(state)::text IS NULL OR state=sqlc.narg(state)::text)
AND(sqlc.narg(warehouse)::uuid IS NULL OR warehouse_id=sqlc.narg(warehouse)::uuid)
AND(sqlc.narg(order_id)::uuid IS NULL OR order_id=sqlc.narg(order_id)::uuid)
AND(sqlc.narg(reservation)::uuid IS NULL OR reservation_id=sqlc.narg(reservation)::uuid)
AND(sqlc.narg(before_id)::uuid IS NULL OR id<sqlc.narg(before_id)::uuid)
ORDER BY id DESC LIMIT LEAST(GREATEST(sqlc.arg(page_size)::int,1),101);
-- name: ListDispatchTransitions :many
SELECT t.* FROM fulfillment_transitions t JOIN dispatches d ON d.id=t.dispatch_id
WHERE t.dispatch_id=sqlc.arg(dispatch_id) AND(sqlc.narg(owner)::uuid IS NULL OR d.owner_machine_id=sqlc.narg(owner)::uuid)
AND(sqlc.narg(before_id)::uuid IS NULL OR t.id<sqlc.narg(before_id)::uuid)
ORDER BY t.id DESC LIMIT LEAST(GREATEST(sqlc.arg(page_size)::int,1),101);
