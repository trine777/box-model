package box

import (
	"errors"
	"testing"
)

// TestValidateSymbolKindWhitelist exercises ValidateSymbol against every
// allowed SymKind value plus a rejected one ("Z").
func TestValidateSymbolKindWhitelist(t *testing.T) {
	good := []string{"D", "R", "Q", "H", "T", "M", "F", "O", "A", "X"}
	for _, v := range good {
		if err := ValidateSymbol(Symbol{Kind: SymKind, Value: v}); err != nil {
			t.Fatalf("Kind=%q expected ok, got %v", v, err)
		}
	}
	if err := ValidateSymbol(Symbol{Kind: SymKind, Value: "Z"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Kind=Z expected ErrValidation, got %v", err)
	}
	// Ref must be empty for SymKind.
	if err := ValidateSymbol(Symbol{Kind: SymKind, Value: "D", Ref: "item_x"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Kind with Ref expected ErrValidation, got %v", err)
	}
}

// TestValidateSymbolStatusWhitelist exercises ValidateSymbol against every
// allowed SymStatus value plus a rejected one ("!").
func TestValidateSymbolStatusWhitelist(t *testing.T) {
	good := []string{"?", "→", "✓", "✗", "~", "◯"}
	for _, v := range good {
		if err := ValidateSymbol(Symbol{Kind: SymStatus, Value: v}); err != nil {
			t.Fatalf("Status=%q expected ok, got %v", v, err)
		}
	}
	if err := ValidateSymbol(Symbol{Kind: SymStatus, Value: "!"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Status=! expected ErrValidation, got %v", err)
	}
}

// TestValidateSymbolRelationRequiresRef asserts that SymRelation rejects
// missing/empty Ref and accepts a non-empty Ref.
func TestValidateSymbolRelationRequiresRef(t *testing.T) {
	if err := ValidateSymbol(Symbol{Kind: SymRelation, Value: "&"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Relation without Ref expected ErrValidation, got %v", err)
	}
	if err := ValidateSymbol(Symbol{Kind: SymRelation, Value: "&", Ref: "item_a"}); err != nil {
		t.Fatalf("Relation with Ref expected ok, got %v", err)
	}
	if err := ValidateSymbol(Symbol{Kind: SymRelation, Value: "@", Ref: "item_a"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Relation=@ expected ErrValidation, got %v", err)
	}
}

// TestValidateSymbolScopeAlphanum asserts that scope/topic values reject
// non-alphanumeric/dash/underscore characters and accept the safe set.
func TestValidateSymbolScopeAlphanum(t *testing.T) {
	if err := ValidateSymbol(Symbol{Kind: SymScope, Value: "team_a-1"}); err != nil {
		t.Fatalf("scope=team_a-1 expected ok, got %v", err)
	}
	if err := ValidateSymbol(Symbol{Kind: SymScope, Value: "team a"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("scope with space expected ErrValidation, got %v", err)
	}
	if err := ValidateSymbol(Symbol{Kind: SymScope, Value: "团队"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("scope with CJK expected ErrValidation, got %v", err)
	}
	if err := ValidateSymbol(Symbol{Kind: SymScope, Value: ""}); !errors.Is(err, ErrValidation) {
		t.Fatalf("empty scope expected ErrValidation, got %v", err)
	}
	// SymTopic mirrors SymScope.
	if err := ValidateSymbol(Symbol{Kind: SymTopic, Value: "billing"}); err != nil {
		t.Fatalf("topic=billing expected ok, got %v", err)
	}
}

// TestValidateSymbolDomainFormat asserts the <ns>:<v> shape rules.
func TestValidateSymbolDomainFormat(t *testing.T) {
	good := []string{"nf:构(extract)", "nf:x", "team_a:核心", "a1:any value here"}
	for _, v := range good {
		if err := ValidateSymbol(Symbol{Kind: SymDomain, Value: v}); err != nil {
			t.Fatalf("domain=%q expected ok, got %v", v, err)
		}
	}
	bad := []string{"NF:x", "a:", "9x:y", "no-colon", ":missingns", "longerNamespace1234567:v"}
	for _, v := range bad {
		if err := ValidateSymbol(Symbol{Kind: SymDomain, Value: v}); !errors.Is(err, ErrValidation) {
			t.Fatalf("domain=%q expected ErrValidation, got %v", v, err)
		}
	}
}

// TestValidateSymbolsRequiresKind asserts that a non-nil slice without a
// SymKind member is rejected.
func TestValidateSymbolsRequiresKind(t *testing.T) {
	syms := []Symbol{
		{Kind: SymStatus, Value: "?"},
		{Kind: SymPriority, Value: "**"},
	}
	if err := ValidateSymbols(syms); !errors.Is(err, ErrValidation) {
		t.Fatalf("slice without SymKind expected ErrValidation, got %v", err)
	}
	syms = append(syms, Symbol{Kind: SymKind, Value: "D"})
	if err := ValidateSymbols(syms); err != nil {
		t.Fatalf("slice with SymKind expected ok, got %v", err)
	}
}

// TestValidateSymbolsEmpty asserts nil is accepted (legacy items),
// while explicit empty slice is rejected.
func TestValidateSymbolsEmpty(t *testing.T) {
	if err := ValidateSymbols(nil); err != nil {
		t.Fatalf("nil expected ok, got %v", err)
	}
	if err := ValidateSymbols([]Symbol{}); !errors.Is(err, ErrValidation) {
		t.Fatalf("empty slice expected ErrValidation, got %v", err)
	}
}
