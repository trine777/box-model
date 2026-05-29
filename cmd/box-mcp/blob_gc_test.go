package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/windborneos/box-model/box"
)

// gcTestRig spins up a FileStore-backed Service + writes a blob into root.
// Returns helpers so tests can mix-and-match scenarios cheaply.
type gcTestRig struct {
	t      *testing.T
	root   string // BOX_HOME
	blobs  string // BOX_HOME/blobs
	svc    *box.Service
	caller string
}

func newGCRig(t *testing.T) *gcTestRig {
	t.Helper()
	root := t.TempDir()
	store, err := box.OpenFileStore(root)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := box.NewService(store)
	if err := svc.EnsureSymbolBootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return &gcTestRig{
		t:      t,
		root:   root,
		blobs:  filepath.Join(root, "blobs"),
		svc:    svc,
		caller: "alice",
	}
}

// putBlob writes the supplied bytes into the blob layout and returns its sha.
// Bypasses the HTTP route — direct disk write for setup speed.
func (r *gcTestRig) putBlob(payload []byte) string {
	r.t.Helper()
	sum := sha256.Sum256(payload)
	sha := hex.EncodeToString(sum[:])
	p := blobPath(r.blobs, sha)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, payload, 0o644); err != nil {
		r.t.Fatalf("write blob: %v", err)
	}
	return sha
}

// makeBoxWithBlobRef stores one item whose storage_uri points at the given
// sha. Returns the box and item.
func (r *gcTestRig) makeBoxWithBlobRef(boxKey, sha string) (box.Box, box.Item) {
	r.t.Helper()
	ctx := context.Background()
	b, err := r.svc.CreateBox(ctx, box.CreateBoxRequest{
		Key:     boxKey,
		OwnerID: r.caller,
		StoragePolicy: box.StoragePolicy{
			AllowedFormats:  []string{"binary", "json"},
			MaxItems:        1000,
			MaxContentBytes: 1024,
		},
	})
	if err != nil {
		r.t.Fatalf("CreateBox: %v", err)
	}
	item, err := r.svc.Store(ctx, r.caller, b.ID, box.StoreRequest{
		Kind:       "A",
		SourceType: "upload",
		StorageURI: blobURIPrefix + sha,
		IdemKey:    "k-" + sha[:8],
		Format:     "binary",
		Symbols:    []box.Symbol{{Kind: box.SymKind, Value: "A"}},
	})
	if err != nil {
		r.t.Fatalf("Store: %v", err)
	}
	return b, item
}

