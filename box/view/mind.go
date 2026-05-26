package view

import (
	"sort"
	"strings"

	"github.com/windborneos/box-model/box"
)

// mindRenderer emits a mermaid `mindmap` document grouping items by
// scope → topic → item. opts.Axis is currently ignored; R0.7.5+ will add
// alternative groupings (kind → scope, etc).
//
// Output is intentionally NON-JSON; the first line is `mindmap`.
type mindRenderer struct{}

// Render produces the mermaid source.
func (r *mindRenderer) Render(items []box.Item, opts RenderOptions) (string, error) {
	_ = opts // axis intentionally ignored for R0.7.4; reserved for R0.7.5+.

	var b strings.Builder
	b.WriteString("mindmap\n")
	b.WriteString("  root((Box))\n")

	drawable := filterDrawable(items)
	total := len(drawable)
	if total > graphNodeCap {
		// Truncation note as a mermaid comment line. mindmap parses lines
		// starting with `%%` as comments.
		writeTruncationNote(&b, total, graphNodeCap)
		drawable = drawable[:graphNodeCap]
	}

	// Group by scope -> topic -> []item. Missing scope/topic fall under
	// "(no-scope)" / "(no-topic)" buckets so every item is represented.
	scopes := map[string]map[string][]box.Item{}
	scopeOrder := []string{}
	scopeSeen := map[string]bool{}
	for _, it := range drawable {
		scope := firstSymbolValue(it, box.SymScope)
		if scope == "" {
			scope = "(no-scope)"
		}
		topic := firstSymbolValue(it, box.SymTopic)
		if topic == "" {
			topic = "(no-topic)"
		}
		if !scopeSeen[scope] {
			scopeOrder = append(scopeOrder, scope)
			scopeSeen[scope] = true
			scopes[scope] = map[string][]box.Item{}
		}
		scopes[scope][topic] = append(scopes[scope][topic], it)
	}
	sort.Strings(scopeOrder)

	for _, scope := range scopeOrder {
		b.WriteString("    ")
		if strings.HasPrefix(scope, "(") {
			b.WriteString(scope)
		} else {
			b.WriteString("Scope:")
			b.WriteString(mermaidEscape(scope))
		}
		b.WriteByte('\n')
		topicOrder := []string{}
		for t := range scopes[scope] {
			topicOrder = append(topicOrder, t)
		}
		sort.Strings(topicOrder)
		for _, topic := range topicOrder {
			b.WriteString("      ")
			if strings.HasPrefix(topic, "(") {
				b.WriteString(topic)
			} else {
				b.WriteString("Topic:")
				b.WriteString(mermaidEscape(topic))
			}
			b.WriteByte('\n')
			for _, it := range scopes[scope][topic] {
				b.WriteString("        ")
				b.WriteString(mindItemLabel(it))
				b.WriteByte('\n')
			}
		}
	}
	return b.String(), nil
}

// mindItemLabel renders the leaf line for a single item in the mindmap.
// Format: `<id-tail-8> [<kind>/<status>]`. Missing kind/status fall back to `-`.
func mindItemLabel(it box.Item) string {
	k := firstSymbolValue(it, box.SymKind)
	if k == "" {
		k = "-"
	}
	s := firstSymbolValue(it, box.SymStatus)
	if s == "" {
		s = "-"
	}
	return shortID(it.ID, 8) + " " + mermaidEscape("["+k+"/"+s+"]")
}
