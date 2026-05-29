package box

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// seedWritable builds a writable service and creates one box + one item so the
// read-only service (sharing the same store) has something to attempt to
// mutate.
func seedWritable(t *testing.T) (Store, string, string) {
	t.Helper()
	ctx := context.Background()
	store := NewMemoryStore()
	rw := NewService(store)
	b, err := rw.CreateBox(ctx, CreateBoxRequest{Key: "ro-seed", OwnerID: "owner"})
	if err != nil {
		t.Fatalf("seed CreateBox: %v", err)
	}
	it, err := rw.Store(ctx, "owner", b.ID, StoreRequest{
		Kind:       "note",
		SourceType: "manual",
		StorageURI: "row://notes/1",
		Content:    json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatalf("seed Store: %v", err)
	}
	return store, b.ID, it.ID
}

// TestReadOnlyRejectsWrites asserts that a Service built with WithReadOnly()
// rejects representative write methods with ErrReadOnlyReplica, before any
// validation runs.
func TestReadOnlyRejectsWrites(t *testing.T) {
	ctx := context.Background()
	store, boxID, itemID := seedWritable(t)
	ro := NewService(store, WithReadOnly())

	if _, err := ro.CreateBox(ctx, CreateBoxRequest{Key: "nope", OwnerID: "owner"}); !errors.Is(err, ErrReadOnlyReplica) {
		t.Fatalf("CreateBox: want ErrReadOnlyReplica, got %v", err)
	}
	if _, err := ro.Store(ctx, "owner", boxID, StoreRequest{
		Kind: "note", SourceType: "manual", StorageURI: "row://notes/2",
	}); !errors.Is(err, ErrReadOnlyReplica) {
		t.Fatalf("Store: want ErrReadOnlyReplica, got %v", err)
	}
	if err := ro.AppendEvent(ctx, "owner", itemID, TraceStep{Op: "x"}); !errors.Is(err, ErrReadOnlyReplica) {
		t.Fatalf("AppendEvent: want ErrReadOnlyReplica, got %v", err)
	}
	if _, err := ro.SetItemSymbols(ctx, "owner", itemID, []Symbol{{Kind: SymStatus, Value: "✓"}}); !errors.Is(err, ErrReadOnlyReplica) {
		t.Fatalf("SetItemSymbols: want ErrReadOnlyReplica, got %v", err)
	}
	if err := ro.SealBox(ctx, "owner", boxID); !errors.Is(err, ErrReadOnlyReplica) {
		t.Fatalf("SealBox: want ErrReadOnlyReplica, got %v", err)
	}
}

// TestReadOnlyAllowsReads asserts the read surface is untouched in read-only
// mode: Browse and GetItem on the seeded item still succeed.
func TestReadOnlyAllowsReads(t *testing.T) {
	ctx := context.Background()
	store, boxID, itemID := seedWritable(t)
	ro := NewService(store, WithReadOnly())

	items, err := ro.Browse(ctx, boxID, BrowseFilter{})
	if err != nil {
		t.Fatalf("Browse under read-only: %v", err)
	}
	if len(items) != 1 || items[0].ID != itemID {
		t.Fatalf("Browse should return the seeded item, got %#v", items)
	}
	if _, err := ro.GetItem(ctx, "owner", itemID); err != nil {
		t.Fatalf("GetItem under read-only: %v", err)
	}
}
