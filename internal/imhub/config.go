package imhub

// config.go holds the inbound IM channel configuration. xalgorix stores all
// channel settings in the environment file (~/.xalgorix.env) rather than a PG
// settings table, so this file reads XALGORIX_IM_* / vendor variables and
// reports which channels are usable. Secrets never leave the process: the
// dashboard only ever learns the *_set booleans from the env settings API.

import (
	"os"
	"strconv"
	"strings"
)

// WecomCallbackPath is the enterprise-WeChat callback endpoint. The route
// table, the auth exemption and the URL echo all use this one constant.
const WecomCallbackPath = "/api/im/wecom/callback"

// FeishuConfig / DingtalkConfig / TelegramConfig / WecomConfig mirror the four
// inbound channels. Enabled is derived, not configured: a channel is on when
// its credential set is complete.
type FeishuConfig struct {
	Enabled   bool
	AppID     string
	AppSecret string
}

type DingtalkConfig struct {
	Enabled      bool
	ClientID     string
	ClientSecret string
}

type TelegramConfig struct {
	Enabled  bool
	BotToken string
	Proxy    string
}

type WecomConfig struct {
	Enabled        bool
	CorpID         string
	CorpSecret     string
	AgentID        int64
	CallbackToken  string
	EncodingAESKey string
}

// Config is the resolved channel set.
type Config struct {
	Feishu   FeishuConfig
	Dingtalk DingtalkConfig
	Telegram TelegramConfig
	Wecom    WecomConfig
}

// AnyEnabled reports whether at least one inbound channel is usable.
func (c Config) AnyEnabled() bool {
	return c.Feishu.Enabled || c.Dingtalk.Enabled || c.Telegram.Enabled || c.Wecom.Enabled
}

// LoadConfigFromEnv reads the inbound IM configuration. Every channel is
// disabled unless its full credential set is present, so a half-configured
// deployment cannot accept spoofable traffic.
func LoadConfigFromEnv() Config {
	feishuID := env("XALGORIX_FEISHU_APP_ID", "FEISHU_APP_ID")
	feishuSecret := env("XALGORIX_FEISHU_APP_SECRET", "FEISHU_APP_SECRET")
	dingID := strings.TrimSpace(os.Getenv("XALGORIX_DINGTALK_CLIENT_ID"))
	dingSecret := strings.TrimSpace(os.Getenv("XALGORIX_DINGTALK_CLIENT_SECRET"))
	tgToken := env("XALGORIX_TELEGRAM_BOT_TOKEN", "TELEGRAM_BOT_TOKEN")
	wecomCorp := env("XALGORIX_WECOM_CORP_ID", "WECOM_CORP_ID")
	wecomSecret := env("XALGORIX_WECOM_SECRET", "WECOM_SECRET")
	wecomToken := env("XALGORIX_WECOM_TOKEN", "WECOM_TOKEN")
	wecomAES := env("XALGORIX_WECOM_AES_KEY", "WECOM_AES_KEY")
	agentID, _ := strconv.ParseInt(strings.TrimSpace(os.Getenv("XALGORIX_WECOM_AGENT_ID")), 10, 64)

	return Config{
		Feishu: FeishuConfig{
			Enabled:   feishuID != "" && feishuSecret != "",
			AppID:     feishuID,
			AppSecret: feishuSecret,
		},
		Dingtalk: DingtalkConfig{
			Enabled:      dingID != "" && dingSecret != "",
			ClientID:     dingID,
			ClientSecret: dingSecret,
		},
		Telegram: TelegramConfig{
			Enabled:  tgToken != "",
			BotToken: tgToken,
			Proxy:    strings.TrimSpace(os.Getenv("XALGORIX_TELEGRAM_PROXY")),
		},
		Wecom: WecomConfig{
			Enabled:        wecomCorp != "" && wecomSecret != "" && wecomToken != "" && wecomAES != "",
			CorpID:         wecomCorp,
			CorpSecret:     wecomSecret,
			AgentID:        agentID,
			CallbackToken:  wecomToken,
			EncodingAESKey: wecomAES,
		},
	}
}

func env(primary, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(primary)); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(fallback))
}
