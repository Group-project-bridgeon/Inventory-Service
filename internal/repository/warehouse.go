package repository

import (
	"context"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"github.com/google/uuid"
)

type CreateWarehouseParams struct {
	ID                  uuid.UUID
	Code, Name, Address string
}
type UpdateWarehouseParams struct {
	ID            uuid.UUID
	Name, Address string
	IsDefault     *bool
}
type ListWarehousesParams struct {
	AfterCode string
	AfterID   uuid.UUID
	PageSize  int32
}
type WarehouseRepository interface {
	Create(context.Context, CreateWarehouseParams) (domain.Warehouse, error)
	GetByID(context.Context, uuid.UUID) (domain.Warehouse, error)
	List(context.Context, ListWarehousesParams) ([]domain.Warehouse, error)
	Update(context.Context, UpdateWarehouseParams) (domain.Warehouse, error)
	SetActive(context.Context, uuid.UUID, bool) (domain.Warehouse, error)
}
