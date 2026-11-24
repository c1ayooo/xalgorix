// Package imhub is the inbound IM gateway: messages sent from Feishu, DingTalk,
// WeCom or Telegram are mapped to a platform task and handed to the host as
// operator turns. It owns protocol/transport, per-chat serialization and slash
// commands; it deliberately does NOT own the scan lifecycle — the host injects
// a Host implementation that knows how to bind tasks, create them and deliver a
// human turn.
package imhub

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// Target identifies the task/run an IM chat is bound to.
type Target struct {
	TaskID     string
	RunID      string
	InstanceID string
	Label      string
}

// Host is implemented by the web layer. Every method returns a short
// operator-facing message (sent back to IM) so protocol errors never masquerade
// as agent output.
type Host interface {
	// BindChat resolves a task reference (id or name) and returns the target to
	// bind. An error message is shown verbatim to the IM user.
	BindChat(ctx context.Context, platform, chatID, userID, ref string) (Target, error)
	// CreateTask creates (and starts) a new task from free text.
	CreateTask(ctx context.Context, platform, chatID, userID, text string) (Target, error)
	// Status describes the bound target's current state.
	Status(ctx context.Context, platform, chatID string) (string, error)
	// Submit delivers one operator turn. requestID is the idempotency key.
	Submit(ctx context.Context, tgt Target, text, requestID string) (reply string, err error)
}

// Store persists chat bindings, message dedupe and adapter offsets.
type Store interface {
	GetBinding(ctx context.Context, platform, chatID string) (Target, bool, error)
	PutBinding(ctx context.Context, platform, chatID, userID string, tgt Target) error
	DeleteBinding(ctx context.Context, platform, chatID string) error
	// MarkMessage records a platform message id and reports whether it was new.
	MarkMessage(ctx context.Context, platform, messageID string) (fresh bool, err error)
	GetOffset(ctx context.Context, platform, key string) (string, bool, error)
	SetOffset(ctx context.Context, platform, key, value string) error
}

// Hub manages the per-platform loops and routes inbound text.
type Hub struct {
	host  Host
	store Store

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	locks   sync.Map // platform \x00 chatID -> *sync.Mutex
}

// New builds the hub. host/store are required; without them Start is a no-op.
func New(host Host, store Store) *Hub {
	return &Hub{host: host, store: store}
}

// Start aligns the platform loops with the current environment configuration.
func (h *Hub) Start(ctx context.Context) {
	if h.host == nil || h.store == nil {
		return
	}
	h.mu.Lock()
	if h.cancels == nil {
		h.cancels = map[string]context.CancelFunc{}
	}
	h.mu.Unlock()
	h.Apply(ctx)
}

// Apply restarts the loops whose configuration changed. Called at startup and
// after the settings page rewrites ~/.xalgorix.env.
func (h *Hub) Apply(parent context.Context) {
	cfg := LoadConfigFromEnv()
	want := map[string]bool{}
	if cfg.Feishu.Enabled {
		want["feishu"] = true
	}
	if cfg.Dingtalk.Enabled {
		want["dingtalk"] = true
	}
	if cfg.Telegram.Enabled {
		want["telegram"] = true
	}

	h.mu.Lock()
	if h.cancels == nil {
		h.cancels = map[string]context.CancelFunc{}
	}
	var spawns []struct {
		platform string
		ctx      context.Context
	}
	for platform, cancel := range h.cancels {
		if !want[platform] {
			cancel()
			delete(h.cancels, platform)
		}
	}
	for platform := range want {
		if _, running := h.cancels[platform]; running {
			continue
		}
		ctx, cancel := context.WithCancel(parent)
		h.cancels[platform] = cancel
		spawns = append(spawns, struct {
			platform string
			ctx      context.Context
		}{platform, ctx})
	}
	h.mu.Unlock()

	for _, sp := range spawns {
		switch sp.platform {
		case "feishu":
			go h.imFeishuLoop(sp.ctx, cfg)
		case "dingtalk":
			go h.imDingtalkLoop(sp.ctx, cfg)
		case "telegram":
			go h.imTelegramLoop(sp.ctx, cfg)
		}
		log.Printf("[im] %s 入站已启动", sp.platform)
	}
}

// StopAll cancels every platform loop.
func (h *Hub) StopAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for platform, cancel := range h.cancels {
		cancel()
		delete(h.cancels, platform)
	}
}

func (h *Hub) lockChat(platform, chatID string) func() {
	v, _ := h.locks.LoadOrStore(platform+"\x00"+chatID, &sync.Mutex{})
	mu, ok := v.(*sync.Mutex)
	if !ok {
		mu = &sync.Mutex{}
	}
	mu.Lock()
	return mu.Unlock
}

