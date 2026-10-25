package stock

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"time"
)

var (
	ErrReferenceUnavailable = errors.New("stock reference validation unavailable; retry same identities and key")
	ErrReferenceNotFound    = errors.New("stock reference not found")
	ErrReferenceIneligible  = errors.New("stock reference is not piece eligible")
	ErrForbidden            = errors.New("stock producer or actor forbidden")
	ErrKeyConflict          = errors.New("stock idempotency key reused")
	ErrOperationConflict    = errors.New("stock operation identity reused")
	ErrWarehouseNotFound    = errors.New("stock warehouse not found")
	ErrWarehouseInactive    = errors.New("stock warehouse inactive")
	ErrVersionExhausted     = errors.New("stock balance version exhausted")
	ErrBusy                 = errors.New("stock temporarily busy")
	ErrInProgress           = errors.New("stock operation in progress; retry same identities and key")
	ErrOutcomeUnknown       = errors.New("stock commit outcome unknown; retry same identities and key")
	ErrStoredResult         = errors.New("invalid or unsupported durable stock result")
	ErrInternal             = errors.New("stock persistence invariant failure")
	ErrRetryTransaction     = errors.New("stock transaction rolled back; retry permitted")
)

// ProducerAuthorizer must verify trusted identity, current workflow authority and
// the exact producer namespace. UserID alone never establishes these properties.
type ProducerAuthorizer interface {
	Authorize(context.Context, Principal, Command) error
}

// ReferenceProvider returns authenticated coherent observations in production.
// The optional HTTPS adapter supplies this interface; posting needs an explicitly
// authorized workflow and is not registered as a public mutation endpoint.
type ReferenceProvider interface {
	Resolve(context.Context, Command) ([]Observation, error)
}

// Observation carries historical labels separately from caller intent.
type Observation struct {
	ReferenceSnapshot
	ProductCode, ProductName, SKU, BaseUnitCode string
	PackLabel                                   *string
}

// FreshnessPolicy is mandatory and runs outside transactions on every attempt.
// The approved Catalog policy is max age 5s / future allowance 1s.
type FreshnessPolicy interface {
	Check(context.Context, []Observation) error
}

type PreparedLine struct {
	CommandLine
	Observation
	Pieces, Factor Quantity
}

func Prepare(c Command, observations []Observation) ([]PreparedLine, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if len(observations) != len(c.Lines) {
		return nil, ErrReferenceChanged
	}
	byVariant := make(map[[16]byte]Observation, len(observations))
	for _, o := range observations {
		if _, exists := byVariant[o.VariantID]; exists {
			return nil, ErrReferenceChanged
		}
		byVariant[o.VariantID] = o
	}
	lines := make([]PreparedLine, 0, len(c.Lines))
	for _, l := range c.Lines {
		o, ok := byVariant[l.VariantID]
		if !ok {
			return nil, ErrReferenceChanged
		}
		if err := o.ValidateFor(l); err != nil {
			return nil, err
		}
		if !boundedText(o.ProductCode, 50) || !boundedText(o.ProductName, 200) || !boundedText(o.SKU, 100) || !boundedText(o.BaseUnitCode, 30) || (o.Pack == nil && o.PackLabel != nil) {
			return nil, ErrInvalidArgument
		}
		factor := Quantity(1)
		if o.Pack != nil {
			p := *o.Pack
			o.Pack = &p
			factor = p.PiecesPerPack
		}
		if o.PackLabel != nil {
			label := *o.PackLabel
			o.PackLabel = &label
		}
		pieces, err := PackToPieces(l.InputCount, factor)
		if err != nil {
			return nil, err
		}
		lines = append(lines, PreparedLine{l, o, pieces, factor})
	}
	return lines, nil
}

// PostingStore is an internal trusted service boundary. Apply must recheck the
// key inside its transaction, and returns success only after durable confirmation.
type PostingStore interface {
	Lookup(context.Context, Principal, Command, RequestFingerprint) (PostedResult, bool, error)
	Apply(context.Context, Principal, Command, RequestFingerprint, []PreparedLine) (PostedResult, error)
}

type PostingOptions struct {
	TotalTimeout, ReferenceTimeout, TransactionBudget, RetryBackoff time.Duration
	MaxAttempts                                                     int
}

// Defaults are internal execution budgets, not an approved Catalog age or SLA.
func DefaultPostingOptions() PostingOptions {
	return PostingOptions{5 * time.Second, 2 * time.Second, 3 * time.Second, 10 * time.Millisecond, 3}
}

