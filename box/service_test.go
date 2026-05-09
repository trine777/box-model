package box

import (
	"context"
	"encoding/json"
	"testing"
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
	if consumed.Status != "consumed" {
		t.Fatalf("expected consumed status, got %q", consumed.Status)
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
