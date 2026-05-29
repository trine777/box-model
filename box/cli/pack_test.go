package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/windborneos/box-model/box"
)

// R9.0 export/import round-trip: build a box with history + a blob + a
// trace on disk via a FileStore, export it, import into a fresh root,
// and assert full fidelity (item count incl. history, blob bytes, trace).

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func runCLI(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	envFn := func(k string) string { return env[k] }
	full := append([]string{"box"}, args...) // args[0] is the program name
	code := Run(full, strings.NewReader(""), &out, &errb, envFn, nil)
	return code, out.String(), errb.String()
}

// seedBox creates a FileStore-backed box at root with: one item that has a
// revision chain (v1→v2, so a superseded history file exists), one item
// pointing at a blob whose bytes we write into the blob layer, and a trace.
func seedBox(t *testing.T, root, key string) {
	t.Helper()
	ctx := context.Background()
	st, err := box.OpenFileStore(root)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	defer st.Close()
	svc := box.NewService(st)
	if err := svc.EnsureSymbolBootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	b, err := svc.CreateBox(ctx, box.CreateBoxRequest{
		Key: key, OwnerID: "alice",
		StoragePolicy: box.StoragePolicy{AllowedFormats: []string{"text", "binary", "json"}, MaxItems: 100, MaxContentBytes: 1 << 20},
	})
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	// revision chain: store v1 then replace → v2 (v1 becomes superseded history)
	v1, err := svc.Store(ctx, "alice", b.ID, box.StoreRequest{
		Kind: "M", SourceType: "manual", StorageURI: "row://doc/1", IdemKey: "doc1",
		Format: "text", Content: []byte(`"version one"`),
		Symbols: []box.Symbol{{Kind: box.SymKind, Value: "M"}},
	})
	if err != nil {
		t.Fatalf("Store v1: %v", err)
	}
	if _, err := svc.ReplaceItem(ctx, "alice", v1.ID, box.StoreRequest{
		Kind: "M", SourceType: "manual", StorageURI: "row://doc/1", IdemKey: "doc1-v2",
		Format: "text", Content: []byte(`"version two"`),
		Symbols: []box.Symbol{{Kind: box.SymKind, Value: "M"}},
	}); err != nil {
		t.Fatalf("ReplaceItem: %v", err)
	}
	// blob-backed item: write blob bytes into the layer, then an item pointing at it
	payload := []byte("BLOB-PAYLOAD-CONTENT")
	sha := writeBlob(t, root, payload)
	if _, err := svc.Store(ctx, "alice", b.ID, box.StoreRequest{
		Kind: "A", SourceType: "upload", StorageURI: "blob://sha256/" + sha, IdemKey: "blobitem",
		Format: "binary", Symbols: []box.Symbol{{Kind: box.SymKind, Value: "A"}},
	}); err != nil {
		t.Fatalf("Store blob item: %v", err)
	}
}

func writeBlob(t *testing.T, root string, payload []byte) string {
	t.Helper()
	sum := sha256Hex(payload)
	p := filepath.Join(root, "blobs", sum[:2], sum[2:4], sum)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir blob: %v", err)
	}
	if err := os.WriteFile(p, payload, 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	return sum
}

func TestExportImportRoundtrip(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	seedBox(t, src, "deliverable")

	// sanity: source has history (3 item files: v1 superseded + v2 + blob item)
	srcItems, _ := filepath.Glob(filepath.Join(src, "boxes", "deliverable", "items", "*.json"))
	if len(srcItems) != 3 {
		t.Fatalf("expected 3 item files (v1+v2+blob), got %d", len(srcItems))
	}

	pack := filepath.Join(t.TempDir(), "deliverable.boxpack")
	// export
	code, out, errb := runCLI(t, map[string]string{"BOX_HOME": src}, "export", "deliverable", "-o", pack)
	if code != 0 {
		t.Fatalf("export exit %d, stderr=%s", code, errb)
	}
	if !strings.Contains(out, "3 items") {
		t.Errorf("export should report 3 items, got: %s", out)
	}
	if _, err := os.Stat(pack); err != nil {
		t.Fatalf("pack not created: %v", err)
	}

	// import into fresh root
	code, out, errb = runCLI(t, map[string]string{"BOX_HOME": dst}, "import", pack)
	if code != 0 {
		t.Fatalf("import exit %d, stderr=%s", code, errb)
	}

	// fidelity checks on the destination filesystem
	dstItems, _ := filepath.Glob(filepath.Join(dst, "boxes", "deliverable", "items", "*.json"))
	if len(dstItems) != 3 {
		t.Errorf("history not preserved: dst has %d item files, want 3", len(dstItems))
	}
	if _, err := os.Stat(filepath.Join(dst, "boxes", "deliverable", "box.json")); err != nil {
		t.Errorf("box.json not imported: %v", err)
	}
	// blob bytes round-tripped
	blobs, _ := filepath.Glob(filepath.Join(dst, "blobs", "*", "*", "*"))
	if len(blobs) != 1 {
		t.Fatalf("expected 1 blob imported, got %d", len(blobs))
	}
	got, _ := os.ReadFile(blobs[0])
	if string(got) != "BLOB-PAYLOAD-CONTENT" {
		t.Errorf("blob bytes corrupted: %q", got)
	}
}

func TestImportConflictGuard(t *testing.T) {
	src := t.TempDir()
	seedBox(t, src, "dup")
	pack := filepath.Join(t.TempDir(), "dup.boxpack")
	if code, _, e := runCLI(t, map[string]string{"BOX_HOME": src}, "export", "dup", "-o", pack); code != 0 {
		t.Fatalf("export: %s", e)
	}
	// import into a root that already has "dup"
	dst := t.TempDir()
	seedBox(t, dst, "dup")
	// default: error
	if code, _, _ := runCLI(t, map[string]string{"BOX_HOME": dst}, "import", pack); code != 5 {
		t.Errorf("expected exit 5 on conflict, got %d", code)
	}
	// skip
	if code, out, _ := runCLI(t, map[string]string{"BOX_HOME": dst}, "import", pack, "--on-conflict", "skip"); code != 0 || !strings.Contains(out, "skipped") {
		t.Errorf("skip mode failed: code=%d out=%s", code, out)
	}
}

func TestExportMissingBox(t *testing.T) {
	root := t.TempDir()
	if code, _, _ := runCLI(t, map[string]string{"BOX_HOME": root}, "export", "ghost"); code != 4 {
		t.Errorf("expected exit 4 for missing box, got %d", code)
	}
}
