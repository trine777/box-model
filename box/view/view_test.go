package view_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/windborneos/box-model/box"
	"github.com/windborneos/box-model/box/view"
)

// mkItem is a compact constructor for tests. The kind argument is the DB
// kind field (NOT the SymKind symbol).
func mkItem(id, kind, storage string, storedAt time.Time, syms ...box.Symbol) box.Item {
	return box.Item{
		ID:         id,
		Kind:       kind,
		StorageURI: storage,
		StoredAt:   storedAt,
		Revision:   1,
		IsLatest:   true,
		Symbols:    syms,
	}
}

// TestMapToList/Kanban/Timeline asserts each perspective resolves to a
// non-nil Renderer.
func TestMapToAllPerspectives(t *testing.T) {
	for _, p := range []view.Perspective{view.List, view.Kanban, view.Timeline} {
		r, err := view.MapTo(p)
		if err != nil {
			t.Fatalf("MapTo(%q) err=%v", p, err)
		}
		if r == nil {
			t.Fatalf("MapTo(%q) returned nil renderer", p)
		}
	}
}

// TestMapToUnknown asserts an unknown perspective returns ErrUnknownPerspective.
func TestMapToUnknown(t *testing.T) {
	_, err := view.MapTo(view.Perspective("garbage"))
	if !errors.Is(err, view.ErrUnknownPerspective) {
		t.Fatalf("err=%v, want ErrUnknownPerspective", err)
	}
}

// TestListRendererBasic asserts the list output contains the expected header
// columns and one row per item.
func TestListRendererBasic(t *testing.T) {
	t0 := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	items := []box.Item{
		mkItem("item-aaaaaaaa1", "note", "row://t/a", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"},
			box.Symbol{Kind: box.SymStatus, Value: "?"}),
		mkItem("item-aaaaaaaa2", "doc", "row://t/b", t0,
			box.Symbol{Kind: box.SymKind, Value: "D"},
			box.Symbol{Kind: box.SymStatus, Value: "→"}),
		mkItem("item-aaaaaaaa3", "task", "row://t/c", t0,
			box.Symbol{Kind: box.SymKind, Value: "T"},
			box.Symbol{Kind: box.SymStatus, Value: "✓"}),
	}
	r, _ := view.MapTo(view.List)
	out, err := r.Render(items, view.RenderOptions{})
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	for _, want := range []string{"ID", "Rev", "Kind", "Status"} {
		if !strings.Contains(out, want) {
			t.Fatalf("header missing %q in:\n%s", want, out)
		}
	}
	// 3 data rows
	for _, want := range []string{"note", "doc", "task"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing row %q in:\n%s", want, out)
		}
	}
}

// TestKanbanGroupsByStatus asserts 3 items in 3 SymStatus values produce 3
// labeled columns.
func TestKanbanGroupsByStatus(t *testing.T) {
	t0 := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	items := []box.Item{
		mkItem("a1", "note", "row://t/a", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"},
			box.Symbol{Kind: box.SymStatus, Value: "?"}),
		mkItem("a2", "note", "row://t/b", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"},
			box.Symbol{Kind: box.SymStatus, Value: "→"}),
		mkItem("a3", "note", "row://t/c", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"},
			box.Symbol{Kind: box.SymStatus, Value: "✓"}),
	}
	r, _ := view.MapTo(view.Kanban)
	out, err := r.Render(items, view.RenderOptions{})
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	for _, want := range []string{"?", "→", "✓"} {
		if !strings.Contains(out, want) {
			t.Fatalf("kanban missing column %q in:\n%s", want, out)
		}
	}
}

