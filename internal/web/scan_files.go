package web

// Read-only file browser over one scan's directory (the "Run 文件浏览器").
//
// Routes (dispatched from the /api/scans/ hub in server.go):
//
//	GET /api/scans/{id}/files?path=          single-level listing (lazy, no recursion)
//	GET /api/scans/{id}/files/content?path=  bounded inline text read (2 MiB cap)
//	GET /api/scans/{id}/files/preview?path=  images only, extension AND magic-byte checked
//	GET /api/scans/{id}/files/download?path= attachment stream (http.ServeContent)
//	GET /api/scans/{id}/files/archive?path=  streaming ZIP of a subtree (pre-flight capped)
//
// Security model (mirrors data_dirs.go discipline, all reads, no writes):
//
//  1. scanID resolves through resolveScanDirByID — Base-sanitized and
//     ambiguity-refusing, the same primitive the detail/events/report
//     handlers use.
//  2. The untrusted ?path= value is a scan-dir-relative POSIX path validated
//     with fs.ValidPath (".." , leading "/", and non-portable forms are
//     rejected syntactically before any syscall).
//  3. Every filesystem access goes through os.OpenRoot(scanDir). os.Root
//     refuses to open anything outside the scan dir, including through
//     symlinked intermediate components, so a symlink can never escape to
//     operator files.
//  4. Final-component symlinks are additionally refused on open (and shown as
//     type "link" in listings): what the dashboard browses must be exactly
//     what is on disk.
//  5. HTML/SVG are never served inline as documents (only as JSON-escaped
//     text via /content) — the stored-XSS surface of a scan dir stays closed.

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// scanFilesListCap bounds a single listing: scan dirs can hold tens of
// thousands of entries (walkScanDirs' documented invariant about artifact
// subtrees), so the browser fetches lazily and the API never dumps unbounded
// directory contents.
const scanFilesListCap = 1000

// scanFilesReadCap caps an inline text read (2 MiB, the same order 4zreco's
// workspace reader uses). Oversized files are flagged with truncated=true and
// the operator downloads them instead.
const scanFilesReadCap = 2 << 20

// Archive pre-flight caps: metadata-only walk first, so an over-cap subtree
// is refused with a clean 413 instead of a truncated ZIP delivered as
// success.
const (
	scanFilesArchiveMaxFiles = 2000
	scanFilesArchiveMaxBytes = 1 << 30 // 1 GiB uncompressed
	scanFilesArchiveMaxDepth = 16
)

// scanFilesImageExt is the preview whitelist. It deliberately excludes SVG —
// image/svg+xml executes script when rendered inline.
var scanFilesImageExt = map[string]bool{
	"png": true, "jpg": true, "jpeg": true,
	"gif": true, "webp": true, "bmp": true,
}

// Typed refusals mapped to HTTP statuses by writeScanFileError.
var (
	errScanFilesInvalidPath = errors.New("invalid path")
	errScanFilesNotFound    = errors.New("not found")
	errScanFilesNotDir      = errors.New("not a directory")
	errScanFilesNotFile     = errors.New("not a regular file")
	errScanFilesSymlink     = errors.New("symlinked entries cannot be opened")
	errScanFilesTooLarge    = errors.New("archive exceeds limits")
)

// writeScanFileError maps a scan-files sentinel to its HTTP status and JSON
// body. These are routine refusals, not server errors, so none of them are
// error-logged (same discipline as writeDataDirError).
func writeScanFileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errScanFilesNotFound):
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not found"})
	case errors.Is(err, errScanFilesNotDir):
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "not a directory"})
	case errors.Is(err, errScanFilesNotFile):
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "not a regular file"})
	case errors.Is(err, errScanFilesSymlink):
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": errScanFilesSymlink.Error()})
	case errors.Is(err, errScanFilesTooLarge):
		writeJSONStatus(w, http.StatusRequestEntityTooLarge,
			map[string]string{"error": "directory exceeds archive limits"})
	default: // errScanFilesInvalidPath and anything unrecognized
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
	}
}

// scanFilesRelPath validates the untrusted ?path= query value as a portable
// scan-dir-relative path. "" and "." both mean the scan dir root; anything
// fs.ValidPath rejects ("..", absolute paths, "a/../b", empty segments) is a
// 400 before any filesystem syscall.
func scanFilesRelPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ".", nil
	}
	if !fs.ValidPath(raw) {
		return "", errScanFilesInvalidPath
	}
	return raw, nil
}

