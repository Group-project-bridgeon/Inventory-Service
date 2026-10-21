package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/repository"
	"github.com/google/uuid"
)

const (
	DefaultPageLimit = 50
	MaxPageLimit     = 100
	maxNameLength    = 100
	maxAddressLength = 500
	maxRawTextLength = 10000
	maxCursorLength  = 512
)

var codePattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,49}$`)
var rawCodePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,49}$`)

type CreateWarehouseInput struct{ Code, Name, Address string }
type UpdateWarehouseInput struct {
	Name, Address string
	IsDefault     *bool
}
type PageInput struct {
	Limit  int
	Cursor string
}
type WarehousePage struct {
	Items      []domain.Warehouse
	NextCursor *string
}
type warehouseCursor struct {
	Code string    `json:"code"`
	ID   uuid.UUID `json:"id"`
}
type WarehouseService struct {
	repo repository.WarehouseRepository
}

func NewWarehouseService(repo repository.WarehouseRepository) *WarehouseService {
	return &WarehouseService{repo: repo}
}

func validText(value string, min, max int) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < min || utf8.RuneCountInString(value) > max {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func details(name, address string) (string, string, error) {
	// Reject controls before trimming so embedded/newline controls cannot be hidden.
	if !validText(name, 0, maxRawTextLength) || !validText(address, 0, maxRawTextLength) {
		return "", "", domain.ErrInvalidArgument
	}
	name, address = strings.TrimSpace(name), strings.TrimSpace(address)
	if !validText(name, 1, maxNameLength) || !validText(address, 0, maxAddressLength) {
		return "", "", domain.ErrInvalidArgument
	}
	return name, address, nil
}
func (s *WarehouseService) Create(ctx context.Context, in CreateWarehouseInput) (domain.Warehouse, error) {
	name, address, err := details(in.Name, in.Address)
	trimmedCode := strings.TrimSpace(in.Code)
	code := strings.ToUpper(trimmedCode)
	if err != nil || !rawCodePattern.MatchString(trimmedCode) || !codePattern.MatchString(code) {
		return domain.Warehouse{}, domain.ErrInvalidArgument
	}
	id, err := uuid.NewV7()
	if err != nil {
		return domain.Warehouse{}, err
	}
	return s.repo.Create(ctx, repository.CreateWarehouseParams{ID: id, Code: code, Name: name, Address: address})
}
func (s *WarehouseService) GetByID(ctx context.Context, id uuid.UUID) (domain.Warehouse, error) {
	if id == uuid.Nil {
		return domain.Warehouse{}, domain.ErrInvalidArgument
	}
	return s.repo.GetByID(ctx, id)
}
func (s *WarehouseService) Update(ctx context.Context, id uuid.UUID, in UpdateWarehouseInput) (domain.Warehouse, error) {
	name, address, err := details(in.Name, in.Address)
	if err != nil || id == uuid.Nil {
		return domain.Warehouse{}, domain.ErrInvalidArgument
	}
	return s.repo.Update(ctx, repository.UpdateWarehouseParams{ID: id, Name: name, Address: address, IsDefault: in.IsDefault})
}
func (s *WarehouseService) SetActive(ctx context.Context, id uuid.UUID, active bool) (domain.Warehouse, error) {
	if id == uuid.Nil {
		return domain.Warehouse{}, domain.ErrInvalidArgument
	}
	return s.repo.SetActive(ctx, id, active)
}
func (s *WarehouseService) List(ctx context.Context, in PageInput) (WarehousePage, error) {
	if in.Limit == 0 {
		in.Limit = DefaultPageLimit
	}
	if in.Limit < 1 || in.Limit > MaxPageLimit || len(in.Cursor) > maxCursorLength {
		return WarehousePage{}, domain.ErrInvalidArgument
	}
	var cursor warehouseCursor
	if in.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(in.Cursor)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.ID == uuid.Nil || !codePattern.MatchString(cursor.Code) {
			return WarehousePage{}, domain.ErrInvalidArgument
		}
	}
	items, err := s.repo.List(ctx, repository.ListWarehousesParams{AfterCode: cursor.Code, AfterID: cursor.ID, PageSize: int32(in.Limit + 1)})
	if err != nil {
		return WarehousePage{}, err
	}
	if items == nil {
		items = make([]domain.Warehouse, 0)
	}
	page := WarehousePage{Items: items}
	if len(items) > in.Limit {
		page.Items = items[:in.Limit]
		last := page.Items[in.Limit-1]
		data, err := json.Marshal(warehouseCursor{Code: last.Code, ID: last.ID})
		if err != nil {
			return WarehousePage{}, err
		}
		next := base64.RawURLEncoding.EncodeToString(data)
		page.NextCursor = &next
	}
	return page, nil
}
