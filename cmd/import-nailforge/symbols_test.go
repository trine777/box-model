package main

import (
	"strings"
	"testing"

	"github.com/windborneos/box-model/box"
)

// containsValue is a small helper: returns true if any Symbol in syms has the
// given Kind+Value (Ref ignored).
func containsValue(syms []box.Symbol, kind box.SymbolKind, value string) bool {
	for _, s := range syms {
		if s.Kind == kind && s.Value == value {
			return true
		}
	}
	return false
}

// containsString reports whether the slice has the given value.
func containsString(slice []string, v string) bool {
	for _, s := range slice {
		if s == v {
			return true
		}
	}
	return false
}

// uniqueStrings returns true iff the slice has no duplicates.
func uniqueStrings(slice []string) bool {
	seen := map[string]struct{}{}
	for _, s := range slice {
		if _, dup := seen[s]; dup {
			return false
		}
		seen[s] = struct{}{}
	}
	return true
}

// TestParseAtomFull exercises the canonical NailForge atom shape:
// verb(action, lhs → rhs).
func TestParseAtomFull(t *testing.T) {
	got := parseAtom("构(extract, D·t·m → K·g·m)")
	if !uniqueStrings(got) {
		t.Fatalf("duplicates: %v", got)
	}
	want := []string{
		"nf:构(extract, D·t·m → K·g·m)",
		"nf:构",
		"nf:构(extract)",
		"nf:D·t·m→K·g·m",
	}
	for _, w := range want {
		if !containsString(got, w) {
			t.Errorf("missing %q in %v", w, got)
		}
	}
}

// TestParseAtomVerbOnly covers atoms with no state-transition tail:
// verb(action).
func TestParseAtomVerbOnly(t *testing.T) {
	got := parseAtom("生(create)")
	if !uniqueStrings(got) {
		t.Fatalf("duplicates: %v", got)
	}
	// Expect at least the verbatim atom and the verb-only token.
	if !containsString(got, "nf:生(create)") {
		t.Errorf("missing nf:生(create) in %v", got)
	}
	if !containsString(got, "nf:生") {
		t.Errorf("missing nf:生 in %v", got)
	}
}

// TestParseAtomDegenerate verifies that a string without parens collapses to a
// single verbatim symbol.
func TestParseAtomDegenerate(t *testing.T) {
	got := parseAtom("无规则字符串")
	if len(got) != 1 {
		t.Fatalf("expected 1 symbol, got %d: %v", len(got), got)
	}
	if got[0] != "nf:无规则字符串" {
		t.Errorf("unexpected symbol: %q", got[0])
	}
}

// TestParseAtomEmpty: empty input emits no symbols.
func TestParseAtomEmpty(t *testing.T) {
	got := parseAtom("")
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %v", got)
	}
	got = parseAtom("   ")
	if len(got) != 0 {
		t.Fatalf("expected empty slice for whitespace, got %v", got)
	}
}

// TestExtractSymbolsFromActionNail: nail.atom drives the symbol set.
func TestExtractSymbolsFromActionNail(t *testing.T) {
	parsed := map[string]any{
		"nail": map[string]any{
			"id":   "extract_lake_requirements",
			"type": "action_nail",
			"atom": "构(extract, D·t·m → K·g·m)",
		},
	}
	got := extractSymbols(parsed, "action_nail")
	if !containsValue(got, box.SymDomain, "nf:构(extract)") {
		t.Errorf("missing nf:构(extract) in %v", got)
	}
	if !containsValue(got, box.SymDomain, "nf:构") {
		t.Errorf("missing nf:构 in %v", got)
	}
	for _, s := range got {
		if s.Kind != box.SymDomain {
			t.Errorf("extractSymbols emitted non-domain kind: %v", s)
		}
		if !strings.HasPrefix(s.Value, "nf:") {
			t.Errorf("symbol missing nf: prefix: %v", s)
		}
	}
}

