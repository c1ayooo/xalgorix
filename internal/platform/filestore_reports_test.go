package platform

import (
	"context"
	"errors"
	"testing"
)

func TestFileStoreReportLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, TaskInput{Name: "报告任务"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, task.ID, RunMount{InstanceID: "inst-report-1", Mode: "single"})
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.CreateReport(ctx, ReportMount{
		TaskID:  task.ID,
		RunIDs:  []string{run.ID},
		Formats: []string{"pdf", "md"},
		Mode:    "template",
	})
	if err != nil {
		t.Fatalf("CreateReport: %v", err)
	}
	if first.Revision != 1 || first.Status != ReportQueued {
		t.Fatalf("first report = %+v, want revision 1 / queued", first)
	}
	if first.ProjectID != project.ID {
		t.Fatalf("project id = %q, want %q", first.ProjectID, project.ID)
	}

	// A second active report for the same task is a conflict.
	if _, err := store.CreateReport(ctx, ReportMount{TaskID: task.ID, Formats: []string{"pdf"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second active CreateReport err = %v, want ErrConflict", err)
	}

	if err := store.UpdateReportStatus(ctx, first.ID, ReportGenerating, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateReportStatus(ctx, first.ID, ReportReady, ""); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetReport(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ReportReady || got.StartedAt == nil || got.FinishedAt == nil {
		t.Fatalf("ready report = %+v, want timestamps", got)
	}

	second, err := store.CreateReport(ctx, ReportMount{TaskID: task.ID, Formats: []string{"md"}, Mode: "agent"})
	if err != nil {
		t.Fatalf("second revision: %v", err)
	}
	if second.Revision != 2 {
		t.Fatalf("second revision = %d, want 2", second.Revision)
	}

	list, err := store.ListReportsByTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Revision != 2 {
		t.Fatalf("ListReportsByTask = %+v, want newest first", list)
	}
	all, err := store.ListReports(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("ListReports = %d, want 2", len(all))
	}

	artifacts := []ReportArtifact{
		{Kind: "total", Format: "md", Path: "/tmp/report.md", Bytes: 12, SHA256: "abc"},
		{Kind: "sub", ScanID: "scan-1", Target: "a.test", Format: "md", Path: "/tmp/a.md", Bytes: 8},
	}
	if err := store.PutReportArtifacts(ctx, first.ID, artifacts); err != nil {
		t.Fatal(err)
	}
	// Upsert, not duplicate.
	if err := store.PutReportArtifacts(ctx, first.ID, []ReportArtifact{{Kind: "total", Format: "md", Path: "/tmp/report2.md", Bytes: 20}}); err != nil {
		t.Fatal(err)
	}
	gotArtifacts, err := store.ListReportArtifacts(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotArtifacts) != 2 {
		t.Fatalf("artifacts = %+v, want 2 after upsert", gotArtifacts)
	}

	if err := store.DeleteReport(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetReport(ctx, second.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetReport after delete err = %v, want ErrNotFound", err)
	}
}
