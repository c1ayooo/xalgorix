package web

// Tests for the read-only scan-files browser (scan_files.go). They pin the
// security posture the routes promise: traversal and symlink escapes are
// refused before any read, ambiguous/unknown scan ids never resolve, binary
// content is never inlined, image previews are double-checked (extension AND
// magic bytes), and archives skip symlinks and refuse over-cap subtrees
// before a single ZIP byte is written.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedScanFilesTree builds one scan dir under s.dataDir exercising every
// entry kind the browser must handle, plus an outside secret the symlinks
// point at (they must stay refused and intact). The scan id is the dir slug.
func seedScanFilesTree(t *testing.T, s *Server) (scanDir, scanID, outsideSecret string) {
	t.Helper()
	scanID = "scan-files-tree"
	scanDir = filepath.Join(s.dataDir, "files.example", "2026-01-01", scanID)
	if err := os.MkdirAll(scanDir, 0o755); err != nil {
		t.Fatalf("seed scan dir: %v", err)
	}
	record := `{"id":"` + scanID + `","target":"files.example"}`
	if err := os.WriteFile(filepath.Join(scanDir, "scan.json"), []byte(record), 0o644); err != nil {
		t.Fatalf("seed scan.json: %v", err)
	}
	write := func(rel, content string) {
		p := filepath.Join(scanDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("seed parent of %q: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("seed %q: %v", rel, err)
		}
	}
	write("notes.txt", "hello notes")
	write("bin.dat", "ab\x00cd")                 // NUL byte → binary
	write("fake.png", "definitely not an image") // image ext, text magic bytes
	write("tmp/agent-1/payload.txt", "payload")
	write("tool-outputs/to_000001", "tool: probe\nbody")
	// Outside secret + two symlinks pointing at it from inside the scan dir.
	outsideSecret = filepath.Join(s.dataDir, "outside-secret.txt")
	if err := os.WriteFile(outsideSecret, []byte("operator secret"), 0o600); err != nil {
		t.Fatalf("seed outside secret: %v", err)
	}
	if err := os.Symlink(outsideSecret, filepath.Join(scanDir, "link-out")); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}
	if err := os.Symlink(s.dataDir, filepath.Join(scanDir, "link-dir-out")); err != nil {
		t.Fatalf("seed dir symlink: %v", err)
	}
	return scanDir, scanID, outsideSecret
}

