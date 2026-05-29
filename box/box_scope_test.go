package box

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// R13: box-level SymScope symbol — naming/sphere becomes a first-class
// symbol (ValidateSymbol-gated, box_trace-reachable) instead of a free
// label string.

func TestBoxScopeSymbol_CreateAndRead(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b, err := svc.CreateBox(ctx, CreateBoxRequest{
		Key: "dev-x", OwnerID: "alice",
		Symbols: []Symbol{{Kind: SymScope, Value: "dev"}},
	})
	if err != nil {
		t.Fatalf("CreateBox with scope symbol: %v", err)
	}
	if BoxScopeOf(b) != "dev" {
		t.Errorf("BoxScopeOf: got %q want dev", BoxScopeOf(b))
	}
}

func TestBoxScopeSymbol_RejectsBadScope(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	// scope value with a space violates scopeTopicRe → ValidateSymbol error
	_, err := svc.CreateBox(ctx, CreateBoxRequest{
		Key: "bad", OwnerID: "alice",
		Symbols: []Symbol{{Kind: SymScope, Value: "not valid"}},
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation for bad scope value, got %v", err)
	}
}

func TestSetBoxSymbols_AndGate(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b, _ := svc.CreateBox(ctx, CreateBoxRequest{Key: "k", OwnerID: "alice"})
	// owner can set
	out, err := svc.SetBoxSymbols(ctx, "alice", b.ID, []Symbol{{Kind: SymScope, Value: "km"}})
	if err != nil {
		t.Fatalf("SetBoxSymbols: %v", err)
	}
	if BoxScopeOf(out) != "km" {
		t.Errorf("scope not set: %v", out.Symbols)
	}
	if out.Version <= b.Version {
		t.Errorf("version should bump")
	}
	// non-owner blocked, with reason
	_, err = svc.SetBoxSymbols(ctx, "mallory", b.ID, []Symbol{{Kind: SymScope, Value: "stolen"}})
	if !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "caller_owner_mismatch") {
		t.Errorf("expected forbidden+reason, got %v", err)
	}
}

func TestBoxScopeOf_LabelFallback(t *testing.T) {
	// Migration compatibility: a box with no scope symbol but the legacy
	// __op:sphere label still resolves via BoxScopeOf.
	b := Box{Labels: map[string]string{"__op:sphere": "ops"}}
	if BoxScopeOf(b) != "ops" {
		t.Errorf("label fallback failed: %q", BoxScopeOf(b))
	}
	// symbol wins over label when both present
	b.Symbols = []Symbol{{Kind: SymScope, Value: "dev"}}
	if BoxScopeOf(b) != "dev" {
		t.Errorf("symbol should win over label: %q", BoxScopeOf(b))
	}
}

func TestGlobes_GroupsByScopeSymbol(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	mkScoped := func(key, owner, scope string) {
		_, err := svc.CreateBox(ctx, CreateBoxRequest{
			Key: key, OwnerID: owner,
			Symbols: []Symbol{{Kind: SymScope, Value: scope}},
		})
		if err != nil {
			t.Fatalf("CreateBox %s: %v", key, err)
		}
	}
	mkScoped("a", "alice", "dev")
	mkScoped("b", "alice", "dev")
	mkScoped("c", "alice", "km")

	rep, err := svc.Globes(ctx, "alice", GlobesOptions{})
	if err != nil {
		t.Fatalf("Globes: %v", err)
	}
	got := map[string]int{}
	for _, g := range rep.Globes {
		got[g.Sphere] = g.BoxCount
	}
	if got["dev"] != 2 || got["km"] != 1 {
		t.Errorf("globes by scope symbol wrong: %v", got)
	}
}
