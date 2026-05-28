package box

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// R6 sphere model tests — both Service.SetBoxLabels and Service.Globes
// over MemoryStore. FileStore-backed coverage runs through the wider
// integration tests (file_store_test.go does box CRUD already).

func newSphereSvc(t *testing.T) *Service {
	t.Helper()
	return NewService(NewMemoryStore())
}

func mkBox(t *testing.T, svc *Service, key, owner string, labels map[string]string) Box {
	t.Helper()
	b, err := svc.CreateBox(context.Background(), CreateBoxRequest{
		Key: key, OwnerID: owner, Labels: labels,
	})
	if err != nil {
		t.Fatalf("CreateBox(%s): %v", key, err)
	}
	return b
}

// --- SetBoxLabels ---------------------------------------------------------

func TestSetBoxLabels_MergeAddsAndOverwrites(t *testing.T) {
	ctx := context.Background()
	svc := newSphereSvc(t)
	b := mkBox(t, svc, "k", "alice", map[string]string{"keep": "v1", "drop_later": "x"})

	updated, err := svc.SetBoxLabels(ctx, "alice", b.ID, map[string]string{
		SphereLabelKey: "engineering",
		"keep":         "v2", // overwrite
	}, "merge")
	if err != nil {
		t.Fatalf("SetBoxLabels: %v", err)
	}
	if updated.Labels[SphereLabelKey] != "engineering" {
		t.Errorf("sphere label not set: %v", updated.Labels)
	}
	if updated.Labels["keep"] != "v2" {
		t.Errorf("merge should overwrite 'keep'; got %q", updated.Labels["keep"])
	}
	if updated.Labels["drop_later"] != "x" {
		t.Errorf("merge should preserve unrelated keys; got %v", updated.Labels)
	}
	if updated.Version <= b.Version {
		t.Errorf("Version should bump; was %d now %d", b.Version, updated.Version)
	}
}

func TestSetBoxLabels_MergeEmptyDeletes(t *testing.T) {
	ctx := context.Background()
	svc := newSphereSvc(t)
	b := mkBox(t, svc, "k", "alice", map[string]string{"foo": "bar", "baz": "qux"})

	updated, err := svc.SetBoxLabels(ctx, "alice", b.ID, map[string]string{
		"foo": "", // delete
	}, "merge")
	if err != nil {
		t.Fatalf("SetBoxLabels: %v", err)
	}
	if _, ok := updated.Labels["foo"]; ok {
		t.Errorf("merge + empty value should delete; got %v", updated.Labels)
	}
	if updated.Labels["baz"] != "qux" {
		t.Errorf("merge must preserve unrelated keys")
	}
}

func TestSetBoxLabels_Replace(t *testing.T) {
	ctx := context.Background()
	svc := newSphereSvc(t)
	b := mkBox(t, svc, "k", "alice", map[string]string{"old1": "1", "old2": "2"})

	updated, err := svc.SetBoxLabels(ctx, "alice", b.ID, map[string]string{
		SphereLabelKey: "marketing",
	}, "replace")
	if err != nil {
		t.Fatalf("SetBoxLabels: %v", err)
	}
	if len(updated.Labels) != 1 || updated.Labels[SphereLabelKey] != "marketing" {
		t.Errorf("replace should leave only the new map; got %v", updated.Labels)
	}
}

func TestSetBoxLabels_CallerOwnerGate(t *testing.T) {
	ctx := context.Background()
	svc := newSphereSvc(t)
	b := mkBox(t, svc, "k", "alice", nil)

	_, err := svc.SetBoxLabels(ctx, "mallory", b.ID, map[string]string{
		SphereLabelKey: "stolen",
	}, "merge")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	if !strings.Contains(err.Error(), "caller_owner_mismatch") {
		t.Errorf("R0.23 reason payload should be preserved on SetBoxLabels too; got %q", err.Error())
	}
}

func TestSetBoxLabels_InvalidMode(t *testing.T) {
	ctx := context.Background()
	svc := newSphereSvc(t)
	b := mkBox(t, svc, "k", "alice", nil)

	_, err := svc.SetBoxLabels(ctx, "alice", b.ID, map[string]string{"x": "y"}, "garbage")
	if !errors.Is(err, ErrValidation) {
		t.Errorf("unknown mode should be ErrValidation; got %v", err)
	}
}

// --- Globes ---------------------------------------------------------------

