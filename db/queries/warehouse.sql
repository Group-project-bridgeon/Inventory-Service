-- name: LockWarehouses :exec
SELECT pg_advisory_xact_lock(782341092);

-- name: HasWarehouses :one
SELECT EXISTS(SELECT 1 FROM warehouses);

-- name: CreateWarehouse :one
INSERT INTO warehouses (id, code, name, address, is_default)
VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- name: GetWarehouse :one
SELECT * FROM warehouses WHERE id = $1;

-- name: ListWarehouses :many
SELECT * FROM warehouses
WHERE (code, id) > (sqlc.arg(after_code)::text, sqlc.arg(after_id)::uuid)
ORDER BY code, id LIMIT sqlc.arg(page_size);

-- name: ClearDefaultWarehouse :exec
UPDATE warehouses SET is_default = false, updated_at = now() WHERE is_default;

-- name: UpdateWarehouse :one
UPDATE warehouses SET name = $2, address = $3, is_default = $4, updated_at = now()
WHERE id = $1 RETURNING *;

-- name: SetWarehouseStatus :one
UPDATE warehouses SET is_active = $2, updated_at = now() WHERE id = $1 RETURNING *;
