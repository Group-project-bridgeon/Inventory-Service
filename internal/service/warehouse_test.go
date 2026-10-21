package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository"
	"github.com/google/uuid"
)

type warehouseRepo struct {
	rows    []domain.Warehouse
	created repository.CreateWarehouseParams
	listed  repository.ListWarehousesParams
	err     error
}

func (r *warehouseRepo) Create(_ context.Context, p repository.CreateWarehouseParams) (domain.Warehouse, error) {
	r.created = p
	if r.err != nil {
		return domain.Warehouse{}, r.err
	}
	return domain.Warehouse{ID: p.ID, Code: p.Code, Name: p.Name, Address: p.Address, IsActive: true}, nil
}
func (r *warehouseRepo) GetByID(context.Context, uuid.UUID) (domain.Warehouse, error) {
	return domain.Warehouse{}, r.err
}
func (r *warehouseRepo) List(_ context.Context, p repository.ListWarehousesParams) ([]domain.Warehouse, error) {
	r.listed = p
	return r.rows, r.err
}
func (r *warehouseRepo) Update(context.Context, repository.UpdateWarehouseParams) (domain.Warehouse, error) {
	return domain.Warehouse{}, r.err
}
func (r *warehouseRepo) SetActive(context.Context, uuid.UUID, bool) (domain.Warehouse, error) {
	return domain.Warehouse{}, r.err
}

func TestCreateNormalizesAndValidates(t *testing.T) {
	r := &warehouseRepo{}
	s := NewWarehouseService(r)
	w, err := s.Create(context.Background(), CreateWarehouseInput{Code: "  wh-01 ", Name: " Main ", Address: " Street 1 "})
	if err != nil || w.Code != "WH-01" || w.Name != "Main" || w.Address != "Street 1" || w.ID == uuid.Nil {
		t.Fatalf("warehouse=%+v err=%v", w, err)
	}
	for _, in := range []CreateWarehouseInput{{Code: "!bad", Name: "x"}, {Code: "é", Name: "x"}, {Code: "A", Name: " "}, {Code: "A", Name: strings.Repeat("x", 101)}, {Code: "A", Name: "x\ny"}, {Code: "A", Name: "x", Address: strings.Repeat("a", 501)}} {
		if _, err := s.Create(context.Background(), in); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("accepted invalid input %+v: %v", in, err)
		}
	}
}

func TestListBoundsAndCursor(t *testing.T) {
	r := &warehouseRepo{rows: make([]domain.Warehouse, 3)}
	s := NewWarehouseService(r)
	for i := range r.rows {
		r.rows[i] = domain.Warehouse{ID: uuid.New(), Code: string(rune('A' + i))}
	}
	if _, err := s.List(context.Background(), PageInput{Limit: 101}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatal("accepted oversized page")
	}
	if _, err := s.List(context.Background(), PageInput{Limit: 2, Cursor: "bad"}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatal("accepted malformed cursor")
	}
	p, err := s.List(context.Background(), PageInput{Limit: 2})
	if err != nil || len(p.Items) != 2 || p.NextCursor == nil || r.listed.PageSize != 3 {
		t.Fatalf("page=%+v params=%+v err=%v", p, r.listed, err)
	}
	if _, err = s.List(context.Background(), PageInput{Limit: 2, Cursor: *p.NextCursor}); err != nil || r.listed.AfterCode != "B" {
		t.Fatalf("cursor not propagated: %+v %v", r.listed, err)
	}
}
