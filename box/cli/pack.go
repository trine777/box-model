package cli

// R9.0: box export / import — self-contained .boxpack archives.
//
// A .boxpack is a gzip-compressed tar holding everything needed to
// reconstruct one box on another box-model install, with full fidelity:
//
//	manifest.json              format + source key + counts + timestamp
//	box/box.json               the Box definition (key/owner/policy/labels)
//	box/items/<id>.json        EVERY item — including superseded history
//	                           revisions (the files are copied verbatim, so
//	                           is_latest / revision_of links are preserved)
//	box/tasks/<id>.trace.jsonl program-track (程辙) event logs, if any
//	blobs/<sha256>             raw bytes for every blob:// referenced by an
//	                           item (so blob downloads work on the target)
//
// Deliberately file-level (not via the Service API): box_store can only
// create latest items, so an API-based export silently drops history +
// blob bytes + traces. Copying the FileStore files verbatim is the only
// way to get a faithful archive — which is exactly what DR / delivery /
// history-completeness all need.
//
// stdlib-only (archive/tar + compress/gzip + encoding/json), consistent
// with the box/ core constraint.

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const boxpackFormat = "boxpack/1"

// packManifest is the self-describing header of a .boxpack.
type packManifest struct {
	Format     string `json:"format"`
	SourceKey  string `json:"source_key"`
	ExportedAt string `json:"exported_at"`
	ItemFiles  int    `json:"item_files"`
	TraceFiles int    `json:"trace_files"`
	BlobFiles  int    `json:"blob_files"`
	Note       string `json:"note"`
}

// blobURIPrefix mirrors the cmd/box-mcp constant; an item whose storage_uri
// starts with this carries its bytes in the blob layer.
const blobURIPrefix = "blob://sha256/"

// cmdExport packs one box (by key) into a .boxpack file.
//
//	box export <key> [-o out.boxpack] [--root PATH]
func (rc *rootContext) cmdExport() int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	root := fs.String("root", "", "override storage root")
	out := fs.String("o", "", "output file (default <key>.boxpack)")
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: export requires <box_key>")
		return 2
	}
	key := pos[0]
	home, err := rc.resolveRoot(*root)
	if err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err)
		return 2
	}
	boxDir := filepath.Join(home, "boxes", key)
	if fi, err := os.Stat(boxDir); err != nil || !fi.IsDir() {
		fmt.Fprintf(rc.stderr, "Error: box %q not found under %s\n", key, home)
		return 4
	}

	outPath := *out
	if outPath == "" {
		outPath = key + ".boxpack"
	}
	f, err := os.Create(outPath)
	if err != nil {
		fmt.Fprintf(rc.stderr, "Error: create %s: %s\n", outPath, err)
		return 1
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	man := packManifest{
		Format:     boxpackFormat,
		SourceKey:  key,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Note:       "self-contained: items incl. history, traces, referenced blobs",
	}

	// Collect referenced blob shas while walking item files.
	blobShas := map[string]bool{}

	itemsDir := filepath.Join(boxDir, "items")
	tasksDir := filepath.Join(boxDir, "tasks")

	// 1. box.json
	if err := addFileToTar(tw, filepath.Join(boxDir, "box.json"), "box/box.json"); err != nil {
		fmt.Fprintf(rc.stderr, "Error: pack box.json: %s\n", err)
		return 1
	}
	// 2. items (all versions) — also scan for blob refs
	itemFiles, _ := filepath.Glob(filepath.Join(itemsDir, "*.json"))
	for _, p := range itemFiles {
		if err := addFileToTar(tw, p, "box/items/"+filepath.Base(p)); err != nil {
			fmt.Fprintf(rc.stderr, "Error: pack item %s: %s\n", p, err)
			return 1
		}
		if sha := blobShaOf(p); sha != "" {
			blobShas[sha] = true
		}
	}
	man.ItemFiles = len(itemFiles)
	// 3. traces
	traceFiles, _ := filepath.Glob(filepath.Join(tasksDir, "*.trace.jsonl"))
	for _, p := range traceFiles {
		if err := addFileToTar(tw, p, "box/tasks/"+filepath.Base(p)); err != nil {
			fmt.Fprintf(rc.stderr, "Error: pack trace %s: %s\n", p, err)
			return 1
		}
	}
	man.TraceFiles = len(traceFiles)
	// 4. blobs referenced by items
	for sha := range blobShas {
		src := filepath.Join(home, "blobs", sha[:2], sha[2:4], sha)
		if _, err := os.Stat(src); err != nil {
			// Missing blob — record but don't fail (the item points at a
			// blob whose bytes aren't on disk; surface as a soft warning).
			fmt.Fprintf(rc.stderr, "warning: referenced blob %s not on disk; skipped\n", sha)
			continue
		}
		if err := addFileToTar(tw, src, "blobs/"+sha); err != nil {
			fmt.Fprintf(rc.stderr, "Error: pack blob %s: %s\n", sha, err)
			return 1
		}
		man.BlobFiles++
	}
	// 5. manifest (last so counts are final) — written first in tar order is
	// nicer, but tar readers don't require order; we add it now.
	manBytes, _ := json.MarshalIndent(man, "", "  ")
	if err := addBytesToTar(tw, manBytes, "manifest.json"); err != nil {
		fmt.Fprintf(rc.stderr, "Error: pack manifest: %s\n", err)
		return 1
	}

	if err := tw.Close(); err != nil {
		fmt.Fprintf(rc.stderr, "Error: finalize tar: %s\n", err)
		return 1
	}
	if err := gz.Close(); err != nil {
		fmt.Fprintf(rc.stderr, "Error: finalize gzip: %s\n", err)
		return 1
	}
	fmt.Fprintf(rc.stdout, "exported %q → %s (%d items, %d traces, %d blobs)\n",
		key, outPath, man.ItemFiles, man.TraceFiles, man.BlobFiles)
	return 0
}

