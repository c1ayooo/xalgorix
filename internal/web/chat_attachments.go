package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/platform"
)

// Chat attachments. A conversation turn can carry a file: the operator drops a
// document into the composer, the server stores it inside the run's scan
// directory (the one tree the sandbox already allows tools to read), and the
// guidance message references that path so the agent can open it like any other
// artefact it produced.
//
// Storage lands under <scanDir>/attachments/ — never the data root, never a
// user-controlled path, and the filename is reduced to its base component.
const (
	chatAttachmentMaxBytes = 20 << 20 // 20 MiB
	chatAttachmentDir      = "attachments"
)

func (s *Server) handleConversationAttachment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	store, ok := s.requirePlatformStore(w)
	if !ok {
		return
	}
	instanceID := strings.TrimSpace(r.URL.Query().Get("instance_id"))
	if instanceID == "" {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "instance_id required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	run, err := s.runForInstance(ctx, store, instanceID)
	if err != nil {
		writePlatformError(w, err)
		return
	}
	dir := s.scanDirForRun(instanceID, run)
	if dir == "" {
		writeJSONStatus(w, http.StatusConflict, map[string]string{
			"error": "scan directory for this conversation is not available yet",
		})
		return
	}

	if err := r.ParseMultipartForm(chatAttachmentMaxBytes); err != nil {
		writeJSONStatus(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "file too large (20 MiB max)"})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "file required"})
		return
	}
	defer file.Close()

	name := filepath.Base(strings.TrimSpace(header.Filename))
	if name == "" || name == "." || name == ".." {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid file name"})
		return
	}
	stored := fmt.Sprintf("%s-%s", time.Now().Format("20060102-150405"), name)
	destDir := filepath.Join(dir, chatAttachmentDir)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not create attachment directory"})
		return
	}
	dest := filepath.Join(destDir, stored)
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not store attachment"})
		return
	}
	written, copyErr := io.Copy(out, io.LimitReader(file, chatAttachmentMaxBytes))
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(dest)
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not store attachment"})
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"path": dest,
		"name": name,
		"size": written,
	})
}

// scanDirForRun resolves the scan directory backing a conversation. Imported
// runs point at their own legacy scan id; dispatches use the instance id.
func (s *Server) scanDirForRun(instanceID string, run *platform.Run) string {
	scanID := instanceID
	if run != nil && strings.TrimSpace(run.LegacyScanID) != "" {
		scanID = run.LegacyScanID
	}
	entries := s.findAllScanSummaries()
	dir, ok := s.resolveScanDirByIDFrom(scanID, func() []scanEntry { return entries })
	if !ok {
		return ""
	}
	return dir
}