func TestGCBlobsDryRun(t *testing.T) {
	r := newGCRig(t)
	sha := r.putBlob([]byte("referenced"))
	r.makeBoxWithBlobRef("b1", sha)
	orphanSha := r.putBlob([]byte("orphan"))

	// Backdate the orphan so OlderThan filter accepts it.
	orphanPath := blobPath(r.blobs, orphanSha)
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(orphanPath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	rep, err := runBlobGC(context.Background(), r.svc, r.blobs, gcOptions{
		DryRun:    true,
		OlderThan: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("runBlobGC: %v", err)
	}
	if rep.DiskBlobs != 2 {
		t.Errorf("disk_blobs: got %d want 2", rep.DiskBlobs)
	}
	if rep.RefBlobs != 1 {
		t.Errorf("ref_blobs: got %d want 1", rep.RefBlobs)
	}
	if len(rep.Orphans) != 1 || rep.Orphans[0] != orphanSha {
		t.Errorf("orphans: got %v want [%s]", rep.Orphans, orphanSha)
	}
	if len(rep.OrphansDeleted) != 0 {
		t.Errorf("dry-run must not delete; got %d", len(rep.OrphansDeleted))
	}
	if _, err := os.Stat(orphanPath); err != nil {
		t.Errorf("orphan file should still exist after dry-run: %v", err)
	}
	if len(rep.Missing) != 0 {
		t.Errorf("no missing refs expected, got %v", rep.Missing)
	}
}

func TestGCBlobsApply(t *testing.T) {
	r := newGCRig(t)
	r.makeBoxWithBlobRef("b1", r.putBlob([]byte("ref")))
	orphan := r.putBlob([]byte("orphan-payload"))
	orphanPath := blobPath(r.blobs, orphan)
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(orphanPath, old, old)

	rep, err := runBlobGC(context.Background(), r.svc, r.blobs, gcOptions{
		DryRun:    false,
		OlderThan: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("runBlobGC: %v", err)
	}
	if len(rep.OrphansDeleted) != 1 || rep.OrphansDeleted[0] != orphan {
		t.Errorf("expected delete %s, got %v", orphan, rep.OrphansDeleted)
	}
	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Errorf("orphan file should be gone, got %v", err)
	}
}

func TestGCBlobsSparesFreshOrphans(t *testing.T) {
	r := newGCRig(t)
	freshOrphan := r.putBlob([]byte("just-uploaded"))
	// Don't backdate; mtime ~ now.
	rep, err := runBlobGC(context.Background(), r.svc, r.blobs, gcOptions{
		DryRun:    false,
		OlderThan: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("runBlobGC: %v", err)
	}
	if len(rep.Orphans) != 0 {
		t.Errorf("fresh orphan must be spared (in-flight upload), got %v", rep.Orphans)
	}
	if _, err := os.Stat(blobPath(r.blobs, freshOrphan)); err != nil {
		t.Errorf("fresh orphan deleted unexpectedly: %v", err)
	}
}

func TestGCBlobsDetectsMissingRef(t *testing.T) {
	r := newGCRig(t)
	// item points at sha that has no on-disk blob
	missingSha := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	r.makeBoxWithBlobRef("b1", missingSha)

	rep, err := runBlobGC(context.Background(), r.svc, r.blobs, gcOptions{DryRun: true})
	if err != nil {
		t.Fatalf("runBlobGC: %v", err)
	}
	if len(rep.Missing) != 1 || rep.Missing[0].StorageURI != blobURIPrefix+missingSha {
		t.Errorf("expected one missing ref to %s, got %v", missingSha, rep.Missing)
	}
}

func TestGCBlobsIgnoresNonShaFiles(t *testing.T) {
	r := newGCRig(t)
	// Drop a tempfile-like name in the blob root.
	if err := os.MkdirAll(r.blobs, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(r.blobs, "upload-junk.tmp"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	rep, err := runBlobGC(context.Background(), r.svc, r.blobs, gcOptions{DryRun: false})
	if err != nil {
		t.Fatalf("runBlobGC: %v", err)
	}
	if rep.DiskBlobs != 0 {
		t.Errorf("non-sha files must not be counted as blobs; got %d", rep.DiskBlobs)
	}
	if len(rep.Orphans) != 0 {
		t.Errorf("non-sha files must not be candidates; got %v", rep.Orphans)
	}
	if _, err := os.Stat(filepath.Join(r.blobs, "upload-junk.tmp")); err != nil {
		t.Errorf("non-sha file deleted unexpectedly: %v", err)
	}
}

// TestItemBlobDownloadRoute spins up the full Bearer + routes stack and
// exercises the one-shot /items/<id>/blob download an external machine
// would actually use.
func TestItemBlobDownloadRoute(t *testing.T) {
	r := newGCRig(t)

	payload := []byte("downloadable content here\n")
	sha := r.putBlob(payload)
	_, item := r.makeBoxWithBlobRef("ext", sha)

	tok := "test-token"
	mux := http.NewServeMux()
	itemSub := http.NewServeMux()
	registerItemBlobRoute(itemSub, r.svc, r.blobs, r.caller)
	mux.Handle("/items/", withBearer(false, tok, itemSub))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// 1. Bearer required.
	resp, err := http.Get(srv.URL + "/items/" + item.ID + "/blob")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without Bearer, got %d", resp.StatusCode)
	}

	// 2. Happy path.
	req, _ := http.NewRequest("GET", srv.URL+"/items/"+item.ID+"/blob", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, payload) {
		t.Errorf("payload mismatch")
	}
	if etag := resp.Header.Get("ETag"); etag != `"`+sha+`"` {
		t.Errorf("ETag: got %q want %q", etag, `"`+sha+`"`)
	}
	if h := resp.Header.Get("X-Box-Sha256"); h != sha {
		t.Errorf("X-Box-Sha256: got %q want %q", h, sha)
	}

	// 3. Missing blob → 502.
	deadSha := "00000000000000000000000000000000000000000000000000000000000000aa"
	_, deadItem := r.makeBoxWithBlobRef("ext2", deadSha)
	req2, _ := http.NewRequest("GET", srv.URL+"/items/"+deadItem.ID+"/blob", nil)
	req2.Header.Set("Authorization", "Bearer "+tok)
	resp2, _ := http.DefaultClient.Do(req2)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502 for missing blob, got %d", resp2.StatusCode)
	}

	// 4. Wrong URL shape → 400.
	bad, _ := http.NewRequest("GET", srv.URL+"/items/abc", nil)
	bad.Header.Set("Authorization", "Bearer "+tok)
	bresp, _ := http.DefaultClient.Do(bad)
	bresp.Body.Close()
	if bresp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for malformed path, got %d", bresp.StatusCode)
	}
}