// TestKanbanAxisKind asserts axis=kind groups items by SymKind values.
func TestKanbanAxisKind(t *testing.T) {
	t0 := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	items := []box.Item{
		mkItem("k1", "note", "row://t/a", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"}),
		mkItem("k2", "note", "row://t/b", t0,
			box.Symbol{Kind: box.SymKind, Value: "D"}),
		mkItem("k3", "note", "row://t/c", t0,
			box.Symbol{Kind: box.SymKind, Value: "T"}),
	}
	r, _ := view.MapTo(view.Kanban)
	out, err := r.Render(items, view.RenderOptions{Axis: view.AxisKind})
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	// Expect columns headed by R, D, T (in the whitelist order).
	for _, want := range []string{"R", "D", "T"} {
		if !strings.Contains(out, want) {
			t.Fatalf("kanban kind missing column %q in:\n%s", want, out)
		}
	}
}

// TestKanbanRejectsStoredAtAxis asserts axis=stored_at is refused.
func TestKanbanRejectsStoredAtAxis(t *testing.T) {
	r, _ := view.MapTo(view.Kanban)
	_, err := r.Render(nil, view.RenderOptions{Axis: view.AxisStoredAt})
	if !errors.Is(err, view.ErrInvalidAxis) {
		t.Fatalf("err=%v, want ErrInvalidAxis", err)
	}
}

// TestTimelineSortDesc asserts items are emitted newest-first.
func TestTimelineSortDesc(t *testing.T) {
	t0 := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	items := []box.Item{
		mkItem("id-oldid01", "note", "row://t/a", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"}),
		mkItem("id-midid02", "note", "row://t/b", t1,
			box.Symbol{Kind: box.SymKind, Value: "R"}),
		mkItem("id-newid03", "note", "row://t/c", t2,
			box.Symbol{Kind: box.SymKind, Value: "R"}),
	}
	r, _ := view.MapTo(view.Timeline)
	out, err := r.Render(items, view.RenderOptions{})
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	iNew := strings.Index(out, "newid03")
	iMid := strings.Index(out, "midid02")
	iOld := strings.Index(out, "oldid01")
	if iNew < 0 || iMid < 0 || iOld < 0 {
		t.Fatalf("missing ids in:\n%s", out)
	}
	if !(iNew < iMid && iMid < iOld) {
		t.Fatalf("expected newest first; got new=%d mid=%d old=%d\n%s", iNew, iMid, iOld, out)
	}
	if !strings.Contains(out, "●") {
		t.Fatalf("expected ● bullet in timeline, got:\n%s", out)
	}
}

// TestTimelineRejectsNonStoredAtAxis asserts axis=status is refused.
func TestTimelineRejectsNonStoredAtAxis(t *testing.T) {
	r, _ := view.MapTo(view.Timeline)
	_, err := r.Render(nil, view.RenderOptions{Axis: view.AxisStatus})
	if !errors.Is(err, view.ErrInvalidAxis) {
		t.Fatalf("err=%v, want ErrInvalidAxis", err)
	}
}

// ---------------------------------------------------------------------------
// R0.7.4 — relation views (graph / tree / mind), mermaid output
// ---------------------------------------------------------------------------

// firstLine returns the first newline-terminated chunk of s, without the
// trailing newline. Used by mermaid tests that assert the header literal.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// TestGraphMermaidPrefix asserts graph renderer emits `graph LR\n` as the
// first line even when items is empty.
func TestGraphMermaidPrefix(t *testing.T) {
	r, err := view.MapTo(view.Graph)
	if err != nil {
		t.Fatalf("MapTo(Graph) err=%v", err)
	}
	out, err := r.Render(nil, view.RenderOptions{})
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	if got := firstLine(out); got != "graph LR" {
		t.Fatalf("first line = %q, want %q\nfull:\n%s", got, "graph LR", out)
	}
}

// TestGraphRendersNodesAndEdges asserts the graph contains both node
// declarations and a depends-on edge labeled `&`.
func TestGraphRendersNodesAndEdges(t *testing.T) {
	t0 := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	items := []box.Item{
		mkItem("item-a", "note", "row://t/a", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"},
			box.Symbol{Kind: box.SymRelation, Value: "&", Ref: "item-b"}),
		mkItem("item-b", "note", "row://t/b", t0,
			box.Symbol{Kind: box.SymKind, Value: "T"}),
	}
	r, _ := view.MapTo(view.Graph)
	out, err := r.Render(items, view.RenderOptions{})
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	for _, want := range []string{"item-a", "item-b", "-->|&|"} {
		// Last 8 of "item-a" / "item-b" — both are 6 chars, so the id-tail
		// appears verbatim in the label.
		if !strings.Contains(out, want) {
			t.Fatalf("graph missing %q in:\n%s", want, out)
		}
	}
}

// TestGraphTruncatesAt100 asserts an input of 101 items emits the
// truncation banner and stops at 100 nodes.
func TestGraphTruncatesAt100(t *testing.T) {
	t0 := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	items := make([]box.Item, 0, 101)
	for i := 0; i < 101; i++ {
		items = append(items, mkItem(
			"item-"+intToStr(i), "note", "row://t/x", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"},
		))
	}
	r, _ := view.MapTo(view.Graph)
	out, err := r.Render(items, view.RenderOptions{})
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	if !strings.Contains(out, "%% truncated to 100") {
		t.Fatalf("missing truncation note in:\n%s", out[:min(400, len(out))])
	}
}

// intToStr is a test-only helper to avoid pulling strconv just for the
// truncation fixture.
func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestTreeMermaidPrefix asserts the tree renderer emits `graph TD` first.
func TestTreeMermaidPrefix(t *testing.T) {
	r, err := view.MapTo(view.Tree)
	if err != nil {
		t.Fatalf("MapTo(Tree) err=%v", err)
	}
	out, err := r.Render(nil, view.RenderOptions{})
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	if got := firstLine(out); got != "graph TD" {
		t.Fatalf("first line = %q, want %q\nfull:\n%s", got, "graph TD", out)
	}
}

// TestTreeOnlyHierarchyRelations asserts the tree view draws `<` / `⊃`
// edges but skips non-hierarchy relations like `&`.
func TestTreeOnlyHierarchyRelations(t *testing.T) {
	t0 := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	// a refines b; c depends-on a. Hierarchy edge: a → b (via `<` from a).
	// Non-hierarchy edge: c → a (via `&`) — must NOT appear in output.
	items := []box.Item{
		mkItem("aaaa", "note", "row://t/a", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"},
			box.Symbol{Kind: box.SymRelation, Value: "<", Ref: "bbbb"}),
		mkItem("bbbb", "note", "row://t/b", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"}),
		mkItem("cccc", "note", "row://t/c", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"},
			box.Symbol{Kind: box.SymRelation, Value: "&", Ref: "aaaa"}),
	}
	r, _ := view.MapTo(view.Tree)
	out, err := r.Render(items, view.RenderOptions{})
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	if !strings.Contains(out, "aaaa") || !strings.Contains(out, "bbbb") {
		t.Fatalf("tree missing a/b nodes:\n%s", out)
	}
	if strings.Contains(out, "-->|&|") {
		t.Fatalf("tree must not draw depends-on edge:\n%s", out)
	}
}

// TestTreeRejectsInvalidAxis asserts tree refuses axis=scope.
func TestTreeRejectsInvalidAxis(t *testing.T) {
	r, _ := view.MapTo(view.Tree)
	_, err := r.Render(nil, view.RenderOptions{Axis: view.AxisScope})
	if !errors.Is(err, view.ErrInvalidAxis) {
		t.Fatalf("err=%v, want ErrInvalidAxis", err)
	}
}

// TestMindMermaidPrefix asserts the mind renderer's first line is `mindmap`
// and its second line includes the root.
func TestMindMermaidPrefix(t *testing.T) {
	r, err := view.MapTo(view.Mind)
	if err != nil {
		t.Fatalf("MapTo(Mind) err=%v", err)
	}
	out, err := r.Render(nil, view.RenderOptions{})
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	if got := firstLine(out); got != "mindmap" {
		t.Fatalf("first line = %q, want %q\nfull:\n%s", got, "mindmap", out)
	}
	if !strings.Contains(out, "root((Box))") {
		t.Fatalf("missing root((Box)) in:\n%s", out)
	}
}

// TestMindGroupsByScopeTopic asserts the mind view emits Scope:/Topic:
// labels for items carrying SymScope / SymTopic symbols.
func TestMindGroupsByScopeTopic(t *testing.T) {
	t0 := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	items := []box.Item{
		mkItem("m1", "note", "row://t/a", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"},
			box.Symbol{Kind: box.SymScope, Value: "arch"},
			box.Symbol{Kind: box.SymTopic, Value: "routing"}),
		mkItem("m2", "note", "row://t/b", t0,
			box.Symbol{Kind: box.SymKind, Value: "T"},
			box.Symbol{Kind: box.SymScope, Value: "cli"},
			box.Symbol{Kind: box.SymTopic, Value: "view"}),
		mkItem("m3", "note", "row://t/c", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"},
			box.Symbol{Kind: box.SymScope, Value: "arch"}),
	}
	r, _ := view.MapTo(view.Mind)
	out, err := r.Render(items, view.RenderOptions{})
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	for _, want := range []string{"Scope:arch", "Scope:cli", "Topic:routing", "Topic:view"} {
		if !strings.Contains(out, want) {
			t.Fatalf("mind missing %q in:\n%s", want, out)
		}
	}
}

// ---------------------------------------------------------------------------
// R0.7.5 — matrix view (kind × status 2-D table)
// ---------------------------------------------------------------------------

// firstNonEmptyLine returns the first newline-terminated chunk of s that
// contains at least one non-whitespace byte, without the trailing newline.
func firstNonEmptyLine(s string) string {
	for len(s) > 0 {
		var line string
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			line = s[:i]
			s = s[i+1:]
		} else {
			line = s
			s = ""
		}
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// TestMatrixHeaderRow asserts the first non-empty line of the matrix output
// is the column-header row, starting with 14 spaces of label gutter then `?`.
func TestMatrixHeaderRow(t *testing.T) {
	r, err := view.MapTo(view.Matrix)
	if err != nil {
		t.Fatalf("MapTo(Matrix) err=%v", err)
	}
	out, err := r.Render(nil, view.RenderOptions{})
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	hdr := firstNonEmptyLine(out)
	want := "              ?"
	if !strings.HasPrefix(hdr, want) {
		t.Fatalf("header line = %q, want prefix %q\nfull:\n%s", hdr, want, out)
	}
}

// TestMatrixCounts asserts cells reflect counts across 3 kinds × 2 statuses.
func TestMatrixCounts(t *testing.T) {
	t0 := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	items := []box.Item{
		// R × ? (1)
		mkItem("r1", "note", "row://t/r1", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"},
			box.Symbol{Kind: box.SymStatus, Value: "?"}),
		// R × → (3)
		mkItem("r2", "note", "row://t/r2", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"},
			box.Symbol{Kind: box.SymStatus, Value: "→"}),
		mkItem("r3", "note", "row://t/r3", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"},
			box.Symbol{Kind: box.SymStatus, Value: "→"}),
		mkItem("r4", "note", "row://t/r4", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"},
			box.Symbol{Kind: box.SymStatus, Value: "→"}),
		// D × → (1)
		mkItem("d1", "note", "row://t/d1", t0,
			box.Symbol{Kind: box.SymKind, Value: "D"},
			box.Symbol{Kind: box.SymStatus, Value: "→"}),
		// T × ? (1)
		mkItem("t1", "note", "row://t/t1", t0,
			box.Symbol{Kind: box.SymKind, Value: "T"},
			box.Symbol{Kind: box.SymStatus, Value: "?"}),
	}
	r, _ := view.MapTo(view.Matrix)
	out, err := r.Render(items, view.RenderOptions{})
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	// R row: '?' col = 1, '→' col = 3. Cell widths are 5 = `%-5s`.
	// Row label is left-padded to 14, so the R line begins "R" + 13 spaces.
	for _, want := range []string{
		"R             1    3", // R row, ? col=1, → col=3
		"D             .    1", // D row, ? col=., → col=1
		"T             1    ", // T row, ? col=1
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("matrix missing %q in:\n%s", want, out)
		}
	}
}

// TestMatrixHandlesEmptyKindStatus asserts items without SymKind and without
// SymStatus land in the (none) row × (none) col.
func TestMatrixHandlesEmptyKindStatus(t *testing.T) {
	t0 := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	items := []box.Item{
		mkItem("naked", "note", "row://t/n", t0),
	}
	r, _ := view.MapTo(view.Matrix)
	out, err := r.Render(items, view.RenderOptions{})
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	if !strings.Contains(out, "(none)") {
		t.Fatalf("matrix missing (none) row/col in:\n%s", out)
	}
	// The (none) row should show 1 in the (none) col.
	noneLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "(none)") {
			noneLine = line
			break
		}
	}
	if noneLine == "" {
		t.Fatalf("no (none) row found in:\n%s", out)
	}
	// Total at end of the (none) row should be 1.
	if !strings.HasSuffix(strings.TrimRight(noneLine, " "), "1") {
		t.Fatalf("(none) row total != 1: %q", noneLine)
	}
}

// TestMatrixTotalRowAndColumn asserts 5 items produce a Total row whose grand
// total is 5.
func TestMatrixTotalRowAndColumn(t *testing.T) {
	t0 := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	items := []box.Item{
		mkItem("r1", "note", "row://t/r1", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"},
			box.Symbol{Kind: box.SymStatus, Value: "?"}),
		mkItem("r2", "note", "row://t/r2", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"},
			box.Symbol{Kind: box.SymStatus, Value: "→"}),
		mkItem("r3", "note", "row://t/r3", t0,
			box.Symbol{Kind: box.SymKind, Value: "R"},
			box.Symbol{Kind: box.SymStatus, Value: "✓"}),
		mkItem("d1", "note", "row://t/d1", t0,
			box.Symbol{Kind: box.SymKind, Value: "D"},
			box.Symbol{Kind: box.SymStatus, Value: "✓"}),
		mkItem("t1", "note", "row://t/t1", t0,
			box.Symbol{Kind: box.SymKind, Value: "T"},
			box.Symbol{Kind: box.SymStatus, Value: "?"}),
	}
	r, _ := view.MapTo(view.Matrix)
	out, err := r.Render(items, view.RenderOptions{})
	if err != nil {
		t.Fatalf("render err=%v", err)
	}
	if !strings.Contains(out, "Total") {
		t.Fatalf("matrix missing Total label in:\n%s", out)
	}
	// Find the Total row and confirm its last numeric cell is 5.
	totalLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Total") {
			totalLine = line
			break
		}
	}
	if totalLine == "" {
		t.Fatalf("no Total row found in:\n%s", out)
	}
	if !strings.HasSuffix(strings.TrimRight(totalLine, " "), "5") {
		t.Fatalf("grand total != 5 in Total row: %q\nfull:\n%s", totalLine, out)
	}
}

// TestMatrixIgnoresAxis asserts Matrix + AxisStoredAt does NOT return
// ErrInvalidAxis (matrix is a fixed kind × status table; axis is ignored).
func TestMatrixIgnoresAxis(t *testing.T) {
	r, _ := view.MapTo(view.Matrix)
	_, err := r.Render(nil, view.RenderOptions{Axis: view.AxisStoredAt})
	if err != nil {
		t.Fatalf("matrix should ignore axis, got err=%v", err)
	}
}
