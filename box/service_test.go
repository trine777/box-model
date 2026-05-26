package box

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/windborneos/box-model/box/obs"
)

func TestStoreBrowseAndConsume(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())

	b, err := svc.CreateBox(ctx, CreateBoxRequest{
		Key:       "decisions",
		OwnerType: "area",
		OwnerID:   "area-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	item, err := svc.Store(ctx, "area-1", b.ID, StoreRequest{
		IdemKey:    "decision-1/v1",
		Kind:       "decision",
		SourceType: "discussion_resolution",
		SourceRef:  map[string]string{"session_id": "s1", "revision": "1"},
		Labels:     map[string]string{"__op:area_id": "area-1", "__sem:topic": "billing"},
		LocationID: "loc-billing",
		StorageURI: "row://discussion_resolutions/decision-1",
		Content:    json.RawMessage(`{"decision":"ship"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.Browse(ctx, b.ID, BrowseFilter{
		Kind:      "decision",
		Labels:    map[string]string{"__sem:topic": "billing"},
		SourceRef: map[string]string{"session_id": "s1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != item.ID {
		t.Fatalf("unexpected browse result: %#v", got)
	}

	_, err = svc.GetItem(ctx, "user-1", item.ID)
	if err != nil {
		t.Fatal(err)
	}
	consumed, err := svc.GetItem(ctx, "user-2", item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if consumed.Status != "available" {
		t.Fatalf("expected available status (GetItem must not mutate), got %q", consumed.Status)
	}
}

func TestGetItemNoSideEffect(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())

	b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "no-side", OwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := svc.Store(ctx, "owner", b.ID, StoreRequest{
		IdemKey:    "k/v1",
		Kind:       "decision",
		SourceType: "discussion_resolution",
		StorageURI: "row://decisions/k",
		Content:    json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		got, err := svc.GetItem(ctx, "reader", item.ID)
		if err != nil {
			t.Fatalf("GetItem call %d: %v", i, err)
		}
		if got.Status != "available" {
			t.Fatalf("GetItem call %d: expected available, got %q", i, got.Status)
		}
	}
	logs, err := svc.ListConsumes(ctx, "owner", item.ID)
	if err != nil {
		t.Fatalf("ListConsumes: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("expected 0 consume logs, got %d", len(logs))
	}
}

func TestConsumeExplicitMarks(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())

	b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "mark", OwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := svc.Store(ctx, "owner", b.ID, StoreRequest{
		IdemKey:    "m/v1",
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/m",
		Content:    json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.Consume(ctx, "worker-1", item.ID, ConsumeOptions{MarkConsumed: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "consumed" {
		t.Fatalf("expected consumed, got %q", got.Status)
	}
	logs, err := svc.ListConsumes(ctx, "owner", item.ID)
	if err != nil {
		t.Fatalf("ListConsumes: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 consume log, got %d", len(logs))
	}
}

func TestConsumeAuditOnly(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())

	b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "audit", OwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := svc.Store(ctx, "owner", b.ID, StoreRequest{
		IdemKey:    "a/v1",
		Kind:       "decision",
		SourceType: "discussion_resolution",
		StorageURI: "row://decisions/a",
		Content:    json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.Consume(ctx, "reader", item.ID, ConsumeOptions{MarkConsumed: false})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "available" {
		t.Fatalf("expected available, got %q", got.Status)
	}
	logs, err := svc.ListConsumes(ctx, "owner", item.ID)
	if err != nil {
		t.Fatalf("ListConsumes: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 consume log, got %d", len(logs))
	}
}

func TestIdempotentStore(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "artifacts", OwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	req := StoreRequest{
		IdemKey:    "artifact-1/v1",
		Kind:       "row",
		SourceType: "artifact",
		StorageURI: "row://artifacts/artifact-1",
		Content:    json.RawMessage(`{"ok":true}`),
	}
	first, err := svc.Store(ctx, "owner", b.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Store(ctx, "owner", b.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected idempotent insert, got %s and %s", first.ID, second.ID)
	}
}

func TestRejectUnsupportedStorageURI(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "bad-uri", OwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Store(ctx, "owner", b.ID, StoreRequest{
		Kind:       "document",
		SourceType: "external",
		StorageURI: "http://example.com/doc",
		Content:    json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

// --- Helpers for revision/labels tests ---

func setupRevisionBox(t *testing.T, ownerID string) (context.Context, *Service, *MemoryStore, Box, Item) {
	t.Helper()
	ctx := context.Background()
	store := NewMemoryStore()
	svc := NewService(store)
	b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "rev-box-" + ownerID, OwnerID: ownerID})
	if err != nil {
		t.Fatal(err)
	}
	v1, err := svc.Store(ctx, ownerID, b.ID, StoreRequest{
		IdemKey:    "k/v1",
		Kind:       "task",
		SourceType: "queue",
		Labels:     map[string]string{"a": "1"},
		StorageURI: "row://tasks/k",
		Content:    json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, svc, store, b, v1
}

func TestReplaceItemHappyPath(t *testing.T) {
	ctx, svc, _, b, v1 := setupRevisionBox(t, "alice")

	v2, err := svc.ReplaceItem(ctx, "alice", v1.ID, StoreRequest{
		IdemKey:    "k/v2",
		Kind:       "task",
		SourceType: "queue",
		Labels:     map[string]string{"a": "2"},
		StorageURI: "row://tasks/k2",
		Content:    json.RawMessage(`{"x":2}`),
	})
	if err != nil {
		t.Fatalf("ReplaceItem: %v", err)
	}
	if v2.Revision != 2 {
		t.Fatalf("expected revision=2, got %d", v2.Revision)
	}
	if !v2.IsLatest {
		t.Fatalf("expected IsLatest=true on new item")
	}
	if v2.RevisionOf != v1.ID {
		t.Fatalf("expected RevisionOf=%s, got %s", v1.ID, v2.RevisionOf)
	}
	if v2.Status != "available" {
		t.Fatalf("expected status=available, got %q", v2.Status)
	}
	if v2.BoxID != b.ID {
		t.Fatalf("expected boxID=%s, got %s", b.ID, v2.BoxID)
	}

	gotPrev, err := svc.store.GetItem(ctx, v1.ID)
	if err != nil {
		t.Fatalf("GetItem prev: %v", err)
	}
	if gotPrev.IsLatest {
		t.Fatalf("expected prev IsLatest=false")
	}
	if gotPrev.Status != "superseded" {
		t.Fatalf("expected prev status=superseded, got %q", gotPrev.Status)
	}
	if gotPrev.SupersededAt == nil {
		t.Fatalf("expected prev SupersededAt non-nil")
	}

	gotNew, err := svc.store.GetItem(ctx, v2.ID)
	if err != nil {
		t.Fatalf("GetItem new: %v", err)
	}
	if !gotNew.IsLatest {
		t.Fatalf("expected new IsLatest=true")
	}
	if gotNew.Status != "available" {
		t.Fatalf("expected new status=available, got %q", gotNew.Status)
	}
}

func TestReplaceItemRejectsNonLatest(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	_, err := svc.ReplaceItem(ctx, "alice", v1.ID, StoreRequest{
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/k2",
		Content:    json.RawMessage(`{"x":2}`),
	})
	if err != nil {
		t.Fatalf("first ReplaceItem: %v", err)
	}
	_, err = svc.ReplaceItem(ctx, "alice", v1.ID, StoreRequest{
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/k3",
		Content:    json.RawMessage(`{"x":3}`),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestReplaceItemRejectsKindMismatch(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	_, err := svc.ReplaceItem(ctx, "alice", v1.ID, StoreRequest{
		Kind:       "decision",
		SourceType: "queue",
		StorageURI: "row://tasks/k2",
		Content:    json.RawMessage(`{"x":2}`),
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
}

func TestReplaceItemInheritsKindWhenEmpty(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	v2, err := svc.ReplaceItem(ctx, "alice", v1.ID, StoreRequest{
		Kind:       "",
		SourceType: "queue",
		StorageURI: "row://tasks/k2",
		Content:    json.RawMessage(`{"x":2}`),
	})
	if err != nil {
		t.Fatalf("ReplaceItem: %v", err)
	}
	if v2.Kind != "task" {
		t.Fatalf("expected inherited kind=task, got %q", v2.Kind)
	}
}

func TestReplaceItemAutoIdemKey(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	v2, err := svc.ReplaceItem(ctx, "alice", v1.ID, StoreRequest{
		IdemKey:    "",
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/k2",
		Content:    json.RawMessage(`{"x":2}`),
	})
	if err != nil {
		t.Fatalf("ReplaceItem: %v", err)
	}
	expect := "k/v1/r2"
	if v2.IdemKey != expect {
		t.Fatalf("expected idem_key=%q, got %q", expect, v2.IdemKey)
	}
}

func TestReplaceItemForbiddenWhenNotOwner(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	_, err := svc.ReplaceItem(ctx, "bob", v1.ID, StoreRequest{
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/k2",
		Content:    json.RawMessage(`{"x":2}`),
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestBrowseDefaultExcludesHistory(t *testing.T) {
	ctx, svc, _, b, v1 := setupRevisionBox(t, "alice")
	v2, err := svc.ReplaceItem(ctx, "alice", v1.ID, StoreRequest{
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/k2",
		Content:    json.RawMessage(`{"x":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := svc.Browse(ctx, b.ID, BrowseFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item (latest only), got %d", len(items))
	}
	if items[0].ID != v2.ID {
		t.Fatalf("expected latest=%s, got %s", v2.ID, items[0].ID)
	}
}

func TestBrowseIncludeHistory(t *testing.T) {
	ctx, svc, _, b, v1 := setupRevisionBox(t, "alice")
	_, err := svc.ReplaceItem(ctx, "alice", v1.ID, StoreRequest{
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/k2",
		Content:    json.RawMessage(`{"x":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := svc.Browse(ctx, b.ID, BrowseFilter{IncludeHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
}

func TestBrowseOnlyHistory(t *testing.T) {
	ctx, svc, _, b, v1 := setupRevisionBox(t, "alice")
	_, err := svc.ReplaceItem(ctx, "alice", v1.ID, StoreRequest{
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/k2",
		Content:    json.RawMessage(`{"x":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := svc.Browse(ctx, b.ID, BrowseFilter{OnlyHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item (history only), got %d", len(items))
	}
	if items[0].ID != v1.ID {
		t.Fatalf("expected old=%s, got %s", v1.ID, items[0].ID)
	}
	if items[0].IsLatest {
		t.Fatalf("history item must have IsLatest=false")
	}
}

func TestBrowseInvalidHistoryFlags(t *testing.T) {
	ctx, svc, _, b, _ := setupRevisionBox(t, "alice")
	_, err := svc.Browse(ctx, b.ID, BrowseFilter{IncludeHistory: true, OnlyHistory: true})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
}

func TestUpdateLabelsPatchOnly(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	got, err := svc.UpdateLabels(ctx, "alice", v1.ID, map[string]string{"b": "2"})
	if err != nil {
		t.Fatalf("UpdateLabels: %v", err)
	}
	if got.ID != v1.ID {
		t.Fatalf("expected ID unchanged, got %s vs %s", got.ID, v1.ID)
	}
	if got.Revision != 1 {
		t.Fatalf("expected Revision=1, got %d", got.Revision)
	}
	if !got.IsLatest {
		t.Fatalf("expected IsLatest=true")
	}
	if len(got.Labels) != 1 || got.Labels["b"] != "2" {
		t.Fatalf("expected labels={b:2}, got %#v", got.Labels)
	}
	if _, exists := got.Labels["a"]; exists {
		t.Fatalf("expected old label 'a' removed (full replace)")
	}
	if got.ContentHash != v1.ContentHash {
		t.Fatalf("expected content_hash unchanged, got %q vs %q", got.ContentHash, v1.ContentHash)
	}
	if got.StorageURI != v1.StorageURI {
		t.Fatalf("expected storage_uri unchanged")
	}
}

func TestUpdateLabelsValidatesKeys(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	_, err := svc.UpdateLabels(ctx, "alice", v1.ID, map[string]string{"!!!": "x"})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
}

func TestUpdateLabelsForbidden(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	_, err := svc.UpdateLabels(ctx, "bob", v1.ID, map[string]string{"b": "2"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestConsumeRefetchStatus(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	svc := NewService(store)
	b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "consume-refetch", OwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := svc.Store(ctx, "owner", b.ID, StoreRequest{
		IdemKey:    "c/v1",
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/c",
		Content:    json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Consume(ctx, "owner", item.ID, ConsumeOptions{MarkConsumed: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "consumed" {
		t.Fatalf("expected consumed, got %q", got.Status)
	}
}

func TestDeleteItemSoftDelete(t *testing.T) {
	ctx, svc, _, b, v1 := setupRevisionBox(t, "alice")
	got, err := svc.DeleteItem(ctx, "alice", v1.ID)
	if err != nil {
		t.Fatalf("DeleteItem: %v", err)
	}
	if got.Status != "deleted" {
		t.Fatalf("expected status=deleted, got %q", got.Status)
	}
	if got.IsLatest {
		t.Fatalf("expected IsLatest=false after delete")
	}
	if _, err := svc.GetItem(ctx, "alice", v1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
	items, err := svc.Browse(ctx, b.ID, BrowseFilter{IncludeHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.ID == v1.ID {
			t.Fatalf("deleted item should not appear in Browse, got %#v", it)
		}
	}
}

func TestDeleteItemHistorical(t *testing.T) {
	ctx, svc, _, b, v1 := setupRevisionBox(t, "alice")
	v2, err := svc.ReplaceItem(ctx, "alice", v1.ID, StoreRequest{
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/k2",
		Content:    json.RawMessage(`{"x":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteItem(ctx, "alice", v1.ID); err != nil {
		t.Fatalf("DeleteItem v1: %v", err)
	}
	if _, err := svc.GetItem(ctx, "alice", v1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("v1 should be ErrNotFound, got %v", err)
	}
	gotV2, err := svc.GetItem(ctx, "alice", v2.ID)
	if err != nil {
		t.Fatalf("v2 should still be visible: %v", err)
	}
	if !gotV2.IsLatest {
		t.Fatalf("v2 should still be IsLatest")
	}
	items, err := svc.Browse(ctx, b.ID, BrowseFilter{IncludeHistory: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != v2.ID {
		t.Fatalf("expected only v2 in Browse, got %#v", items)
	}
}

func TestDeleteItemForbidden(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	if _, err := svc.DeleteItem(ctx, "bob", v1.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestDeleteItemAlreadyDeleted(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	if _, err := svc.DeleteItem(ctx, "alice", v1.ID); err != nil {
		t.Fatalf("first DeleteItem: %v", err)
	}
	_, err := svc.DeleteItem(ctx, "alice", v1.ID)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on second delete, got %v", err)
	}
}

func TestReplaceItemIdemConflict(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())

	b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "ic", OwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}

	// 两条互不相关的 item,占着不同的 idem_key
	v1, err := svc.Store(ctx, "owner", b.ID, StoreRequest{
		IdemKey:    "a/v1",
		Kind:       "decision",
		SourceType: "discussion",
		StorageURI: "row://decisions/a",
		Content:    json.RawMessage(`{"a":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.Store(ctx, "owner", b.ID, StoreRequest{
		IdemKey:    "occupied",
		Kind:       "decision",
		SourceType: "discussion",
		StorageURI: "row://decisions/b",
		Content:    json.RawMessage(`{"b":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	// 试图用 occupied 的 idem_key replace v1
	_, err = svc.ReplaceItem(ctx, "owner", v1.ID, StoreRequest{
		IdemKey:    "occupied",
		Kind:       "decision",
		SourceType: "discussion",
		StorageURI: "row://decisions/a-v2",
		Content:    json.RawMessage(`{"a":2}`),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	// 验证 v1 没被悄悄翻状态
	again, err := svc.GetItem(ctx, "reader", v1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !again.IsLatest || again.Status != "available" {
		t.Fatalf("v1 should remain IsLatest=true status=available, got IsLatest=%v status=%q", again.IsLatest, again.Status)
	}
	if again.SupersededAt != nil {
		t.Fatalf("v1.SupersededAt should be nil, got %v", *again.SupersededAt)
	}

	// 验证 other 没变
	otherAgain, err := svc.GetItem(ctx, "reader", other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if otherAgain.ID != other.ID || otherAgain.Status != "available" {
		t.Fatalf("other item should be unchanged, got %#v", otherAgain)
	}
}

func TestServiceGetBoxByKey(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())

	b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "my-box", OwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.GetBoxByKey(ctx, "owner", "my-box")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != b.ID {
		t.Fatalf("expected box %q, got %q", b.ID, got.ID)
	}
}

func TestServiceGetBoxByKeyNotFound(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	_, err := svc.GetBoxByKey(ctx, "owner", "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestServiceGetBoxByKeyEmpty(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	_, err := svc.GetBoxByKey(ctx, "owner", "")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
}

// TestServiceMetricsEndToEnd exercises one happy-path run through the main
// Service verbs with a MemObserver wired in, then asserts that each verb
// emitted its `*.success` counter (per docs/observability.md §5).
func TestServiceMetricsEndToEnd(t *testing.T) {
	ctx := context.Background()
	o := obs.NewMemObserver(io.Discard, slog.LevelInfo)
	svc := NewService(NewMemoryStore(), WithObserver(o))

	b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "metrics", OwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := svc.Store(ctx, "owner", b.ID, StoreRequest{
		IdemKey:    "m/v1",
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/m",
		Content:    json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Browse(ctx, b.ID, BrowseFilter{}); err != nil {
		t.Fatal(err)
	}
	v2, err := svc.ReplaceItem(ctx, "owner", item.ID, StoreRequest{
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/m2",
		Content:    json.RawMessage(`{"x":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteItem(ctx, "owner", v2.ID); err != nil {
		t.Fatal(err)
	}

	snap := o.Snapshot()
	// Collect totals for each metric name across all tag combinations.
	totals := map[string]int64{}
	for k, v := range snap.Counters {
		name := k
		if i := indexBar(k); i >= 0 {
			name = k[:i]
		}
		totals[name] += v
	}
	for _, name := range []string{
		"box.create.success",
		"item.store.success",
		"item.browse.success",
		"item.replace.success",
		"item.delete.success",
	} {
		if totals[name] < 1 {
			t.Fatalf("counter %q < 1: counters=%v", name, snap.Counters)
		}
	}
	// Spot-check the result_count observation lands for browse.
	if len(snap.Observed["item.browse.result_count"]) == 0 {
		t.Fatalf("expected item.browse.result_count Observe, got %v", snap.Observed)
	}
	// Replace revision was 2 — assert it was observed.
	found := false
	for k := range snap.Observed {
		if (k == "item.replace.revision" || indexBar(k) > 0 && k[:indexBar(k)] == "item.replace.revision") && len(snap.Observed[k]) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected item.replace.revision Observe, got %v", snap.Observed)
	}
}

// TestServiceErrClassification asserts that classifyErr maps each of the
// four domain errors to a distinct err_type tag and bumps the matching
// *.error counter.
func TestServiceErrClassification(t *testing.T) {
	ctx := context.Background()
	o := obs.NewMemObserver(io.Discard, slog.LevelInfo)
	svc := NewService(NewMemoryStore(), WithObserver(o))

	// Bootstrap a box + item for the forbidden/conflict scenarios.
	b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "errs", OwnerID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	v1, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey: "e/v1", Kind: "task", SourceType: "queue",
		StorageURI: "row://tasks/e", Content: json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1) validation: empty kind
	if _, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		Kind: "", SourceType: "queue", StorageURI: "row://tasks/x",
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("want validation, got %v", err)
	}
	// 2) forbidden: wrong caller deleting item
	if _, err := svc.DeleteItem(ctx, "mallory", v1.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("want forbidden, got %v", err)
	}
	// 3) notfound: bogus id
	if _, err := svc.GetItem(ctx, "alice", "item_bogus"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want notfound, got %v", err)
	}
	// 4) conflict: double-delete
	if _, err := svc.DeleteItem(ctx, "alice", v1.ID); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if _, err := svc.DeleteItem(ctx, "alice", v1.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}

	snap := o.Snapshot()
	have := map[string]bool{}
	for k := range snap.Counters {
		// look for any tagged error counter; we only need to confirm presence
		// of one entry per err_type value across all *.error names.
		if i := indexBar(k); i > 0 && contains(k[i:], "err_type=") {
			// extract value
			tagPart := k[i+1:]
			et := tagValue(tagPart, "err_type")
			have[et] = true
		}
	}
	for _, et := range []string{"validation", "forbidden", "notfound", "conflict"} {
		if !have[et] {
			t.Fatalf("err_type=%q missing from counters: %v", et, snap.Counters)
		}
	}
}

// indexBar/contains/tagValue are tiny test-only helpers to avoid pulling in
// strings just for these. They are duplicated rather than exported.
func indexBar(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '|' {
			return i
		}
	}
	return -1
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// tagValue extracts the value for key from a comma-joined "k=v,k=v" string.
func tagValue(tags, key string) string {
	prefix := key + "="
	for len(tags) > 0 {
		end := len(tags)
		for i := 0; i < len(tags); i++ {
			if tags[i] == ',' {
				end = i
				break
			}
		}
		pair := tags[:end]
		if len(pair) >= len(prefix) && pair[:len(prefix)] == prefix {
			return pair[len(prefix):]
		}
		if end == len(tags) {
			return ""
		}
		tags = tags[end+1:]
	}
	return ""
}

// --- D#2: Service.ListConsumes ---

func TestServiceListConsumesHappy(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	// Two audit-only consumes (don't flip status).
	if _, err := svc.Consume(ctx, "reader-1", v1.ID, ConsumeOptions{MarkConsumed: false, Purpose: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Consume(ctx, "reader-2", v1.ID, ConsumeOptions{MarkConsumed: false, Purpose: "second"}); err != nil {
		t.Fatal(err)
	}
	logs, err := svc.ListConsumes(ctx, "alice", v1.ID)
	if err != nil {
		t.Fatalf("ListConsumes: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 logs, got %d", len(logs))
	}
	if logs[0].Purpose != "first" || logs[1].Purpose != "second" {
		t.Fatalf("expected order [first, second], got [%q, %q]", logs[0].Purpose, logs[1].Purpose)
	}
	if !logs[0].ConsumedAt.Before(logs[1].ConsumedAt) && !logs[0].ConsumedAt.Equal(logs[1].ConsumedAt) {
		t.Fatalf("expected ascending ConsumedAt order")
	}
}

func TestServiceListConsumesForbidden(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	if _, err := svc.Consume(ctx, "reader", v1.ID, ConsumeOptions{MarkConsumed: false}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.ListConsumes(ctx, "bob", v1.ID)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestServiceListConsumesEmpty(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	logs, err := svc.ListConsumes(ctx, "alice", v1.ID)
	if err != nil {
		t.Fatalf("ListConsumes: %v", err)
	}
	if logs == nil {
		t.Fatalf("expected empty slice, got nil")
	}
	if len(logs) != 0 {
		t.Fatalf("expected 0 logs, got %d", len(logs))
	}
}

// --- D#5: UpdateLabels history-guard ---

func TestUpdateLabelsRejectsHistorical(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	_, err := svc.ReplaceItem(ctx, "alice", v1.ID, StoreRequest{
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/k2",
		Content:    json.RawMessage(`{"x":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	// v1 is now historical (IsLatest=false).
	_, err = svc.UpdateLabels(ctx, "alice", v1.ID, map[string]string{"b": "2"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on historical UpdateLabels, got %v", err)
	}
}

func TestUpdateLabelsAllowHistoryOpt(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	_, err := svc.ReplaceItem(ctx, "alice", v1.ID, StoreRequest{
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/k2",
		Content:    json.RawMessage(`{"x":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.UpdateLabels(ctx, "alice", v1.ID, map[string]string{"b": "2"}, WithAllowHistory(true))
	if err != nil {
		t.Fatalf("UpdateLabels with WithAllowHistory(true) on historical: %v", err)
	}
	if got.IsLatest {
		t.Fatalf("expected historical item to remain IsLatest=false")
	}
	if got.Labels["b"] != "2" {
		t.Fatalf("expected labels patched to {b:2}, got %#v", got.Labels)
	}
}

// --- D#6: MergeLabels / RemoveLabels ---

func TestMergeLabelsAdd(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	// Initial labels via setupRevisionBox = {"a":"1"}.
	got, err := svc.MergeLabels(ctx, "alice", v1.ID, map[string]string{"b": "2"})
	if err != nil {
		t.Fatalf("MergeLabels: %v", err)
	}
	if got.Labels["a"] != "1" || got.Labels["b"] != "2" {
		t.Fatalf("expected {a:1,b:2}, got %#v", got.Labels)
	}
}

func TestMergeLabelsOverwrite(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	got, err := svc.MergeLabels(ctx, "alice", v1.ID, map[string]string{"a": "x"})
	if err != nil {
		t.Fatalf("MergeLabels: %v", err)
	}
	if got.Labels["a"] != "x" {
		t.Fatalf("expected {a:x}, got %#v", got.Labels)
	}
}

func TestMergeLabelsKeepsHistoryGuard(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	_, err := svc.ReplaceItem(ctx, "alice", v1.ID, StoreRequest{
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/k2",
		Content:    json.RawMessage(`{"x":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.MergeLabels(ctx, "alice", v1.ID, map[string]string{"b": "2"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on historical MergeLabels, got %v", err)
	}
}

func TestRemoveLabelsHappy(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	// Add a second label so we can remove one and verify the other survives.
	if _, err := svc.MergeLabels(ctx, "alice", v1.ID, map[string]string{"b": "2"}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.RemoveLabels(ctx, "alice", v1.ID, []string{"a"})
	if err != nil {
		t.Fatalf("RemoveLabels: %v", err)
	}
	if _, has := got.Labels["a"]; has {
		t.Fatalf("expected 'a' removed, got %#v", got.Labels)
	}
	if got.Labels["b"] != "2" {
		t.Fatalf("expected 'b' preserved, got %#v", got.Labels)
	}
}

func TestRemoveLabelsMissingKeyOk(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	got, err := svc.RemoveLabels(ctx, "alice", v1.ID, []string{"nonexistent"})
	if err != nil {
		t.Fatalf("RemoveLabels with missing key: %v", err)
	}
	if got.Labels["a"] != "1" {
		t.Fatalf("expected labels unchanged (a:1 present), got %#v", got.Labels)
	}
}

func TestRemoveLabelsKeepsHistoryGuard(t *testing.T) {
	ctx, svc, _, _, v1 := setupRevisionBox(t, "alice")
	_, err := svc.ReplaceItem(ctx, "alice", v1.ID, StoreRequest{
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/k2",
		Content:    json.RawMessage(`{"x":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.RemoveLabels(ctx, "alice", v1.ID, []string{"a"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on historical RemoveLabels, got %v", err)
	}
}

// --- D#9: DeleteItem releases IdemKey ---

func TestDeleteReleasesIdemKey(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	svc := NewService(store)
	b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "idem-del", OwnerID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "k1",
		Kind:       "task",
		SourceType: "queue",
		StorageURI: "row://tasks/x",
		Content:    json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteItem(ctx, "alice", first.ID); err != nil {
		t.Fatal(err)
	}
	// Same idem key — D#9 demands a NEW item, not the deleted one.
	second, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
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
		t.Fatalf("expected new item ID; got same ID %q (idem key not released)", second.ID)
	}
}

// --- D#10: Service.GetBox ---

func TestServiceGetBox(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "gb", OwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetBox(ctx, "", b.ID)
	if err != nil {
		t.Fatalf("GetBox: %v", err)
	}
	if got.ID != b.ID {
		t.Fatalf("expected %q, got %q", b.ID, got.ID)
	}
}

func TestServiceGetBoxEmpty(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	_, err := svc.GetBox(ctx, "", "")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
}

func TestServiceGetBoxNotFound(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	_, err := svc.GetBox(ctx, "", "box_bogus")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// --- R0.6.1: MaxContentBytes ---

func TestStoragePolicyDefaultIncludesMaxContent(t *testing.T) {
	if got, want := DefaultPolicy().MaxContentBytes, 256*1024; got != want {
		t.Fatalf("DefaultPolicy().MaxContentBytes = %d, want %d", got, want)
	}
}

func TestCreateBoxDefaultsMaxContent(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "default-mc", OwnerID: "owner"})
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	if got, want := b.StoragePolicy.MaxContentBytes, 256*1024; got != want {
		t.Fatalf("box.StoragePolicy.MaxContentBytes = %d, want %d", got, want)
	}
}

func TestCreateBoxRejectsNegativeMaxContent(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	_, err := svc.CreateBox(ctx, CreateBoxRequest{
		Key:     "neg-mc",
		OwnerID: "owner",
		StoragePolicy: StoragePolicy{
			AllowedFormats:  []string{"json"},
			MaxItems:        100,
			MaxContentBytes: -1,
		},
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation for negative MaxContentBytes, got %v", err)
	}
}

func TestCreateBoxRejectsNegativeMaxItems(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	_, err := svc.CreateBox(ctx, CreateBoxRequest{
		Key:     "neg-mi",
		OwnerID: "owner",
		StoragePolicy: StoragePolicy{
			AllowedFormats: []string{"json"},
			MaxItems:       -5,
		},
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation for negative MaxItems, got %v", err)
	}
}

func TestStoreRejectsOversizedContent(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "size-cap", OwnerID: "owner"})
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	// max = 256*1024 bytes; build a JSON payload whose len strictly exceeds it.
	// Build a JSON string: `"` + N bytes + `"` -> total = N + 2.
	// Pick N = 257*1024 (strictly > 256*1024 even ignoring the quotes).
	body := make([]byte, 257*1024)
	for i := range body {
		body[i] = 'a'
	}
	oversized := append([]byte{'"'}, body...)
	oversized = append(oversized, '"')
	if len(oversized) <= b.StoragePolicy.MaxContentBytes {
		t.Fatalf("test setup: oversized payload not actually oversized (%d <= %d)",
			len(oversized), b.StoragePolicy.MaxContentBytes)
	}
	_, err = svc.Store(ctx, "owner", b.ID, StoreRequest{
		Kind:       "blob",
		SourceType: "test",
		StorageURI: "row://t/x",
		Content:    json.RawMessage(oversized),
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation for oversized content, got %v", err)
	}

	// Boundary: exactly == max should still succeed.
	// Build payload of length exactly max: `"` + (max-2) bytes + `"`.
	maxLen := b.StoragePolicy.MaxContentBytes
	innerLen := maxLen - 2
	exact := make([]byte, 0, maxLen)
	exact = append(exact, '"')
	for i := 0; i < innerLen; i++ {
		exact = append(exact, 'a')
	}
	exact = append(exact, '"')
	if len(exact) != maxLen {
		t.Fatalf("test setup: expected exact size %d, got %d", maxLen, len(exact))
	}
	_, err = svc.Store(ctx, "owner", b.ID, StoreRequest{
		Kind:       "blob",
		SourceType: "test",
		StorageURI: "row://t/y",
		Content:    json.RawMessage(exact),
	})
	if err != nil {
		t.Fatalf("expected exact-size content to succeed, got %v", err)
	}
}

func TestStoreUnlimitedWhenZero(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	// Explicitly pass a policy whose MaxItems != 0 so the default-fill branch is
	// skipped, but MaxContentBytes = 0 (= unlimited).
	b, err := svc.CreateBox(ctx, CreateBoxRequest{
		Key:     "unlimited",
		OwnerID: "owner",
		StoragePolicy: StoragePolicy{
			AllowedFormats:  []string{"json"},
			MaxItems:        100,
			MaxContentBytes: 0,
		},
	})
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	if b.StoragePolicy.MaxContentBytes != 0 {
		t.Fatalf("expected MaxContentBytes=0 to be preserved, got %d", b.StoragePolicy.MaxContentBytes)
	}
	// 1 MiB payload, comfortably above the default 256 KiB.
	body := make([]byte, 1024*1024)
	for i := range body {
		body[i] = 'b'
	}
	payload := append([]byte{'"'}, body...)
	payload = append(payload, '"')
	_, err = svc.Store(ctx, "owner", b.ID, StoreRequest{
		Kind:       "blob",
		SourceType: "test",
		StorageURI: "row://t/big",
		Content:    json.RawMessage(payload),
	})
	if err != nil {
		t.Fatalf("expected unlimited (0) policy to accept 1MB content, got %v", err)
	}
}

func TestReplaceItemRespectsMaxContentBytes(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "replace-cap", OwnerID: "owner"})
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	// v1: small content
	v1, err := svc.Store(ctx, "owner", b.ID, StoreRequest{
		Kind:       "blob",
		SourceType: "test",
		StorageURI: "row://t/x",
		Content:    json.RawMessage(`"ok"`),
	})
	if err != nil {
		t.Fatalf("Store v1: %v", err)
	}
	body := make([]byte, 257*1024)
	for i := range body {
		body[i] = 'c'
	}
	oversized := append([]byte{'"'}, body...)
	oversized = append(oversized, '"')
	_, err = svc.ReplaceItem(ctx, "owner", v1.ID, StoreRequest{
		Kind:       "blob",
		SourceType: "test",
		StorageURI: "row://t/x",
		Content:    json.RawMessage(oversized),
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
	// Prev state must be unchanged.
	prev, err := svc.GetItem(ctx, "owner", v1.ID)
	if err != nil {
		t.Fatalf("GetItem prev: %v", err)
	}
	if !prev.IsLatest {
		t.Fatalf("prev.IsLatest should remain true after failed replace")
	}
	if prev.Status != "available" {
		t.Fatalf("prev.Status should remain available, got %q", prev.Status)
	}
}

// --- R0.10 task surface ---------------------------------------------------

// happyTaskReq returns a fully-populated CreateTaskRequest suitable for the
// "happy path" tests; individual tests mutate one field to assert a specific
// validation rule.
func happyTaskReq() CreateTaskRequest {
	pc, _ := json.Marshal(PassCriteria{
		Kind:   "exists",
		Query:  SymbolQuery{Kind: []SymbolKind{SymKind}, Value: []string{"R"}},
		Reason: "the R item must exist (agent will check)",
	})
	return CreateTaskRequest{
		Intent:       "ship feature X",
		Source:       []Symbol{{Kind: SymTopic, Value: "billing"}},
		Goal:         []Symbol{{Kind: SymStatus, Value: "✓"}},
		PassCriteria: pc,
		NailChain:    []string{"database_engine_forge/a1"},
	}
}

// newTaskBox is a small helper: CreateBox + return id.
func newTaskBox(t *testing.T, svc *Service, key, owner string) Box {
	t.Helper()
	b, err := svc.CreateBox(context.Background(), CreateBoxRequest{Key: key, OwnerID: owner})
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	return b
}

func TestCreateTaskHappyPath(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "t1", "alice")
	item, err := svc.CreateTask(ctx, "alice", b.ID, happyTaskReq())
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if item.Kind != "task" {
		t.Errorf("expected Kind=task, got %q", item.Kind)
	}
	if item.SourceType != "task" {
		t.Errorf("expected SourceType=task, got %q", item.SourceType)
	}
	// Expect symbols = [T, ?]
	gotT, gotQ := false, false
	for _, s := range item.Symbols {
		if s.Kind == SymKind && s.Value == "T" {
			gotT = true
		}
		if s.Kind == SymStatus && s.Value == "?" {
			gotQ = true
		}
	}
	if !gotT || !gotQ {
		t.Errorf("expected symbols [T, ?], got %+v", item.Symbols)
	}
	// Content carries intent / pass_criteria etc verbatim.
	var payload map[string]any
	if err := json.Unmarshal(item.Content, &payload); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if payload["intent"] != "ship feature X" {
		t.Errorf("expected intent='ship feature X', got %v", payload["intent"])
	}
}

func TestCreateTaskMissingIntent(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "t2", "alice")
	req := happyTaskReq()
	req.Intent = ""
	_, err := svc.CreateTask(ctx, "alice", b.ID, req)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
}

func TestCreateTaskMissingGoal(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "t3", "alice")
	req := happyTaskReq()
	req.Goal = nil
	_, err := svc.CreateTask(ctx, "alice", b.ID, req)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
}

func TestCreateTaskInvalidGoalSymbol(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "t4", "alice")
	req := happyTaskReq()
	// invalid SymKind value (Z is not in the whitelist)
	req.Goal = []Symbol{{Kind: SymKind, Value: "Z"}}
	_, err := svc.CreateTask(ctx, "alice", b.ID, req)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
}

// R0.13.2: pass_criteria is opaque JSON. TestCreateTaskInvalidPassCriteriaKind
// and TestCreateTaskMissingPassReason (which asserted the kind whitelist and
// Reason requirement) were removed because Box no longer validates either.
// The replacement TestPassCriteriaIsOpaqueJSON below verifies the new
// contract — accept any JSON, reject only invalid JSON.

func TestPassCriteriaIsOpaqueJSON(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "t-pc-opaque", "alice")

	// Any JSON shape passes (no kind whitelist, no Reason requirement).
	for _, body := range []string{
		`{"kind":"weird","whatever":true}`,
		`{"agent_invented":"shape"}`,
		`["array","is","fine"]`,
		`null`,
		`42`,
	} {
		req := happyTaskReq()
		req.PassCriteria = json.RawMessage(body)
		if _, err := svc.CreateTask(ctx, "alice", b.ID, req); err != nil {
			t.Errorf("expected pass_criteria=%q to be accepted, got %v", body, err)
		}
	}
	// Invalid JSON still rejected.
	req := happyTaskReq()
	req.PassCriteria = json.RawMessage(`{not valid`)
	if _, err := svc.CreateTask(ctx, "alice", b.ID, req); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation on bad JSON, got %v", err)
	}
}

func TestCreateTaskWithNailChain(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "t7", "alice")
	req := happyTaskReq()
	req.NailChain = []string{"a/1", "b/2", "c/3"}
	item, err := svc.CreateTask(ctx, "alice", b.ID, req)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	var payload struct {
		NailChain []string `json:"nail_chain"`
	}
	if err := json.Unmarshal(item.Content, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(payload.NailChain) != 3 || payload.NailChain[0] != "a/1" {
		t.Errorf("nail_chain round-trip wrong: %+v", payload.NailChain)
	}
}

func TestCreateTaskRejectsEmptyNailChainEntry(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "t7e", "alice")
	req := happyTaskReq()
	req.NailChain = []string{"a/1", ""}
	_, err := svc.CreateTask(ctx, "alice", b.ID, req)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
}

func TestSetItemSymbolsBasic(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "t8", "alice")
	item, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "k/v1",
		Kind:       "doc",
		SourceType: "manual",
		StorageURI: "row://x/1",
		Content:    json.RawMessage(`{}`),
		Symbols:    []Symbol{{Kind: SymKind, Value: "R"}},
	})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	out, err := svc.SetItemSymbols(ctx, "alice", item.ID, []Symbol{
		{Kind: SymKind, Value: "R"},
		{Kind: SymStatus, Value: "✓"},
	})
	if err != nil {
		t.Fatalf("SetItemSymbols: %v", err)
	}
	if len(out.Symbols) != 2 {
		t.Errorf("expected 2 symbols, got %+v", out.Symbols)
	}
}

func TestSetItemSymbolsHistoryGuardBlocked(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "t9", "alice")
	v1, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "k/v1",
		Kind:       "doc",
		SourceType: "manual",
		StorageURI: "row://x/v1",
		Symbols:    []Symbol{{Kind: SymKind, Value: "R"}},
	})
	if err != nil {
		t.Fatalf("Store v1: %v", err)
	}
	_, err = svc.ReplaceItem(ctx, "alice", v1.ID, StoreRequest{
		StorageURI: "row://x/v2",
		Symbols:    []Symbol{{Kind: SymKind, Value: "R"}},
	})
	if err != nil {
		t.Fatalf("ReplaceItem: %v", err)
	}
	// v1 is now historical. SetItemSymbols without WithAllowHistory must fail.
	_, err = svc.SetItemSymbols(ctx, "alice", v1.ID, []Symbol{{Kind: SymKind, Value: "R"}})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestSetItemSymbolsAllowHistoryOpt(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "t10", "alice")
	v1, _ := svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "k/v1",
		Kind:       "doc",
		SourceType: "manual",
		StorageURI: "row://x/v1",
		Symbols:    []Symbol{{Kind: SymKind, Value: "R"}},
	})
	_, _ = svc.ReplaceItem(ctx, "alice", v1.ID, StoreRequest{
		StorageURI: "row://x/v2",
		Symbols:    []Symbol{{Kind: SymKind, Value: "R"}},
	})
	_, err := svc.SetItemSymbols(ctx, "alice", v1.ID,
		[]Symbol{{Kind: SymKind, Value: "R"}, {Kind: SymStatus, Value: "◯"}},
		WithAllowHistory(true),
	)
	if err != nil {
		t.Fatalf("expected success with WithAllowHistory, got %v", err)
	}
}

func TestSetItemSymbolsRejectInvalid(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "t11", "alice")
	item, _ := svc.Store(ctx, "alice", b.ID, StoreRequest{
		Kind:       "doc",
		SourceType: "manual",
		StorageURI: "row://x/1",
		Symbols:    []Symbol{{Kind: SymKind, Value: "R"}},
	})
	_, err := svc.SetItemSymbols(ctx, "alice", item.ID, []Symbol{{Kind: SymKind, Value: "ZZ"}})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
}

func TestAppendEventHappy(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "t12", "alice")
	task, err := svc.CreateTask(ctx, "alice", b.ID, happyTaskReq())
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := svc.AppendEvent(ctx, "alice", task.ID, TraceStep{Op: "store"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	got, err := svc.ListEvents(ctx, "alice", task.ID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 trace step, got %d", len(got))
	}
	if got[0].Op != "store" || got[0].Step != 0 {
		t.Errorf("unexpected step: %+v", got[0])
	}
	if got[0].AppendedAt.IsZero() {
		t.Errorf("expected AppendedAt to be set")
	}
}

func TestAppendEventMultiple(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "t13", "alice")
	task, err := svc.CreateTask(ctx, "alice", b.ID, happyTaskReq())
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	for _, op := range []string{"a", "b", "c"} {
		if err := svc.AppendEvent(ctx, "alice", task.ID, TraceStep{Op: op}); err != nil {
			t.Fatalf("AppendEvent %q: %v", op, err)
		}
	}
	got, err := svc.ListEvents(ctx, "alice", task.ID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(got))
	}
	for i, want := range []string{"a", "b", "c"} {
		if got[i].Op != want || got[i].Step != i {
			t.Errorf("step %d: got Op=%q Step=%d, want Op=%q Step=%d", i, got[i].Op, got[i].Step, want, i)
		}
	}
}

// R0.13.2: AppendEvent works on any item kind (was kind=task-only).
// This test replaces TestAppendTaskTraceRejectsNonTask, which asserted the
// gate that R0.13.2 deliberately removed (invariant #10 cleanup).
func TestAppendEventToAnyItemKind(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "t14", "alice")
	for _, kindSym := range []string{"M", "A", "D", "R"} {
		note, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
			Kind:       kindSym + "-item",
			SourceType: "manual",
			StorageURI: "row://x/" + kindSym,
			IdemKey:    "k-" + kindSym,
			Symbols:    []Symbol{{Kind: SymKind, Value: kindSym}},
		})
		if err != nil {
			t.Fatalf("Store(%s): %v", kindSym, err)
		}
		if err := svc.AppendEvent(ctx, "alice", note.ID, TraceStep{Op: "note_event"}); err != nil {
			t.Errorf("AppendEvent on kind=%s should succeed, got %v", kindSym, err)
		}
		events, err := svc.ListEvents(ctx, "alice", note.ID)
		if err != nil {
			t.Fatalf("ListEvents: %v", err)
		}
		if len(events) != 1 || events[0].Op != "note_event" {
			t.Errorf("kind=%s: expected 1 event op=note_event, got %v", kindSym, events)
		}
	}
}

func TestSetTaskStatusViaSetItemSymbols(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "t15", "alice")
	task, err := svc.CreateTask(ctx, "alice", b.ID, happyTaskReq())
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// Initially status=?
	hasOpen := false
	for _, s := range task.Symbols {
		if s.Kind == SymStatus && s.Value == "?" {
			hasOpen = true
		}
	}
	if !hasOpen {
		t.Fatalf("expected initial status=?, got %+v", task.Symbols)
	}
	// Flip to ✓ via SetItemSymbols.
	updated, err := svc.SetItemSymbols(ctx, "alice", task.ID, []Symbol{
		{Kind: SymKind, Value: "T"},
		{Kind: SymStatus, Value: "✓"},
	})
	if err != nil {
		t.Fatalf("SetItemSymbols: %v", err)
	}
	hasDone := false
	for _, s := range updated.Symbols {
		if s.Kind == SymStatus && s.Value == "✓" {
			hasDone = true
		}
	}
	if !hasDone {
		t.Errorf("expected status=✓ after flip, got %+v", updated.Symbols)
	}
}

// Box does NOT execute pass_criteria.query. After CreateTask we manually
// store an item that would *satisfy* the query (kind=R). The task status
// must remain "?" until the agent calls SetItemSymbols — Box does not
// auto-flip (invariant #10).
func TestBoxDoesNotInterpretPassCriteria(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "t16", "alice")
	task, err := svc.CreateTask(ctx, "alice", b.ID, happyTaskReq())
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// Now satisfy the pass_criteria.query by inserting a kind=R item.
	_, err = svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "satisfier",
		Kind:       "req",
		SourceType: "manual",
		StorageURI: "row://x/r",
		Symbols:    []Symbol{{Kind: SymKind, Value: "R"}},
	})
	if err != nil {
		t.Fatalf("Store satisfier: %v", err)
	}
	// Re-fetch the task — status should still be "?".
	got, err := svc.GetItem(ctx, "alice", task.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	for _, s := range got.Symbols {
		if s.Kind == SymStatus && s.Value != "?" {
			t.Errorf("Box must NOT auto-flip status; found %+v", s)
		}
	}
}

// -----------------------------------------------------------------------------
// R0.10 v2 schema upgrade tests (NailDag + compound PassCriteria + TraceStep
// node/branch fields).
// -----------------------------------------------------------------------------

func TestNailDagNodeBasic(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "ndag1", "alice")
	req := happyTaskReq()
	req.NailDag = []NailDagNode{
		{ID: "a1", NailRef: "x/a1"},
		{ID: "a2", NailRef: "x/a2", DependsOn: []string{"a1"}, BranchID: "main"},
		{ID: "a3", NailRef: "x/a3", DependsOn: []string{"a1"}, BranchID: "branch_b"},
	}
	item, err := svc.CreateTask(ctx, "alice", b.ID, req)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	var payload struct {
		NailDag []NailDagNode `json:"nail_dag"`
	}
	if err := json.Unmarshal(item.Content, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(payload.NailDag) != 3 {
		t.Fatalf("nail_dag round-trip wrong: %+v", payload.NailDag)
	}
}

func TestNailDagRejectsDuplicateID(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "ndag2", "alice")
	req := happyTaskReq()
	req.NailDag = []NailDagNode{
		{ID: "a1", NailRef: "x/a1"},
		{ID: "a1", NailRef: "x/a1b"},
	}
	_, err := svc.CreateTask(ctx, "alice", b.ID, req)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation on duplicate id, got %v", err)
	}
}

// TestBoxNotInterpretingDAG ensures Box rejects an unresolved depends_on
// reference (structural validation) but still does NOT execute / topo-sort /
// schedule the DAG itself (invariant #10).
func TestBoxNotInterpretingDAG(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "ndag3", "alice")
	req := happyTaskReq()
	req.NailDag = []NailDagNode{
		{ID: "a1", NailRef: "x/a1", DependsOn: []string{"missing"}},
	}
	_, err := svc.CreateTask(ctx, "alice", b.ID, req)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation for unknown depends_on, got %v", err)
	}
}

func TestSchemaValidateCycleDetection(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "ndag4", "alice")
	req := happyTaskReq()
	req.NailDag = []NailDagNode{
		{ID: "A", NailRef: "x/A", DependsOn: []string{"B"}},
		{ID: "B", NailRef: "x/B", DependsOn: []string{"A"}},
	}
	_, err := svc.CreateTask(ctx, "alice", b.ID, req)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation for cycle, got %v", err)
	}
}

// R0.13.2 removed TestPassCriteriaCompoundAND / TestPassCriteriaCompoundDepth
// / TestPassCriteriaRejectDepthOver3 / TestPassCriteriaCompoundRequiresMinTwo.
// All four asserted closed-set validation on PassCriteria that R0.13.2
// eliminated; TestPassCriteriaIsOpaqueJSON (above) replaces them with the
// new "any JSON shape passes" contract.

// -----------------------------------------------------------------------------
// R0.13.1 程辙层 (YiCheng / program-track) tests
// -----------------------------------------------------------------------------

func TestStartYiChengHappy(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "yc1", "alice")
	task, token, err := svc.StartYiCheng(ctx, "alice", b.ID, happyTaskReq())
	if err != nil {
		t.Fatalf("StartYiCheng: %v", err)
	}
	if token == "" || !startsWith(token, "tsk_") {
		t.Fatalf("expected tsk_ token, got %q", token)
	}
	// Task should be marked → (work in progress).
	gotProgress := false
	for _, s := range task.Symbols {
		if s.Kind == SymStatus && s.Value == "→" {
			gotProgress = true
		}
	}
	if !gotProgress {
		t.Errorf("expected status →, got %+v", task.Symbols)
	}
	// Trace should contain task_start.
	trace, err := svc.ListEvents(ctx, "alice", task.ID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(trace) != 1 || trace[0].Op != "task_start" {
		t.Fatalf("expected single task_start trace, got %+v", trace)
	}
	// Token validation should report active.
	sess, ok, err := svc.ValidateYiCheng(ctx, token)
	if err != nil || !ok {
		t.Fatalf("ValidateYiCheng got ok=%v err=%v", ok, err)
	}
	if sess.TaskID != task.ID {
		t.Errorf("session TaskID mismatch: got %q want %q", sess.TaskID, task.ID)
	}
}

func TestFinishYiCheng(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "yc2", "alice")
	_, token, err := svc.StartYiCheng(ctx, "alice", b.ID, happyTaskReq())
	if err != nil {
		t.Fatalf("StartYiCheng: %v", err)
	}
	out, err := svc.FinishYiCheng(ctx, token, "✓", "all done")
	if err != nil {
		t.Fatalf("FinishYiCheng: %v", err)
	}
	gotDone := false
	for _, s := range out.Symbols {
		if s.Kind == SymStatus && s.Value == "✓" {
			gotDone = true
		}
	}
	if !gotDone {
		t.Errorf("expected ✓ status after finish, got %+v", out.Symbols)
	}
	// Token should be revoked.
	if _, ok, _ := svc.ValidateYiCheng(ctx, token); ok {
		t.Errorf("expected token revoked after Finish")
	}
	// Trace tail should be task_finish.
	trace, _ := svc.ListEvents(ctx, "alice", out.ID)
	if trace[len(trace)-1].Op != "task_finish" {
		t.Errorf("expected tail op=task_finish, got %q", trace[len(trace)-1].Op)
	}
}

func TestAbortYiCheng(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "yc3", "alice")
	_, token, err := svc.StartYiCheng(ctx, "alice", b.ID, happyTaskReq())
	if err != nil {
		t.Fatalf("StartYiCheng: %v", err)
	}
	out, err := svc.AbortYiCheng(ctx, token, "user cancelled")
	if err != nil {
		t.Fatalf("AbortYiCheng: %v", err)
	}
	gotX := false
	for _, s := range out.Symbols {
		if s.Kind == SymStatus && s.Value == "✗" {
			gotX = true
		}
	}
	if !gotX {
		t.Errorf("expected ✗ status after abort, got %+v", out.Symbols)
	}
	// Re-abort is idempotent (returns ErrNotFound — second call sees revoked token).
	_, err2 := svc.AbortYiCheng(ctx, token, "again")
	if !errors.Is(err2, ErrNotFound) {
		t.Errorf("expected ErrNotFound on re-abort, got %v", err2)
	}
}

func TestValidateYiChengExpired(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	if _, ok, err := svc.ValidateYiCheng(ctx, "tsk_nonexistent"); err != nil || ok {
		t.Errorf("expected ok=false err=nil for unknown token; got ok=%v err=%v", ok, err)
	}
	if _, _, err := svc.ValidateYiCheng(ctx, ""); !errors.Is(err, ErrValidation) {
		t.Errorf("expected ErrValidation on empty token, got %v", err)
	}
}

// TestYiChengTokenAutoTrace verifies that a write performed under
// WithYiChengToken auto-appends one trace event on the bound task.
func TestYiChengTokenAutoTrace(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "yc4", "alice")
	task, token, err := svc.StartYiCheng(ctx, "alice", b.ID, happyTaskReq())
	if err != nil {
		t.Fatalf("StartYiCheng: %v", err)
	}
	// Perform a "writer" call: Store an item with WithYiChengToken.
	if _, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		IdemKey:    "k1",
		Kind:       "note",
		SourceType: "manual",
		StorageURI: "row://x/k1",
		Symbols:    []Symbol{{Kind: SymKind, Value: "M"}},
	}, WithYiChengToken(token)); err != nil {
		t.Fatalf("Store w/ token: %v", err)
	}
	trace, _ := svc.ListEvents(ctx, "alice", task.ID)
	foundStoreEvent := false
	for _, st := range trace {
		if st.Op == "store" {
			foundStoreEvent = true
			break
		}
	}
	if !foundStoreEvent {
		t.Errorf("expected an auto-trace `store` event under token; trace=%+v", trace)
	}
}

// TestCrashRecoveryOrphanScan crafts a task with an unclosed trace and
// asserts that re-opening the FileStore appends an orphan_by_crash event.
func TestCrashRecoveryOrphanScan(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	// First open: create a task, start a YiCheng (writes task_start), and
	// then *do not* call Finish/Abort — simulating a crash.
	{
		st, err := OpenFileStore(root)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		svc := NewService(st)
		b, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "orphan", OwnerID: "alice"})
		if err != nil {
			t.Fatalf("create box: %v", err)
		}
		if _, _, err := svc.StartYiCheng(ctx, "alice", b.ID, happyTaskReq()); err != nil {
			t.Fatalf("StartYiCheng: %v", err)
		}
		// process "crashes" — token never gets Finish/Abort.
		_ = st.Close()
	}
	// Second open: should detect the orphan and append a crash event.
	st2, err := OpenFileStore(root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	svc2 := NewService(st2)
	b, err := svc2.GetBoxByKey(ctx, "", "orphan")
	if err != nil {
		t.Fatalf("GetBoxByKey: %v", err)
	}
	items, err := svc2.Browse(ctx, b.ID, BrowseFilter{Kind: "task", Limit: 100})
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 task, got %d", len(items))
	}
	trace, err := svc2.ListEvents(ctx, "alice", items[0].ID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	tail := trace[len(trace)-1]
	if tail.Op != "orphan_by_crash" {
		t.Errorf("expected tail orphan_by_crash, got %q (full=%+v)", tail.Op, trace)
	}
}

// TestPathLedgerFinishNotFrozen — path-ledger invariant: FinishYiCheng does
// NOT freeze the task. SetItemSymbols after Finish must still succeed and
// flip the cursor back (→ for example).
func TestPathLedgerFinishNotFrozen(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	b := newTaskBox(t, svc, "ledger1", "alice")
	task, token, err := svc.StartYiCheng(ctx, "alice", b.ID, happyTaskReq())
	if err != nil {
		t.Fatalf("StartYiCheng: %v", err)
	}
	if _, err := svc.FinishYiCheng(ctx, token, "✓", "done"); err != nil {
		t.Fatalf("FinishYiCheng: %v", err)
	}
	// After finish, flip back to → — must succeed (no frozen guard).
	if _, err := svc.SetItemSymbols(ctx, "alice", task.ID, []Symbol{
		{Kind: SymKind, Value: "T"},
		{Kind: SymStatus, Value: "→"},
	}); err != nil {
		t.Fatalf("SetItemSymbols after finish must succeed (path-ledger): %v", err)
	}
}

// startsWith is a tiny local helper so the test does not pull in extra imports.
func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
