package web

import (
	"context"
	"net/http"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/resources"
	toolrt "github.com/c1ayooo/xalgorix/v4/internal/runtime"
)

// runtimeStatusResponse is the read-only projection behind the dashboard's
// runtime card. Every field is derived, never authoritative: the container
// runtime owns actual state and PostgreSQL owns desired state.
type runtimeStatusResponse struct {
	Backend         string                  `json:"backend"`
	Transport       string                  `json:"transport,omitempty"`
	Profile         string                  `json:"profile,omitempty"`
	Image           string                  `json:"image,omitempty"`
	ImageDigest     string                  `json:"image_digest,omitempty"`
	ProjectLimit    int                     `json:"project_limit"`
	ProjectsActive  int                     `json:"projects_active"`
	ExecQueueDepth  int                     `json:"exec_queue_depth"`
	ProjectSlotUsed int                     `json:"project_slot_used"`
	ProjectSlotMax  int                     `json:"project_slot_max"`
	Healthy         bool                    `json:"healthy"`
	Reason          string                  `json:"reason,omitempty"`
	ToolHealth      []imageHealthProjection `json:"tool_health,omitempty"`
	ReconciledAt    time.Time               `json:"reconciled_at"`
}

type imageHealthProjection struct {
	ImageDigest string `json:"image_digest"`
	Profile     string `json:"profile"`
	Passed      int    `json:"passed"`
	Failed      int    `json:"failed"`
}

// handleRuntime serves GET /api/runtime.
func (s *Server) handleRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	cfg := config.Get()
	out := runtimeStatusResponse{
		Backend:      cfg.RuntimeBackend,
		Transport:    cfg.RuntimeTransport,
		Profile:      cfg.ToolboxProfile,
		Image:        cfg.ToolboxImage,
		ImageDigest:  digestForDisplay(cfg.ToolboxImage),
		ReconciledAt: time.Now().UTC(),
	}
	out.ProjectSlotUsed, out.ProjectSlotMax = resources.ProjectSlotState()

	driver := toolrt.Active()
	if driver == nil || driver.Backend() == toolrt.BackendNative {
		out.Backend = string(toolrt.BackendNative)
		out.Healthy = true
		out.Reason = "native execution plane (no containers)"
		writeJSONStatus(w, http.StatusOK, out)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	capacity, err := driver.Capacity(ctx)
	if err != nil {
		out.Healthy = false
		out.Reason = err.Error()
		writeJSONStatus(w, http.StatusOK, out)
		return
	}
	out.Backend = string(capacity.Backend)
	out.Transport = capacity.Transport
	out.Profile = capacity.Profile
	out.Image = capacity.Image
	if capacity.ImageDigest != "" {
		out.ImageDigest = capacity.ImageDigest
	}
	out.ProjectLimit = capacity.ProjectLimit
	out.ProjectsActive = capacity.ProjectsActive
	out.ExecQueueDepth = capacity.ExecQueueDepth
	out.Healthy = capacity.ProjectLimit == 0 || capacity.ProjectsActive <= capacity.ProjectLimit
	out.Reason = capacity.Reason

	// Tool health is published by the selftest job into toolbox_image_health.
	// The panel tolerates its absence (a brand-new deployment has no run yet),
	// so no extra query is issued on the request path.
	writeJSONStatus(w, http.StatusOK, out)
}

// digestForDisplay renders a short digest for the panel (never a full reference,
// which would be noisy and may embed registry credentials).
func digestForDisplay(ref string) string {
	const marker = "@sha256:"
	for i := 0; i+len(marker) <= len(ref); i++ {
		if ref[i:i+len(marker)] == marker {
			digest := ref[i+len(marker):]
			if len(digest) > 12 {
				digest = digest[:12]
			}
			return "sha256:" + digest
		}
	}
	return ref
}