// cmdImport unpacks a .boxpack into a box home.
//
//	box import <file.boxpack> [--root PATH] [--on-conflict error|skip|overwrite]
//
// After import, restart box-mcp so its FileStore re-scans the new files.
func (rc *rootContext) cmdImport() int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(rc.stderr)
	root := fs.String("root", "", "override storage root")
	onConflict := fs.String("on-conflict", "error", "error|skip|overwrite when target box exists")
	pos, flagArgs := splitArgs(rc.args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(rc.stderr, "Error: import requires <file.boxpack>")
		return 2
	}
	packPath := pos[0]
	home, err := rc.resolveRoot(*root)
	if err != nil {
		fmt.Fprintf(rc.stderr, "Error: %s\n", err)
		return 2
	}
	f, err := os.Open(packPath)
	if err != nil {
		fmt.Fprintf(rc.stderr, "Error: open %s: %s\n", packPath, err)
		return 4
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		fmt.Fprintf(rc.stderr, "Error: gunzip: %s\n", err)
		return 1
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	// First pass: buffer entries (a .boxpack is small enough; lets us read
	// the manifest, resolve the key, and check conflict before writing).
	entries := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(rc.stderr, "Error: read tar: %s\n", err)
			return 1
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// Guard against path traversal.
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			fmt.Fprintf(rc.stderr, "Error: unsafe path in archive: %s\n", hdr.Name)
			return 1
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			fmt.Fprintf(rc.stderr, "Error: read entry %s: %s\n", hdr.Name, err)
			return 1
		}
		entries[clean] = data
	}

	manRaw, ok := entries["manifest.json"]
	if !ok {
		fmt.Fprintln(rc.stderr, "Error: no manifest.json — not a .boxpack")
		return 2
	}
	var man packManifest
	if err := json.Unmarshal(manRaw, &man); err != nil {
		fmt.Fprintf(rc.stderr, "Error: bad manifest: %s\n", err)
		return 2
	}
	if !strings.HasPrefix(man.Format, "boxpack/") {
		fmt.Fprintf(rc.stderr, "Error: unknown format %q\n", man.Format)
		return 2
	}
	key := man.SourceKey
	boxDir := filepath.Join(home, "boxes", key)
	if _, err := os.Stat(boxDir); err == nil {
		switch *onConflict {
		case "skip":
			fmt.Fprintf(rc.stdout, "box %q already exists; skipped (--on-conflict=skip)\n", key)
			return 0
		case "overwrite":
			// fall through and write over
		default:
			fmt.Fprintf(rc.stderr, "Error: box %q already exists. Use --on-conflict=overwrite|skip\n", key)
			return 5
		}
	}

	// Write box/* and blobs/* to disk.
	wrote := 0
	for name, data := range entries {
		if name == "manifest.json" {
			continue
		}
		var dest string
		switch {
		case strings.HasPrefix(name, "box/"):
			dest = filepath.Join(boxDir, strings.TrimPrefix(name, "box/"))
		case strings.HasPrefix(name, "blobs/"):
			sha := strings.TrimPrefix(name, "blobs/")
			if len(sha) < 4 {
				continue
			}
			dest = filepath.Join(home, "blobs", sha[:2], sha[2:4], sha)
			if _, err := os.Stat(dest); err == nil {
				continue // blob already present (content-addressed dedup)
			}
		default:
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			fmt.Fprintf(rc.stderr, "Error: mkdir %s: %s\n", filepath.Dir(dest), err)
			return 1
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			fmt.Fprintf(rc.stderr, "Error: write %s: %s\n", dest, err)
			return 1
		}
		wrote++
	}
	fmt.Fprintf(rc.stdout, "imported %q from %s (%d files: %d items, %d traces, %d blobs)\n",
		key, packPath, wrote, man.ItemFiles, man.TraceFiles, man.BlobFiles)
	fmt.Fprintln(rc.stdout, "→ restart box-mcp so its FileStore re-scans the new files.")
	return 0
}

// addFileToTar streams a file into the tar with the given archive name.
func addFileToTar(tw *tar.Writer, srcPath, name string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	return addBytesToTar(tw, data, name)
}

func addBytesToTar(tw *tar.Writer, data []byte, name string) error {
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// blobShaOf reads an item JSON file and returns the blob sha if its
// storage_uri is a blob:// reference, else "".
func blobShaOf(itemPath string) string {
	data, err := os.ReadFile(itemPath)
	if err != nil {
		return ""
	}
	var it struct {
		StorageURI string `json:"storage_uri"`
	}
	if json.Unmarshal(data, &it) != nil {
		return ""
	}
	if strings.HasPrefix(it.StorageURI, blobURIPrefix) {
		return strings.TrimPrefix(it.StorageURI, blobURIPrefix)
	}
	return ""
}
