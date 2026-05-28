package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// blobServer spins up a minimal httptest.Server with the blob routes mounted
// behind the same Bearer middleware production uses. The test caller gets
// the URL prefix and the temp root back so it can assert on-disk layout.
func blobServer(t *testing.T) (urlBase, token, root string) {
	t.Helper()
	root = t.TempDir()
	token = "test-bearer-token"
	sub := http.NewServeMux()
	if err := registerBlobRoutes(sub, root); err != nil {
		t.Fatalf("registerBlobRoutes: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/blob/", withBearer(token, sub))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, token, root
}

func TestBlobUploadRoundtrip(t *testing.T) {
	base, tok, root := blobServer(t)

	payload := []byte("hello, blob world\n")
	want := sha256.Sum256(payload)
	wantHex := hex.EncodeToString(want[:])

	// POST /blob/upload
	req, _ := http.NewRequest("POST", base+"/blob/upload", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, body)
	}
	var got struct {
		Sha256     string `json:"sha256"`
		Size       int64  `json:"size"`
		Deduped    bool   `json:"deduped"`
		StorageURI string `json:"storage_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Sha256 != wantHex {
		t.Errorf("sha256 mismatch: got %s want %s", got.Sha256, wantHex)
	}
	if got.Size != int64(len(payload)) {
		t.Errorf("size mismatch: got %d want %d", got.Size, len(payload))
	}
	if got.Deduped {
		t.Errorf("expected deduped=false on first upload")
	}
	if got.StorageURI != "blob://sha256/"+wantHex {
		t.Errorf("storage_uri shape: got %q", got.StorageURI)
	}

	// On-disk layout: <root>/<aa>/<bb>/<sha>
	expectPath := filepath.Join(root, wantHex[:2], wantHex[2:4], wantHex)
	if _, err := os.Stat(expectPath); err != nil {
		t.Fatalf("expected file at %s, got %v", expectPath, err)
	}
	if data, err := os.ReadFile(expectPath); err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("disk bytes mismatch: %v", err)
	}

	// HEAD /blob/<sha>
	hreq, _ := http.NewRequest("HEAD", base+"/blob/"+wantHex, nil)
	hreq.Header.Set("Authorization", "Bearer "+tok)
	hresp, err := http.DefaultClient.Do(hreq)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	hresp.Body.Close()
	if hresp.StatusCode != http.StatusOK {
		t.Errorf("HEAD: expected 200, got %d", hresp.StatusCode)
	}

	// GET /blob/<sha>
	greq, _ := http.NewRequest("GET", base+"/blob/"+wantHex, nil)
	greq.Header.Set("Authorization", "Bearer "+tok)
	gresp, err := http.DefaultClient.Do(greq)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer gresp.Body.Close()
	if gresp.StatusCode != http.StatusOK {
		t.Fatalf("GET expected 200, got %d", gresp.StatusCode)
	}
	body, _ := io.ReadAll(gresp.Body)
	if !bytes.Equal(body, payload) {
		t.Errorf("downloaded bytes mismatch")
	}
}

func TestBlobUploadDedup(t *testing.T) {
	base, tok, _ := blobServer(t)
	payload := []byte("dedup-me")

	doUpload := func() (deduped bool, code int) {
		req, _ := http.NewRequest("POST", base+"/blob/upload", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		defer resp.Body.Close()
		var got struct {
			Deduped bool `json:"deduped"`
		}
		json.NewDecoder(resp.Body).Decode(&got)
		return got.Deduped, resp.StatusCode
	}

	if dup, code := doUpload(); dup || code != http.StatusCreated {
		t.Errorf("first upload: deduped=%v code=%d, want false/201", dup, code)
	}
	if dup, code := doUpload(); !dup || code != http.StatusOK {
		t.Errorf("second upload: deduped=%v code=%d, want true/200", dup, code)
	}
}

func TestBlobUnauthorized(t *testing.T) {
	base, _, _ := blobServer(t)
	req, _ := http.NewRequest("POST", base+"/blob/upload", bytes.NewReader([]byte("x")))
	// no Authorization header
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without Bearer, got %d", resp.StatusCode)
	}
}

func TestBlobNotFound(t *testing.T) {
	base, tok, _ := blobServer(t)
	missing := strings.Repeat("0", 64)
	req, _ := http.NewRequest("GET", base+"/blob/"+missing, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for missing sha, got %d", resp.StatusCode)
	}
}

func TestBlobRejectBadShaPath(t *testing.T) {
	base, tok, _ := blobServer(t)
	for _, bad := range []string{
		"/blob/short",
		"/blob/" + strings.Repeat("g", 64), // non-hex
		"/blob/" + strings.Repeat("0", 63), // wrong length
	} {
		req, _ := http.NewRequest("GET", base+bad, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("get %s: %v", bad, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("path %s: expected 400, got %d", bad, resp.StatusCode)
		}
	}
}

// TestBlobLargeStreaming exercises the streaming path: a 5 MB payload should
// flow through the multi-writer (file + hasher) without buffering whole bytes
// in memory. We just assert correctness, not memory — net/http handles the
// streaming naturally as long as the handler uses io.Copy (which it does).
func TestBlobLargeStreaming(t *testing.T) {
	base, tok, _ := blobServer(t)

	const size = 5 * 1024 * 1024
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte(i % 251)
	}
	want := sha256.Sum256(buf)
	wantHex := hex.EncodeToString(want[:])

	req, _ := http.NewRequest("POST", base+"/blob/upload", bytes.NewReader(buf))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var got struct {
		Sha256 string `json:"sha256"`
		Size   int64  `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Sha256 != wantHex {
		t.Errorf("sha mismatch on large payload")
	}
	if got.Size != size {
		t.Errorf("size mismatch: got %d want %d", got.Size, size)
	}

	// Verify download round-trip and recompute sha.
	greq, _ := http.NewRequest("GET", base+"/blob/"+wantHex, nil)
	greq.Header.Set("Authorization", "Bearer "+tok)
	gresp, err := http.DefaultClient.Do(greq)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer gresp.Body.Close()
	gotBytes, _ := io.ReadAll(gresp.Body)
	if len(gotBytes) != size {
		t.Fatalf("download size mismatch: %d", len(gotBytes))
	}
	gotSha := sha256.Sum256(gotBytes)
	if hex.EncodeToString(gotSha[:]) != wantHex {
		t.Fatalf("download sha mismatch")
	}
}

