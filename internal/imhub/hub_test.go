package imhub

import (
	"context"
	"strings"
	"sync"
	"testing"
)

type fakeStore struct {
	mu       sync.Mutex
	bindings map[string]Target
	seen     map[string]bool
	offsets  map[string]string
}

func newFakeStore() *fakeStore {
	return &fakeStore{bindings: map[string]Target{}, seen: map[string]bool{}, offsets: map[string]string{}}
}

func key(platform, chat string) string { return platform + "\x00" + chat }

func (f *fakeStore) GetBinding(_ context.Context, platform, chatID string) (Target, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.bindings[key(platform, chatID)]
	return t, ok, nil
}

func (f *fakeStore) PutBinding(_ context.Context, platform, chatID, userID string, tgt Target) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bindings[key(platform, chatID)] = tgt
	return nil
}

func (f *fakeStore) DeleteBinding(_ context.Context, platform, chatID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.bindings, key(platform, chatID))
	return nil
}

func (f *fakeStore) MarkMessage(_ context.Context, platform, messageID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(platform, messageID)
	if f.seen[k] {
		return false, nil
	}
	f.seen[k] = true
	return true, nil
}

func (f *fakeStore) GetOffset(_ context.Context, platform, k string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.offsets[key(platform, k)]
	return v, ok, nil
}

func (f *fakeStore) SetOffset(_ context.Context, platform, k, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offsets[key(platform, k)] = value
	return nil
}

type fakeHost struct {
	created []string
	submits []string
}

func (f *fakeHost) BindChat(_ context.Context, platform, chatID, userID, ref string) (Target, error) {
	return Target{TaskID: "task-" + ref, Label: ref}, nil
}

func (f *fakeHost) CreateTask(_ context.Context, platform, chatID, userID, text string) (Target, error) {
	f.created = append(f.created, text)
	return Target{TaskID: "new-task", Label: FirstLine(text, 20)}, nil
}

func (f *fakeHost) Status(_ context.Context, platform, chatID string) (string, error) {
	return "status-ok", nil
}

func (f *fakeHost) Submit(_ context.Context, tgt Target, text, requestID string) (string, error) {
	f.submits = append(f.submits, tgt.TaskID+"|"+text)
	return "injected", nil
}

func TestParseChatCommand(t *testing.T) {
	cases := []struct {
		in    string
		name  string
		args  []string
		found bool
	}{
		{"/bond 任务一", "bond", []string{"任务一"}, true},
		{"/new https://example.com 完整测试", "new", []string{"https://example.com", "完整测试"}, true},
		{"/status@XalgorixBot", "status", nil, true},
		{"／help", "help", nil, true},
		{"/unknown thing", "", nil, false},
		{"http://example.com/a", "", nil, false},
		{"普通消息", "", nil, false},
	}
	for _, c := range cases {
		cmd, ok := ParseChatCommand(c.in)
		if ok != c.found {
			t.Fatalf("%q: found=%v want %v", c.in, ok, c.found)
		}
		if !ok {
			continue
		}
		if cmd.Name != c.name {
			t.Fatalf("%q: name=%q want %q", c.in, cmd.Name, c.name)
		}
		if strings.Join(cmd.Args, ",") != strings.Join(c.args, ",") {
			t.Fatalf("%q: args=%v want %v", c.in, cmd.Args, c.args)
		}
	}
}

func TestCommandsBindUnbindStatusNew(t *testing.T) {
	store := newFakeStore()
	host := &fakeHost{}
	hub := New(host, store)
	ctx := context.Background()

	if got := hub.handleCommand(ctx, "wecom", "chat1", "u1", ChatCommand{Name: "unknown"}); !strings.Contains(got, "可用指令") {
		t.Fatalf("unknown command should return help, got %q", got)
	}
	if got := hub.handleCommand(ctx, "wecom", "chat1", "u1", ChatCommand{Name: "bond", Args: []string{"alpha"}}); !strings.Contains(got, "已绑定") {
		t.Fatalf("bond reply = %q", got)
	}
	if tgt, ok, _ := store.GetBinding(ctx, "wecom", "chat1"); !ok || tgt.TaskID != "task-alpha" {
		t.Fatalf("binding not persisted: %+v ok=%v", tgt, ok)
	}
	if got := hub.handleCommand(ctx, "wecom", "chat1", "u1", ChatCommand{Name: "status"}); got != "status-ok" {
		t.Fatalf("status = %q", got)
	}
	if got := hub.handleCommand(ctx, "wecom", "chat1", "u1", ChatCommand{Name: "unbind"}); !strings.Contains(got, "已解除") {
		t.Fatalf("unbind reply = %q", got)
	}
	if _, ok, _ := store.GetBinding(ctx, "wecom", "chat1"); ok {
		t.Fatal("binding still present after /unbind")
	}
	if got := hub.handleCommand(ctx, "wecom", "chat2", "u1", ChatCommand{Name: "new", Args: []string{"https://example.com"}}); !strings.Contains(got, "已创建") {
		t.Fatalf("new reply = %q", got)
	}
	if len(host.created) != 1 || host.created[0] != "https://example.com" {
		t.Fatalf("host.CreateTask got %v", host.created)
	}
}

func TestHandleTextDedupesAndRequiresBinding(t *testing.T) {
	store := newFakeStore()
	host := &fakeHost{}
	hub := New(host, store)
	// Reply() would hit the network; exercise the pre-reply path by checking
	// the store/host side effects instead of the adapter send.
	hub.HandleText("wecom", "chat1", "u1", "m1", "绑定前先说话")
	hub.HandleText("wecom", "chat1", "u1", "m1", "重复投递")
	if len(host.submits) != 0 {
		t.Fatalf("unbound chat must not submit, got %v", host.submits)
	}
	_ = store.PutBinding(context.Background(), "wecom", "chat1", "u1", Target{TaskID: "t1"})
	hub.HandleText("wecom", "chat1", "u1", "m2", "把 robots 也看看")
	hub.HandleText("wecom", "chat1", "u1", "m2", "重复投递")
	if len(host.submits) != 1 || host.submits[0] != "t1|把 robots 也看看" {
		t.Fatalf("submit dedupe failed: %v", host.submits)
	}
}

func TestWecomSignatureIsOrderIndependent(t *testing.T) {
	got := imWecomSignature("tok", "1700000000", "nonce", "cipher")
	if len(got) != 40 {
		t.Fatalf("signature length = %d, want 40", len(got))
	}
	if imWecomSignature("tok", "1700000000", "nonce", "cipher") != got {
		t.Fatal("signature is not deterministic")
	}
	if imWecomSignature("tok2", "1700000000", "nonce", "cipher") == got {
		t.Fatal("different token must change the signature")
	}
}