// TestExtractSymbolsFromActionNailMapAtom exercises the structured atom form
// used by real NailForge YAMLs ({transform_type, method, object,
// state_transition}). Symbols should mirror the string form's output.
func TestExtractSymbolsFromActionNailMapAtom(t *testing.T) {
	parsed := map[string]any{
		"nail": map[string]any{
			"id":   "extract_lake_requirements",
			"type": "action_nail",
			"atom": map[string]any{
				"transform_type":   "构",
				"method":           "extract",
				"object":           "D·t·m → K·g·m",
				"state_transition": "无 → 构",
			},
		},
	}
	got := extractSymbols(parsed, "action_nail")
	want := []string{
		"nf:构",
		"nf:构(extract)",
		"nf:无→构",
	}
	for _, w := range want {
		if !containsValue(got, box.SymDomain, w) {
			t.Errorf("missing %q in %v", w, got)
		}
	}
	for _, s := range got {
		if s.Kind != box.SymDomain {
			t.Errorf("extractSymbols emitted non-domain kind: %v", s)
		}
		if !strings.HasPrefix(s.Value, "nf:") {
			t.Errorf("symbol missing nf: prefix: %v", s)
		}
	}
}

// TestExtractSymbolsFromFamily: family.atomic_theory.formula → one symbol.
func TestExtractSymbolsFromFamily(t *testing.T) {
	parsed := map[string]any{
		"family": map[string]any{
			"id": "data_lake_organizer",
			"atomic_theory": map[string]any{
				"formula": "a1 → a2 → a3 → a4",
			},
		},
	}
	got := extractSymbols(parsed, "nail_family")
	if !containsValue(got, box.SymDomain, "nf:a1 → a2 → a3 → a4") {
		t.Errorf("missing formula symbol in %v", got)
	}
}

// TestExtractSymbolsCLanguage: family.c_language.expression → one symbol
// (世界模型 family).
func TestExtractSymbolsCLanguage(t *testing.T) {
	parsed := map[string]any{
		"family": map[string]any{
			"id": "shi_jie_mo_xing",
			"c_language": map[string]any{
				"expression": "⟐ → ⟡",
			},
		},
	}
	got := extractSymbols(parsed, "nail_family")
	if !containsValue(got, box.SymDomain, "nf:⟐ → ⟡") {
		t.Errorf("missing c_language symbol in %v", got)
	}
}

// TestBuildItemSymbolsKindStatus: kind + lifecycle yield the right SymKind and
// SymStatus on top of any domain symbols.
func TestBuildItemSymbolsKindStatus(t *testing.T) {
	parsed := map[string]any{
		"nail": map[string]any{
			"type": "action_nail",
			"atom": "构(extract, D·t·m → K·g·m)",
		},
		"family": map[string]any{
			"lifecycle": map[string]any{"status": "active"},
		},
	}
	got := buildItemSymbols(parsed, "action_nail")
	if !containsValue(got, box.SymKind, "A") {
		t.Errorf("missing SymKind A in %v", got)
	}
	if !containsValue(got, box.SymStatus, "✓") {
		t.Errorf("missing SymStatus ✓ in %v", got)
	}
	// Validate every emitted symbol passes box.ValidateSymbol.
	if err := box.ValidateSymbols(got); err != nil {
		t.Errorf("ValidateSymbols failed: %v\nsymbols=%v", err, got)
	}

	// Deprecated → ✗.
	parsed["family"] = map[string]any{"deprecated": true}
	got = buildItemSymbols(parsed, "action_nail")
	if !containsValue(got, box.SymStatus, "✗") {
		t.Errorf("expected ✗ for deprecated, got %v", got)
	}

	// Kind mapping for organ_nail → T, action_nail → A, nail_family → R,
	// nail_lineage → M, nail_version → X.
	cases := []struct {
		kind string
		want string
	}{
		{"nail_family", "R"},
		{"organ_nail", "T"},
		{"action_nail", "A"},
		{"nail_lineage", "M"},
		{"nail_version", "X"},
	}
	for _, tc := range cases {
		sym := kindSymbol(tc.kind)
		if sym.Kind != box.SymKind || sym.Value != tc.want {
			t.Errorf("kindSymbol(%q) = %v, want SymKind %s", tc.kind, sym, tc.want)
		}
	}
}
