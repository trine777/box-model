package box

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// SymbolKind is the meta-type of a Symbol. Box core recognizes seven kinds.
// Other kinds are rejected by ValidateSymbol.
type SymbolKind string

const (
	SymKind     SymbolKind = "kind"
	SymStatus   SymbolKind = "status"
	SymRelation SymbolKind = "relation"
	SymScope    SymbolKind = "scope"
	SymTopic    SymbolKind = "topic"
	SymPriority SymbolKind = "priority"
	SymDomain   SymbolKind = "domain"
)

// Symbol is a routing marker carried by an Item. Together, the Symbols on an
// Item form the navigation graph that Trace/Neighbors/LegendOf walk.
type Symbol struct {
	Kind  SymbolKind `json:"kind"`
	Value string     `json:"value"`
	// Ref is only meaningful for SymRelation; it points at another Item's id
	// (or, in the future, an item-uri for cross-box references).
	Ref string `json:"ref,omitempty"`
}

// Whitelists for the closed-set symbol kinds. Map-backed for O(1) membership.
var (
	validKinds = map[string]struct{}{
		"D": {}, "R": {}, "Q": {}, "H": {}, "T": {},
		"M": {}, "F": {}, "O": {}, "A": {}, "X": {},
	}
	validStatuses = map[string]struct{}{
		"?": {}, "→": {}, "✓": {}, "✗": {}, "~": {}, "◯": {},
	}
	validRelations = map[string]struct{}{
		">": {}, "<": {}, "&": {}, "|": {}, "≈": {}, "⊃": {},
	}
	validPriorities = map[string]struct{}{
		"*": {}, "**": {}, "***": {},
	}
)

// scopeTopicRe enforces a safe ASCII identifier shape for SymScope / SymTopic.
var scopeTopicRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// domainNsRe enforces lowercase identifier shape for the namespace portion of
// a SymDomain value.
var domainNsRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)

// ValidateSymbol checks a single Symbol against the per-kind whitelist and
// shape rules. See box/symbol.go for the per-kind contract.
func ValidateSymbol(sym Symbol) error {
	switch sym.Kind {
	case SymKind:
		if _, ok := validKinds[sym.Value]; !ok {
			return fmt.Errorf("%w: unknown kind symbol %q", ErrValidation, sym.Value)
		}
		if sym.Ref != "" {
			return fmt.Errorf("%w: kind symbol must not carry Ref", ErrValidation)
		}
		return nil
	case SymStatus:
		if _, ok := validStatuses[sym.Value]; !ok {
			return fmt.Errorf("%w: unknown status symbol %q", ErrValidation, sym.Value)
		}
		if sym.Ref != "" {
			return fmt.Errorf("%w: status symbol must not carry Ref", ErrValidation)
		}
		return nil
	case SymRelation:
		if _, ok := validRelations[sym.Value]; !ok {
			return fmt.Errorf("%w: unknown relation symbol %q", ErrValidation, sym.Value)
		}
		if sym.Ref == "" {
			return fmt.Errorf("%w: relation symbol requires Ref", ErrValidation)
		}
		return nil
	case SymScope, SymTopic:
		if sym.Value == "" {
			return fmt.Errorf("%w: %s symbol value is required", ErrValidation, sym.Kind)
		}
		if !scopeTopicRe.MatchString(sym.Value) {
			return fmt.Errorf("%w: %s value %q must match [A-Za-z0-9_-]+", ErrValidation, sym.Kind, sym.Value)
		}
		return nil
	case SymPriority:
		if _, ok := validPriorities[sym.Value]; !ok {
			return fmt.Errorf("%w: unknown priority symbol %q", ErrValidation, sym.Value)
		}
		return nil
	case SymDomain:
		idx := strings.Index(sym.Value, ":")
		if idx <= 0 {
			return fmt.Errorf("%w: domain value %q must be <ns>:<v>", ErrValidation, sym.Value)
		}
		ns := sym.Value[:idx]
		v := sym.Value[idx+1:]
		if !domainNsRe.MatchString(ns) {
			return fmt.Errorf("%w: domain namespace %q must match ^[a-z][a-z0-9_]{0,15}$", ErrValidation, ns)
		}
		if v == "" {
			return fmt.Errorf("%w: domain value after %q: is empty", ErrValidation, ns)
		}
		return nil
	default:
		return fmt.Errorf("%w: unknown symbol kind %q", ErrValidation, sym.Kind)
	}
}

