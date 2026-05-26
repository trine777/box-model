package view

import (
	"fmt"
	"sort"
	"strings"

	"github.com/windborneos/box-model/box"
)

// treeRenderer emits a mermaid `graph TD` (top-down) document showing only
// the hierarchy relations: `<` (refines) and `⊃` (has-part). All other
// relation kinds (`&` `>` `|` `≈`) are intentionally ignored — this view's
// job is to display containment, not dependency.
//
// Output is intentionally NON-JSON; first line is `graph TD`.
type treeRenderer struct{}

// hierarchyRels is the closed set of relation literals walked by treeRenderer.
var hierarchyRels = map[string]struct{}{
	"<": {}, // refines
	"⊃": {}, // has-part
}

// Render produces the mermaid source.
func (r *treeRenderer) Render(items []box.Item, opts RenderOptions) (string, error) {
	// tree only accepts the kind axis (sub-tree grouping) or no axis.
	switch opts.Axis {
	case "", AxisKind:
		// ok
	default:
		return "", fmt.Errorf("%w: tree only supports kind", ErrInvalidAxis)
	}

	var b strings.Builder
	b.WriteString("graph TD\n")

	drawable := filterDrawable(items)
	total := len(drawable)
	if total > graphNodeCap {
		writeTruncationNote(&b, total, graphNodeCap)
		drawable = drawable[:graphNodeCap]
	}

	// Build adjacency on hierarchy edges only.
	byID := make(map[string]box.Item, len(drawable))
	for _, it := range drawable {
		byID[it.ID] = it
	}
	// children[parent] = list of child item ids
	children := make(map[string][]string, len(drawable))
	// indegree tracks how many hierarchy edges point at each id.
	indegree := make(map[string]int, len(drawable))
	for _, it := range drawable {
		for _, sym := range it.Symbols {
			if sym.Kind != box.SymRelation {
				continue
			}
			if _, ok := hierarchyRels[sym.Value]; !ok {
				continue
			}
			if _, ok := byID[sym.Ref]; !ok {
				continue
			}
			// Edge: it.ID --(<|⊃)--> sym.Ref.
			// For tree semantics, sym.Ref is the parent and it.ID the child
			// (the source is "refined by" / "has-part of" the target).
			children[sym.Ref] = append(children[sym.Ref], it.ID)
			indegree[it.ID]++
		}
	}

	// Roots: items with no incoming hierarchy edge. If everything is a
	// leaf (no hierarchy edges anywhere), every item is its own root.
	var roots []string
	for _, it := range drawable {
		if indegree[it.ID] == 0 {
			roots = append(roots, it.ID)
		}
	}
	sort.Strings(roots)

	// Emit nodes; we walk via BFS from roots so order is deterministic and
	// nodes that aren't reachable from any root (shouldn't happen with the
	// in-degree definition) still appear as roots.
	emitted := make(map[string]struct{}, len(drawable))
	if opts.Axis == AxisKind {
		writeTreeGroupedByKind(&b, drawable, byID, children, roots, emitted)
	} else {
		writeTreeFromRoots(&b, byID, children, roots, emitted)
	}
	// Emit any orphaned items (cycles or unreachable) so the doc lists
	// every drawable item.
	for _, it := range drawable {
		if _, done := emitted[it.ID]; done {
			continue
		}
		writeMermaidNode(&b, it)
		emitted[it.ID] = struct{}{}
	}
	return b.String(), nil
}

// writeTreeFromRoots walks each root in turn (BFS) and emits nodes plus
// hierarchy edges. Each non-root child receives an edge from its first
// observed parent so the diagram stays a tree (cycles, should they occur,
// are pruned at the second visit).
func writeTreeFromRoots(b *strings.Builder, byID map[string]box.Item, children map[string][]string, roots []string, emitted map[string]struct{}) {
	for _, rootID := range roots {
		queue := []string{rootID}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			if _, done := emitted[id]; done {
				continue
			}
			it, ok := byID[id]
			if !ok {
				continue
			}
			writeMermaidNode(b, it)
			emitted[id] = struct{}{}
			kids := append([]string(nil), children[id]...)
			sort.Strings(kids)
			for _, cid := range kids {
				if _, done := emitted[cid]; done {
					continue
				}
				writeMermaidEdge(b, id, cid, "<")
				queue = append(queue, cid)
			}
		}
	}
}

// writeTreeGroupedByKind emits one mermaid subgraph per SymKind value, with
// the items of that kind drawn underneath. Within each kind we use the same
// root walker as the default mode so hierarchy edges remain present.
func writeTreeGroupedByKind(b *strings.Builder, drawable []box.Item, byID map[string]box.Item, children map[string][]string, roots []string, emitted map[string]struct{}) {
	byKind := map[string][]string{}
	kinds := []string{}
	seen := map[string]bool{}
	for _, it := range drawable {
		k := firstSymbolValue(it, box.SymKind)
		if k == "" {
			k = "-"
		}
		if !seen[k] {
			kinds = append(kinds, k)
			seen[k] = true
		}
		byKind[k] = append(byKind[k], it.ID)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		b.WriteString("  subgraph kind_" + k + "[\"Kind:" + mermaidEscape(k) + "\"]\n")
		// Determine kind-scoped roots: an item whose hierarchy parent is
		// outside this kind, or who has no parent, counts as a root here.
		localRoots := []string{}
		rootSet := map[string]bool{}
		for _, id := range roots {
			if it := byID[id]; firstSymbolValue(it, box.SymKind) == k || (k == "-" && firstSymbolValue(it, box.SymKind) == "") {
				localRoots = append(localRoots, id)
				rootSet[id] = true
			}
		}
		for _, id := range byKind[k] {
			if rootSet[id] {
				continue
			}
			// If no parent within the same kind, treat as local root.
			parentInSame := false
			for parent, kids := range children {
				if firstSymbolValue(byID[parent], box.SymKind) != k {
					continue
				}
				for _, c := range kids {
					if c == id {
						parentInSame = true
						break
					}
				}
				if parentInSame {
					break
				}
			}
			if !parentInSame {
				localRoots = append(localRoots, id)
			}
		}
		sort.Strings(localRoots)
		writeTreeFromRoots(b, byID, children, localRoots, emitted)
		b.WriteString("  end\n")
	}
}

// filterDrawable returns items with Status != "deleted". (IsLatest is left
// untouched so callers that synthesise items without filling IsLatest still
// see them; the Browse path provides only latest items by default.)
func filterDrawable(items []box.Item) []box.Item {
	out := make([]box.Item, 0, len(items))
	for _, it := range items {
		if it.Status == "deleted" {
			continue
		}
		out = append(out, it)
	}
	return out
}
