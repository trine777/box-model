package box

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newFileStoreForTest(t *testing.T) (*FileStore, string) {
	t.Helper()
	root := t.TempDir()
	fs, err := OpenFileStore(root)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	return fs, root
}

// TestFileStoreOpenEmpty verifies that opening a fresh empty root succeeds and
// queries against it return ErrNotFound.
func TestFileStoreOpenEmpty(t *testing.T) {
	fs, _ := newFileStoreForTest(t)
	ctx := context.Background()
	if _, err := fs.GetBox(ctx, "box_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := fs.Browse(ctx, "box_missing", BrowseFilter{}); err != nil {
		// Browse of unknown box should return empty list, not an error
		// (matches MemoryStore semantics)
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestFileStoreCreateBoxPersist verifies that a Box and its items round-trip
// across a reopen of the FileStore.
func TestFileStoreCreateBoxPersist(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	fs1, err := OpenFileStore(root)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	svc1 := NewService(fs1)
	b, err := svc1.CreateBox(ctx, CreateBoxRequest{Key: "persist", OwnerID: "alice"})
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	item, err := svc1.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "p/v1",
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/p",
		Content:    json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := fs1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Disk layout sanity
	boxJSON := filepath.Join(root, "boxes", "persist", "box.json")
	if _, err := os.Stat(boxJSON); err != nil {
		t.Fatalf("expected box.json on disk: %v", err)
	}
	itemJSON := filepath.Join(root, "boxes", "persist", "items", item.ID+".json")
	if _, err := os.Stat(itemJSON); err != nil {
		t.Fatalf("expected item file on disk: %v", err)
	}

	// Reopen and verify state
	fs2, err := OpenFileStore(root)
	if err != nil {
		t.Fatalf("OpenFileStore reopen: %v", err)
	}
	defer fs2.Close()
	svc2 := NewService(fs2)
	gotBox, err := svc2.store.GetBox(ctx, b.ID)
	if err != nil {
		t.Fatalf("GetBox after reopen: %v", err)
	}
	if gotBox.Key != "persist" || gotBox.OwnerID != "alice" {
		t.Fatalf("unexpected box: %#v", gotBox)
	}
	items, err := svc2.Browse(ctx, b.ID, BrowseFilter{})
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if len(items) != 1 || items[0].ID != item.ID {
		t.Fatalf("expected 1 item with ID=%s, got %#v", item.ID, items)
	}
}

// TestFileStoreReplaceItemPersist verifies that ReplaceItem leaves both prev
// and new item files coherent across a reopen.
func TestFileStoreReplaceItemPersist(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	fs1, err := OpenFileStore(root)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	svc1 := NewService(fs1)
	b, err := svc1.CreateBox(ctx, CreateBoxRequest{Key: "rep", OwnerID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	v1, err := svc1.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "rep/v1",
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/rep",
		Content:    json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := svc1.ReplaceItem(ctx, "alice", v1.ID, StoreRequest{
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/rep2",
		Content:    json.RawMessage(`{"x":2}`),
	})
	if err != nil {
		t.Fatalf("ReplaceItem: %v", err)
	}
	_ = fs1.Close()

	fs2, err := OpenFileStore(root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer fs2.Close()

	gotV1, err := fs2.GetItem(ctx, v1.ID)
	if err != nil {
		t.Fatalf("GetItem v1: %v", err)
	}
	if gotV1.IsLatest {
		t.Fatalf("v1 IsLatest should be false")
	}
	if gotV1.Status != "superseded" {
		t.Fatalf("v1 status=%q, want superseded", gotV1.Status)
	}
	gotV2, err := fs2.GetItem(ctx, v2.ID)
	if err != nil {
		t.Fatalf("GetItem v2: %v", err)
	}
	if !gotV2.IsLatest {
		t.Fatalf("v2 IsLatest should be true")
	}
	svc2 := NewService(fs2)
	def, err := svc2.Browse(ctx, b.ID, BrowseFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(def) != 1 || def[0].ID != v2.ID {
		t.Fatalf("default Browse expected [v2], got %#v", def)
	}
	all, err := svc2.Browse(ctx, b.ID, BrowseFilter{IncludeHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("IncludeHistory Browse expected 2 items, got %d", len(all))
	}
}

// TestFileStoreReplayReplaceJournal simulates a crash that left a fully-written
// journal but didn't apply it; reopen must replay then delete the journal.
func TestFileStoreReplayReplaceJournal(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	fs1, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc1 := NewService(fs1)
	b, err := svc1.CreateBox(ctx, CreateBoxRequest{Key: "replay", OwnerID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	v1, err := svc1.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "replay/v1",
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/replay",
		Content:    json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = fs1.Close()

	// Construct the post-image versions manually
	now := nowUTC()
	prevAfter := v1
	prevAfter.IsLatest = false
	prevAfter.Status = "superseded"
	prevAfter.SupersededAt = &now

	newItem := Item{
		ID:          NewID("item_"),
		BoxID:       v1.BoxID,
		IdemKey:     "replay/v2",
		Kind:        "task",
		SourceType:  "queue",
		SourceRef:   map[string]string{},
		Labels:      map[string]string{},
		StorageURI:  "row://tasks/replay2",
		Format:      "json",
		Content:     json.RawMessage(`{"x":2}`),
		ContentHash: ContentHash(json.RawMessage(`{"x":2}`)),
		Metadata:    map[string]string{},
		Status:      "available",
		StoredBy:    "alice",
		StoredAt:    now,
		RevisionOf:  v1.ID,
		Revision:    2,
		IsLatest:    true,
	}

	entry := journalEntry{
		Kind:      "replace_item",
		BoxKey:    "replay",
		PrevID:    v1.ID,
		PrevAfter: &prevAfter,
		NewItem:   &newItem,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	jpath := filepath.Join(root, "boxes", "replay", "items", ".pending-test.json")
	if err := os.WriteFile(jpath, data, 0644); err != nil {
		t.Fatal(err)
	}

	fs2, err := OpenFileStore(root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer fs2.Close()

	gotV1, err := fs2.GetItem(ctx, v1.ID)
	if err != nil {
		t.Fatalf("GetItem v1 after replay: %v", err)
	}
	if gotV1.IsLatest {
		t.Fatalf("v1 IsLatest should be false after replay")
	}
	if gotV1.Status != "superseded" {
		t.Fatalf("v1.Status=%q after replay, want superseded", gotV1.Status)
	}
	gotNew, err := fs2.GetItem(ctx, newItem.ID)
	if err != nil {
		t.Fatalf("GetItem new after replay: %v", err)
	}
	if !gotNew.IsLatest {
		t.Fatalf("new item IsLatest should be true after replay")
	}
	if _, err := os.Stat(jpath); !os.IsNotExist(err) {
		t.Fatalf("journal file should be removed after replay, stat err=%v", err)
	}
}

// TestFileStoreDeleteItemPersist verifies soft-delete persistence: the item
// file remains on disk but Status=deleted causes GetItem/Browse to hide it.
func TestFileStoreDeleteItemPersist(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	fs1, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc1 := NewService(fs1)
	b, err := svc1.CreateBox(ctx, CreateBoxRequest{Key: "del", OwnerID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := svc1.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "del/v1",
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/del",
		Content:    json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc1.DeleteItem(ctx, "alice", item.ID); err != nil {
		t.Fatal(err)
	}
	_ = fs1.Close()

	itemPath := filepath.Join(root, "boxes", "del", "items", item.ID+".json")
	if _, err := os.Stat(itemPath); err != nil {
		t.Fatalf("item file should still exist on disk: %v", err)
	}
	raw, err := os.ReadFile(itemPath)
	if err != nil {
		t.Fatal(err)
	}
	// Allow both indented and compact JSON forms
	body := string(raw)
	if !strings.Contains(body, `"status":"deleted"`) && !strings.Contains(body, `"status": "deleted"`) {
		t.Fatalf("expected status:deleted in file, got %s", body)
	}

	fs2, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer fs2.Close()
	if _, err := fs2.GetItem(ctx, item.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after reopen GetItem deleted should be ErrNotFound, got %v", err)
	}
	svc2 := NewService(fs2)
	items, err := svc2.Browse(ctx, b.ID, BrowseFilter{IncludeHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.ID == item.ID {
			t.Fatalf("deleted item should not appear in Browse after reopen")
		}
	}
}

// TestFileStoreConsumeAppendJSONL verifies consumes.jsonl is an append-only
// JSONL file and that MarkConsumed status persists.
func TestFileStoreConsumeAppendJSONL(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	fs1, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc1 := NewService(fs1)
	b, err := svc1.CreateBox(ctx, CreateBoxRequest{Key: "log", OwnerID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := svc1.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "log/v1",
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/log",
		Content:    json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc1.Consume(ctx, "alice", item.ID, ConsumeOptions{MarkConsumed: true}); err != nil {
		t.Fatal(err)
	}
	_ = fs1.Close()

	logPath := filepath.Join(root, "boxes", "log", "consumes.jsonl")
	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("consumes.jsonl open: %v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	lines := 0
	for scanner.Scan() {
		var cl ConsumeLog
		if err := json.Unmarshal(scanner.Bytes(), &cl); err != nil {
			t.Fatalf("invalid JSON line: %v (%s)", err, scanner.Text())
		}
		if cl.ItemID != item.ID {
			t.Fatalf("unexpected item_id in log: %q", cl.ItemID)
		}
		lines++
	}
	if lines != 1 {
		t.Fatalf("expected 1 line in consumes.jsonl, got %d", lines)
	}

	fs2, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer fs2.Close()
	got, err := fs2.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "consumed" {
		t.Fatalf("expected consumed status after reopen, got %q", got.Status)
	}
}

// TestFileStoreIdemKeyConflictAcrossReopen verifies the idempotency index is
// rebuilt from disk: a Store with the same IdemKey after reopen returns the
// original item.
func TestFileStoreIdemKeyConflictAcrossReopen(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	fs1, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc1 := NewService(fs1)
	b, err := svc1.CreateBox(ctx, CreateBoxRequest{Key: "idem", OwnerID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	req := StoreRequest{
		IdemKey:    "k",
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/idem",
		Content:    json.RawMessage(`{"x":1}`),
	}
	first, err := svc1.Store(ctx, "alice", b.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	_ = fs1.Close()

	fs2, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer fs2.Close()
	svc2 := NewService(fs2)
	second, err := svc2.Store(ctx, "alice", b.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected idem dedupe across reopen, got %s vs %s", first.ID, second.ID)
	}
}

// TestFileStoreAtomicSingleFile verifies that no .tmp files linger after a
// successful write, and that an orphan .tmp file is ignored on reopen (not
// loaded as an item).
func TestFileStoreAtomicSingleFile(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	fs1, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc1 := NewService(fs1)
	b, err := svc1.CreateBox(ctx, CreateBoxRequest{Key: "atom", OwnerID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := svc1.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "atom/v1",
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/atom",
		Content:    json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	// No orphan .tmp files post successful write
	itemsDir := filepath.Join(root, "boxes", "atom", "items")
	entries, err := os.ReadDir(itemsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("found orphan .tmp file after Store: %s", e.Name())
		}
	}

	// Force orphan .tmp file
	orphan := filepath.Join(itemsDir, "item_orphan.json.tmp")
	if err := os.WriteFile(orphan, []byte(`{"id":"item_orphan"}`), 0644); err != nil {
		t.Fatal(err)
	}
	_ = fs1.Close()

	fs2, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer fs2.Close()
	if _, err := fs2.GetItem(ctx, "item_orphan"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("orphan .tmp should not be loaded, GetItem returned err=%v", err)
	}
	// Real item still readable
	if _, err := fs2.GetItem(ctx, item.ID); err != nil {
		t.Fatalf("real item should still be loadable: %v", err)
	}
}

func TestFileStoreGetBoxByKey(t *testing.T) {
	fs, _ := newFileStoreForTest(t)
	_, err := fs.CreateBox(context.Background(), Box{ID: "box_x", Key: "kx", Version: 1, OwnerType: "standalone", OwnerID: "owner", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := fs.GetBoxByKey(context.Background(), "kx")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "box_x" {
		t.Fatalf("got %q", got.ID)
	}
}

// TestFileStoreListConsumesAcrossReopen verifies that consume audit logs are
// recovered from consumes.jsonl after a reopen.
func TestFileStoreListConsumesAcrossReopen(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	fs1, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc1 := NewService(fs1)
	b, err := svc1.CreateBox(ctx, CreateBoxRequest{Key: "lc", OwnerID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := svc1.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "lc/v1",
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/lc",
		Content:    json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc1.Consume(ctx, "reader-1", item.ID, ConsumeOptions{MarkConsumed: false, Purpose: "p1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc1.Consume(ctx, "reader-2", item.ID, ConsumeOptions{MarkConsumed: false, Purpose: "p2"}); err != nil {
		t.Fatal(err)
	}
	_ = fs1.Close()

	fs2, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer fs2.Close()
	svc2 := NewService(fs2)
	logs, err := svc2.ListConsumes(ctx, "alice", item.ID)
	if err != nil {
		t.Fatalf("ListConsumes after reopen: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 logs after reopen, got %d", len(logs))
	}
	if logs[0].Purpose != "p1" || logs[1].Purpose != "p2" {
		t.Fatalf("expected order [p1, p2], got [%q, %q]", logs[0].Purpose, logs[1].Purpose)
	}
}

// TestFileStoreDeleteReleasesAcrossReopen verifies D#9: a deleted item's
// idem_key is released so a subsequent Store with the same idem creates a new
// item (and the deleted item remains on disk for audit).
func TestFileStoreDeleteReleasesAcrossReopen(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	fs1, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	svc1 := NewService(fs1)
	b, err := svc1.CreateBox(ctx, CreateBoxRequest{Key: "idem-del", OwnerID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc1.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "k1",
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/x",
		Content:    json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc1.DeleteItem(ctx, "alice", first.ID); err != nil {
		t.Fatal(err)
	}
	_ = fs1.Close()

	fs2, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer fs2.Close()
	svc2 := NewService(fs2)
	second, err := svc2.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "k1",
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/y",
		Content:    json.RawMessage(`{"x":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatalf("expected new item after reopen+delete; got same ID %q", second.ID)
	}
	// Deleted item should still be on disk and visible via include-history.
	items, err := svc2.Browse(ctx, b.ID, BrowseFilter{IncludeHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	// Only the new item should be visible (deleted ones are hidden), but the
	// deleted item file remains on disk for audit.
	foundNew := false
	for _, it := range items {
		if it.ID == second.ID {
			foundNew = true
		}
		if it.ID == first.ID {
			t.Fatalf("deleted item should not appear in Browse after reopen")
		}
	}
	if !foundNew {
		t.Fatalf("expected new item visible in Browse, got %#v", items)
	}
	itemPath := filepath.Join(root, "boxes", "idem-del", "items", first.ID+".json")
	if _, err := os.Stat(itemPath); err != nil {
		t.Fatalf("deleted item file should remain on disk for audit: %v", err)
	}
}

// TestFileStoreRoundTripsMaxContentBytes verifies that a custom MaxContentBytes
// survives a close/reopen cycle of the FileStore.
func TestFileStoreRoundTripsMaxContentBytes(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	fs1, err := OpenFileStore(root)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	svc1 := NewService(fs1)
	_, err = svc1.CreateBox(ctx, CreateBoxRequest{
		Key:     "rt-mc",
		OwnerID: "alice",
		StoragePolicy: StoragePolicy{
			AllowedFormats:  []string{"json"},
			MaxItems:        50,
			MaxContentBytes: 512,
		},
	})
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	if err := fs1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	fs2, err := OpenFileStore(root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer fs2.Close()
	got, err := fs2.GetBoxByKey(ctx, "rt-mc")
	if err != nil {
		t.Fatalf("GetBoxByKey: %v", err)
	}
	if got.StoragePolicy.MaxContentBytes != 512 {
		t.Fatalf("expected MaxContentBytes=512 after reopen, got %d", got.StoragePolicy.MaxContentBytes)
	}
}

// TestOldBoxJSONWithoutMaxContentField verifies backward compatibility: an old
// box.json without a max_content_bytes field loads with MaxContentBytes=0,
// which is treated as "unlimited" by Store/ReplaceItem.
func TestOldBoxJSONWithoutMaxContentField(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	// Hand-roll an old-style box.json with no max_content_bytes field.
	boxDir := filepath.Join(root, "boxes", "legacy")
	if err := os.MkdirAll(filepath.Join(boxDir, "items"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const legacyBoxJSON = `{
  "id": "box_legacy_0001",
  "key": "legacy",
  "version": 1,
  "owner_type": "standalone",
  "owner_id": "alice",
  "storage_policy": {
    "allowed_formats": ["json", "markdown", "text"],
    "max_items": 1000
  },
  "status": "active",
  "created_at": "2024-01-01T00:00:00Z"
}`
	if err := os.WriteFile(filepath.Join(boxDir, "box.json"), []byte(legacyBoxJSON), 0o644); err != nil {
		t.Fatalf("write legacy box.json: %v", err)
	}

	fs, err := OpenFileStore(root)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	defer fs.Close()

	b, err := fs.GetBoxByKey(ctx, "legacy")
	if err != nil {
		t.Fatalf("GetBoxByKey: %v", err)
	}
	if b.StoragePolicy.MaxContentBytes != 0 {
		t.Fatalf("expected MaxContentBytes=0 on legacy box, got %d", b.StoragePolicy.MaxContentBytes)
	}

	// Large content (>256KB) should still succeed because 0 = unlimited.
	svc := NewService(fs)
	body := make([]byte, 300*1024)
	for i := range body {
		body[i] = 'z'
	}
	payload := append([]byte{'"'}, body...)
	payload = append(payload, '"')
	_, err = svc.Store(ctx, "alice", b.ID, StoreRequest{
		Kind:       "blob",
		SourceType: "test",
		StorageURI: "row://t/legacy",
		Content:    json.RawMessage(payload),
	})
	if err != nil {
		t.Fatalf("expected legacy box (MaxContentBytes=0) to accept large content, got %v", err)
	}
}

// TestFileStoreSymbolsAcrossReopen persists an item with Symbols, closes the
// FileStore, reopens, and verifies Symbols are intact.
func TestFileStoreSymbolsAcrossReopen(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	fs1, err := OpenFileStore(root)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	svc1 := NewService(fs1)
	b, err := svc1.CreateBox(ctx, CreateBoxRequest{Key: "sym-persist", OwnerID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	syms := []Symbol{
		{Kind: SymKind, Value: "D"},
		{Kind: SymStatus, Value: "✓"},
		{Kind: SymRelation, Value: "&", Ref: "item_other"},
		{Kind: SymTopic, Value: "billing"},
	}
	v1, err := svc1.Store(ctx, "alice", b.ID, StoreRequest{
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
	if err := fs1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	fs2, err := OpenFileStore(root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer fs2.Close()
	got, err := fs2.GetItem(ctx, v1.ID)
	if err != nil {
		t.Fatalf("GetItem after reopen: %v", err)
	}
	if len(got.Symbols) != 4 {
		t.Fatalf("expected 4 symbols after reopen, got %d (%#v)", len(got.Symbols), got.Symbols)
	}
	if got.Symbols[2].Kind != SymRelation || got.Symbols[2].Ref != "item_other" {
		t.Fatalf("expected SymRelation with Ref=item_other, got %#v", got.Symbols[2])
	}
}

// TestFileStoreAppendTracePersist verifies that trace.jsonl entries survive
// a FileStore reopen (writes go through O_APPEND + fsync).
func TestFileStoreAppendTracePersist(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	fs1, err := OpenFileStore(root)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	svc1 := NewService(fs1)
	b, err := svc1.CreateBox(ctx, CreateBoxRequest{Key: "tr", OwnerID: "alice"})
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	task, err := svc1.CreateTask(ctx, "alice", b.ID, CreateTaskRequest{
		Intent: "do stuff",
		Goal:   []Symbol{{Kind: SymStatus, Value: "✓"}},
		PassCriteria: PassCriteria{
			Kind:   "exists",
			Query:  SymbolQuery{Kind: []SymbolKind{SymKind}, Value: []string{"R"}},
			Reason: "r exists",
		},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	for _, op := range []string{"alpha", "beta"} {
		if err := svc1.AppendTaskTrace(ctx, "alice", task.ID, TraceStep{Op: op}); err != nil {
			t.Fatalf("AppendTaskTrace: %v", err)
		}
	}
	if err := fs1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen and confirm the on-disk file is the source of truth.
	fs2, err := OpenFileStore(root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer fs2.Close()
	svc2 := NewService(fs2)
	got, err := svc2.ListTaskTrace(ctx, "alice", task.ID)
	if err != nil {
		t.Fatalf("ListTaskTrace: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 trace steps after reopen, got %d", len(got))
	}
	if got[0].Op != "alpha" || got[1].Op != "beta" {
		t.Errorf("ops out of order: %+v", got)
	}
	if got[0].Step != 0 || got[1].Step != 1 {
		t.Errorf("step indices out of order: %d, %d", got[0].Step, got[1].Step)
	}
}

// TestFileStoreTraceFileLocation verifies the on-disk layout matches the
// architected layout: <root>/boxes/<box_key>/tasks/<task_id>.trace.jsonl.
func TestFileStoreTraceFileLocation(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	fs, err := OpenFileStore(root)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	defer fs.Close()
	svc := NewService(fs)
	b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "loc", OwnerID: "alice"})
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	task, err := svc.CreateTask(ctx, "alice", b.ID, CreateTaskRequest{
		Intent: "x",
		Goal:   []Symbol{{Kind: SymStatus, Value: "✓"}},
		PassCriteria: PassCriteria{
			Kind:   "exists",
			Query:  SymbolQuery{Kind: []SymbolKind{SymKind}, Value: []string{"R"}},
			Reason: "x",
		},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := svc.AppendTaskTrace(ctx, "alice", task.ID, TraceStep{Op: "y"}); err != nil {
		t.Fatalf("AppendTaskTrace: %v", err)
	}
	expected := filepath.Join(root, "boxes", "loc", "tasks", task.ID+".trace.jsonl")
	info, err := os.Stat(expected)
	if err != nil {
		t.Fatalf("expected file %s, got %v", expected, err)
	}
	if info.Size() == 0 {
		t.Errorf("expected non-empty trace file, got %d bytes", info.Size())
	}
	// File contents: one JSON line ending with \n.
	data, err := os.ReadFile(expected)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := bufio.NewScanner(strings.NewReader(string(data)))
	count := 0
	for lines.Scan() {
		count++
	}
	if count != 1 {
		t.Errorf("expected 1 line, got %d", count)
	}
}

