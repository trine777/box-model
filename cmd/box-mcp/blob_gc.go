package main

// R0.19: blob-layer garbage collection + ref integrity check.
//
// Why GC: the blob layer and the metadata layer are two independent stores
// (invariant #10 keeps them decoupled). Their writes happen in sequence:
//   1) POST /blob/upload  →  bytes on disk
//   2) box_store          →  item.storage_uri = "blob://sha256/<sha>"
//
// Step 1 always precedes Step 2 — sha is computed during upload and only
// returned afterwards, so the client cannot reference a missing blob. The
// asymmetric failure mode is "blob without item" (orphan), never "item
// without blob". GC reclaims orphans on a time delay (default 24h) and
// reports any item-without-blob discrepancies as warnings (never auto-
// fixes; that would be data loss).

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/windborneos/box-model/box"
)

const blobURIPrefix = "blob://sha256/"

// missingRef points at an item whose storage_uri references a blob that is
// no longer on disk. This is never produced by the normal upload flow; it
// indicates someone (or some process) manually deleted blob files.
type missingRef struct {
	ItemID     string `json:"item_id"`
	BoxID      string `json:"box_id"`
	StorageURI string `json:"storage_uri"`
}

// gcOptions controls one GC pass.
type gcOptions struct {
	DryRun    bool
	OlderThan time.Duration // orphans newer than this are spared
}

// gcReport is the JSON-serialised summary the MCP tool returns.
type gcReport struct {
	BlobRoot       string       `json:"blob_root"`
	DiskBlobs      int          `json:"disk_blobs"`
	RefBlobs       int          `json:"ref_blobs"`
	Orphans        []string     `json:"orphans"`
	OrphansDeleted []string     `json:"orphans_deleted"`
	Missing        []missingRef `json:"missing"`
	DryRun         bool         `json:"dry_run"`
	OlderThanSec   int          `json:"older_than_sec"`
}

// extractBlobSha returns (sha, true) when uri is a well-formed blob:// URI.
// Returns ("", false) otherwise. Defensive: rejects malformed hex too.
func extractBlobSha(uri string) (string, bool) {
	if !strings.HasPrefix(uri, blobURIPrefix) {
		return "", false
	}
	sha := strings.TrimPrefix(uri, blobURIPrefix)
	if !isHexSha256(sha) {
		return "", false
	}
	return sha, true
}

// runBlobGC executes one GC pass against the supplied Service + blob root.
// Two-phase plan:
//   1. Enumerate references: walk every box → Browse(IncludeHistory=true)
//      → parse storage_uri → collect referenced shas.
//   2. Enumerate disk: walk root recursively → collect file shas + mtimes.
//   3. Diff:
//        Orphans = Disk \ Refs, filtered by mtime ≥ OlderThan ago.
//        Missing = Refs \ Disk (warning, never auto-deleted).
//   4. If !DryRun: os.Remove each orphan.
//
// History inclusion is deliberate: a v1 revision that points at sha X
// remains a legitimate reference even after v2 supersedes it with a
// different sha. Garbage-collecting X would destroy the v1 payload.
func runBlobGC(ctx context.Context, svc *box.Service, root string, opts gcOptions) (gcReport, error) {
	rep := gcReport{
		BlobRoot:       root,
		DryRun:         opts.DryRun,
		OlderThanSec:   int(opts.OlderThan / time.Second),
		Orphans:        []string{},
		OrphansDeleted: []string{},
		Missing:        []missingRef{},
	}

	// Phase 1: enumerate refs across every box.
	boxIDs, err := svc.AllBoxIDs(ctx)
	if err != nil {
		return rep, fmt.Errorf("enumerate boxes: %w", err)
	}
	refs := map[string][]missingRef{} // sha → which items reference it
	for _, bid := range boxIDs {
		items, err := svc.Browse(ctx, bid, box.BrowseFilter{IncludeHistory: true})
		if err != nil {
			return rep, fmt.Errorf("browse %s: %w", bid, err)
		}
		for _, it := range items {
			sha, ok := extractBlobSha(it.StorageURI)
			if !ok {
				continue
			}
			refs[sha] = append(refs[sha], missingRef{
				ItemID:     it.ID,
				BoxID:      bid,
				StorageURI: it.StorageURI,
			})
		}
	}
	rep.RefBlobs = len(refs)

	// Phase 2: enumerate disk.
	type diskBlob struct {
		path  string
		mtime time.Time
	}
	disk := map[string]diskBlob{}
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Tolerate transient walk errors (e.g. file removed mid-walk);
			// keep going. We return rep with whatever we got.
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !isHexSha256(name) {
			// Skip temp uploads ("upload-*.tmp") and anything not a
			// canonical blob name. Important: never delete these in
			// orphan sweep.
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		disk[name] = diskBlob{path: path, mtime: info.ModTime()}
		return nil
	}); err != nil {
		return rep, fmt.Errorf("walk blob root: %w", err)
	}
	rep.DiskBlobs = len(disk)

	// Phase 3: diff.
	cutoff := time.Now().Add(-opts.OlderThan)
	for sha, db := range disk {
		if _, referenced := refs[sha]; referenced {
			continue
		}
		// Spare blobs that are newer than the cutoff — they may belong to
		// an in-flight upload whose box_store hasn't landed yet.
		if db.mtime.After(cutoff) {
			continue
		}
		rep.Orphans = append(rep.Orphans, sha)
	}
	for sha, locs := range refs {
		if _, present := disk[sha]; !present {
			// Items reference a blob the disk doesn't have. This is bad
			// (probably manual deletion or driver swap); report and move on.
			rep.Missing = append(rep.Missing, locs...)
		}
	}

	// Phase 4: apply deletions if not dry-run.
	if !opts.DryRun {
		for _, sha := range rep.Orphans {
			db, ok := disk[sha]
			if !ok {
				continue
			}
			if err := os.Remove(db.path); err != nil {
				// Best-effort; report what survived.
				continue
			}
			rep.OrphansDeleted = append(rep.OrphansDeleted, sha)
		}
	}

	return rep, nil
}
