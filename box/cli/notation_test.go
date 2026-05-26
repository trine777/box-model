package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/windborneos/box-model/box"
)

// TestParseNotationKind asserts the single-kind happy path.
func TestParseNotationKind(t *testing.T) {
	got, err := ParseNotation("R")
	if err != nil {
		t.Fatalf("ParseNotation: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len=%d, want 1: %+v", len(got), got)
	}
	if got[0].Kind != box.SymKind || got[0].Value != "R" {
		t.Fatalf("symbol=%+v, want {Kind:kind, Value:R}", got[0])
	}
}

// TestParseNotationFull asserts a full multi-dimension token stream including
// a relation symbol whose Ref is the following token.
func TestParseNotationFull(t *testing.T) {
	got, err := ParseNotation("R → @arch #routing ** & item_abc")
	if err != nil {
		t.Fatalf("ParseNotation: %v", err)
	}
	// Expect 6 symbols: SymKind(R), SymStatus(→), SymScope(arch),
	// SymTopic(routing), SymPriority(**), SymRelation(&, Ref=item_abc).
	want := []box.Symbol{
		{Kind: box.SymKind, Value: "R"},
		{Kind: box.SymStatus, Value: "→"},
		{Kind: box.SymScope, Value: "arch"},
		{Kind: box.SymTopic, Value: "routing"},
		{Kind: box.SymPriority, Value: "**"},
		{Kind: box.SymRelation, Value: "&", Ref: "item_abc"},
	}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("symbol[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestParseNotationQuoted asserts a quoted token preserves spaces / parens /
// Unicode as a single SymDomain value, and that a preceding relation still
// consumes the *unquoted* item_id as its Ref.
func TestParseNotationQuoted(t *testing.T) {
	got, err := ParseNotation(`R ≈ item_abc "nf:构(extract)"`)
	if err != nil {
		t.Fatalf("ParseNotation: %v", err)
	}
	want := []box.Symbol{
		{Kind: box.SymKind, Value: "R"},
		{Kind: box.SymRelation, Value: "≈", Ref: "item_abc"},
		{Kind: box.SymDomain, Value: "nf:构(extract)"},
	}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("symbol[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestParseNotationDomainUnquoted asserts a domain token without internal
// whitespace doesn't require quotes.
func TestParseNotationDomainUnquoted(t *testing.T) {
	got, err := ParseNotation("R nf:abc")
	if err != nil {
		t.Fatalf("ParseNotation: %v", err)
	}
	want := []box.Symbol{
		{Kind: box.SymKind, Value: "R"},
		{Kind: box.SymDomain, Value: "nf:abc"},
	}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("symbol[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestParseNotationRelationMissingRef asserts a relation symbol without a
// following Ref token is ErrValidation.
func TestParseNotationRelationMissingRef(t *testing.T) {
	_, err := ParseNotation("R &")
	if !errors.Is(err, box.ErrValidation) {
		t.Fatalf("err=%v, want ErrValidation", err)
	}
}

// TestParseNotationUnmatchedQuote asserts a dangling open-quote is ErrValidation.
func TestParseNotationUnmatchedQuote(t *testing.T) {
	_, err := ParseNotation(`R "abc`)
	if !errors.Is(err, box.ErrValidation) {
		t.Fatalf("err=%v, want ErrValidation", err)
	}
}

// TestParseNotationUnknownToken asserts a token that matches no category
// (single-letter not in SymKind whitelist, no sigil, no ':' separator) is
// ErrValidation.
func TestParseNotationUnknownToken(t *testing.T) {
	_, err := ParseNotation("R Z")
	if !errors.Is(err, box.ErrValidation) {
		t.Fatalf("err=%v, want ErrValidation", err)
	}
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "unknown") &&
		!strings.Contains(strings.ToLower(err.Error()), "token") {
		t.Fatalf("err msg lacks 'unknown' or 'token': %v", err)
	}
}

// TestParseNotationEmptyString asserts whitespace-only and empty input parse
// to nil with no error. (Validate-at-least-one-Kind is the Service's job.)
func TestParseNotationEmptyString(t *testing.T) {
	for _, in := range []string{"", "   ", "\t\n"} {
		got, err := ParseNotation(in)
		if err != nil {
			t.Fatalf("ParseNotation(%q): %v", in, err)
		}
		if len(got) != 0 {
			t.Fatalf("ParseNotation(%q) = %+v, want empty", in, got)
		}
	}
}