type PostingService struct {
	store      PostingStore
	authorizer ProducerAuthorizer
	references ReferenceProvider
	freshness  FreshnessPolicy
	options    PostingOptions
}

func NewPostingService(store PostingStore, auth ProducerAuthorizer, refs ReferenceProvider, freshness FreshnessPolicy, options PostingOptions) (*PostingService, error) {
	if store == nil || auth == nil || refs == nil || freshness == nil || options.TotalTimeout <= 0 || options.ReferenceTimeout <= 0 || options.TransactionBudget <= 0 || options.RetryBackoff < 0 || options.RetryBackoff > options.TotalTimeout/2 || options.MaxAttempts < 1 || options.MaxAttempts > 3 {
		return nil, ErrInvalidArgument
	}
	return &PostingService{store, auth, refs, freshness, options}, nil
}

func (s *PostingService) replay(ctx context.Context, p Principal, c Command, fp RequestFingerprint) (PostedResult, bool, error) {
	r, found, err := s.store.Lookup(ctx, p, c, fp)
	if err != nil || !found {
		return r, found, err
	}
	if err = s.authorize(ctx, p, c); err != nil {
		return PostedResult{}, false, err
	}
	return r, true, nil
}

func (s *PostingService) authorize(ctx context.Context, p Principal, c Command) error {
	err := s.authorizer.Authorize(ctx, p, c)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return ErrForbidden
	}
	return nil
}

func referenceFailure(err error) error {
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, ErrInvalidArgument, ErrQuantityOverflow, ErrReferenceChanged, ErrReferenceInactive, ErrReferenceNotFound, ErrReferenceIneligible, ErrReferenceUnauthorized, ErrReferenceDenied, ErrReferenceMalformed, ErrReferenceStale} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrReferenceUnavailable
}

// Post performs no logging. The future authenticated workflow boundary owns one
// safe zerolog event with request_id and operation_id after cleanup.
func (s *PostingService) Post(ctx context.Context, p Principal, c Command) (PostedResult, error) {
	ctx, cancel := context.WithTimeout(ctx, s.options.TotalTimeout)
	defer cancel()
	c.Lines = slices.Clone(c.Lines)
	fp, err := c.Fingerprint(p)
	if err != nil {
		return PostedResult{}, err
	}
	if err = s.authorize(ctx, p, c); err != nil {
		return PostedResult{}, err
	}
	if r, found, err := s.replay(ctx, p, c, fp); err != nil || found {
		return r, err
	}
	remaining := s.options.TransactionBudget
	for attempt := 0; attempt < s.options.MaxAttempts; attempt++ {
		refctx, stop := context.WithTimeout(ctx, s.options.ReferenceTimeout)
		observations, err := s.references.Resolve(refctx, c)
		var lines []PreparedLine
		if err == nil {
			lines, err = Prepare(c, observations)
		}
		if err == nil {
			err = s.freshness.Check(refctx, observations)
		}
		if refctx.Err() != nil {
			err = refctx.Err()
		}
		stop()
		if err != nil {
			// A peer may have committed while this now-redundant lookup failed.
			if r, found, e := s.replay(ctx, p, c, fp); e != nil || found {
				return r, e
			}
			return PostedResult{}, referenceFailure(err)
		}
		if err = ctx.Err(); err != nil {
			return PostedResult{}, err
		}
		if err = s.authorize(ctx, p, c); err != nil {
			return PostedResult{}, err
		}
		if remaining <= 0 {
			return PostedResult{}, context.DeadlineExceeded
		}
		// Authorization/preparation can consume time after response acceptance.
		// Recheck immediately before starting each new mutation attempt.
		if err = s.freshness.Check(ctx, observations); err != nil {
			return PostedResult{}, referenceFailure(err)
		}
		txctx, stop := context.WithTimeout(ctx, remaining)
		start := time.Now()
		r, err := s.store.Apply(txctx, p, c, fp, lines)
		remaining -= time.Since(start)
		stop()
		if err == nil {
			if r.Replayed {
				if e := s.authorize(ctx, p, c); e != nil {
					return PostedResult{}, e
				}
			}
			return r, nil
		}
		if !errors.Is(err, ErrRetryTransaction) {
			return PostedResult{}, err
		}
		if attempt+1 == s.options.MaxAttempts {
			return PostedResult{}, ErrBusy
		}
		// Only known rolled-back transactions get another effect attempt. Fresh
		// observations are reacquired, outside locks, on each iteration.
		delay := s.options.RetryBackoff
		if delay > 0 {
			delay += time.Duration(rand.Int64N(int64(delay)))
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return PostedResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	return PostedResult{}, ErrBusy
}
