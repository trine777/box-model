// Package cli — notation.go implements the Symbolic Literal Protocol (SLP)
// parser used by the AI-facing surface of `box`. SLP packs Symbol routing
// information into a single human-readable string so a caller can pass e.g.
//
//	"R → @arch #routing ** & item_abc"
//
// instead of long --kind-sym=... --status=... flag chains.
//
// SLP is CLI-only: box core never sees the string form. ParseNotation is the
// only exported entrypoint and intentionally does NOT call ValidateSymbol —
// the Service layer remains the single source of truth for symbol validity.
package cli

import (
	"fmt"
	"strings"

	"github.com/windborneos/box-model/box"
)

// ParseNotation parses an SLP token stream into a list of Symbols.
//
// Token rules (in priority order):
//   - whitespace separates tokens
//   - tokens wrapped in "..." are taken as a single unit verbatim
//   - sigils:
//       D/R/Q/H/T/M/F/O/A/X (single char) → SymKind
//       ?/→/✓/✗/~/◯                       → SymStatus
//       > / < / & / | / ≈ / ⊃             → SymRelation, NEXT token is Ref
//       @<name>                            → SymScope, value = <name>
//       #<name>                            → SymTopic, value = <name>
//       * / ** / ***                       → SymPriority
//       contains ':'                       → SymDomain, value = full token
//       otherwise                          → ErrValidation
//
// Returns ErrValidation for unmatched quotes, dangling relation, or unknown
// tokens. An empty / whitespace-only string returns (nil, nil).
func ParseNotation(s string) ([]box.Symbol, error) {
	tokens, err := tokenizeSLP(s)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, nil
	}
	out := make([]box.Symbol, 0, len(tokens))
	i := 0
	for i < len(tokens) {
		tok := tokens[i]
		// Single-char SymKind whitelist.
		if len(tok) == 1 {
			switch tok {
			case "D", "R", "Q", "H", "T", "M", "F", "O", "A", "X":
				out = append(out, box.Symbol{Kind: box.SymKind, Value: tok})
				i++
				continue
			case "?", "~":
				out = append(out, box.Symbol{Kind: box.SymStatus, Value: tok})
				i++
				continue
			case "*":
				out = append(out, box.Symbol{Kind: box.SymPriority, Value: tok})
				i++
				continue
			}
		}
		// Unicode status (multi-byte single-rune).
		switch tok {
		case "→", "✓", "✗", "◯":
			out = append(out, box.Symbol{Kind: box.SymStatus, Value: tok})
			i++
			continue
		case "**", "***":
			out = append(out, box.Symbol{Kind: box.SymPriority, Value: tok})
			i++
			continue
		case ">", "<", "&", "|", "≈", "⊃":
			// Relation must be followed by a Ref token.
			if i+1 >= len(tokens) {
				return nil, fmt.Errorf("%w: relation symbol %q requires following ref token", box.ErrValidation, tok)
			}
			out = append(out, box.Symbol{Kind: box.SymRelation, Value: tok, Ref: tokens[i+1]})
			i += 2
			continue
		}
		// Sigil prefixes.
		if strings.HasPrefix(tok, "@") {
			out = append(out, box.Symbol{Kind: box.SymScope, Value: tok[1:]})
			i++
			continue
		}
		if strings.HasPrefix(tok, "#") {
			out = append(out, box.Symbol{Kind: box.SymTopic, Value: tok[1:]})
			i++
			continue
		}
		// Domain (contains ':').
		if strings.Contains(tok, ":") {
			out = append(out, box.Symbol{Kind: box.SymDomain, Value: tok})
			i++
			continue
		}
		return nil, fmt.Errorf("%w: unknown token %q", box.ErrValidation, tok)
	}
	return out, nil
}

// tokenizeSLP splits s on whitespace, honouring "..." quoted segments. A
// quote opens on a '"' character anywhere; the matching close quote ends the
// token. An unmatched open quote returns ErrValidation.
func tokenizeSLP(s string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		if inQuote {
			if r == '"' {
				tokens = append(tokens, cur.String())
				cur.Reset()
				inQuote = false
				continue
			}
			cur.WriteRune(r)
			continue
		}
		if r == '"' {
			flush()
			inQuote = true
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			flush()
			continue
		}
		cur.WriteRune(r)
	}
	if inQuote {
		return nil, fmt.Errorf("%w: unmatched quote in notation", box.ErrValidation)
	}
	flush()
	return tokens, nil
}
