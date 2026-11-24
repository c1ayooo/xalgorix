package web

import (
	"context"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	toolrt "github.com/c1ayooo/xalgorix/v4/internal/runtime"
)

// containerStateAbsent is a projection-only value: the runtime answered, but no
// container exists for the project. It is not part of runtime's state
// vocabulary because it describes a missing container, not a live one.
const containerStateAbsent = "absent"

// InstanceContainerStatus is the one-line, read-only container projection
// attached to GET /api/instances. Actual state comes from the container runtime;
// desired state/watermark comes from PG toolbox_projects. The two are kept
// separate on purpose (charter §5) — neither is derived from the other.
type InstanceContainerStatus struct {
	ProjectID       string `json:"project_id,omitempty"`
	State           string `json:"state"`
	ContainerID     string `json:"container_id,omitempty"`
	ImageDigest     string `json:"image_digest,omitempty"`
	Profile         string `json:"profile,omitempty"`
	DesiredState    string `json:"desired_state,omitempty"`
	ReconcileStatus string `json:"reconcile_status,omitempty"`
	LastError       string `json:"last_error,omitempty"`
	ObservedAt      string `json:"observed_at,omitempty"`
}

type containerActualProjection struct {
	State       string `json:"state"`
	ContainerID string `json:"container_id,omitempty"`
	Image       string `json:"image,omitempty"`
}

