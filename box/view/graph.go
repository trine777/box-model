package view

import (
	"strings"

	"github.com/windborneos/box-model/box"
)

// graphRenderer emits a mermaid `graph LR` document. Every IsLatest,
// non-deleted item becomes a node; every SymRelation symbol becomes a labeled
// directed edge.
//
// Output is intentionally NON-JSON — it is mermaid source the user copies
// into mmdc or a markdown preview. The first line is always `graph LR`
// (per R0.7.4 spec; grep-able header).
type graphRenderer struct{}

// graphNodeCap is the maximum number of nodes we emit before truncating.
// 100 keeps mermaid renderable in mmdc / GitHub preview.
const graphNodeCap = 100

// Render produces the mermaid source.
func (r *graphRenderer) Render(items []box.Item, opts RenderOptions) (string, error) {
	// graph perspective ignores axis (all relation kinds are drawn). We
	// deliberately accept any opts.Axis to keep `box rotate` simple.
	_ = opts

	var b strings.Builder
	// Header — first line is exactly "graph LR" so callers can grep it.
	b.WriteString("graph LR\n")

	// Filter to drawable items: non-deleted; non-latest revisions skipped
	// when explicitly marked (Revision > 0 && !IsLatest). Items synthesised
	// from --stdin JSON without is_latest get Revision==0 and are drawn.
	drawable := make([]box.Item, 0, len(items))
	for _, it := range items {
		if it.Status == "deleted" {
			continue
		}
		if it.Revision > 0 && !it.IsLatest {
			continue
		}
		drawable = append(drawable, it)
	}

	total := len(drawable)
	if total > graphNodeCap {
		// Truncation marker first (after the header) so it's visually
		// adjacent to `graph LR`.
		writeTruncationNote(&b, total, graphNodeCap)
		drawable = drawable[:graphNodeCap]
	}

	// nodeSet tracks which item IDs are present so we only draw edges to
	// nodes we've emitted (avoids dangling references after truncation).
	nodeSet := make(map[string]struct{}, len(drawable))
	for _, it := range drawable {
		writeMermaidNode(&b, it)
		nodeSet[it.ID] = struct{}{}
	}
	for _, it := range drawable {
		for _, sym := range it.Symbols {
			if sym.Kind != box.SymRelation || sym.Ref == "" {
				continue
			}
			if _, ok := nodeSet[sym.Ref]; !ok {
				continue
			}
			writeMermaidEdge(&b, it.ID, sym.Ref, sym.Value)
		}
	}
	return b.String(), nil
}

// writeMermaidNode emits one mermaid node declaration of the form:
//
//	n_<id> ["[<kind-sym>] <id-tail-8> <status-sym>"]
//
// All label parts are mermaid-escaped. Missing kind/status fall back to `-`.
func writeMermaidNode(b *strings.Builder, it box.Item) {
	kindSym := firstSymbolValue(it, box.SymKind)
	if kindSym == "" {
		kindSym = "-"
	}
	statusSym := firstSymbolValue(it, box.SymStatus)
	if statusSym == "" {
		statusSym = "-"
	}
	label := "[" + kindSym + "] " + shortID(it.ID, 8) + " " + statusSym
	b.WriteString("  ")
	b.WriteString(mermaidNodeID(it.ID))
	b.WriteString("[\"")
	b.WriteString(mermaidEscape(label))
	b.WriteString("\"]\n")
}

// writeMermaidEdge emits one directed edge from `from` to `to` labeled with
// the relation literal (e.g. `&` for depends-on).
func writeMermaidEdge(b *strings.Builder, from, to, rel string) {
	b.WriteString("  ")
	b.WriteString(mermaidNodeID(from))
	b.WriteString(" -->|")
	b.WriteString(mermaidEscape(rel))
	b.WriteString("| ")
	b.WriteString(mermaidNodeID(to))
	b.WriteByte('\n')
}

// writeTruncationNote emits the mermaid comment used by graph / tree / mind
// when the input exceeds graphNodeCap.
func writeTruncationNote(b *strings.Builder, total, limit int) {
	b.WriteString("%% truncated to ")
	b.WriteString(itoa(limit))
	b.WriteString(" nodes (had ")
	b.WriteString(itoa(total))
	b.WriteString(")\n")
}

// itoa is a tiny integer-to-string helper to avoid pulling strconv just for
// the truncation banner.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
