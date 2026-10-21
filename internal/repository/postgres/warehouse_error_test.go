package postgres

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Group-project-bridgeon/Inventory-Service/internal/domain"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/platform/logger"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestWarehouseConstraintMapping(t *testing.T) {
	for _, tc := range []struct {
		code, constraint string
		want             error
	}{
		{"23505", "warehouses_code_unique", domain.ErrDuplicateCode},
		{"23505", "warehouses_one_default", domain.ErrDefaultConflict},
		{"23514", "warehouses_exactly_one_default", domain.ErrDefaultConflict},
		{"23514", "warehouses_default_active", domain.ErrInactiveDefault},
		{"23514", "warehouses_code_format", domain.ErrInvalidArgument},
		{"23514", "warehouses_name_valid", domain.ErrInvalidArgument},
		{"23514", "warehouses_address_valid", domain.ErrInvalidArgument},
		{"23514", "warehouses_unknown", nil},
		{"23505", "warehouses_pkey", nil},
		{"23505", "warehouses_name_valid", nil},
		{"23514", "warehouses_code_unique", nil},
		{"23502", "", nil},
	} {
		for _, wrapped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%s/wrapped=%t", tc.code, tc.constraint, wrapped), func(t *testing.T) {
				pg := &pgconn.PgError{Code: tc.code, ConstraintName: tc.constraint, Message: "SECRET-CANARY", Detail: "SECRET-CANARY", Hint: "SECRET-CANARY"}
				var input error = pg
				if wrapped {
					input = fmt.Errorf("wrapped: %w", input)
				}
				got := mapErr("commit_warehouse", input)
				if tc.want != nil {
					if !errors.Is(got, tc.want) {
						t.Fatalf("incorrect business mapping: %T", got)
					}
				} else {
					var f *logger.Failure
					if !errors.As(got, &f) || f.SQLState != tc.code || f.Operation != "commit_warehouse" || f.Reason != "postgres_server_error" {
						t.Fatal("missing safe diagnostics")
					}
					if !errors.Is(got, pg) {
						t.Fatal("lost typed cause")
					}
				}
				if strings.Contains(got.Error(), "SECRET-CANARY") {
					t.Fatal("raw database detail escaped")
				}
			})
		}
	}
}
