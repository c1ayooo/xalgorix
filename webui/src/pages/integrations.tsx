import { Link } from "react-router-dom";
import { useI18n } from "@/i18n";
import {
  CheckCircle2,
  ExternalLink,
  MessageSquare,
  Plug,
  Send,
  ShieldCheck,
  Sparkles,
  Bot,
} from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  useEnvironmentSettings,
  useRateLimit,
  useVersion,
} from "@/api/queries";
import { cn } from "@/lib/utils";

type Integration = {
  key: string;
  name: string;
  description: string;
  category: "AI" | "Notifications" | "Engagement";
  icon: typeof Plug;
  configurePath?: string;
  configureLabel?: string;
  external?: string;
  isConfigured: boolean;
  detail?: string;
};

export default function IntegrationsPage() {
  const { t } = useI18n();
  const rate = useRateLimit();
  const version = useVersion();
  const environment = useEnvironmentSettings();
  const ai = version.data?.ai;
  const usesVercelGateway = ai?.gateway === "vercel";
  const discordWebhook = environment.data?.variables.find(
    (variable) => variable.key === "XALGORIX_DISCORD_WEBHOOK",
  );
  const telegramToken = environment.data?.variables.find(
    (variable) => variable.key === "XALGORIX_TELEGRAM_BOT_TOKEN",
  );

  const integrations: Integration[] = [
    {
      key: "llm-provider",
      name: usesVercelGateway ? "Vercel AI Gateway" : t("settings.card.llmProvider"),
      description: usesVercelGateway
        ? t("integrations.card.llmGatewayDesc")
        : t("integrations.card.llmDesc"),
      category: "AI",
      icon: Sparkles,
      configurePath: "/settings?tab=llm",
      configureLabel: t("integrations.card.openSettings"),
      external: usesVercelGateway ? "https://vercel.com/docs/ai-gateway" : undefined,
      isConfigured: Boolean(ai?.configured),
      detail: ai
        ? `${ai.provider}${ai.model ? ` · ${ai.model}` : ""}`
        : t("integrations.card.loadingProvider"),
    },
    {
      key: "discord",
      name: t("settings.notifications.webhookLabel"),
      description: t("integrations.card.discordDesc"),
      category: "Notifications",
      icon: MessageSquare,
      configurePath: "/settings?tab=notifications",
      configureLabel: t("integrations.card.configure"),
      external: "https://discord.com/developers/docs/resources/webhook",
      isConfigured: Boolean(discordWebhook?.hasValue),
      detail: discordWebhook?.hasValue
        ? t("integrations.card.discordDetailOn")
        : t("integrations.card.discordDetailOff"),
    },
    {
      key: "telegram",
      name: t("integrations.card.telegramName"),
      description: t("integrations.card.telegramDesc"),
      category: "Notifications",
      icon: Send,
      configurePath: "/settings?tab=notifications",
      configureLabel: t("integrations.card.configure"),
      external: "https://core.telegram.org/bots/api",
      isConfigured: Boolean(telegramToken?.hasValue),
      detail: telegramToken?.hasValue
        ? t("integrations.card.telegramDetailOn")
        : t("integrations.card.telegramDetailOff"),
    },
    {
      key: "rate-limit",
      name: t("integrations.card.rateName"),
      description: t("integrations.card.rateDesc"),
      category: "Engagement",
      icon: ShieldCheck,
      configurePath: "/settings?tab=engagement",
      configureLabel: t("integrations.card.rateAdjust"),
      isConfigured: Boolean(rate.data?.requests),
      detail: rate.data
        ? `${rate.data.requests} ${t("integrations.card.reqUnit")} / ${rate.data.window} ${t("integrations.card.windowUnit")}`
        : t("common.loading"),
    },
    // 集成模块：MCP / Skills / Agents——独立管理页（各含配置 + 调用记录两 tab）。
    {
      key: "mcp",
      name: t("nav.mcp"),
      description: t("mcp.subtitle"),
      category: "Engagement",
      icon: Plug,
      configurePath: "/mcp",
      configureLabel: t("integrations.card.manage"),
      isConfigured: true,
    },
    {
      key: "skills",
      name: t("nav.skills"),
      description: t("skills.subtitle"),
      category: "Engagement",
      icon: Sparkles,
      configurePath: "/skills",
      configureLabel: t("integrations.card.manage"),
      isConfigured: true,
    },
    {
      key: "agents",
      name: t("nav.agents"),
      description: t("agents.subtitle"),
      category: "Engagement",
      icon: Bot,
      configurePath: "/agents",
      configureLabel: t("integrations.card.manage"),
      isConfigured: true,
    },
  ];

  const byCategory = groupBy(integrations, (i) => i.category);

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{t("integrations.title")}</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          {t("integrations.subtitle")}
        </p>
      </div>

      {(["AI", "Notifications", "Engagement"] as const).map((cat) => {
        const items = byCategory[cat] ?? [];
        if (!items.length) return null;
        return (
          <section key={cat} className="space-y-3">
            <h2 className="text-[11px] uppercase tracking-wider text-muted-foreground">
              {t(`integrations.category.${cat}`)}
            </h2>
            <div className="grid gap-3 md:grid-cols-2">
              {items.map((it) => (
                <IntegrationCard key={it.key} integration={it} />
              ))}
            </div>
          </section>
        );
      })}
    </div>
  );
}

function IntegrationCard({ integration }: { integration: Integration }) {
  const { t } = useI18n();
  const Icon = integration.icon;
  return (
    <Card className="overflow-hidden">
      <CardContent className="space-y-3 p-4">
        <div className="flex items-start gap-3">
          <div
            className={cn(
              "flex h-10 w-10 shrink-0 items-center justify-center rounded-md border",
              integration.isConfigured
                ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
                : "border-border bg-muted text-muted-foreground",
            )}
          >
            <Icon className="h-5 w-5" />
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <h3 className="text-sm font-medium text-foreground">
                {integration.name}
              </h3>
              {integration.isConfigured ? (
                <Badge className="bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-500/30 text-[10px]">
                  <CheckCircle2 className="h-3 w-3" /> {t("integrations.connected")}
                </Badge>
              ) : (
                <Badge variant="outline" className="text-[10px] text-muted-foreground">
                  {t("integrations.notConnected")}
                </Badge>
              )}
            </div>
            <p className="mt-1 text-xs text-muted-foreground text-pretty">
              {integration.description}
            </p>
            {integration.detail && (
              <p className="mt-2 mono text-[11px] text-muted-foreground">
                {integration.detail}
              </p>
            )}
          </div>
        </div>
        <div className="flex items-center justify-end gap-2 border-t border-border pt-3">
          {integration.external && (
            <Button asChild size="sm" variant="ghost">
              <a href={integration.external} target="_blank" rel="noreferrer">
                {t("integrations.card.docs")} <ExternalLink className="h-3 w-3" />
              </a>
            </Button>
          )}
          {integration.configurePath && (
            <Button asChild size="sm" variant="outline">
              <Link to={integration.configurePath}>
                {integration.configureLabel || t("integrations.card.configure")}
              </Link>
            </Button>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

function groupBy<T, K extends string>(arr: T[], key: (t: T) => K) {
  return arr.reduce<Record<string, T[]>>((acc, item) => {
    const k = key(item);
    (acc[k] ||= []).push(item);
    return acc;
  }, {});
}
