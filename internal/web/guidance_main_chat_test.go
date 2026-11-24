package web

// guidance_main_chat_test.go — R7 regression: a message sent while the run is
// still starting (the normal "open a conversation" flow) must reach the main
// agent as a conversation turn instead of being silently folded into the task
// instruction.

import (
	"context"
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/platform"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/scopeguard"
)

func TestDeliverQueuedGuidanceToMainChat(t *testing.T) {
	s, store := newApprovalsTestServer(t, nil)
	ctx := context.Background()
	project, err := store.EnsureDefaultProject(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, project.ID, platform.TaskInput{Name: "queued main chat"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"inst-qg", "inst-other"} {
		if _, err := store.CreateRun(ctx, task.ID, platform.RunMount{InstanceID: id, Mode: "single"}); err != nil {
			t.Fatal(err)
		}
	}

	queued, created, err := store.CreateGuidance(ctx, platform.GuidanceInput{
		TaskID: task.ID, InstanceID: "inst-qg", Message: "你是谁", RequestID: "rq-qg-1",
	})
	if err != nil || !created {
		t.Fatalf("create queued guidance: created=%v err=%v", created, err)
	}
	foreign, created, err := store.CreateGuidance(ctx, platform.GuidanceInput{
		TaskID: task.ID, InstanceID: "inst-other", Message: "另一个 run 的消息", RequestID: "rq-qg-2",
	})
	if err != nil || !created {
		t.Fatalf("create foreign guidance: created=%v err=%v", created, err)
	}

	agnt := agent.NewAgent(
		&config.Config{}, "XalgorixAgent", make(chan agent.Event, 16),
		scopeguard.Config{}, scanctx.New("qg-scan", s.dataDir),
		agent.WithPlannerWorkerMode(),
	)
	if agnt == nil {
		t.Fatal("agent construction failed")
	}
	s.deliverQueuedGuidanceToMain(&scanSession{instanceID: "inst-qg"}, agnt)

	items, err := store.ListGuidance(ctx, task.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	statuses := make(map[string]string, len(items))
	for _, item := range items {
		statuses[item.ID] = item.Status
	}
	if statuses[queued.ID] != platform.GuidanceInjected {
		t.Fatalf("queued turn for this run = %q, want injected", statuses[queued.ID])
	}
	if statuses[foreign.ID] != platform.GuidanceQueued {
		t.Fatalf("guidance for another run must stay queued, got %q", statuses[foreign.ID])
	}
}
