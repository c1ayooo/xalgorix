package web

import (
	"context"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

func TestBackfillSessionTaskCoordinates(t *testing.T) {
	s := newPlatformTestServer(t)
	store := s.currentPlatformStore()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, "Resume Project", platform.Scope{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	objective := &platform.Objective{Kind: platform.ObjectiveKindEngagement, Statement: "prove access"}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{
		Name:      "Resume Task",
		Objective: objective,
		AuditMode: platform.TaskAuditEnforce,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{
		InstanceID: "inst-backfill", Mode: "single", Trigger: "manual",
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	sctx := scanctx.New("scan-backfill", t.TempDir())
	sess := &scanSession{instanceID: "inst-backfill"}
	s.backfillSessionTaskCoordinates(sess, sctx)

	if sctx.ProjectID != project.ID {
		t.Fatalf("ProjectID = %q, want %q", sctx.ProjectID, project.ID)
	}
	if sctx.TaskID != task.ID {
		t.Fatalf("TaskID = %q, want %q", sctx.TaskID, task.ID)
	}
	if sctx.AuditMode != platform.TaskAuditEnforce {
		t.Fatalf("AuditMode = %q, want enforce", sctx.AuditMode)
	}
	if sess.objectiveProjectID != project.ID || sess.objectiveTaskID != task.ID {
		t.Fatalf("session coordinates not repaired: %+v", sess)
	}
	if sess.objective == nil || sess.objective.Statement != "prove access" {
		t.Fatalf("objective not restored: %+v", sess.objective)
	}
}

func TestBackfillSessionTaskCoordinatesLeavesAdHocRunsAlone(t *testing.T) {
	s := newPlatformTestServer(t)
	sctx := scanctx.New("scan-adhoc", t.TempDir())
	sess := &scanSession{instanceID: "no-run-row"}
	s.backfillSessionTaskCoordinates(sess, sctx)
	if sctx.ProjectID != "" || sctx.TaskID != "" {
		t.Fatalf("ad-hoc run gained task coordinates: project=%q task=%q", sctx.ProjectID, sctx.TaskID)
	}
}
