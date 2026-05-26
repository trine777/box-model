package box

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// --- R0.7.1: symbol engine via Service ---

func setupSymBox(t *testing.T, ownerID, key string) (context.Context, *Service, Box) {
	t.Helper()
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: key, OwnerID: ownerID})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, svc, b
}

// TestStoreAcceptsSymbols stores an item carrying a valid Symbols slice and
// verifies the slice is round-tripped through the Service.
func TestStoreAcceptsSymbols(t *testing.T) {
	ctx, svc, b := setupSymBox(t, "alice", "sym-accept")
	syms := []Symbol{
		{Kind: SymKind, Value: "D"},
		{Kind: SymStatus, Value: "✓"},
		{Kind: SymTopic, Value: "billing"},
	}
	got, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "s/v1",
		Kind:       "decision",
		SourceType: "discussion",
		StorageURI: "row://d/s",
		Content:    json.RawMessage(`{"x":1}`),
		Symbols:    syms,
	})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if len(got.Symbols) != 3 {
		t.Fatalf("expected 3 symbols, got %d", len(got.Symbols))
	}
	if got.Symbols[0].Kind != SymKind || got.Symbols[0].Value != "D" {
		t.Fatalf("unexpected first symbol: %#v", got.Symbols[0])
	}
}

// TestStoreRejectsInvalidSymbols rejects a non-whitelisted SymKind value.
func TestStoreRejectsInvalidSymbols(t *testing.T) {
	ctx, svc, b := setupSymBox(t, "alice", "sym-reject")
	_, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "s/v1",
		Kind:       "decision",
		SourceType: "discussion",
		StorageURI: "row://d/s",
		Content:    json.RawMessage(`{"x":1}`),
		Symbols:    []Symbol{{Kind: SymKind, Value: "Z"}},
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
}

// TestStoreSymbolsOptional confirms an item with no Symbols field still works
// (legacy data path).
func TestStoreSymbolsOptional(t *testing.T) {
	ctx, svc, b := setupSymBox(t, "alice", "sym-optional")
	got, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "s/v1",
		Kind:       "decision",
		SourceType: "discussion",
		StorageURI: "row://d/s",
		Content:    json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatalf("Store no symbols: %v", err)
	}
	if len(got.Symbols) != 0 {
		t.Fatalf("expected zero symbols, got %#v", got.Symbols)
	}
}

// TestReplaceItemSymbols verifies the new revision carries the new Symbols.
func TestReplaceItemSymbols(t *testing.T) {
	ctx, svc, b := setupSymBox(t, "alice", "sym-replace")
	v1, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "s/v1",
		Kind:       "decision",
		SourceType: "discussion",
		StorageURI: "row://d/s",
		Content:    json.RawMessage(`{"x":1}`),
		Symbols:    []Symbol{{Kind: SymKind, Value: "D"}, {Kind: SymStatus, Value: "?"}},
	})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	v2, err := svc.ReplaceItem(ctx, "alice", v1.ID, StoreRequest{
		Kind:       "decision",
		SourceType: "discussion",
		StorageURI: "row://d/s2",
		Content:    json.RawMessage(`{"x":2}`),
		Symbols:    []Symbol{{Kind: SymKind, Value: "D"}, {Kind: SymStatus, Value: "✓"}},
	})
	if err != nil {
		t.Fatalf("ReplaceItem: %v", err)
	}
	if len(v2.Symbols) != 2 {
		t.Fatalf("expected 2 symbols, got %d", len(v2.Symbols))
	}
	if v2.Symbols[1].Value != "✓" {
		t.Fatalf("expected updated status ✓, got %q", v2.Symbols[1].Value)
	}
}