// containerDesiredProjection is a whitelist of the toolbox_projects row.
// DesiredProject.Spec is deliberately omitted: it carries Env (proxy
// credentials), mounts, and socket paths that must never reach the API.
type containerDesiredProjection struct {
	DesiredState    string `json:"desired_state,omitempty"`
	ReconcileStatus string `json:"reconcile_status,omitempty"`
	DesiredGen      int64  `json:"desired_gen"`
	AppliedGen      int64  `json:"applied_gen"`
	Profile         string `json:"profile,omitempty"`
	NetProfile      string `json:"net_profile,omitempty"`
	ImageDigest     string `json:"image_digest,omitempty"`
	ContainerID     string `json:"container_id,omitempty"`
	LastError       string `json:"last_error,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

type containerToolsProjection struct {
	Available bool                  `json:"available"`
	Profile   string                `json:"profile,omitempty"`
	Source    string                `json:"source,omitempty"`
	Reason    string                `json:"reason,omitempty"`
	Items     []toolrt.ToolpackTool `json:"items,omitempty"`
}

type instanceContainerDetailResponse struct {
	InstanceID string                      `json:"instance_id"`
	ProjectID  string                      `json:"project_id"`
	Backend    string                      `json:"backend"`
	Available  bool                        `json:"available"`
	Reason     string                      `json:"reason,omitempty"`
	Actual     *containerActualProjection  `json:"actual,omitempty"`
	Desired    *containerDesiredProjection `json:"desired,omitempty"`
	Tools      containerToolsProjection    `json:"tools"`
	ObservedAt string                      `json:"observed_at"`
}

// instanceProjectIDLocked resolves the toolbox project (== session/scan ID)
// behind an instance. Callers must hold inst.mu. sctx is cleared on finalize,
// so scanDir is the durable fallback (rebuildInstancesFromDisk keeps it).
func instanceProjectIDLocked(inst *ScanInstance) string {
	if inst == nil {
		return ""
	}
	if inst.sctx != nil && inst.sctx.ID != "" {
		return inst.sctx.ID
	}
	if inst.scanDir != "" {
		return filepath.Base(inst.scanDir)
	}
	return ""
}

// attachInstanceContainers joins the current page of instances against one
// runtime enumeration and one batched desired-state read. It never inspects a
// container per card and never fails the list: on any runtime/store error the
// instances keep no Container field and the page still renders.
func attachInstanceContainers(ctx context.Context, instances []*ScanInstance, projectIDs map[*ScanInstance]string, now time.Time) {
	drv := toolrt.ActiveContainer()
	if drv == nil || len(instances) == 0 {
		return
	}
	lister, ok := drv.(toolrt.ProjectLister)
	if !ok {
		return
	}

	ids := make([]string, 0, len(instances))
	seen := make(map[string]struct{}, len(instances))
	for _, inst := range instances {
		id := projectIDs[inst]
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return
	}

	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	states, err := lister.ListProjects(queryCtx)
	if err != nil {
		log.Printf("[container] list projects: %v", err)
		return
	}
	byProject := make(map[string]toolrt.ProjectState, len(states))
	for _, state := range states {
		byProject[state.ProjectID] = state
	}

	desiredByProject := make(map[string]toolrt.DesiredProject, len(ids))
	if store := toolrt.StateStore(); store != nil {
		rows, listErr := store.ListByProjectIDs(queryCtx, ids)
		if listErr != nil {
			log.Printf("[container] read desired state: %v", listErr)
		} else {
			for _, row := range rows {
				desiredByProject[row.ProjectID] = row
			}
		}
	}

	observedAt := now.UTC().Format(time.RFC3339)
	for _, inst := range instances {
		projectID := projectIDs[inst]
		if projectID == "" {
			continue
		}
		projection := &InstanceContainerStatus{
			ProjectID:  projectID,
			ObservedAt: observedAt,
		}
		if state, found := byProject[projectID]; found {
			projection.State = state.State
			projection.ContainerID = state.ContainerID
			projection.ImageDigest = digestForDisplay(state.Image)
		} else {
			projection.State = containerStateAbsent
		}
		if desired, found := desiredByProject[projectID]; found {
			projection.Profile = desired.Profile
			projection.DesiredState = desired.DesiredState
			projection.ReconcileStatus = desired.Status
			projection.LastError = desired.LastError
			if desired.ImageDigest != "" {
				projection.ImageDigest = digestForDisplay(desired.ImageDigest)
			}
			if projection.ContainerID == "" {
				projection.ContainerID = desired.ContainerID
			}
		}
		inst.Container = projection
	}
}

// handleInstanceContainer serves GET /api/instances/{id}/container.
//
// Read-only: it may inspect/exec inside an existing project container, but it
// never creates, starts, stops, or reconciles one.
func (s *Server) handleInstanceContainer(w http.ResponseWriter, r *http.Request, instanceID string) {
	projectID := ""
	if inst, ok := s.resolveInstanceForAction(instanceID); ok && inst != nil {
		inst.mu.RLock()
		projectID = instanceProjectIDLocked(inst)
		inst.mu.RUnlock()
	}
	if projectID == "" {
		// Terminal/evicted instances keep only their persisted record. Accept
		// either the external instance id or the canonical record id.
		if _, rec := s.findScanByInstanceID(instanceID); rec != nil {
			projectID = rec.ID
		} else if _, rec := s.findScanByID(instanceID); rec != nil {
			projectID = rec.ID
		}
	}
	if projectID == "" {
		http.Error(w, `{"error":"instance not found"}`, http.StatusNotFound)
		return
	}

	now := time.Now().UTC()
	resp := instanceContainerDetailResponse{
		InstanceID: instanceID,
		ProjectID:  projectID,
		Backend:    "native",
		ObservedAt: now.Format(time.RFC3339),
	}
	drv := toolrt.ActiveContainer()
	if drv == nil {
		resp.Reason = "native execution plane (no containers)"
		writeJSONStatus(w, http.StatusOK, resp)
		return
	}
	resp.Backend = string(drv.Backend())

	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()

	profile := ""
	if store := toolrt.StateStore(); store != nil {
		row, err := store.Get(ctx, projectID)
		switch {
		case err == nil:
			profile = row.Profile
			resp.Desired = &containerDesiredProjection{
				DesiredState:    row.DesiredState,
				ReconcileStatus: row.Status,
				DesiredGen:      row.DesiredGen,
				AppliedGen:      row.AppliedGen,
				Profile:         row.Profile,
				NetProfile:      row.NetProfile,
				ImageDigest:     digestForDisplay(row.ImageDigest),
				ContainerID:     row.ContainerID,
				LastError:       row.LastError,
				CreatedAt:       formatProjectTime(row.CreatedAt),
				UpdatedAt:       formatProjectTime(row.UpdatedAt),
			}
		case errors.Is(err, toolrt.ErrProjectNotFound):
			// No durable intent (finished+GC'd, or a pre-R6 scan). The actual
			// runtime state below is still authoritative.
		default:
			log.Printf("[container] desired state for %s: %v", projectID, err)
		}
	}
	if profile == "" {
		profile = config.Get().ToolboxProfile
	}

	actual, inspectErr := drv.Inspect(ctx, projectID)
	switch {
	case inspectErr == nil:
		resp.Available = true
		resp.Actual = &containerActualProjection{
			State:       actual.State,
			ContainerID: actual.ContainerID,
			Image:       actual.Image,
		}
	case toolrt.IsCode(inspectErr, toolrt.CodeNotFound):
		resp.Actual = &containerActualProjection{State: containerStateAbsent}
		resp.Reason = "container not found"
	default:
		resp.Reason = inspectErr.Error()
	}

	resp.Tools = containerToolsProjection{Profile: profile}
	switch {
	case !resp.Available:
		resp.Tools.Reason = "container is not available"
	case !isRunningContainerState(actual.State):
		resp.Tools.Reason = "container is not running"
	default:
		pack, err := toolrt.LoadToolpackFromProject(ctx, drv, projectID)
		if err != nil {
			resp.Tools.Reason = err.Error()
		} else {
			resp.Tools.Available = true
			resp.Tools.Source = toolrt.MountToolpack
			resp.Tools.Items = pack.ToolsForProfile(profile)
		}
	}
	writeJSONStatus(w, http.StatusOK, resp)
}

func isRunningContainerState(state string) bool {
	switch state {
	case toolrt.StateReady, toolrt.StateBusy, toolrt.StateIdle, toolrt.StateStarting:
		return true
	default:
		return false
	}
}

func formatProjectTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
