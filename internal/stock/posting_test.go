package stock

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type testAuth func(context.Context, Principal, Command) error

func (f testAuth) Authorize(ctx context.Context, p Principal, c Command) error { return f(ctx, p, c) }

type testRefs func(context.Context, Command) ([]Observation, error)

func (f testRefs) Resolve(ctx context.Context, c Command) ([]Observation, error) { return f(ctx, c) }

type testFresh func(context.Context, []Observation) error

func (f testFresh) Check(ctx context.Context, o []Observation) error { return f(ctx, o) }

type testStore struct {
	lookup func(context.Context, Principal, Command, RequestFingerprint) (PostedResult, bool, error)
	apply  func(context.Context, Principal, Command, RequestFingerprint, []PreparedLine) (PostedResult, error)
}

func (s testStore) Lookup(ctx context.Context, p Principal, c Command, f RequestFingerprint) (PostedResult, bool, error) {
	return s.lookup(ctx, p, c, f)
}
func (s testStore) Apply(ctx context.Context, p Principal, c Command, f RequestFingerprint, l []PreparedLine) (PostedResult, error) {
	return s.apply(ctx, p, c, f, l)
}
func syntheticObservation() Observation {
	return Observation{ReferenceSnapshot: testReference(), ProductCode: "P", ProductName: "Synthetic", SKU: "SKU", BaseUnitCode: "PC"}
}

func TestPostingServiceBoundaries(t *testing.T) {
	for _, mode := range []string{"completed", "reference failure replay", "forbidden", "retry", "retry exhausted", "no retry business", "no retry unknown", "freshness rejection", "late freshness rejection", "cancellation"} {
		t.Run(mode, func(t *testing.T) {
			lookups, refs, attempts, auths, fresh := 0, 0, 0, 0, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := testStore{
				lookup: func(context.Context, Principal, Command, RequestFingerprint) (PostedResult, bool, error) {
					lookups++
					found := mode == "completed" || (mode == "reference failure replay" && lookups == 2)
					return PostedResult{Replayed: found}, found, nil
				},
				apply: func(_ context.Context, _ Principal, c Command, f RequestFingerprint, l []PreparedLine) (PostedResult, error) {
					attempts++
					if l[0].Pieces != 12 || l[0].Factor != 6 {
						return PostedResult{}, ErrInternal
					}
					want, _ := testCommand().Fingerprint(testPrincipal())
					if f != want || c.IdempotencyKey != testCommand().IdempotencyKey {
						return PostedResult{}, ErrInternal
					}
					switch mode {
					case "retry":
						if attempts < 3 {
							return PostedResult{}, ErrRetryTransaction
						}
					case "retry exhausted":
						return PostedResult{}, ErrRetryTransaction
					case "no retry business":
						return PostedResult{}, ErrInsufficientStock
					case "no retry unknown":
						return PostedResult{}, ErrOutcomeUnknown
					}
					return PostedResult{}, nil
				},
			}
			s, err := NewPostingService(store, testAuth(func(context.Context, Principal, Command) error {
				auths++
				if mode == "forbidden" {
					return ErrForbidden
				}
				return nil
			}), testRefs(func(context.Context, Command) ([]Observation, error) {
				refs++
				if mode == "reference failure replay" {
					return nil, ErrReferenceInactive
				}
				if mode == "cancellation" {
					cancel()
				}
				return []Observation{syntheticObservation()}, nil
			}), testFresh(func(context.Context, []Observation) error {
				fresh++
				if mode == "late freshness rejection" && fresh == 2 {
					return ErrReferenceStale
				}
				if mode == "freshness rejection" {
					return ErrReferenceChanged
				}
				return nil
			}), PostingOptions{time.Second, time.Second, time.Second, 0, 3})
			if err != nil {
				t.Fatal(err)
			}
			r, err := s.Post(ctx, testPrincipal(), testCommand())
			want := map[string]error{"forbidden": ErrForbidden, "retry exhausted": ErrBusy, "no retry business": ErrInsufficientStock, "no retry unknown": ErrOutcomeUnknown, "freshness rejection": ErrReferenceChanged, "late freshness rejection": ErrReferenceStale, "cancellation": context.Canceled}[mode]
			if !errors.Is(err, want) {
				t.Fatalf("got %v want %v", err, want)
			}
			if (mode == "completed" || mode == "reference failure replay") && !r.Replayed {
				t.Fatal("lost replay")
			}
			if mode == "completed" && (refs != 0 || attempts != 0 || auths != 2) {
				t.Fatal("completed replay boundary")
			}
			if mode == "reference failure replay" && (lookups != 2 || attempts != 0) {
				t.Fatal("failed reference replay boundary")
			}
			if mode == "forbidden" && (lookups != 0 || refs != 0 || attempts != 0) {
				t.Fatal("authorization bypass")
			}
			if mode == "late freshness rejection" && attempts != 0 {
				t.Fatal("stale observations reached mutation")
			}
			if mode == "retry" && (refs != 3 || attempts != 3 || fresh != 6) {
				t.Fatal("retry must reacquire observations")
			}
			if strings.HasPrefix(mode, "no retry") && attempts != 1 {
				t.Fatal("unsafe retry")
			}
		})
	}
}