// TestTraceByKind queries by SymKind value.
func TestTraceByKind(t *testing.T) {
	ctx, svc, b := setupSymBox(t, "alice", "sym-trace-kind")
	mk := func(idem, kindSym string) {
		_, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
			IdemKey:    idem,
			Kind:       "any",
			SourceType: "queue",
			StorageURI: "row://q/" + idem,
			Content:    json.RawMessage(`{}`),
			Symbols:    []Symbol{{Kind: SymKind, Value: kindSym}},
		})
		if err != nil {
			t.Fatalf("Store %s: %v", idem, err)
		}
	}
	mk("a", "R")
	mk("b", "D")
	mk("c", "R")
	got, err := svc.Trace(ctx, "alice", b.Key, SymbolQuery{
		Kind:  []SymbolKind{SymKind},
		Value: []string{"R"},
	})
	if err != nil {
		t.Fatalf("Trace: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(got))
	}
}

// TestTraceByRelation queries by SymRelation with a specific Ref.
func TestTraceByRelation(t *testing.T) {
	ctx, svc, b := setupSymBox(t, "alice", "sym-trace-rel")
	itemB, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey: "b", Kind: "task", SourceType: "queue", StorageURI: "row://q/b",
		Content: json.RawMessage(`{}`),
		Symbols: []Symbol{{Kind: SymKind, Value: "T"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	itemA, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey: "a", Kind: "task", SourceType: "queue", StorageURI: "row://q/a",
		Content: json.RawMessage(`{}`),
		Symbols: []Symbol{
			{Kind: SymKind, Value: "T"},
			{Kind: SymRelation, Value: "&", Ref: itemB.ID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Trace(ctx, "alice", b.Key, SymbolQuery{
		Kind:  []SymbolKind{SymRelation},
		Value: []string{"&"},
		Ref:   itemB.ID,
	})
	if err != nil {
		t.Fatalf("Trace: %v", err)
	}
	if len(got) != 1 || got[0].ID != itemA.ID {
		t.Fatalf("expected exactly itemA, got %#v", got)
	}
}

// TestTraceAcrossAllBoxes — boxKey="" means cross-all-boxes.
func TestTraceAcrossAllBoxes(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b1, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "b1", OwnerID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	b2, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "b2", OwnerID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	store := func(box Box, idem string) {
		_, err := svc.Store(ctx, "alice", box.ID, StoreRequest{
			IdemKey: idem, Kind: "any", SourceType: "queue",
			StorageURI: "row://q/" + idem, Content: json.RawMessage(`{}`),
			Symbols: []Symbol{{Kind: SymKind, Value: "R"}},
		})
		if err != nil {
			t.Fatalf("Store: %v", err)
		}
	}
	store(b1, "a")
	store(b2, "b")
	got, err := svc.Trace(ctx, "alice", "", SymbolQuery{Kind: []SymbolKind{SymKind}, Value: []string{"R"}})
	if err != nil {
		t.Fatalf("Trace: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 cross-box matches, got %d", len(got))
	}
}

// TestNeighbors1Hop: a -> b -> c. Neighbors(a, 1) = {a, b}; Neighbors(a, 2) = {a, b, c}.
func TestNeighbors1Hop(t *testing.T) {
	ctx, svc, b := setupSymBox(t, "alice", "sym-neigh")
	c, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey: "c", Kind: "task", SourceType: "queue", StorageURI: "row://q/c",
		Content: json.RawMessage(`{}`),
		Symbols: []Symbol{{Kind: SymKind, Value: "T"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	bb, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey: "b", Kind: "task", SourceType: "queue", StorageURI: "row://q/b",
		Content: json.RawMessage(`{}`),
		Symbols: []Symbol{
			{Kind: SymKind, Value: "T"},
			{Kind: SymRelation, Value: "&", Ref: c.ID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey: "a", Kind: "task", SourceType: "queue", StorageURI: "row://q/a",
		Content: json.RawMessage(`{}`),
		Symbols: []Symbol{
			{Kind: SymKind, Value: "T"},
			{Kind: SymRelation, Value: "&", Ref: bb.ID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sub1, err := svc.Neighbors(ctx, "alice", a.ID, 1)
	if err != nil {
		t.Fatalf("Neighbors 1: %v", err)
	}
	if len(sub1.Nodes) != 2 {
		t.Fatalf("expected 2 nodes at 1 hop, got %d (%v)", len(sub1.Nodes), sub1.Nodes)
	}
	sub2, err := svc.Neighbors(ctx, "alice", a.ID, 2)
	if err != nil {
		t.Fatalf("Neighbors 2: %v", err)
	}
	if len(sub2.Nodes) != 3 {
		t.Fatalf("expected 3 nodes at 2 hops, got %d", len(sub2.Nodes))
	}
}

// TestNeighborsIncomingEdges: a -> b ⇒ Neighbors(b, 1) must include a (in-edge).
func TestNeighborsIncomingEdges(t *testing.T) {
	ctx, svc, b := setupSymBox(t, "alice", "sym-neigh-in")
	bb, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey: "b", Kind: "task", SourceType: "queue", StorageURI: "row://q/b",
		Content: json.RawMessage(`{}`),
		Symbols: []Symbol{{Kind: SymKind, Value: "T"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey: "a", Kind: "task", SourceType: "queue", StorageURI: "row://q/a",
		Content: json.RawMessage(`{}`),
		Symbols: []Symbol{
			{Kind: SymKind, Value: "T"},
			{Kind: SymRelation, Value: "&", Ref: bb.ID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := svc.Neighbors(ctx, "alice", bb.ID, 1)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	foundA := false
	for _, n := range sub.Nodes {
		if n.ItemID == a.ID {
			foundA = true
			break
		}
	}
	if !foundA {
		t.Fatalf("expected incoming edge to surface item a in subgraph, got %#v", sub.Nodes)
	}
}

// TestNeighborsHopsBoundary asserts hops ∉ [1,5] is ErrValidation.
func TestNeighborsHopsBoundary(t *testing.T) {
	ctx, svc, b := setupSymBox(t, "alice", "sym-neigh-bound")
	it, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey: "x", Kind: "task", SourceType: "queue", StorageURI: "row://q/x",
		Content: json.RawMessage(`{}`),
		Symbols: []Symbol{{Kind: SymKind, Value: "T"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Neighbors(ctx, "alice", it.ID, 0); !errors.Is(err, ErrValidation) {
		t.Fatalf("hops=0 expected ErrValidation, got %v", err)
	}
	if _, err := svc.Neighbors(ctx, "alice", it.ID, 6); !errors.Is(err, ErrValidation) {
		t.Fatalf("hops=6 expected ErrValidation, got %v", err)
	}
}

// TestLegendOfBuiltin asserts that EnsureSymbolBootstrap populates the
// __symbols__ box and LegendOf returns the matching item.
func TestLegendOfBuiltin(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	if err := svc.EnsureSymbolBootstrap(ctx); err != nil {
		t.Fatalf("EnsureSymbolBootstrap: %v", err)
	}
	item, err := svc.LegendOf(ctx, "system", Symbol{Kind: SymKind, Value: "D"})
	if err != nil {
		t.Fatalf("LegendOf: %v", err)
	}
	var payload struct {
		Value   string `json:"value"`
		Kind    string `json:"kind"`
		Meaning string `json:"meaning"`
	}
	if err := json.Unmarshal(item.Content, &payload); err != nil {
		t.Fatalf("unmarshal legend content: %v", err)
	}
	if payload.Meaning != "Decision" {
		t.Fatalf("expected meaning=Decision, got %q", payload.Meaning)
	}
}

// TestEnsureSymbolBootstrapIdempotent runs bootstrap twice and confirms the
// __symbols__ box still has exactly 25 items.
func TestEnsureSymbolBootstrapIdempotent(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	if err := svc.EnsureSymbolBootstrap(ctx); err != nil {
		t.Fatalf("EnsureSymbolBootstrap first: %v", err)
	}
	if err := svc.EnsureSymbolBootstrap(ctx); err != nil {
		t.Fatalf("EnsureSymbolBootstrap second: %v", err)
	}
	box, err := svc.GetBoxByKey(ctx, "system", "__symbols__")
	if err != nil {
		t.Fatalf("GetBoxByKey: %v", err)
	}
	items, err := svc.Browse(ctx, box.ID, BrowseFilter{Limit: 1000})
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if len(items) != 25 {
		t.Fatalf("expected 25 symbol items, got %d", len(items))
	}
}