// getScanFiles drives the handleScanFiles hub directly (the repo's handler
// test style — data_dirs_test.go calls its handler the same way), so the
// action dispatch, path validation, and guards run end to end.
func getScanFiles(t *testing.T, s *Server, scanID, action, relPath string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/scans/" + scanID + "/files"
	if action != "" {
		target += "/" + action
	}
	if relPath != "" {
		target += "?path=" + relPath
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	s.handleScanFiles(rec, req)
	return rec
}

func entryByName(t *testing.T, body []byte, name string) scanFileEntry {
	t.Helper()
	var listing scanFileListing
	if err := json.Unmarshal(body, &listing); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	for _, e := range listing.Entries {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("entry %q not found in listing: %s", name, body)
	return scanFileEntry{}
}

func TestScanFilesListRoot(t *testing.T) {
	s := newTestServer(t, nil)
	_, scanID, _ := seedScanFilesTree(t, s)

	rec := getScanFiles(t, s, scanID, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if got := entryByName(t, rec.Body.Bytes(), "notes.txt"); got.Type != "file" {
		t.Errorf("notes.txt type = %q, want file", got.Type)
	}
	if got := entryByName(t, rec.Body.Bytes(), "tmp"); got.Type != "dir" {
		t.Errorf("tmp type = %q, want dir", got.Type)
	}
	// Symlinks are listed (so the operator sees them) but typed as links.
	if got := entryByName(t, rec.Body.Bytes(), "link-out"); got.Type != "link" {
		t.Errorf("link-out type = %q, want link", got.Type)
	}
	// Dirs sort before files regardless of name.
	var listing scanFileListing
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	firstFileIdx, firstDirIdx := -1, -1
	for i, e := range listing.Entries {
		if firstDirIdx == -1 && e.Type == "dir" {
			firstDirIdx = i
		}
		if firstFileIdx == -1 && e.Type == "file" {
			firstFileIdx = i
		}
	}
	if firstDirIdx == -1 || firstFileIdx == -1 {
		t.Fatalf("listing lacks dir/file entries: %s", rec.Body.String())
	}
	if firstDirIdx > firstFileIdx {
		t.Errorf("dirs must sort before files: dir idx %d > file idx %d", firstDirIdx, firstFileIdx)
	}
}

func TestScanFilesListTraversalRefused(t *testing.T) {
	s := newTestServer(t, nil)
	_, scanID, _ := seedScanFilesTree(t, s)

	for _, path := range []string{"..", "../", "../../etc/passwd", "/etc/passwd", "tmp/../scan.json"} {
		rec := getScanFiles(t, s, scanID, "", path)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("path %q: status = %d, want 400 (body %s)", path, rec.Code, rec.Body)
		}
	}
}

func TestScanFilesListMissingAndNotDir(t *testing.T) {
	s := newTestServer(t, nil)
	_, scanID, _ := seedScanFilesTree(t, s)

	if rec := getScanFiles(t, s, scanID, "", "no-such-dir"); rec.Code != http.StatusNotFound {
		t.Errorf("missing dir: status = %d, want 404", rec.Code)
	}
	// A file path is not a directory.
	if rec := getScanFiles(t, s, scanID, "", "notes.txt"); rec.Code != http.StatusBadRequest {
		t.Errorf("file as dir: status = %d, want 400", rec.Code)
	}
	// An unknown scan id never resolves.
	if rec := getScanFiles(t, s, "no-such-scan", "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown scan: status = %d, want 404", rec.Code)
	}
}

func TestScanFilesListSubdir(t *testing.T) {
	s := newTestServer(t, nil)
	_, scanID, _ := seedScanFilesTree(t, s)

	rec := getScanFiles(t, s, scanID, "", "tmp/agent-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if got := entryByName(t, rec.Body.Bytes(), "payload.txt"); got.Type != "file" {
		t.Errorf("payload.txt type = %q, want file", got.Type)
	}
}

func TestScanFilesContentTextAndBinary(t *testing.T) {
	s := newTestServer(t, nil)
	_, scanID, _ := seedScanFilesTree(t, s)

	rec := getScanFiles(t, s, scanID, "content", "notes.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var got scanFileContent
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Binary || got.Content != "hello notes" || got.Truncated {
		t.Errorf("text read = %+v, want content %q, text, untruncated", got, "hello notes")
	}

	rec = getScanFiles(t, s, scanID, "content", "bin.dat")
	if rec.Code != http.StatusOK {
		t.Fatalf("binary status = %d", rec.Code)
	}
	got = scanFileContent{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Binary {
		t.Errorf("bin.dat reported binary=false")
	}
	if got.Content != "" {
		t.Errorf("binary file leaked content: %q", got.Content)
	}
}

func TestScanFilesContentTruncated(t *testing.T) {
	s := newTestServer(t, nil)
	_, scanID, _ := seedScanFilesTree(t, s)

	big := strings.Repeat("x", scanFilesReadCap+1024)
	if err := os.WriteFile(filepath.Join(s.dataDir, "files.example", "2026-01-01", "scan-files-tree", "big.txt"),
		[]byte(big), 0o644); err != nil {
		t.Fatalf("seed big.txt: %v", err)
	}

	rec := getScanFiles(t, s, scanID, "content", "big.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got scanFileContent
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Truncated {
		t.Errorf("big.txt truncated = false, want true")
	}
	if len(got.Content) != scanFilesReadCap {
		t.Errorf("content length = %d, want cap %d", len(got.Content), scanFilesReadCap)
	}
}

func TestScanFilesContentSymlinkRefused(t *testing.T) {
	s := newTestServer(t, nil)
	_, scanID, outsideSecret := seedScanFilesTree(t, s)

	rec := getScanFiles(t, s, scanID, "content", "link-out")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("symlink read: status = %d, want 400", rec.Code)
	}
	// The pointed-at secret must be intact and unread through the browser.
	if data, err := os.ReadFile(outsideSecret); err != nil || string(data) != "operator secret" {
		t.Fatalf("outside secret disturbed: err=%v data=%q", err, data)
	}
	// A symlinked *directory* is refused the same way for listing.
	if rec := getScanFiles(t, s, scanID, "", "link-dir-out"); rec.Code != http.StatusBadRequest {
		t.Errorf("symlink dir list: status = %d, want 400", rec.Code)
	}
}

func TestScanFilesPreviewImageGate(t *testing.T) {
	s := newTestServer(t, nil)
	_, scanID, _ := seedScanFilesTree(t, s)
	png := filepath.Join(s.dataDir, "files.example", "2026-01-01", "scan-files-tree", "real.png")
	writeTestPNG(t, png)

	// Real PNG: extension + magic bytes both pass → served as image/png.
	rec := getScanFiles(t, s, scanID, "preview", "real.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("real png: status = %d, body = %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Errorf("content-type = %q, want image/png", ct)
	}

	// Text masquerading as .png: extension passes, magic bytes fail → 400.
	rec = getScanFiles(t, s, scanID, "preview", "fake.png")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("fake png: status = %d, want 400", rec.Code)
	}

	// Non-image extension never previews, whatever the bytes are.
	rec = getScanFiles(t, s, scanID, "preview", "notes.txt")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("txt preview: status = %d, want 400", rec.Code)
	}
}

func TestScanFilesDownload(t *testing.T) {
	s := newTestServer(t, nil)
	_, scanID, _ := seedScanFilesTree(t, s)

	rec := getScanFiles(t, s, scanID, "download", "tmp/agent-1/payload.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "payload.txt") {
		t.Errorf("content-disposition = %q, want attachment with filename", cd)
	}
	if body, _ := io.ReadAll(rec.Body); string(body) != "payload" {
		t.Errorf("body = %q, want %q", body, "payload")
	}
}

func TestScanFilesArchiveSkipsSymlinks(t *testing.T) {
	s := newTestServer(t, nil)
	_, scanID, outsideSecret := seedScanFilesTree(t, s)

	rec := getScanFiles(t, s, scanID, "archive", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("response is not a valid zip: %v", err)
	}
	want := map[string]bool{
		"scan.json":               false,
		"notes.txt":               false,
		"tmp/agent-1/payload.txt": false,
		"tool-outputs/to_000001":  false,
	}
	for _, f := range zr.File {
		if _, ok := want[f.Name]; ok {
			want[f.Name] = true
		}
		if strings.Contains(f.Name, "link-out") {
			t.Errorf("symlink %q archived; symlinks must be skipped", f.Name)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("regular file %q missing from archive", name)
		}
	}
	if data, err := os.ReadFile(outsideSecret); err != nil || string(data) != "operator secret" {
		t.Fatalf("outside secret disturbed: err=%v data=%q", err, data)
	}
}

func TestScanFilesArchiveSubdirNames(t *testing.T) {
	s := newTestServer(t, nil)
	_, scanID, _ := seedScanFilesTree(t, s)

	rec := getScanFiles(t, s, scanID, "archive", "tmp")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("bad zip: %v", err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "agent-1/payload.txt" {
		t.Errorf("entries = %v, want [agent-1/payload.txt]", zr.File)
	}
}

func TestScanFilesArchiveTooLarge(t *testing.T) {
	s := newTestServer(t, nil)
	_, scanID, _ := seedScanFilesTree(t, s)

	overflow := filepath.Join(s.dataDir, "files.example", "2026-01-01", "scan-files-tree", "overflow")
	if err := os.MkdirAll(overflow, 0o755); err != nil {
		t.Fatal(err)
	}
	one := []byte("x")
	for i := 0; i <= scanFilesArchiveMaxFiles; i++ {
		name := filepath.Join(overflow, strings.Repeat("f", 10)+itoaHelper(i)+".txt")
		if err := os.WriteFile(name, one, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	rec := getScanFiles(t, s, scanID, "archive", "overflow")
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	// A refused archive must not have started a ZIP (no zip magic in the body).
	if body, _ := io.ReadAll(rec.Body); bytes.HasPrefix(body, []byte("PK")) {
		t.Errorf("refused archive leaked zip bytes")
	}
}

// itoaHelper keeps the overflow-name loop dependency-light.
func itoaHelper(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

func TestScanFilesMethodGate(t *testing.T) {
	s := newTestServer(t, nil)
	_, scanID, _ := seedScanFilesTree(t, s)

	req := httptest.NewRequest(http.MethodPost, "/api/scans/"+scanID+"/files", nil)
	rec := httptest.NewRecorder()
	s.handleScanFiles(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}
