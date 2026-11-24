package web

// im_gateway.go wires the inbound IM hub (internal/imhub) to xalgorix's
// platform repository. The hub owns protocol/transport; this file owns the
// semantics of "an IM chat is bound to a task" and turns each ordinary message
// into the canonical human input — a guidance row plus a live injection when a
// run is currently receiving guidance.

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/imhub"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
)

// StartIMGateway builds the hub and starts the loops enabled by the current
// environment configuration. Called once after the platform store is wired.
func (s *Server) StartIMGateway(ctx context.Context, store imhub.Store) {
	if s == nil || store == nil {
		return
	}
	s.imMu.Lock()
	defer s.imMu.Unlock()
	if s.imHub != nil {
		return
	}
	s.imHub = imhub.New(&imHost{s: s}, store)
	s.imHub.Start(ctx)
}

// handleIMWecomCallback serves the WeCom event callback. The endpoint verifies
// its own signature/ciphertext, so it is exempt from session auth and CSRF.
func (s *Server) handleIMWecomCallback(w http.ResponseWriter, r *http.Request) {
	s.imMu.RLock()
	hub := s.imHub
	s.imMu.RUnlock()
	if hub == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "IM gateway is not configured"})
		return
	}
	hub.WecomCallback(w, r)
}

// imHost implements imhub.Host against the platform repository.
type imHost struct{ s *Server }

func (h *imHost) repo() (platform.Repository, error) {
	store := h.s.currentPlatformStore()
	if store == nil {
		return nil, fmt.Errorf("platform store is not initialized (PostgreSQL is required for IM tasks)")
	}
	return store, nil
}

func (h *imHost) BindChat(ctx context.Context, platformName, chatID, userID, ref string) (imhub.Target, error) {
	store, err := h.repo()
	if err != nil {
		return imhub.Target{}, err
	}
	task, err := findTaskByRef(ctx, store, ref)
	if err != nil {
		return imhub.Target{}, err
	}
	if task.ArchivedAt != nil {
		return imhub.Target{}, fmt.Errorf("task %q is archived", task.Name)
	}
	tgt := imhub.Target{TaskID: task.ID, Label: task.Name}
	if run, ok := h.latestRun(ctx, store, task.ID); ok {
		tgt.RunID = run.ID
		tgt.InstanceID = run.InstanceID
	}
	return tgt, nil
}

func (h *imHost) CreateTask(ctx context.Context, platformName, chatID, userID, text string) (imhub.Target, error) {
	store, err := h.repo()
	if err != nil {
		return imhub.Target{}, err
	}
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		return imhub.Target{}, err
	}
	name := imhub.FirstLine(text, 60)
	if name == "" {
		name = "IM task"
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{
		Name:        name,
		Goal:        text,
		Instruction: text,
		AuditMode:   "inherit",
	})
	if err != nil {
		return imhub.Target{}, err
	}
	return imhub.Target{TaskID: task.ID, Label: task.Name}, nil
}

func (h *imHost) Status(ctx context.Context, platformName, chatID string) (string, error) {
	if _, err := h.repo(); err != nil {
		return "", err
	}
	return "请在仪表盘任务页查看实时状态；本聊天已绑定任务，直接发消息即注入人工指令。", nil
}

// Submit turns one IM message into the canonical human input: a guidance row
// with a platform-scoped request id (idempotent) and, when a live agent exists,
// an immediate injection into the main conversation. Otherwise the row stays
// queued and is folded into the next session briefing.
func (h *imHost) Submit(ctx context.Context, tgt imhub.Target, text, requestID string) (string, error) {
	store, err := h.repo()
	if err != nil {
		return "", err
	}
	if tgt.RunID == "" || tgt.InstanceID == "" {
		if run, ok := h.latestRun(ctx, store, tgt.TaskID); ok {
			tgt.RunID = run.ID
			tgt.InstanceID = run.InstanceID
		}
	}
	g, created, err := store.CreateGuidance(ctx, platform.GuidanceInput{
		TaskID:     tgt.TaskID,
		RunID:      tgt.RunID,
		InstanceID: tgt.InstanceID,
		Message:    text,
		RequestID:  requestID,
		CreatedBy:  "im:" + requestID,
	})
	if err != nil {
		return "", err
	}
	if !created {
		return "这条消息已经收到过，无需重复发送。", nil
	}
	if tgt.InstanceID != "" {
		if agnt := h.s.instanceAgent(tgt.InstanceID); agnt != nil {
			if h.s.injectGuidanceToMain(tgt.InstanceID, *g, agnt) {
				return "已注入运行中的会话。", nil
			}
		}
	}
	return "已排队，将在该任务下一次会话启动时并入简报。", nil
}

func (h *imHost) latestRun(ctx context.Context, store platform.Repository, taskID string) (platform.Run, bool) {
	runs, _, err := store.ListRuns(ctx, taskID, 1, 1)
	if err != nil || len(runs) == 0 {
		return platform.Run{}, false
	}
	return runs[0], true
}

// findTaskByRef resolves a UUID, then an exact name, then a unique substring
// match across projects. Ambiguity is an error: binding the wrong task would
// send operator guidance into someone else's scan.
func findTaskByRef(ctx context.Context, store platform.Repository, ref string) (*platform.Task, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("task reference is empty")
	}
	if task, err := store.GetTask(ctx, ref); err == nil && task != nil {
		return task, nil
	}
	projects, err := store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	var exact, partial []platform.Task
	for _, project := range projects {
		tasks, err := store.ListTasks(ctx, project.ID)
		if err != nil {
			continue
		}
		for _, task := range tasks {
			if task.ArchivedAt != nil {
				continue
			}
			switch {
			case strings.EqualFold(task.Name, ref):
				exact = append(exact, task)
			case strings.Contains(strings.ToLower(task.Name), strings.ToLower(ref)):
				partial = append(partial, task)
			}
		}
	}
	switch {
	case len(exact) == 1:
		return &exact[0], nil
	case len(exact) > 1:
		return nil, fmt.Errorf("多个任务同名 %q，请用任务 ID 绑定", ref)
	case len(partial) == 1:
		return &partial[0], nil
	case len(partial) > 1:
		names := make([]string, 0, len(partial))
		for _, t := range partial {
			names = append(names, t.Name+" ("+t.ID+")")
		}
		return nil, fmt.Errorf("匹配到多个任务，请用更精确的名称或 ID：%s", strings.Join(names, "、"))
	}
	return nil, fmt.Errorf("没有找到任务 %q（可用任务 ID 或名称）", ref)
}
