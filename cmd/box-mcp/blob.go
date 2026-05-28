package main

// R0.18 (in progress): local-FS blob layer mounted alongside MCP routes.
//
// HTTP endpoints (all Bearer-protected via withBearer):
//   POST /blob/upload    — body = raw bytes; server hashes, dedups, stores
//   GET  /blob/<sha256>  — stream blob bytes back
//   HEAD /blob/<sha256>  — 200 if exists, 404 otherwise
//
// Storage layout (under BOX_BLOB_ROOT, default $BOX_HOME/blobs):
//   <root>/<aa>/<bb>/<sha256>      where aa/bb are the first 4 hex chars
//
// Content-addressed: the same bytes uploaded twice produce one file. The
// sha256 IS the key — there is no metadata layer here. Pairing with
// box-mcp is via the agent assembling storage_uri = "blob://sha256/<hash>"
// in a subsequent box_store call. Box never reads the blob; this layer is
// dumb bytes (mirrors invariant #10 at a different layer).
//
// Atomic writes: temp file in target dir + rename. Concurrent uploads of
// the same sha collide harmlessly (last rename wins, both yielded identical
// bytes).

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// blobRoot resolves the on-disk root for blob storage. Order:
//   1. $BOX_BLOB_ROOT
//   2. $BOX_HOME/blobs
//   3. ~/.box/blobs
func blobRoot(boxHome string) (string, error) {
	if v := os.Getenv("BOX_BLOB_ROOT"); v != "" {
		return v, nil
	}
	if boxHome != "" {
		return filepath.Join(boxHome, "blobs"), nil
	}
	if v := os.Getenv("BOX_HOME"); v != "" {
		return filepath.Join(v, "blobs"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".box", "blobs"), nil
}

// blobPath returns <root>/<aa>/<bb>/<sha> for the supplied 64-char lowercase
// hex sha256. The aa/bb prefix sharding keeps any single directory's child
// count bounded as the corpus grows.
func blobPath(root, sha string) string {
	return filepath.Join(root, sha[:2], sha[2:4], sha)
}

// isHexSha256 reports whether s is a lowercase 64-char hex sha256 literal.
// We do not use regexp here; a char-by-char check is ~10× faster and the
// pattern is fixed.
func isHexSha256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// blobHandlers registers /blob/upload, /blob/<sha>, /blob/ (404 catchall) on
// the supplied mux. The Bearer middleware is applied outside.
func registerBlobRoutes(mux *http.ServeMux, root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("blob root mkdir: %w", err)
	}
	mux.HandleFunc("/blob/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		handleBlobUpload(w, r, root)
	})
	mux.HandleFunc("/blob/", func(w http.ResponseWriter, r *http.Request) {
		// /blob/<sha256>   GET = stream / HEAD = exists
		sha := strings.TrimPrefix(r.URL.Path, "/blob/")
		if !isHexSha256(sha) {
			http.Error(w, "expected /blob/<64-hex-sha256>", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodGet:
			handleBlobGet(w, r, root, sha)
		case http.MethodHead:
			handleBlobHead(w, r, root, sha)
		default:
			http.Error(w, "GET or HEAD only", http.StatusMethodNotAllowed)
		}
	})
	return nil
}

// handleBlobUpload streams the request body through sha256.New() into a temp
// file, then renames into final position. Returns JSON {sha256,size}.
//
// Idempotency: if the sha already exists, the temp file is discarded and
// the existing entry is reported — last-writer-wins is safe because both
// writers produced identical bytes (sha is the content).
func handleBlobUpload(w http.ResponseWriter, r *http.Request, root string) {
	// Build a temp file in the root so the eventual rename is intra-FS.
	tmp, err := os.CreateTemp(root, "upload-*.tmp")
	if err != nil {
		http.Error(w, "create temp: "+err.Error(), http.StatusInternalServerError)
		return
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if we error out before renaming.
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	hasher := sha256.New()
	mw := io.MultiWriter(tmp, hasher)
	n, err := io.Copy(mw, r.Body)
	closeErr := tmp.Close()
	if err != nil {
		http.Error(w, "copy: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if closeErr != nil {
		http.Error(w, "close: "+closeErr.Error(), http.StatusInternalServerError)
		return
	}

	sha := hex.EncodeToString(hasher.Sum(nil))
	finalPath := blobPath(root, sha)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		http.Error(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// If the final already exists, drop the temp and report ok (dedup).
	if _, err := os.Stat(finalPath); err == nil {
		// tmp will be removed by the deferred cleanup
		writeBlobJSON(w, http.StatusOK, sha, n, true)
		return
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		http.Error(w, "rename: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// rename succeeded; reset tmpPath so the deferred Remove is a no-op
	tmpPath = ""
	writeBlobJSON(w, http.StatusCreated, sha, n, false)
}

func writeBlobJSON(w http.ResponseWriter, code int, sha string, size int64, deduped bool) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"sha256":%q,"size":%d,"deduped":%t,"storage_uri":"blob://sha256/%s"}`, sha, size, deduped, sha)
}

func handleBlobGet(w http.ResponseWriter, r *http.Request, root, sha string) {
	p := blobPath(root, sha)
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "open: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "stat: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.Header().Set("ETag", `"`+sha+`"`)
	http.ServeContent(w, r, sha, info.ModTime(), f) // handles Range requests for free
}

func handleBlobHead(w http.ResponseWriter, _ *http.Request, root, sha string) {
	p := blobPath(root, sha)
	info, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "stat: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("ETag", `"`+sha+`"`)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.WriteHeader(http.StatusOK)
}