// HandleText is the single inbound entry. Adapters normalize the text (strip @
// mentions) and pass the platform message id for dedupe.
func (h *Hub) HandleText(platform, chatID, userID, msgID, text string) {
	if h.host == nil || h.store == nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if msgID != "" {
		if fresh, err := h.store.MarkMessage(context.Background(), platform, msgID); err != nil {
			log.Printf("[im %s] dedupe failed: %v", platform, err)
		} else if !fresh {
			return // platform re-delivery
		}
	}
	unlock := h.lockChat(platform, chatID)
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if cmd, ok := ParseChatCommand(text); ok {
		h.Reply(platform, chatID, h.handleCommand(ctx, platform, chatID, userID, cmd))
		return
	}

	tgt, bound, err := h.store.GetBinding(ctx, platform, chatID)
	if err != nil {
		h.Reply(platform, chatID, "读取会话绑定失败："+err.Error())
		return
	}
	if !bound {
		h.Reply(platform, chatID, "这个聊天还没有绑定任务。\n/bond <任务ID或名称> 绑定已有任务\n/new <目标> 新建并启动任务\n/commands 查看全部指令")
		return
	}
	reactionID := h.imAckWorking(ctx, platform, msgID)
	reply, err := h.host.Submit(ctx, tgt, text, platform+":"+msgID)
	if err != nil {
		h.Reply(platform, chatID, "提交失败："+err.Error())
		h.imClearWorking(platform, msgID, reactionID)
		return
	}
	if strings.TrimSpace(reply) != "" {
		h.Reply(platform, chatID, reply)
	}
	h.imClearWorking(platform, msgID, reactionID)
}

// imAckWorking marks the original message as being processed where the
// platform supports reactions (Feishu). An empty id means "not supported or
// failed", and the caller falls back to a text ack (already sent by Submit).
func (h *Hub) imAckWorking(ctx context.Context, platform, msgID string) string {
	if platform != "feishu" || msgID == "" {
		return ""
	}
	rid, err := h.imFeishuReact(ctx, LoadConfigFromEnv(), msgID, imFeishuWorkingEmoji)
	if err != nil {
		log.Printf("[im feishu] working reaction failed: %v", err)
		return ""
	}
	return rid
}

func (h *Hub) imClearWorking(platform, msgID, reactionID string) {
	if platform != "feishu" || msgID == "" || reactionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.imFeishuUnreact(ctx, LoadConfigFromEnv(), msgID, reactionID); err != nil {
		log.Printf("[im feishu] remove reaction failed: %v", err)
	}
}

func (h *Hub) handleCommand(ctx context.Context, platform, chatID, userID string, cmd ChatCommand) string {
	switch cmd.Name {
	case "commands", "help":
		return CommandHelp()
	case "bond":
		if len(cmd.Args) == 0 {
			tgt, bound, err := h.store.GetBinding(ctx, platform, chatID)
			if err != nil {
				return "读取绑定失败：" + err.Error()
			}
			if !bound {
				return "当前未绑定任务。用法：/bond <任务ID或名称>"
			}
			return "当前绑定：" + describeTarget(tgt)
		}
		ref := strings.Join(cmd.Args, " ")
		tgt, err := h.host.BindChat(ctx, platform, chatID, userID, ref)
		if err != nil {
			return "绑定失败：" + err.Error()
		}
		if err := h.store.PutBinding(ctx, platform, chatID, userID, tgt); err != nil {
			return "保存绑定失败：" + err.Error()
		}
		return "已绑定 " + describeTarget(tgt) + "。直接发消息即为人工指令；/unbind 解除。"
	case "unbind":
		if err := h.store.DeleteBinding(ctx, platform, chatID); err != nil {
			return "解除绑定失败：" + err.Error()
		}
		return "已解除绑定。"
	case "status":
		msg, err := h.host.Status(ctx, platform, chatID)
		if err != nil {
			return "查询状态失败：" + err.Error()
		}
		return msg
	case "new":
		text := strings.TrimSpace(strings.Join(cmd.Args, " "))
		if text == "" {
			return "用法：/new <目标> [指令]\n例如：/new https://example.com 做一次完整的 Web 渗透测试"
		}
		tgt, err := h.host.CreateTask(ctx, platform, chatID, userID, text)
		if err != nil {
			return "创建任务失败：" + err.Error()
		}
		if err := h.store.PutBinding(ctx, platform, chatID, userID, tgt); err != nil {
			return "任务已创建，但保存绑定失败：" + err.Error()
		}
		return "已创建并绑定 " + describeTarget(tgt) + "。"
	default:
		return "未知指令 " + cmd.Raw + "。\n\n" + CommandHelp()
	}
}

func describeTarget(t Target) string {
	label := strings.TrimSpace(t.Label)
	if label == "" {
		label = t.TaskID
	}
	if t.RunID != "" {
		return fmt.Sprintf("任务 %s（Run %s）", label, t.RunID)
	}
	return "任务 " + label
}

// Reply sends text back through the platform adapter. Failures are logged only:
// the operator can resend, and there is no durable outbound queue.
func (h *Hub) Reply(platform, chatID, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	cfg := LoadConfigFromEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var err error
	switch platform {
	case "telegram":
		err = h.imTelegramSend(ctx, cfg, chatID, text)
	case "feishu":
		err = h.imFeishuSend(ctx, cfg, chatID, text)
	case "dingtalk":
		err = h.imDingtalkSend(ctx, cfg, chatID, text)
	case "wecom":
		err = h.imWecomSend(ctx, cfg, chatID, text)
	default:
		err = fmt.Errorf("unknown platform %s", platform)
	}
	if err != nil {
		log.Printf("[im %s/%s] send failed: %v", platform, chatID, err)
	}
}

// FirstLine is the single-line preview helper shared with command output.
func FirstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len([]rune(s)) > max {
		s = string([]rune(s)[:max]) + "…"
	}
	return s
}
