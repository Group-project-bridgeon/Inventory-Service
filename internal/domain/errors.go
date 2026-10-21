package domain

import "errors"

var (
	ErrInvalidArgument = errors.New("invalid warehouse input")
	ErrNotFound        = errors.New("warehouse not found")
	ErrDuplicateCode   = errors.New("warehouse code already exists")
	ErrDefaultConflict = errors.New("select another active default warehouse first")
	ErrInactiveDefault = errors.New("default warehouse must be active")
)