func TestPrepareObservations(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Observation)
		want error
	}{
		{"inactive", func(o *Observation) { o.ProductActive = false }, ErrReferenceInactive},
		{"revision", func(o *Observation) { o.StockReferenceRevision = "other" }, ErrReferenceChanged},
		{"labels", func(o *Observation) { o.SKU = "" }, ErrInvalidArgument},
		{"pack", func(o *Observation) { o.Pack = nil }, ErrReferenceChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := syntheticObservation()
			tc.edit(&o)
			_, err := Prepare(testCommand(), []Observation{o})
			if !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
	if _, err := Prepare(testCommand(), nil); !errors.Is(err, ErrReferenceChanged) {
		t.Fatal("missing reference")
	}
	if _, err := NewPostingService(nil, nil, nil, nil, DefaultPostingOptions()); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal("nil dependencies accepted")
	}
}

func TestDecodeDurableResult(t *testing.T) {
	c := testCommand()
	wire := map[string]any{"operation_id": c.OperationID, "result_version": "1", "posted_at": "2026-10-23T00:00:00.000000Z", "lines": []map[string]any{{"line_number": 1, "variant_id": c.Lines[0].VariantID, "movement_id": c.OperationID, "after_pieces": "9007199254740993", "balance_version": "9223372036854775807"}}}
	data, _ := json.Marshal(wire)
	r, err := DecodePostedResult(data, 1, c)
	if err != nil || r.Lines[0].AfterPieces != 9007199254740993 || !r.Replayed {
		t.Fatal("valid exact durable result")
	}
	for _, bad := range []string{strings.Replace(string(data), `"result_version":"1"`, `"result_version":"2"`, 1), strings.Replace(string(data), `"after_pieces":"9007199254740993"`, `"after_pieces":9007199254740993`, 1), strings.Replace(string(data), `"balance_version":"9223372036854775807"`, `"balance_version":"0"`, 1), strings.Replace(string(data), `"after_pieces":"9007199254740993"`, `"after_pieces":null`, 1), string(data) + ` {}`, `{}`, strings.Replace(string(data), `"lines":`, `"unknown":1,"lines":`, 1)} {
		if _, err := DecodePostedResult([]byte(bad), 1, c); !errors.Is(err, ErrStoredResult) {
			t.Fatal("bad durable result accepted")
		}
	}
	if _, err := DecodePostedResult(data, 2, c); !errors.Is(err, ErrStoredResult) {
		t.Fatal("unknown version accepted")
	}
}

func TestPostingReplayReauthorizationAndSafeDependencyErrors(t *testing.T) {
	for _, mode := range []string{"revoked replay", "unsafe dependency", "canceled authorization"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			auths := 0
			store := testStore{lookup: func(context.Context, Principal, Command, RequestFingerprint) (PostedResult, bool, error) {
				return PostedResult{Replayed: mode == "revoked replay"}, mode == "revoked replay", nil
			}, apply: func(context.Context, Principal, Command, RequestFingerprint, []PreparedLine) (PostedResult, error) {
				return PostedResult{}, ErrInternal
			}}
			s, e := NewPostingService(store, testAuth(func(context.Context, Principal, Command) error {
				auths++
				if mode == "canceled authorization" {
					cancel()
				}
				if mode == "revoked replay" && auths > 1 {
					return ErrForbidden
				}
				return nil
			}), testRefs(func(context.Context, Command) ([]Observation, error) {
				return nil, errors.New("synthetic confidential transport text")
			}), testFresh(func(context.Context, []Observation) error { return nil }), DefaultPostingOptions())
			if e != nil {
				t.Fatal(e)
			}
			_, e = s.Post(ctx, testPrincipal(), testCommand())
			want := ErrReferenceUnavailable
			if mode == "revoked replay" {
				want = ErrForbidden
			}
			if mode == "canceled authorization" {
				want = context.Canceled
			}
			if !errors.Is(e, want) || strings.Contains(e.Error(), "confidential") {
				t.Fatal("authorization or safe error boundary")
			}
		})
	}
}

func TestPostingPreservesUncertainCommitContextErrors(t *testing.T) {
	for _, cause := range []error{nil, context.DeadlineExceeded, context.Canceled} {
		name := "live context"
		if cause != nil {
			name = cause.Error()
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := errors.Join(ErrOutcomeUnknown, cause)
			applies, lookups, references := 0, 0, 0
			store := testStore{
				lookup: func(context.Context, Principal, Command, RequestFingerprint) (PostedResult, bool, error) {
					lookups++
					return PostedResult{}, false, nil
				},
				apply: func(context.Context, Principal, Command, RequestFingerprint, []PreparedLine) (PostedResult, error) {
					applies++
					if cause == context.Canceled {
						cancel()
					}
					return PostedResult{}, want
				},
			}
			s, err := NewPostingService(store, testAuth(func(context.Context, Principal, Command) error { return nil }), testRefs(func(context.Context, Command) ([]Observation, error) {
				references++
				return []Observation{syntheticObservation()}, nil
			}), testFresh(func(context.Context, []Observation) error { return nil }), DefaultPostingOptions())
			if err != nil {
				t.Fatal(err)
			}
			r, err := s.Post(ctx, testPrincipal(), testCommand())
			if err != want || !errors.Is(err, ErrOutcomeUnknown) || applies != 1 || lookups != 1 || references != 1 || !r.PostedAt.IsZero() {
				t.Fatal("service masked uncertainty, retried effect or disclosed a partial result")
			}
			if cause != nil && !errors.Is(err, cause) {
				t.Fatal("service dropped uncertainty cause")
			}
		})
	}
}