func TestGlobes_GroupsByLabel(t *testing.T) {
	ctx := context.Background()
	svc := newSphereSvc(t)
	mkBox(t, svc, "k1", "alice", map[string]string{SphereLabelKey: "engineering"})
	mkBox(t, svc, "k2", "alice", map[string]string{SphereLabelKey: "engineering"})
	mkBox(t, svc, "k3", "alice", map[string]string{SphereLabelKey: "marketing"})
	mkBox(t, svc, "k4", "alice", nil) // unassigned

	rep, err := svc.Globes(ctx, "alice", GlobesOptions{})
	if err != nil {
		t.Fatalf("Globes: %v", err)
	}
	if rep.TotalBoxes != 4 {
		t.Errorf("total: got %d want 4", rep.TotalBoxes)
	}
	if len(rep.Globes) != 2 {
		t.Fatalf("expected 2 named spheres, got %d", len(rep.Globes))
	}
	if rep.Globes[0].Sphere != "engineering" || rep.Globes[0].BoxCount != 2 {
		t.Errorf("engineering: %+v", rep.Globes[0])
	}
	if rep.Globes[1].Sphere != "marketing" || rep.Globes[1].BoxCount != 1 {
		t.Errorf("marketing: %+v", rep.Globes[1])
	}
	if rep.Unassigned == nil || rep.Unassigned.BoxCount != 1 {
		t.Errorf("unassigned: %+v", rep.Unassigned)
	}
}

func TestGlobes_CallerScoped(t *testing.T) {
	ctx := context.Background()
	svc := newSphereSvc(t)
	mkBox(t, svc, "mine", "alice", map[string]string{SphereLabelKey: "x"})
	mkBox(t, svc, "yours", "bob", map[string]string{SphereLabelKey: "x"})

	rep, err := svc.Globes(ctx, "alice", GlobesOptions{})
	if err != nil {
		t.Fatalf("Globes: %v", err)
	}
	if rep.TotalBoxes != 1 || len(rep.Globes) != 1 || rep.Globes[0].BoxCount != 1 {
		t.Errorf("caller-scope leak; got %+v", rep)
	}
}

func TestGlobes_StableAlphabeticalOrder(t *testing.T) {
	ctx := context.Background()
	svc := newSphereSvc(t)
	mkBox(t, svc, "a", "alice", map[string]string{SphereLabelKey: "zeta"})
	mkBox(t, svc, "b", "alice", map[string]string{SphereLabelKey: "alpha"})
	mkBox(t, svc, "c", "alice", map[string]string{SphereLabelKey: "mu"})

	rep, _ := svc.Globes(ctx, "alice", GlobesOptions{})
	got := []string{rep.Globes[0].Sphere, rep.Globes[1].Sphere, rep.Globes[2].Sphere}
	want := []string{"alpha", "mu", "zeta"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("alphabetical order: got %v want %v", got, want)
			break
		}
	}
}

func TestGlobes_CustomSphereLabel(t *testing.T) {
	ctx := context.Background()
	svc := newSphereSvc(t)
	mkBox(t, svc, "k", "alice", map[string]string{"team": "infra"})

	rep, err := svc.Globes(ctx, "alice", GlobesOptions{SphereLabel: "team"})
	if err != nil {
		t.Fatalf("Globes: %v", err)
	}
	if rep.SphereLabel != "team" {
		t.Errorf("custom label not honoured: %q", rep.SphereLabel)
	}
	if len(rep.Globes) != 1 || rep.Globes[0].Sphere != "infra" {
		t.Errorf("expected one infra sphere; got %+v", rep.Globes)
	}
}

func TestGlobes_MaxBoxesCap(t *testing.T) {
	ctx := context.Background()
	svc := newSphereSvc(t)
	// 15 boxes in same sphere
	for i := 0; i < 15; i++ {
		mkBox(t, svc, "k"+string(rune('a'+i)), "alice", map[string]string{SphereLabelKey: "big"})
	}
	rep, _ := svc.Globes(ctx, "alice", GlobesOptions{MaxBoxesPerSphere: 5})
	if len(rep.Globes) != 1 {
		t.Fatalf("expected 1 sphere, got %d", len(rep.Globes))
	}
	if rep.Globes[0].BoxCount != 15 {
		t.Errorf("BoxCount should be full count regardless of cap; got %d", rep.Globes[0].BoxCount)
	}
	if len(rep.Globes[0].Boxes) != 5 {
		t.Errorf("Boxes cap should be 5; got %d", len(rep.Globes[0].Boxes))
	}
}
