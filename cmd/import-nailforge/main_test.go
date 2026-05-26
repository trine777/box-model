package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/windborneos/box-model/box"
)

// writeMockWarehouse lays down a small but representative warehouse tree under
// a fresh tempdir and returns its absolute path. The structure mirrors
// NailForge's real layout but stays minimal:
//
//	<root>/data_engineering_forge/data_lake_organizer/family_map.yaml
//	<root>/data_engineering_forge/data_lake_organizer/lineage.yaml
//	<root>/data_engineering_forge/data_lake_organizer/versions/data_lake_organizer.S0.1-E0-A0-L1.yaml  (organ_nail)
//	<root>/data_engineering_forge/data_lake_organizer/versions/extract_lake_requirements.S0.1-E0-A0-L1.yaml  (action_nail)
func writeMockWarehouse(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	famDir := filepath.Join(root, "data_engineering_forge", "data_lake_organizer")
	verDir := filepath.Join(famDir, "versions")
	if err := os.MkdirAll(verDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writes := map[string]string{
		filepath.Join(famDir, "family_map.yaml"): `family:
  id: data_lake_organizer
  name: 数据湖治理家族
  parent_petal: knowledge_modeling
  parent_organ: xingkongzuo
  lifecycle:
    status: active
`,
		filepath.Join(famDir, "lineage.yaml"): `lineage:
  family_id: data_lake_organizer
  members:
    - nail_id: extract_lake_requirements
      type: action_nail
`,
		filepath.Join(verDir, "data_lake_organizer.S0.1-E0-A0-L1.yaml"): `nail:
  id: data_lake_organizer
  type: organ_nail
  atom: "组合 a1 → a2 → a3 → a4"
`,
		filepath.Join(verDir, "extract_lake_requirements.S0.1-E0-A0-L1.yaml"): `nail:
  id: extract_lake_requirements
  type: action_nail
  atom: "构(extract, D·t·m → K·g·m)"
`,
	}
	for path, content := range writes {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return root
}

// runImport is a test helper: drives run() and returns the captured stdout.
func runImport(t *testing.T, cfg config) (string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	if err := run(cfg, &out, &errb); err != nil {
		t.Fatalf("run: %v\nstderr=%s", err, errb.String())
	}
	return out.String(), errb.String()
}

func openSvc(t *testing.T, boxHome string) (*box.Service, func()) {
	t.Helper()
	fs, err := box.OpenFileStore(boxHome)
	if err != nil {
		t.Fatal(err)
	}
	return box.NewService(fs), func() { _ = fs.Close() }
}

func TestImportFromEmptyBox(t *testing.T) {
	warehouse := writeMockWarehouse(t)
	boxHome := t.TempDir()

	runImport(t, config{
		warehouse: warehouse,
		boxKey:    "nail-index-test",
		boxHome:   boxHome,
		owner:     "trine",
	})

	svc, done := openSvc(t, boxHome)
	defer done()
	b, err := svc.GetBoxByKey(context.Background(), "trine", "nail-index-test")
	if err != nil {
		t.Fatal(err)
	}
	items, err := svc.Browse(context.Background(), b.ID, box.BrowseFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}
}

func TestImportIdempotent(t *testing.T) {
	warehouse := writeMockWarehouse(t)
	boxHome := t.TempDir()
	cfg := config{
		warehouse: warehouse,
		boxKey:    "nail-index-test",
		boxHome:   boxHome,
		owner:     "trine",
	}
	runImport(t, cfg)
	out, _ := runImport(t, cfg)
	if !strings.Contains(out, "skipped=4") {
		t.Fatalf("expected skipped=4 on second run, got:\n%s", out)
	}
	if !strings.Contains(out, "created=0") {
		t.Fatalf("expected created=0 on second run, got:\n%s", out)
	}
	if !strings.Contains(out, "updated=0") {
		t.Fatalf("expected updated=0 on second run, got:\n%s", out)
	}
}

func TestImportDetectsContentChange(t *testing.T) {
	warehouse := writeMockWarehouse(t)
	boxHome := t.TempDir()
	cfg := config{
		warehouse: warehouse,
		boxKey:    "nail-index-test",
		boxHome:   boxHome,
		owner:     "trine",
	}
	runImport(t, cfg)

	// Mutate one yaml — bump the atom string in extract_lake_requirements.
	target := filepath.Join(warehouse, "data_engineering_forge", "data_lake_organizer",
		"versions", "extract_lake_requirements.S0.1-E0-A0-L1.yaml")
	newBody := `nail:
  id: extract_lake_requirements
  type: action_nail
  atom: "构(extract, D·t·m → K·g·m·v2)"
`
	if err := os.WriteFile(target, []byte(newBody), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _ := runImport(t, cfg)
	if !strings.Contains(out, "updated=1") {
		t.Fatalf("expected updated=1, got:\n%s", out)
	}
	if !strings.Contains(out, "skipped=3") {
		t.Fatalf("expected skipped=3, got:\n%s", out)
	}
	if !strings.Contains(out, "created=0") {
		t.Fatalf("expected created=0, got:\n%s", out)
	}
}

func TestImportKindMapping(t *testing.T) {
	warehouse := writeMockWarehouse(t)
	boxHome := t.TempDir()
	runImport(t, config{
		warehouse: warehouse,
		boxKey:    "nail-index-test",
		boxHome:   boxHome,
		owner:     "trine",
	})

	svc, done := openSvc(t, boxHome)
	defer done()
	b, err := svc.GetBoxByKey(context.Background(), "trine", "nail-index-test")
	if err != nil {
		t.Fatal(err)
	}
	items, err := svc.Browse(context.Background(), b.ID, box.BrowseFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"nail_family":  1,
		"nail_lineage": 1,
		"organ_nail":   1,
		"action_nail":  1,
	}
	got := map[string]int{}
	for _, it := range items {
		got[it.Kind]++
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("kind=%s want=%d got=%d (all=%v)", k, v, got[k], got)
		}
	}
}

func TestImportLabelExtraction(t *testing.T) {
	warehouse := writeMockWarehouse(t)
	boxHome := t.TempDir()
	runImport(t, config{
		warehouse: warehouse,
		boxKey:    "nail-index-test",
		boxHome:   boxHome,
		owner:     "trine",
	})

	svc, done := openSvc(t, boxHome)
	defer done()
	b, err := svc.GetBoxByKey(context.Background(), "trine", "nail-index-test")
	if err != nil {
		t.Fatal(err)
	}
	items, err := svc.Browse(context.Background(), b.ID, box.BrowseFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[string]box.Item{}
	for _, it := range items {
		byKind[it.Kind] = it
	}
	// nail_family
	fam, ok := byKind["nail_family"]
	if !ok {
		t.Fatal("missing nail_family item")
	}
	if fam.Labels["__sem:parent_organ"] != "xingkongzuo" {
		t.Errorf("family parent_organ label: got %q", fam.Labels["__sem:parent_organ"])
	}
	if fam.Labels["__sem:parent_petal"] != "knowledge_modeling" {
		t.Errorf("family parent_petal label: got %q", fam.Labels["__sem:parent_petal"])
	}
	if fam.Labels["__op:library"] != "data_engineering_forge" {
		t.Errorf("family __op:library: got %q", fam.Labels["__op:library"])
	}
	if fam.Labels["__op:family"] != "data_lake_organizer" {
		t.Errorf("family __op:family: got %q", fam.Labels["__op:family"])
	}
	if fam.Labels["__gate:status"] != "active" {
		t.Errorf("family __gate:status: got %q", fam.Labels["__gate:status"])
	}
	// action_nail
	act, ok := byKind["action_nail"]
	if !ok {
		t.Fatal("missing action_nail item")
	}
	if act.Labels["__sem:atom"] == "" {
		t.Errorf("action_nail __sem:atom: empty (labels=%v)", act.Labels)
	}
	if act.Labels["__gate:status"] != "active" {
		t.Errorf("action_nail __gate:status: got %q", act.Labels["__gate:status"])
	}
}

func TestImportDryRun(t *testing.T) {
	warehouse := writeMockWarehouse(t)
	boxHome := t.TempDir()
	cfg := config{
		warehouse: warehouse,
		boxKey:    "nail-index-test",
		boxHome:   boxHome,
		owner:     "trine",
		dryRun:    true,
	}
	runImport(t, cfg)

	svc, done := openSvc(t, boxHome)
	defer done()
	// Box may or may not have been created in dry-run, but no items should exist.
	b, err := svc.GetBoxByKey(context.Background(), "trine", "nail-index-test")
	if err != nil {
		if err == box.ErrNotFound {
			return // perfectly fine for dry-run
		}
		t.Fatal(err)
	}
	items, _ := svc.Browse(context.Background(), b.ID, box.BrowseFilter{Limit: 100})
	if len(items) != 0 {
		t.Fatalf("dry-run produced %d items", len(items))
	}
}

func TestImportSkipsNonYaml(t *testing.T) {
	warehouse := writeMockWarehouse(t)
	famDir := filepath.Join(warehouse, "data_engineering_forge", "data_lake_organizer")
	// Drop noise files: USAGE.md, quality_check.py, output/ tree, active/ tree.
	if err := os.WriteFile(filepath.Join(famDir, "USAGE.md"), []byte("# usage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(famDir, "quality_check.py"), []byte("print('hi')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(famDir, "output"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(famDir, "output", "junk.yaml"),
		[]byte("nope:\n  yes: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(famDir, "active"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(famDir, "active", "stale.yaml"),
		[]byte("stale: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	boxHome := t.TempDir()
	runImport(t, config{
		warehouse: warehouse,
		boxKey:    "nail-index-test",
		boxHome:   boxHome,
		owner:     "trine",
	})

	svc, done := openSvc(t, boxHome)
	defer done()
	b, err := svc.GetBoxByKey(context.Background(), "trine", "nail-index-test")
	if err != nil {
		t.Fatal(err)
	}
	items, err := svc.Browse(context.Background(), b.ID, box.BrowseFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d (noise leaked in)", len(items))
	}
}

// TestImportNailForgeSeedsSymbols verifies the R0.7.6 contract: imported
// items carry SymDomain "nf:" symbols extracted from nail.atom (and friends).
func TestImportNailForgeSeedsSymbols(t *testing.T) {
	warehouse := writeMockWarehouse(t)
	boxHome := t.TempDir()
	runImport(t, config{
		warehouse: warehouse,
		boxKey:    "nail-index-test",
		boxHome:   boxHome,
		owner:     "trine",
	})
	svc, done := openSvc(t, boxHome)
	defer done()
	b, err := svc.GetBoxByKey(context.Background(), "trine", "nail-index-test")
	if err != nil {
		t.Fatal(err)
	}
	items, err := svc.Browse(context.Background(), b.ID, box.BrowseFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var action box.Item
	for _, it := range items {
		if it.Kind == "action_nail" {
			action = it
			break
		}
	}
	if action.ID == "" {
		t.Fatal("action_nail item missing")
	}
	// Every item must carry at least one SymKind (Service contract).
	gotKind := false
	gotDomain := false
	for _, s := range action.Symbols {
		if s.Kind == box.SymKind {
			gotKind = true
		}
		if s.Kind == box.SymDomain && strings.HasPrefix(s.Value, "nf:") {
			gotDomain = true
		}
	}
	if !gotKind {
		t.Errorf("action_nail item missing SymKind (symbols=%v)", action.Symbols)
	}
	if !gotDomain {
		t.Errorf("action_nail item missing SymDomain nf:* (symbols=%v)", action.Symbols)
	}
}

// TestImportSymbolsUpdateOnFirstRun simulates the migration path: an existing
// item with the same content_hash but no Symbols should be replaced so that
// the symbol layer is back-filled.
func TestImportSymbolsUpdateOnFirstRun(t *testing.T) {
	warehouse := writeMockWarehouse(t)
	boxHome := t.TempDir()
	cfg := config{
		warehouse: warehouse,
		boxKey:    "nail-index-test",
		boxHome:   boxHome,
		owner:     "trine",
	}
	// First run seeds the box with symbols.
	runImport(t, cfg)

	// Strip Symbols from every item by hand, mimicking the pre-R0.7.6 state.
	svc, done := openSvc(t, boxHome)
	b, err := svc.GetBoxByKey(context.Background(), "trine", "nail-index-test")
	if err != nil {
		t.Fatal(err)
	}
	items, err := svc.Browse(context.Background(), b.ID, box.BrowseFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		// Hand-edit the on-disk JSON to drop the symbols array.
		itemPath := filepath.Join(boxHome, "boxes", b.Key, "items", it.ID+".json")
		raw, err := os.ReadFile(itemPath)
		if err != nil {
			t.Fatalf("read item: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal item: %v", err)
		}
		delete(m, "symbols")
		out, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(itemPath, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	done()

	// Re-run import: content_hash is unchanged but symbols are absent, so
	// every item should be updated (ReplaceItem path).
	out, _ := runImport(t, cfg)
	if !strings.Contains(out, "updated=4") {
		t.Fatalf("expected updated=4 on symbol back-fill, got:\n%s", out)
	}
	if !strings.Contains(out, "skipped=0") {
		t.Fatalf("expected skipped=0 on symbol back-fill, got:\n%s", out)
	}

	// Verify symbols are now present and the item is the latest revision.
	svc, done = openSvc(t, boxHome)
	defer done()
	items, err = svc.Browse(context.Background(), b.ID, box.BrowseFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if len(it.Symbols) == 0 {
			t.Errorf("item %s/%s still has no symbols after back-fill", it.Kind, it.ID)
		}
	}
}

// TestImportFullyIdempotentWithSymbols runs back-to-back imports and asserts
// the second pass is a clean no-op once symbols are in place.
func TestImportFullyIdempotentWithSymbols(t *testing.T) {
	warehouse := writeMockWarehouse(t)
	boxHome := t.TempDir()
	cfg := config{
		warehouse: warehouse,
		boxKey:    "nail-index-test",
		boxHome:   boxHome,
		owner:     "trine",
	}
	out, _ := runImport(t, cfg)
	if !strings.Contains(out, "created=4") {
		t.Fatalf("expected created=4 on first run, got:\n%s", out)
	}
	out2, _ := runImport(t, cfg)
	if !strings.Contains(out2, "skipped=4") {
		t.Fatalf("expected skipped=4 on second run (idempotent), got:\n%s", out2)
	}
	if !strings.Contains(out2, "updated=0") {
		t.Fatalf("expected updated=0 on second run, got:\n%s", out2)
	}
	if !strings.Contains(out2, "created=0") {
		t.Fatalf("expected created=0 on second run, got:\n%s", out2)
	}
}

func TestImportYamlToJsonRoundtrip(t *testing.T) {
	warehouse := writeMockWarehouse(t)
	boxHome := t.TempDir()
	runImport(t, config{
		warehouse: warehouse,
		boxKey:    "nail-index-test",
		boxHome:   boxHome,
		owner:     "trine",
	})

	svc, done := openSvc(t, boxHome)
	defer done()
	b, err := svc.GetBoxByKey(context.Background(), "trine", "nail-index-test")
	if err != nil {
		t.Fatal(err)
	}
	items, err := svc.Browse(context.Background(), b.ID, box.BrowseFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var actionNail box.Item
	for _, it := range items {
		if it.Kind == "action_nail" {
			actionNail = it
			break
		}
	}
	if actionNail.ID == "" {
		t.Fatal("action_nail item not found")
	}
	full, err := svc.GetItem(context.Background(), "trine", actionNail.ID)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(full.Content, &parsed); err != nil {
		t.Fatalf("unmarshal: %v\ncontent=%s", err, string(full.Content))
	}
	nailMap, ok := parsed["nail"].(map[string]any)
	if !ok {
		t.Fatalf("expected nail map, parsed=%v", parsed)
	}
	if nailMap["id"] != "extract_lake_requirements" {
		t.Errorf("nail.id: got %v", nailMap["id"])
	}
	if full.Format != "yaml" {
		t.Errorf("format: got %q want yaml", full.Format)
	}
}