// ValidateSymbols runs ValidateSymbol on each entry and enforces the
// "at least one SymKind" rule when the slice is non-nil. A nil slice is
// accepted for backward-compat with legacy items that pre-date the symbol
// engine; an explicit empty slice is rejected so callers cannot accidentally
// strip routing from new items.
func ValidateSymbols(syms []Symbol) error {
	if syms == nil {
		return nil
	}
	hasKind := false
	for _, s := range syms {
		if err := ValidateSymbol(s); err != nil {
			return err
		}
		if s.Kind == SymKind {
			hasKind = true
		}
	}
	if !hasKind {
		return fmt.Errorf("%w: item must carry at least one kind symbol", ErrValidation)
	}
	return nil
}

// symbolDef captures one built-in symbol's metadata so EnsureSymbolBootstrap
// can ground the __symbols__ box without an external data file.
type symbolDef struct {
	Kind     SymbolKind
	Value    string
	Meaning  string
	Examples []string
}

// traceItems implements the shared Trace logic used by MemoryStore and
// FileStore. It expects the caller to already hold the store's lock.
//
// Filter semantics:
//   - boxID == "" walks every box; otherwise restricted to byBox[boxID]
//   - latest non-deleted items only (mirrors Browse default)
//   - a symbol matches when (Kind list empty OR sym.Kind ∈ Kind) AND
//     (Value list empty OR sym.Value ∈ Value) AND
//     (query.Ref empty OR sym.Ref == query.Ref)
//   - results sorted StoredAt desc
func traceItems(items map[string]Item, byBox map[string][]string, boxID string, q SymbolQuery) []Item {
	var ids []string
	if boxID == "" {
		for _, list := range byBox {
			ids = append(ids, list...)
		}
	} else {
		ids = byBox[boxID]
	}
	out := make([]Item, 0)
	for _, id := range ids {
		it, ok := items[id]
		if !ok {
			continue
		}
		if it.Status == "deleted" || !it.IsLatest {
			continue
		}
		if !itemMatchesQuery(it, q) {
			continue
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StoredAt.After(out[j].StoredAt)
	})
	return out
}

