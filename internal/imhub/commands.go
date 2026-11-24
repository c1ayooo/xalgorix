package imhub

import "strings"

// ChatCommand is one parsed slash command.
type ChatCommand struct {
	Name string
	Args []string
	Raw  string
}

var commandNames = map[string]bool{
	"bond": true, "unbind": true, "new": true,
	"status": true, "commands": true, "help": true,
}

// ParseChatCommand recognizes /name or ／name plus arguments. Anything that is
// not a known command name is returned as (zero, false) so ordinary text that
// happens to start with a slash is still delivered to the agent.
func ParseChatCommand(msg string) (ChatCommand, bool) {
	raw := strings.TrimSpace(msg)
	if raw == "" {
		return ChatCommand{}, false
	}
	if strings.HasPrefix(raw, "／") {
		raw = "/" + strings.TrimPrefix(raw, "／")
	}
	if !strings.HasPrefix(raw, "/") {
		return ChatCommand{}, false
	}
	fields := strings.Fields(strings.TrimPrefix(raw, "/"))
	if len(fields) == 0 {
		return ChatCommand{}, false
	}
	name := strings.ToLower(fields[0])
	if i := strings.Index(name, "@"); i > 0 {
		name = name[:i] // /status@Bot
	}
	if !commandNames[name] {
		return ChatCommand{}, false
	}
	return ChatCommand{Name: name, Args: fields[1:], Raw: raw}, true
}

// CommandHelp is the operator-facing command list.
func CommandHelp() string {
	return strings.Join([]string{
		"可用指令：",
		"/bond <任务ID或名称> — 把本聊天绑定到一个已有任务",
		"/unbind — 解除绑定",
		"/new <目标> [指令] — 新建并启动一个任务",
		"/status — 查看当前绑定任务的状态",
		"/commands — 显示这份清单",
		"",
		"绑定后，直接发普通消息 = 向该任务注入一条人工指令（source=human）。",
	}, "\n")
}
