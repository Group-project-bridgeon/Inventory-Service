package stock

import (
	"encoding/hex"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFingerprintGolden(t *testing.T) {
	c, principal := testCommand(), testPrincipal()
	// Literal canonical bytes independently specify v1, including text lengths,
	// UUID byte order, positive count and pack presence. Do not regenerate on drift.
	const canonicalHex = "696e76656e746f72792e73746f636b2e7265717565737400" +
		"00010001" + "01935f00000070008000000000000005" +
		"0000000e73796e7468657469632d74657374" +
		"01935f00000070008000000000000008" + "01935f00000070008000000000000007" +
		"01935f00000070008000000000000001" + "00000002494e" + "0001" +
		"0001" + "01935f00000070008000000000000002" + "0000000000000002" + "01" +
		"01935f00000070008000000000000004" + "000000067061636b2d31" +
		"0000000b7265666572656e63652d31"
	if got := hex.EncodeToString(c.canonicalIntent(principal)); got != canonicalHex {
		t.Fatalf("canonical v1 changed:\n%s", got)
	}
	fp, err := c.Fingerprint(principal)
	if err != nil || fp.Version != 1 {
		t.Fatalf("fingerprint: %+v %v", fp, err)
	}
	// SHA-256 independently checked with .NET over the literal canonical bytes.
	const goldenHash = "f00e106d983db48d9947f3bcc18de8f379cc23db93b4a0dfca8fe35e4773f786"
	if got := hex.EncodeToString(fp.Hash[:]); got != goldenHash {
		t.Fatalf("hash v1 changed: %s", got)
	}
}

func TestFingerprintEquivalentCommands(t *testing.T) {
	c := testCommand()
	c.Lines = append(c.Lines, c.Lines[0])
	c.Lines[1].LineNumber, c.Lines[1].VariantID = 2, uuid.MustParse("01935f00-0000-7000-8000-000000000012")
	c.Lines[1].PackID, c.Lines[1].PackRevision = uuid.Nil, ""
	c.Lines[1].InputCount = 9007199254740993
	originalLines := slices.Clone(c.Lines)
	want, err := c.Fingerprint(testPrincipal())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Lines, originalLines) {
		t.Fatal("fingerprinting mutated caller lines")
	}
	slices.Reverse(c.Lines)
	c.OperationID = uuid.MustParse(strings.ToUpper(c.OperationID.String()))
	c.Lines[0].InputCount, err = ParsePositiveQuantity("9007199254740993")
	if err != nil {
		t.Fatal(err)
	}
	before := slices.Clone(c.Lines)
	got, err := c.Fingerprint(testPrincipal())
	if err != nil || got != want || !reflect.DeepEqual(before, c.Lines) {
		t.Fatalf("equivalent command changed hash or mutated lines: %v", err)
	}
}

func TestFingerprintMeaningfulChanges(t *testing.T) {
	base, err := testCommand().Fingerprint(testPrincipal())
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Command, *Principal){
		"operation":         func(c *Command, _ *Principal) { c.OperationID = uuid.New() },
		"source":            func(c *Command, _ *Principal) { c.SourceEventID = uuid.New() },
		"producer":          func(c *Command, _ *Principal) { c.Producer = "other-test" },
		"warehouse":         func(c *Command, _ *Principal) { c.WarehouseID = uuid.New() },
		"actor":             func(_ *Command, p *Principal) { p.UserID = uuid.New() },
		"direction":         func(c *Command, _ *Principal) { c.Direction = Out },
		"variant":           func(c *Command, _ *Principal) { c.Lines[0].VariantID = uuid.New() },
		"count":             func(c *Command, _ *Principal) { c.Lines[0].InputCount++ },
		"pack":              func(c *Command, _ *Principal) { c.Lines[0].PackID = uuid.New() },
		"pack revision":     func(c *Command, _ *Principal) { c.Lines[0].PackRevision = "pack-2" },
		"no pack":           func(c *Command, _ *Principal) { c.Lines[0].PackID, c.Lines[0].PackRevision = uuid.Nil, "" },
		"expected revision": func(c *Command, _ *Principal) { c.Lines[0].ExpectedStockReferenceRevision = "reference-2" },
		"extra line": func(c *Command, _ *Principal) {
			c.Lines = append(c.Lines, CommandLine{LineNumber: 2, VariantID: uuid.New(), InputCount: 1, ExpectedStockReferenceRevision: "r"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, p := testCommand(), testPrincipal()
			change(&c, &p)
			got, err := c.Fingerprint(p)
			if err != nil || got == base {
				t.Fatalf("meaningful change did not change hash: %v", err)
			}
		})
	}
	c := testCommand()
	c.Lines = append(c.Lines, CommandLine{LineNumber: 2, VariantID: uuid.New(), InputCount: 1, ExpectedStockReferenceRevision: "r"})
	a, _ := c.Fingerprint(testPrincipal())
	c.Lines[0].LineNumber, c.Lines[1].LineNumber = 2, 1
	b, err := c.Fingerprint(testPrincipal())
	if err != nil || a == b {
		t.Fatal("semantic line-number change did not change fingerprint")
	}
	c.CommandVersion++
	if _, err := c.Fingerprint(testPrincipal()); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal("unsupported command version silently hashed")
	}
	if _, err := testCommand().Fingerprint(Principal{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal("missing actor accepted")
	}
}

func TestFingerprintExcludesExecutionMetadataAndKey(t *testing.T) {
	// Execution data has no place in Command or the fingerprint API. A future
	// adapter may hold these together, but must pass only intent + verified actor.
	attempt := struct {
		command   Command
		reference ReferenceSnapshot
		result    PostedResult
		requestID string
	}{testCommand(), testReference(), testResult(), "first-attempt"}
	want, err := attempt.command.Fingerprint(testPrincipal())
	if err != nil {
		t.Fatal(err)
	}
	attempt.command.IdempotencyKey = uuid.New()
	attempt.reference.CheckedAt = attempt.reference.CheckedAt.Add(time.Hour)
	attempt.reference.StockReferenceRevision = "later-observation"
	attempt.reference.Pack.PiecesPerPack = 12
	attempt.result.Lines[0].MovementID = uuid.New()
	attempt.result.PostedAt = attempt.result.PostedAt.Add(time.Hour)
	attempt.result.Replayed = true
	attempt.requestID = "second-attempt"
	got, err := attempt.command.Fingerprint(testPrincipal())
	if err != nil || got != want {
		t.Fatalf("execution metadata/key changed fingerprint: %v", err)
	}
	// Length prefixes prevent otherwise ambiguous concatenated text boundaries.
	if slices.Equal(appendText(appendText(nil, "ab"), "c"), appendText(appendText(nil, "a"), "bc")) {
		t.Fatal("ambiguous text framing")
	}
}
