package box

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// R0.23 F1 fix: forbidden errors used to be opaque "forbidden" — every
// external agent had to guess that caller-owner mismatch was the cause.
// New behaviour: errors.Is(err, ErrForbidden) still passes, AND the text
// names the gate ("caller_owner_mismatch") plus the two identifiers so the
// caller can fix it in one read.

func TestForbiddenReason_StoreMismatch(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	if _, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "k", OwnerID: "alice"}); err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	b, err := svc.GetBoxByKey(ctx, "alice", "k")
	if err != nil {
		t.Fatalf("GetBoxByKey: %v", err)
	}
	_, err = svc.Store(ctx, "mallory", b.ID, StoreRequest{
		Kind:       "M",
		SourceType: "manual",
		StorageURI: "row://x/1",
		IdemKey:    "k1",
		Symbols:    []Symbol{{Kind: SymKind, Value: "M"}},
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("want ErrForbidden, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"caller_owner_mismatch", `caller="mallory"`, `box_owner="alice"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected error to contain %q; full text was: %q", want, msg)
		}
	}
}

func TestForbiddenReason_DeleteMismatch(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	if _, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "kk", OwnerID: "alice"}); err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	b, _ := svc.GetBoxByKey(ctx, "alice", "kk")
	item, err := svc.Store(ctx, "alice", b.ID, StoreRequest{
		Kind: "M", SourceType: "m", StorageURI: "row://x/1", IdemKey: "k1",
		Symbols: []Symbol{{Kind: SymKind, Value: "M"}},
	})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	_, err = svc.DeleteItem(ctx, "mallory", item.ID)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("want ErrForbidden, got %v", err)
	}
	if !strings.Contains(err.Error(), "caller_owner_mismatch") {
		t.Errorf("DeleteItem should also carry reason; got %q", err.Error())
	}
}

func TestForbiddenReason_SealMismatch(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	if _, err := svc.CreateBox(ctx, CreateBoxRequest{Key: "ks", OwnerID: "alice"}); err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	b, _ := svc.GetBoxByKey(ctx, "alice", "ks")
	err := svc.SealBox(ctx, "mallory", b.ID)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("want ErrForbidden, got %v", err)
	}
	if !strings.Contains(err.Error(), "caller_owner_mismatch") {
		t.Errorf("SealBox should also carry reason; got %q", err.Error())
	}
}
