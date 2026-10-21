package domain

import (
	"github.com/google/uuid"
	"time"
)

type Warehouse struct {
	ID                   uuid.UUID
	Code, Name, Address  string
	IsActive, IsDefault  bool
	CreatedAt, UpdatedAt time.Time
}
