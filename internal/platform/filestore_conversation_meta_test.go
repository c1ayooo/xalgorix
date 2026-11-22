package platform

// filestore_conversation_meta_test.go — pins the chat sidebar contract
// (docs/CHAT_PAGE_REDESIGN.md §4.1): a run carries its own title/pinned/archived
// state, writes are persisted across reopen, and an unknown instance id is
// ErrNotFound rather than a silent no-op.

import (
	"context"
	"errors"
	"testing"
)

func TestFileStoreConversationMetaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, TaskInput{Name: "chat meta"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, task.ID, RunMount{
		InstanceID: "inst-chat-1",
		Mode:       "single",
		Trigger:    "manual",
		Title:      "chat meta · 09-01 10:00",
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Title != "chat meta · 09-01 10:00" {
		t.Fatalf("create kept title %q", run.Title)
	}

	if err := store.RenameRun(ctx, "inst-chat-1", "  改名后的会话  "); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunPinned(ctx, "inst-chat-1", true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunArchived(ctx, "inst-chat-1", true); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := reopened.RunsByInstanceID(ctx, "inst-chat-1")
	if err != nil || len(runs) != 1 {
		t.Fatalf("reopen runs=%d err=%v", len(runs), err)
	}
	got := runs[0]
	if got.Title != "改名后的会话" {
		t.Fatalf("title not trimmed/persisted: %q", got.Title)
	}
	if !got.Pinned || !got.Archived {
		t.Fatalf("flags not persisted: pinned=%v archived=%v", got.Pinned, got.Archived)
	}

	if err := store.RenameRun(ctx, "missing-instance", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown instance should be ErrNotFound, got %v", err)
	}
}
