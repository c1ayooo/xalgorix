package engine

// Engine is the Eino ADK execution path. internal/agent bridges it into
// Agent.Run (eino_runtime.go) and internal/web's scan_session wires the
// checkpoint store; config defaults to this engine while
// XALGORIX_ENGINE=legacy keeps the original loop available as rollback.

import (
	"errors"

	einoTool "github.com/cloudwego/eino/components/tool"

	"github.com/xalgord/xalgorix/v4/internal/tools"
)

// Config supplies the seams needed by the Eino path. Legacy mode ignores both
// dependencies, which keeps default construction side-effect free.
type Config struct {
	Mode     Mode
	Client   ChatClient
	Registry *tools.Registry
}

// Engine bundles the adapters without starting or scheduling anything.
type Engine struct {
	Mode      Mode
	ChatModel *ChatModel
	Tools     []einoTool.InvokableTool
}

// New builds the requested engine skeleton. It deliberately does not wire
// itself into scan lifecycle, approvals, Verifier, WebSocket events or the
// report pipeline; those remain on legacy until the R4 parity gate.
func New(cfg Config) (*Engine, error) {
	mode, err := ParseMode(string(cfg.Mode))
	if err != nil {
		return nil, err
	}
	if mode == ModeLegacy {
		return &Engine{Mode: ModeLegacy}, nil
	}
	if cfg.Client == nil {
		return nil, errors.New("engine: eino mode requires a chat client")
	}
	if cfg.Registry == nil {
		return nil, errors.New("engine: eino mode requires a tool registry")
	}
	toolsList, err := RegistryTools(cfg.Registry)
	if err != nil {
		return nil, err
	}
	return &Engine{
		Mode:      ModeEino,
		ChatModel: NewChatModel(cfg.Client),
		Tools:     toolsList,
	}, nil
}
