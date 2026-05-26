// symbols.go houses the NailForge-specific symbol extraction. The Box core
// stays YAML-agnostic; this file is the only place where "nf:" domain symbols
// are minted, so the rest of the importer (main.go) only knows about generic
// box.Symbol slices.
//
// Architect decision (R0.7.6):
//   - SymDomain values are prefixed "nf:" (NailForge namespace).
//   - parseAtom is greedy + unicode-safe: full atom verbatim, the verb token,
//     the (verb, action) pair, and the state-transition tail.
//   - extractSymbols draws from four sources in order: nail.atom,
//     family.atomic_theory.formula, family.c_language.expression, nail.composes.
//   - buildItemSymbols layers a SymKind (from the importer kind classifier) and
//     a SymStatus (from family.deprecated / family.lifecycle.status) on top.
//   - parseAtom does NOT call box.ValidateSymbol; the Service does that on
//     Store / ReplaceItem (R0.7.1 contract: parser stays unaware of the
//     validation layer).
package main

import (
	"fmt"
	"strings"

	"github.com/windborneos/box-model/box"
)

// domainPrefix is the SymDomain namespace used for every NailForge-sourced
// routing symbol.
const domainPrefix = "nf:"

// extractSymbols inspects a parsed NailForge YAML and produces the SymDomain
// slice (all "nf:" prefixed) that should be carried by the indexed Item.
// kind is the import-side kind classifier ("nail_family" / "organ_nail" /
// "action_nail" / "nail_lineage" / "nail_version"); some extractors are
// kind-aware.
func extractSymbols(parsed map[string]any, kind string) []box.Symbol {
	seen := map[string]struct{}{}
	out := []box.Symbol{}

	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		value := raw
		if !strings.HasPrefix(value, domainPrefix) {
			value = domainPrefix + value
		}
		if _, dup := seen[value]; dup {
			return
		}
		seen[value] = struct{}{}
		out = append(out, box.Symbol{Kind: box.SymDomain, Value: value})
	}

	// 1) nail.atom (string OR map) — parsed via parseAtom for 2-4 derived
	// symbols. Real NailForge YAMLs use the structured map form (atom.method,
	// atom.transform_type, atom.object, atom.state_transition); the string
	// form ("构(extract, ...)") is the canonical compact representation.
	if nail, ok := parsed["nail"].(map[string]any); ok {
		switch a := nail["atom"].(type) {
		case string:
			for _, sym := range parseAtom(a) {
				add(sym)
			}
		case map[string]any:
			for _, sym := range parseAtomMap(a) {
				add(sym)
			}
		}
		// 4) nail.composes (list of strings) — organ nails composing action nails.
		if composes, ok := nail["composes"].([]any); ok {
			for _, c := range composes {
				if s, ok := c.(string); ok {
					add(s)
				}
			}
		}
	}

	// 2) family.atomic_theory.formula (string).
	if v, ok := digString(parsed, "family", "atomic_theory", "formula"); ok {
		add(v)
	}

	// 3) family.c_language.expression (string, 世界模型 family).
	if v, ok := digString(parsed, "family", "c_language", "expression"); ok {
		add(v)
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// parseAtom takes an atom string ("构(extract, D·t·m → K·g·m)") and emits the
// constituent nf-domain symbol values (with "nf:" prefix). See package doc
// for the strategy. Empty input → empty slice. Degenerate input (no parens) →
// a single verbatim symbol.
func parseAtom(atom string) []string {
	atom = strings.TrimSpace(atom)
	if atom == "" {
		return nil
	}

	seen := map[string]struct{}{}
	out := []string{}
	emit := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		val := domainPrefix + s
		if _, dup := seen[val]; dup {
			return
		}
		seen[val] = struct{}{}
		out = append(out, val)
	}

	// 1) full atom verbatim.
	emit(atom)

	open := strings.Index(atom, "(")
	close := strings.LastIndex(atom, ")")
	if open <= 0 || close <= open {
		// Degenerate: no parseable parens. Only the verbatim form is emitted.
		return out
	}

	// 2) verb-only token (before '(').
	verb := strings.TrimSpace(atom[:open])
	emit(verb)

	inside := atom[open+1 : close]

	// 3) (verb, action) — action is the first comma-delimited segment.
	parts := strings.Split(inside, ",")
	action := strings.TrimSpace(parts[0])
	if action != "" && verb != "" {
		emit(fmt.Sprintf("%s(%s)", verb, action))
	}

	// 4) state-transition tail — substring after the final comma, with all
	// whitespace stripped to canonicalize.
	if len(parts) >= 2 {
		tail := strings.TrimSpace(parts[len(parts)-1])
		canonical := stripSpaces(tail)
		if canonical != "" {
			emit(canonical)
		}
	}

	return out
}

