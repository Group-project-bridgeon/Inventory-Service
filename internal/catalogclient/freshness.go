package catalogclient

import (
	"context"
	"github.com/Group-project-bridgeon/Inventory-Service/internal/stock"
	"time"
)

const MaximumObservationAge = 5 * time.Second
const MaximumFutureAllowance = time.Second

var ErrStale = stock.ErrReferenceStale

type Freshness struct{ Now func() time.Time }

func (f Freshness) Check(ctx context.Context, observations []stock.Observation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := time.Now()
	if f.Now != nil {
		now = f.Now()
	}
	if len(observations) == 0 {
		return ErrMalformed
	}
	for _, o := range observations {
		if o.CheckedAt.IsZero() || now.Sub(o.CheckedAt) > MaximumObservationAge || o.CheckedAt.Sub(now) > MaximumFutureAllowance {
			return ErrStale
		}
	}
	return nil
}
