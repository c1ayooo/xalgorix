import { useI18n } from "@/i18n";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { cn } from "@/lib/utils";
import type { ProjectScope } from "@/types/api";

/** 围栏可用的写方法（后端 platform.FenceHTTPMethods 是唯一权威）。 */
export const FENCE_HTTP_METHODS = [
  "GET",
  "HEAD",
  "OPTIONS",
  "POST",
  "PUT",
  "PATCH",
  "DELETE",
] as const;

/**
 * 范围草稿：一张表单同时承载两半边界。
 *
 *   targets/ports  → dispatch 校验（资源启动时可点名的目标）
 *   fence.*        → HITL 运行时围栏（命令/高危 HTTP/代理流量的实际目的地）
 *
 * 文本域与勾选状态的原始值都留在草稿里（不提前规范），这样用户输入 `" , "`
 * 之类的中间态不会被改写；只在提交边界（draftToScope）做清洗，与后端
 * normalizeScope 的语义一致。
 */
export interface ScopeDraft {
  targets: string;
  ports: string;
  enforce: boolean;
  denyTargets: string;
  methods: string[];
  denyPatterns: string;
}

export function emptyScopeDraft(): ScopeDraft {
  return {
    targets: "",
    ports: "",
    enforce: false,
    denyTargets: "",
    methods: [],
    denyPatterns: "",
  };
}

function splitList(raw: string): string[] {
  return raw
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean);
}

function dedupe(values: string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const v of values) {
    const key = v.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(v);
  }
  return out;
}

export function draftTargets(draft: ScopeDraft): string[] {
  return dedupe(splitList(draft.targets));
}

export function draftPorts(draft: ScopeDraft): number[] {
  const out: number[] = [];
  for (const raw of splitList(draft.ports)) {
    const n = Number.parseInt(raw, 10);
    if (Number.isFinite(n) && n > 0 && n <= 65535 && !out.includes(n)) out.push(n);
  }
  return out;
}

/** 草稿 → 线上 Scope。与后端 normalizeScope 对齐（去空、去重、方法大写）。 */
export function draftToScope(draft: ScopeDraft): ProjectScope {
  const scope: ProjectScope = { targets: draftTargets(draft) };
  const ports = draftPorts(draft);
  if (ports.length > 0) scope.allow_ports = ports;
  const denyTargets = dedupe(splitList(draft.denyTargets));
  const methods = FENCE_HTTP_METHODS.filter((m) => draft.methods.includes(m));
  const denyPatterns = dedupe(splitList(draft.denyPatterns));
  const fenceFields =
    draft.enforce || denyTargets.length > 0 || methods.length > 0 || denyPatterns.length > 0;
  if (fenceFields) {
    scope.fence = { enforce: draft.enforce };
    if (denyTargets.length > 0) scope.fence.deny_targets = denyTargets;
    if (methods.length > 0) scope.fence.allow_http_methods = methods;
    if (denyPatterns.length > 0) scope.fence.deny_command_patterns = denyPatterns;
  }
  return scope;
}

export function draftFromScope(scope?: ProjectScope): ScopeDraft {
  if (!scope) return emptyScopeDraft();
  const fence = scope.fence;
  return {
    targets: (scope.targets ?? []).join("\n"),
    ports: (scope.allow_ports ?? []).join(", "),
    enforce: !!fence?.enforce,
    denyTargets: (fence?.deny_targets ?? []).join("\n"),
    methods: fence?.allow_http_methods ?? [],
    denyPatterns: (fence?.deny_command_patterns ?? []).join("\n"),
  };
}

/**
 * 客户端前置校验：只挡「明显存不下去」的输入，真正权威的判定仍在后端
 * （platform.ValidateScope + POST …/scope/validate）。返回 i18n key 列表。
 */
export function draftProblems(draft: ScopeDraft): string[] {
  const problems: string[] = [];
  if (draft.enforce && draftTargets(draft).length === 0) {
    problems.push("scope.problemEnforceWithoutTargets");
  }
  if (draftPorts(draft).length !== splitList(draft.ports).length) {
    problems.push("scope.problemPorts");
  }
  const deny = splitList(draft.denyTargets);
  if (deny.some((entry) => entry.includes("://") || /\]:\d+$/.test(entry) || /^[^:[\]]+:\d+$/.test(entry))) {
    problems.push("scope.problemDenyPorts");
  }
  return problems;
}