func itemMatchesQuery(it Item, q SymbolQuery) bool {
	for _, sym := range it.Symbols {
		if len(q.Kind) > 0 {
			ok := false
			for _, k := range q.Kind {
				if sym.Kind == k {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		if len(q.Value) > 0 {
			ok := false
			for _, v := range q.Value {
				if sym.Value == v {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		if q.Ref != "" && sym.Ref != q.Ref {
			continue
		}
		return true
	}
	return false
}

// buildNeighbors implements BFS-based subgraph construction shared between
// MemoryStore and FileStore. Caller is expected to hold the relevant lock.
//
// The center node is at distance 0. SymRelation edges are followed
// bidirectionally (out via item.Symbols.Ref, in via linear scan). Items with
// Status==deleted are excluded. Same-box only — cross-box ref resolution is
// deferred to R0.7.4.
func buildNeighbors(items map[string]Item, itemID string, hops int) (Subgraph, error) {
	center, ok := items[itemID]
	if !ok || center.Status == "deleted" {
		return Subgraph{}, ErrNotFound
	}
	visited := map[string]int{itemID: 0}
	order := []string{itemID}
	edges := []EdgeRef{}
	edgeSeen := map[string]struct{}{}

	frontier := []string{itemID}
	for depth := 0; depth < hops; depth++ {
		var next []string
		for _, fid := range frontier {
			item, ok := items[fid]
			if !ok {
				continue
			}
			// Out-edges: this item's SymRelation symbols.
			for _, sym := range item.Symbols {
				if sym.Kind != SymRelation || sym.Ref == "" {
					continue
				}
				target, ok := items[sym.Ref]
				if !ok || target.Status == "deleted" {
					continue
				}
				ek := fid + "|" + sym.Ref + "|" + sym.Value
				if _, dup := edgeSeen[ek]; !dup {
					edges = append(edges, EdgeRef{From: fid, To: sym.Ref, Rel: sym.Value})
					edgeSeen[ek] = struct{}{}
				}
				if _, seen := visited[sym.Ref]; !seen {
					visited[sym.Ref] = depth + 1
					order = append(order, sym.Ref)
					next = append(next, sym.Ref)
				}
			}
			// In-edges: linear scan for items whose SymRelation.Ref points at fid.
			for otherID, other := range items {
				if otherID == fid || other.Status == "deleted" {
					continue
				}
				for _, sym := range other.Symbols {
					if sym.Kind != SymRelation || sym.Ref != fid {
						continue
					}
					ek := otherID + "|" + fid + "|" + sym.Value
					if _, dup := edgeSeen[ek]; !dup {
						edges = append(edges, EdgeRef{From: otherID, To: fid, Rel: sym.Value})
						edgeSeen[ek] = struct{}{}
					}
					if _, seen := visited[otherID]; !seen {
						visited[otherID] = depth + 1
						order = append(order, otherID)
						next = append(next, otherID)
					}
				}
			}
		}
		frontier = next
		if len(frontier) == 0 {
			break
		}
	}

	nodes := make([]NodeRef, 0, len(order))
	for _, id := range order {
		it := items[id]
		nr := NodeRef{
			ItemID:   id,
			BoxID:    it.BoxID,
			Kind:     it.Kind,
			Distance: visited[id],
		}
		for _, sym := range it.Symbols {
			if sym.Kind == SymKind && nr.KindSym == "" {
				nr.KindSym = sym.Value
			}
			if sym.Kind == SymStatus && nr.Status == "" {
				nr.Status = sym.Value
			}
		}
		nodes = append(nodes, nr)
	}
	return Subgraph{Center: itemID, Nodes: nodes, Edges: edges}, nil
}

// SymbolDefinitions enumerates every built-in symbol shipped with Box core.
// Scope/Topic/Domain symbols are open sets (user-defined values) and are NOT
// bootstrapped.
var SymbolDefinitions = []symbolDef{
	// Kind — 10
	{SymKind, "D", "Decision", []string{"决策", "architectural decision"}},
	{SymKind, "R", "Requirement", []string{"需求", "user story"}},
	{SymKind, "Q", "Question", []string{"疑问", "open question"}},
	{SymKind, "H", "Hypothesis", []string{"假设", "tentative claim"}},
	{SymKind, "T", "Task", []string{"任务", "actionable work"}},
	{SymKind, "M", "Memo", []string{"备忘", "note"}},
	{SymKind, "F", "Fact", []string{"事实", "established truth"}},
	{SymKind, "O", "Observation", []string{"观察", "logged event"}},
	{SymKind, "A", "Action", []string{"行动", "performed action"}},
	{SymKind, "X", "External", []string{"外引", "external reference"}},
	// Status — 6
	{SymStatus, "?", "Open / not started", []string{}},
	{SymStatus, "→", "Work in progress", []string{}},
	{SymStatus, "✓", "Done / completed", []string{}},
	{SymStatus, "✗", "Rejected", []string{}},
	{SymStatus, "~", "Blocked", []string{}},
	{SymStatus, "◯", "Archived", []string{}},
	// Relation — 6
	{SymRelation, ">", "Supersedes (取代)", []string{}},
	{SymRelation, "<", "Refines / refined-by", []string{}},
	{SymRelation, "&", "Depends-on", []string{}},
	{SymRelation, "|", "Alternative-to", []string{}},
	{SymRelation, "≈", "Similar-to", []string{}},
	{SymRelation, "⊃", "Has-part / contains", []string{}},
	// Priority — 3
	{SymPriority, "*", "Low priority", []string{}},
	{SymPriority, "**", "Medium priority", []string{}},
	{SymPriority, "***", "High priority", []string{}},
}