// handleScanFiles is the /api/scans/{id}/files hub. The server.go dispatcher
// has already matched "{id}/files..." by segment; here the action suffix picks
// the sub-handler.
func (s *Server) handleScanFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/scans/")
	slash := strings.Index(rest, "/")
	if slash <= 0 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	scanID, action := rest[:slash], strings.TrimPrefix(rest[slash+1:], "files")
	switch action {
	case "":
		s.handleScanFilesList(w, r, scanID)
	case "/content":
		s.handleScanFilesContent(w, r, scanID)
	case "/preview":
		s.handleScanFilesPreview(w, r, scanID)
	case "/download":
		s.handleScanFilesDownload(w, r, scanID)
	case "/archive":
		s.handleScanFilesArchive(w, r, scanID)
	default:
		http.Error(w, "unknown files action", http.StatusNotFound)
	}
}

// openScanFilesRoot resolves scanID to its on-disk directory and opens it as
// an os.Root. The caller owns the root and must Close it.
func (s *Server) openScanFilesRoot(scanID string) (*os.Root, error) {
	dir, ok := s.resolveScanDirByID(scanID)
	if !ok {
		return nil, errScanFilesNotFound
	}
	return os.OpenRoot(dir)
}

// openScanFileRegular opens rel under root and returns the open file plus its
// on-disk FileInfo, refusing anything that is not a plain regular file:
// missing paths, directories, devices, and — deliberately — final-component
// symlinks, so the dashboard can only read exactly the bytes stored on disk.
// os.Root already guarantees intermediate components cannot escape the scan
// dir.
func openScanFileRegular(root *os.Root, rel string) (*os.File, os.FileInfo, error) {
	fi, err := root.Lstat(rel)
	if err != nil {
		return nil, nil, errScanFilesNotFound
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, nil, errScanFilesSymlink
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, errScanFilesNotFile
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, nil, errScanFilesNotFound
	}
	return f, fi, nil
}

// scanFileEntry is one row of a listing. type is "dir" | "file" | "link" |
// "other" (devices/fifos — visible but never openable).
type scanFileEntry struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mtime"` // unix millis
}

type scanFileListing struct {
	Path      string          `json:"path"`
	Entries   []scanFileEntry `json:"entries"`
	Truncated bool            `json:"truncated"`
}

// handleScanFilesList serves GET /api/scans/{id}/files?path= — a single-level
// listing, sorted dirs-first then by name, capped at scanFilesListCap entries
// with a truncated flag (the frontend fetches deeper levels lazily).
func (s *Server) handleScanFilesList(w http.ResponseWriter, r *http.Request, scanID string) {
	rel, err := scanFilesRelPath(r.URL.Query().Get("path"))
	if err != nil {
		writeScanFileError(w, err)
		return
	}
	root, err := s.openScanFilesRoot(scanID)
	if err != nil {
		writeScanFileError(w, err)
		return
	}
	defer func() { _ = root.Close() }()

	fi, err := root.Lstat(rel)
	if err != nil {
		writeScanFileError(w, errScanFilesNotFound)
		return
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		writeScanFileError(w, errScanFilesSymlink)
		return
	}
	if !fi.IsDir() {
		writeScanFileError(w, errScanFilesNotDir)
		return
	}
	f, err := root.Open(rel)
	if err != nil {
		writeScanFileError(w, errScanFilesNotFound)
		return
	}
	des, readErr := f.ReadDir(-1)
	f.Close()
	if readErr != nil && len(des) == 0 {
		writeScanFileError(w, errScanFilesNotFound)
		return
	}

	entries := make([]scanFileEntry, 0, len(des))
	for _, de := range des {
		e := scanFileEntry{Name: de.Name(), Type: "other"}
		switch {
		case de.Type()&fs.ModeSymlink != 0:
			e.Type = "link"
		case de.Type().IsDir():
			e.Type = "dir"
		case de.Type().IsRegular():
			e.Type = "file"
		}
		if info, err := de.Info(); err == nil {
			if de.Type().IsRegular() {
				e.Size = info.Size()
			}
			e.ModTime = info.ModTime().UnixMilli()
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if (a.Type == "dir") != (b.Type == "dir") {
			return a.Type == "dir" // dirs first
		}
		return a.Name < b.Name
	})
	truncated := false
	if len(entries) > scanFilesListCap {
		entries = entries[:scanFilesListCap]
		truncated = true
	}
	if entries == nil {
		entries = []scanFileEntry{}
	}
	writeJSONStatus(w, http.StatusOK, scanFileListing{Path: rel, Entries: entries, Truncated: truncated})
}