// parseAtomMap reconstructs the canonical atom-string from the structured
// map form ({transform_type, method, object, state_transition}) and then runs
// parseAtom on it. The string form drives the existing greedy strategy, so
// downstream symbols stay consistent across the two YAML shapes. Returns nil
// when neither verb nor method is present.
func parseAtomMap(a map[string]any) []string {
	transform, _ := a["transform_type"].(string)
	method, _ := a["method"].(string)
	object, _ := a["object"].(string)
	stateTransition, _ := a["state_transition"].(string)

	transform = strings.TrimSpace(transform)
	method = strings.TrimSpace(method)
	object = strings.TrimSpace(object)
	stateTransition = strings.TrimSpace(stateTransition)

	// Build a canonical string and lean on parseAtom. The expected reading is
	// "<transform>(<method>, <object>)" — the trailing comma is omitted when
	// object is absent.
	var canonical string
	switch {
	case transform != "" && method != "" && object != "":
		canonical = fmt.Sprintf("%s(%s, %s)", transform, method, object)
	case transform != "" && method != "":
		canonical = fmt.Sprintf("%s(%s)", transform, method)
	case transform != "":
		canonical = transform
	case method != "":
		canonical = method
	default:
		return nil
	}

	out := parseAtom(canonical)

	// Tack on the state_transition independently — parseAtom would only catch
	// it if it were the tail of the parentheses, but in the structured shape it
	// lives in a sibling key.
	if stateTransition != "" {
		out = appendUnique(out, domainPrefix+stripSpaces(stateTransition))
	}
	return out
}

// appendUnique adds value to slice if absent. Used by parseAtomMap to layer in
// the state_transition symbol without re-running the full dedup loop.
func appendUnique(slice []string, value string) []string {
	for _, s := range slice {
		if s == value {
			return slice
		}
	}
	return append(slice, value)
}

// stripSpaces removes every ASCII space rune from s (used to canonicalize the
// state-transition tail: "D·t·m → K·g·m" → "D·t·m→K·g·m").
func stripSpaces(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// buildItemSymbols layers the per-item SymKind (from kind) and SymStatus
// (from lifecycle metadata) on top of extractSymbols output. The result is
// the Symbols slice handed to box.StoreRequest.
func buildItemSymbols(parsed map[string]any, kind string) []box.Symbol {
	syms := extractSymbols(parsed, kind)
	syms = append(syms, kindSymbol(kind), statusSymbol(parsed))
	return syms
}

// kindSymbol maps the import-side classifier to a Box SymKind value.
//
//	nail_family  → R (requirement-like spec)
//	organ_nail   → T (task — composable action)
//	action_nail  → A (action)
//	nail_lineage → M (memo / lineage record)
//	any other    → X (external)
func kindSymbol(kind string) box.Symbol {
	switch kind {
	case "nail_family":
		return box.Symbol{Kind: box.SymKind, Value: "R"}
	case "organ_nail":
		return box.Symbol{Kind: box.SymKind, Value: "T"}
	case "action_nail":
		return box.Symbol{Kind: box.SymKind, Value: "A"}
	case "nail_lineage":
		return box.Symbol{Kind: box.SymKind, Value: "M"}
	default:
		// nail_version and anything else fall back to External.
		return box.Symbol{Kind: box.SymKind, Value: "X"}
	}
}

// statusSymbol inspects family.deprecated / family.lifecycle.status and emits
// the corresponding SymStatus. Deprecated → ✗, otherwise → ✓.
func statusSymbol(parsed map[string]any) box.Symbol {
	if v, ok := digString(parsed, "family", "deprecated"); ok && v == "true" {
		return box.Symbol{Kind: box.SymStatus, Value: "✗"}
	}
	if v, ok := digString(parsed, "family", "lifecycle", "status"); ok && v == "deprecated" {
		return box.Symbol{Kind: box.SymStatus, Value: "✗"}
	}
	return box.Symbol{Kind: box.SymStatus, Value: "✓"}
}

// digString walks parsed along path, returning the string at the leaf. Bool
// and numeric leaves are stringified (e.g. true → "true"). Missing paths or
// non-stringifiable leaves return ("", false).
func digString(m map[string]any, path ...string) (string, bool) {
	if len(path) == 0 {
		return "", false
	}
	var cur any = m
	for _, key := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		next, present := mm[key]
		if !present {
			return "", false
		}
		cur = next
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case bool:
		if v {
			return "true", true
		}
		return "false", true
	case float64:
		// JSON numbers decode as float64; render without trailing zeros.
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", v), "0"), "."), true
	case int:
		return fmt.Sprintf("%d", v), true
	default:
		return "", false
	}
}

// symbolsEqual compares two Symbol slices as sets. Order is irrelevant; each
// symbol is keyed by Kind|Value|Ref. Two nil/empty slices compare equal.
func symbolsEqual(a, b []box.Symbol) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	key := func(s box.Symbol) string {
		return string(s.Kind) + "|" + s.Value + "|" + s.Ref
	}
	set := make(map[string]int, len(a))
	for _, s := range a {
		set[key(s)]++
	}
	for _, s := range b {
		k := key(s)
		set[k]--
		if set[k] < 0 {
			return false
		}
	}
	for _, n := range set {
		if n != 0 {
			return false
		}
	}
	return true
}
