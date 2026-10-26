package postgres

import (
	"context"
	"errors"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository/postgres/sqlcdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type WarehouseRepository struct{ pool *pgxpool.Pool }

func NewWarehouseRepository(pool *pgxpool.Pool) *WarehouseRepository {
	return &WarehouseRepository{pool: pool}
}
func dbID(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }
func fromDB(w sqlcdb.Warehouse) domain.Warehouse {
	return domain.Warehouse{ID: uuid.UUID(w.ID.Bytes), Code: w.Code, Name: w.Name, Address: w.Address, IsActive: w.IsActive, IsDefault: w.IsDefault, CreatedAt: w.CreatedAt.Time, UpdatedAt: w.UpdatedAt.Time}
}
func mapErr(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch {
		case pg.Code == "23505" && pg.ConstraintName == "warehouses_code_unique":
			return domain.ErrDuplicateCode
		case pg.Code == "23505" && pg.ConstraintName == "warehouses_one_default",
			pg.Code == "23514" && pg.ConstraintName == "warehouses_exactly_one_default":
			return domain.ErrDefaultConflict
		case pg.Code == "23514" && pg.ConstraintName == "warehouses_default_active":
			return domain.ErrInactiveDefault
		case pg.Code == "23514" && (pg.ConstraintName == "warehouses_code_format" ||
			pg.ConstraintName == "warehouses_name_valid" || pg.ConstraintName == "warehouses_address_valid"):
			return domain.ErrInvalidArgument
		}
	}
	return logger.Classify("postgres", op, err)
}
func (r *WarehouseRepository) Create(ctx context.Context, p repository.CreateWarehouseParams) (domain.Warehouse, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Warehouse{}, mapErr("create_warehouse", err)
	}
	defer tx.Rollback(ctx)
	q := sqlcdb.New(tx)
	if err = q.LockWarehouses(ctx); err != nil {
		return domain.Warehouse{}, mapErr("lock_warehouses", err)
	}
	has, err := q.HasWarehouses(ctx)
	if err != nil {
		return domain.Warehouse{}, mapErr("check_warehouses", err)
	}
	w, err := q.CreateWarehouse(ctx, sqlcdb.CreateWarehouseParams{ID: dbID(p.ID), Code: p.Code, Name: p.Name, Address: p.Address, IsDefault: !has})
	if err != nil {
		return domain.Warehouse{}, mapErr("create_warehouse", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Warehouse{}, mapErr("create_warehouse", err)
	}
	return fromDB(w), nil
}
func (r *WarehouseRepository) GetByID(ctx context.Context, id uuid.UUID) (domain.Warehouse, error) {
	w, err := sqlcdb.New(r.pool).GetWarehouse(ctx, dbID(id))
	if err != nil {
		return domain.Warehouse{}, mapErr("get_warehouse", err)
	}
	return fromDB(w), nil
}
func (r *WarehouseRepository) List(ctx context.Context, p repository.ListWarehousesParams) ([]domain.Warehouse, error) {
	rows, err := sqlcdb.New(r.pool).ListWarehouses(ctx, sqlcdb.ListWarehousesParams{AfterCode: p.AfterCode, AfterID: dbID(p.AfterID), PageSize: p.PageSize})
	if err != nil {
		return nil, mapErr("list_warehouses", err)
	}
	out := make([]domain.Warehouse, 0, len(rows))
	for _, w := range rows {
		out = append(out, fromDB(w))
	}
	return out, nil
}
func (r *WarehouseRepository) Update(ctx context.Context, p repository.UpdateWarehouseParams) (domain.Warehouse, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Warehouse{}, mapErr("update_warehouse", err)
	}
	defer tx.Rollback(ctx)
	q := sqlcdb.New(tx)
	if err = q.LockWarehouses(ctx); err != nil {
		return domain.Warehouse{}, mapErr("lock_warehouses", err)
	}
	current, err := q.GetWarehouse(ctx, dbID(p.ID))
	if err != nil {
		return domain.Warehouse{}, mapErr("get_warehouse", err)
	}
	desired := current.IsDefault
	if p.IsDefault != nil {
		desired = *p.IsDefault
	}
	if !desired && current.IsDefault {
		return domain.Warehouse{}, domain.ErrDefaultConflict
	}
	if desired && !current.IsActive {
		return domain.Warehouse{}, domain.ErrInactiveDefault
	}
	if desired && !current.IsDefault {
		if err = q.ClearDefaultWarehouse(ctx); err != nil {
			return domain.Warehouse{}, mapErr("clear_default_warehouse", err)
		}
	}
	w, err := q.UpdateWarehouse(ctx, sqlcdb.UpdateWarehouseParams{ID: dbID(p.ID), Name: p.Name, Address: p.Address, IsDefault: desired})
	if err != nil {
		return domain.Warehouse{}, mapErr("update_warehouse", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Warehouse{}, mapErr("update_warehouse", err)
	}
	return fromDB(w), nil
}
func (r *WarehouseRepository) SetActive(ctx context.Context, id uuid.UUID, active bool) (domain.Warehouse, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Warehouse{}, mapErr("set_warehouse_status", err)
	}
	defer tx.Rollback(ctx)
	q := sqlcdb.New(tx)
	if err = q.LockWarehouses(ctx); err != nil {
		return domain.Warehouse{}, mapErr("lock_warehouses", err)
	}
	current, err := q.LockWarehouseStatus(ctx, dbID(id))
	if err != nil {
		return domain.Warehouse{}, mapErr("get_warehouse", err)
	}
	if current.IsDefault && !active {
		return domain.Warehouse{}, domain.ErrDefaultConflict
	}
	if !active {
		positive, e := q.WarehouseHasStock(ctx, dbID(id))
		if e != nil {
			return domain.Warehouse{}, mapErr("warehouse_stock", e)
		}
		if positive {
			return domain.Warehouse{}, domain.ErrWarehouseHasStock
		}
	}
	w, err := q.SetWarehouseStatus(ctx, sqlcdb.SetWarehouseStatusParams{ID: dbID(id), IsActive: active})
	if err != nil {
		return domain.Warehouse{}, mapErr("set_warehouse_status", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Warehouse{}, mapErr("set_warehouse_status", err)
	}
	return fromDB(w), nil
}

var _ repository.WarehouseRepository = (*WarehouseRepository)(nil)