type scanFileContent struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	ModTime   int64  `json:"mtime"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
	Content   string `json:"content,omitempty"`
}

// handleScanFilesContent serves GET /api/scans/{id}/files/content?path= — a
// bounded text read. Binary files (NUL sniff + content-type sniff) are
// reported with binary=true and no content; the frontend offers a download
// instead. The payload is JSON-escaped text, so even HTML sources are safe to
// show — the no-inline-HTML rule applies to /preview, not here.
func (s *Server) handleScanFilesContent(w http.ResponseWriter, r *http.Request, scanID string) {
	rel, err := scanFilesRelPath(r.URL.Query().Get("path"))
	if err != nil || rel == "." {
		writeScanFileError(w, errScanFilesInvalidPath)
		return
	}
	root, err := s.openScanFilesRoot(scanID)
	if err != nil {
		writeScanFileError(w, err)
		return
	}
	defer func() { _ = root.Close() }()

	f, fi, err := openScanFileRegular(root, rel)
	if err != nil {
		writeScanFileError(w, err)
		return
	}
	defer f.Close()

	data, readErr := io.ReadAll(io.LimitReader(f, scanFilesReadCap+1))
	if readErr != nil && len(data) == 0 {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "read failed"})
		return
	}
	truncated := len(data) > scanFilesReadCap
	if truncated {
		data = data[:scanFilesReadCap]
	}
	binary := scanFileLooksBinary(data)
	resp := scanFileContent{
		Path:      rel,
		Size:      fi.Size(),
		ModTime:   fi.ModTime().UnixMilli(),
		Truncated: truncated,
		Binary:    binary,
	}
	if !binary {
		resp.Content = string(data)
	}
	writeJSONStatus(w, http.StatusOK, resp)
}

// scanFileLooksBinary applies the classic heuristics to the read bytes: a NUL
// byte anywhere in the sample, or a DetectContentType verdict that is neither
// text/* nor JSON.
func scanFileLooksBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	sample := data
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return true
	}
	head := sample
	if len(head) > 512 {
		head = head[:512]
	}
	ct := http.DetectContentType(head)
	return !strings.HasPrefix(ct, "text/") && ct != "application/json"
}

// handleScanFilesPreview serves GET /api/scans/{id}/files/preview?path= —
// images only, guarded twice: the extension must be whitelisted AND the magic
// bytes must sniff as image/*. A file that passes only one check is refused,
// so a renamed HTML payload can never render inline. SVG is excluded at the
// whitelist (script-capable when rendered).
func (s *Server) handleScanFilesPreview(w http.ResponseWriter, r *http.Request, scanID string) {
	rel, err := scanFilesRelPath(r.URL.Query().Get("path"))
	if err != nil || rel == "." {
		writeScanFileError(w, errScanFilesInvalidPath)
		return
	}
	if ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(rel)), "."); !scanFilesImageExt[ext] {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "not a previewable image"})
		return
	}
	root, err := s.openScanFilesRoot(scanID)
	if err != nil {
		writeScanFileError(w, err)
		return
	}
	defer func() { _ = root.Close() }()

	f, fi, err := openScanFileRegular(root, rel)
	if err != nil {
		writeScanFileError(w, err)
		return
	}
	defer f.Close()

	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	ct := http.DetectContentType(head[:n])
	if !strings.HasPrefix(ct, "image/") {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "content is not an image"})
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "read failed"})
		return
	}
	http.ServeContent(w, r, filepath.Base(rel), fi.ModTime(), f)
}

// handleScanFilesDownload serves GET /api/scans/{id}/files/download?path= —
// attachment streaming via http.ServeContent (range requests included).
// mime.FormatMediaType produces the RFC 5987 filename* form for non-ASCII
// names.
func (s *Server) handleScanFilesDownload(w http.ResponseWriter, r *http.Request, scanID string) {
	rel, err := scanFilesRelPath(r.URL.Query().Get("path"))
	if err != nil || rel == "." {
		writeScanFileError(w, errScanFilesInvalidPath)
		return
	}
	root, err := s.openScanFilesRoot(scanID)
	if err != nil {
		writeScanFileError(w, err)
		return
	}
	defer func() { _ = root.Close() }()

	f, fi, err := openScanFileRegular(root, rel)
	if err != nil {
		writeScanFileError(w, err)
		return
	}
	defer f.Close()

	name := filepath.Base(rel)
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

// scanArchiveFile is one pre-flighted regular file queued into the ZIP.
type scanArchiveFile struct {
	rel   string
	size  int64
	mtime time.Time
	mode  fs.FileMode
}

// scanFilesCollect pre-flights the archive: a metadata-only walk (symlinks and
// non-regular entries skipped, unreadable dirs tolerated) that refuses
// early — before a single ZIP byte is written — when the subtree exceeds the
// entry/byte caps. rel uses "/" separators (fs.ValidPath form).
func scanFilesCollect(root *os.Root, rel string) ([]scanArchiveFile, int64, error) {
	var files []scanArchiveFile
	var total int64
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if depth > scanFilesArchiveMaxDepth {
			return errScanFilesTooLarge
		}
		f, err := root.Open(dir)
		if err != nil {
			return nil // unreadable subtree: skipped, not fatal
		}
		des, readErr := f.ReadDir(-1)
		f.Close()
		if readErr != nil && len(des) == 0 {
			return nil
		}
		for _, de := range des {
			if de.Type()&fs.ModeSymlink != 0 {
				continue
			}
			child := de.Name()
			if dir != "." {
				child = dir + "/" + de.Name()
			}
			if de.Type().IsDir() {
				if err := walk(child, depth+1); err != nil {
					return err
				}
				continue
			}
			if !de.Type().IsRegular() {
				continue
			}
			info, err := de.Info()
			if err != nil {
				continue
			}
			total += info.Size()
			files = append(files, scanArchiveFile{
				rel:   child,
				size:  info.Size(),
				mtime: info.ModTime(),
				mode:  info.Mode(),
			})
			if len(files) > scanFilesArchiveMaxFiles || total > scanFilesArchiveMaxBytes {
				return errScanFilesTooLarge
			}
		}
		return nil
	}
	if err := walk(rel, 0); err != nil {
		return nil, 0, err
	}
	return files, total, nil
}

// handleScanFilesArchive serves GET /api/scans/{id}/files/archive?path= —
// streams the subtree as a ZIP. The pre-flight (scanFilesCollect) has already
// bounded entry count and uncompressed bytes, so a refused archive never
// starts; a file vanishing mid-stream aborts the connection and leaves a
// client-detectable truncated ZIP (unzip fails on the missing EOCD) rather
// than a silently incomplete success.
func (s *Server) handleScanFilesArchive(w http.ResponseWriter, r *http.Request, scanID string) {
	rel, err := scanFilesRelPath(r.URL.Query().Get("path"))
	if err != nil {
		writeScanFileError(w, err)
		return
	}
	root, err := s.openScanFilesRoot(scanID)
	if err != nil {
		writeScanFileError(w, err)
		return
	}
	defer func() { _ = root.Close() }()

	if fi, err := root.Lstat(rel); err != nil {
		writeScanFileError(w, errScanFilesNotFound)
		return
	} else if fi.Mode()&fs.ModeSymlink != 0 {
		writeScanFileError(w, errScanFilesSymlink)
		return
	} else if !fi.IsDir() {
		writeScanFileError(w, errScanFilesNotDir)
		return
	}

	files, _, err := scanFilesCollect(root, rel)
	if err != nil {
		writeScanFileError(w, err)
		return
	}

	base := "scan-files"
	if rel != "." {
		base = filepath.Base(rel)
	}
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": base + ".zip"}))

	zw := zip.NewWriter(w)
	for _, af := range files {
		entryName := af.rel
		if rel != "." {
			entryName = strings.TrimPrefix(af.rel, rel+"/")
		}
		fh := &zip.FileHeader{
			Name:     entryName,
			Method:   zip.Deflate,
			Modified: af.mtime,
		}
		fh.SetMode(af.mode)
		ow, err := zw.CreateHeader(fh)
		if err != nil {
			log.Printf("[scan-files] archive zip header %q: %v", af.rel, err)
			return
		}
		src, err := root.Open(af.rel)
		if err != nil {
			log.Printf("[scan-files] archive reopen %q: %v", af.rel, err)
			return
		}
		if _, err := io.Copy(ow, src); err != nil {
			src.Close()
			log.Printf("[scan-files] archive copy %q: %v", af.rel, err)
			return
		}
		src.Close()
	}
	if err := zw.Close(); err != nil {
		log.Printf("[scan-files] archive finalize: %v", err)
	}
}