export function ScopeFields({
  draft,
  onChange,
  disabled,
  idPrefix,
}: {
  draft: ScopeDraft;
  onChange: (next: ScopeDraft) => void;
  disabled?: boolean;
  idPrefix: string;
}) {
  const { t } = useI18n();
  const patch = (p: Partial<ScopeDraft>) => onChange({ ...draft, ...p });

  return (
    <div className="space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor={`${idPrefix}-targets`}>{t("projects.fieldTargets")}</Label>
        <Textarea
          id={`${idPrefix}-targets`}
          rows={3}
          value={draft.targets}
          onChange={(e) => patch({ targets: e.target.value })}
          placeholder={"acme.com\napi.acme.com\n*.acme.com"}
          disabled={disabled}
        />
        <p className="text-[11px] text-muted-foreground">{t("projects.targetsHint")}</p>
      </div>
      <div className="space-y-1.5">
        <Label htmlFor={`${idPrefix}-ports`}>{t("projects.fieldPorts")}</Label>
        <Input
          id={`${idPrefix}-ports`}
          value={draft.ports}
          onChange={(e) => patch({ ports: e.target.value })}
          placeholder="80, 443"
          disabled={disabled}
        />
        <p className="text-[11px] text-muted-foreground">{t("scope.portsHint")}</p>
      </div>

      <div className="space-y-3 rounded-md border border-border p-3">
        <label className="flex items-start gap-2">
          <Switch
            checked={draft.enforce}
            onCheckedChange={(v) => patch({ enforce: v })}
            disabled={disabled}
            aria-label={t("scope.fenceEnforce")}
          />
          <span className="space-y-0.5">
            <span className="block text-sm font-medium">{t("scope.fenceEnforce")}</span>
            <span className="block text-[11px] text-muted-foreground">
              {t("scope.fenceEnforceHint")}
            </span>
          </span>
        </label>

        <div
          className={cn(
            "space-y-3 border-t border-border/60 pt-3",
            !draft.enforce && "opacity-60",
          )}
        >
          <div className="space-y-1.5">
            <Label htmlFor={`${idPrefix}-deny`}>{t("scope.fenceDenyTargets")}</Label>
            <Textarea
              id={`${idPrefix}-deny`}
              rows={2}
              value={draft.denyTargets}
              onChange={(e) => patch({ denyTargets: e.target.value })}
              placeholder={"billing.acme.com\n*.statuspage.acme.com"}
              disabled={disabled}
            />
            <p className="text-[11px] text-muted-foreground">{t("scope.fenceDenyTargetsHint")}</p>
          </div>

          <div className="space-y-1.5">
            <Label>{t("scope.fenceMethods")}</Label>
            <div className="flex flex-wrap gap-1.5">
              {FENCE_HTTP_METHODS.map((method) => {
                const active = draft.methods.includes(method);
                return (
                  <button
                    key={method}
                    type="button"
                    disabled={disabled}
                    aria-pressed={active}
                    onClick={() =>
                      patch({
                        methods: active
                          ? draft.methods.filter((m) => m !== method)
                          : [...draft.methods, method],
                      })
                    }
                    className={cn(
                      "mono rounded-sm border px-1.5 py-0.5 text-[11px] transition-colors",
                      active
                        ? "border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300"
                        : "border-border text-muted-foreground hover:bg-accent",
                    )}
                  >
                    {method}
                  </button>
                );
              })}
            </div>
            <p className="text-[11px] text-muted-foreground">{t("scope.fenceMethodsHint")}</p>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor={`${idPrefix}-patterns`}>{t("scope.fenceDenyCommands")}</Label>
            <Textarea
              id={`${idPrefix}-patterns`}
              rows={2}
              value={draft.denyPatterns}
              onChange={(e) => patch({ denyPatterns: e.target.value })}
              placeholder={"msfconsole\n--os-shell\nsudo"}
              disabled={disabled}
            />
            <p className="text-[11px] text-muted-foreground">{t("scope.fenceDenyCommandsHint")}</p>
          </div>
        </div>
      </div>
    </div>
  );
}
