import { useMemo } from "react";
import { ArrowDown, ArrowUp, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useAuthProfiles, useProviders } from "@/api/queries";
import { useI18n } from "@/i18n";

/**
 * 任务级模型链编辑器（4zreco task-llm-profile-chain 的 xalgorix 形态）。
 *
 * 链项是 LLM 凭据 profile 的 "<provider>:<profileId>" 身份；**数组顺序即优先级**
 * （#1 优先，模型在其 provider 目录里配置）。「当前」项是下一次 Run 的起点；
 * 运行期在配额/认证失败时按链顺序切换（后端 chain failover）。
 *
 * 同一个组件挂两处：任务创建/编辑对话框（草稿态随表单提交）与任务详情概览
 * （就地编辑，显式保存）。不要在两处各写一份 UI。
 */
export function TaskLLMChain({
  chain,
  active,
  onChange,
  disabled,
}: {
  chain: string[];
  active: string;
  onChange: (chain: string[], active: string) => void;
  disabled?: boolean;
}) {
  const { t } = useI18n();
  const profilesQuery = useAuthProfiles();
  const providersQuery = useProviders();

  const profileOptions = useMemo(() => {
    const profiles = profilesQuery.data ?? [];
    const byID = new Map((providersQuery.data ?? []).map((e) => [e.id, e]));
    return profiles.map((p) => ({
      value: `${p.provider}:${p.profileId}`,
      label: `${byID.get(p.provider)?.displayName ?? p.provider} · ${p.profileId}`,
    }));
  }, [profilesQuery.data, providersQuery.data]);

  const chainLabel = (key: string) =>
    profileOptions.find((o) => o.value === key)?.label ?? key;

  function addEntry(key: string) {
    if (!key || chain.includes(key)) return;
    const next = [...chain, key];
    onChange(next, active || key);
  }

  function removeEntry(key: string) {
    const next = chain.filter((k) => k !== key);
    onChange(next, active === key ? (next[0] ?? "") : active);
  }

  // 上/下移 = 优先级调整（数组顺序即链顺序）。
  function moveEntry(index: number, delta: number) {
    const target = index + delta;
    if (target < 0 || target >= chain.length) return;
    const next = [...chain];
    [next[index], next[target]] = [next[target], next[index]];
    onChange(next, active);
  }

  return (
    <div className="space-y-1.5">
      <Label>{t("tasks.fieldLLMChain")}</Label>
      <Select value="" onValueChange={addEntry} disabled={disabled}>
        <SelectTrigger>
          <SelectValue placeholder={t("tasks.llmChainAdd")} />
        </SelectTrigger>
        <SelectContent>
          {profileOptions.length === 0 ? (
            <SelectItem value="__none__" disabled>
              {t("tasks.llmChainNoProfiles")}
            </SelectItem>
          ) : (
            profileOptions.map((opt) => (
              <SelectItem
                key={opt.value}
                value={opt.value}
                disabled={chain.includes(opt.value)}
              >
                {opt.label}
              </SelectItem>
            ))
          )}
        </SelectContent>
      </Select>
      {chain.length === 0 ? (
        <p className="text-[11px] text-muted-foreground">
          {t("tasks.llmChainEmpty")}
        </p>
      ) : (
        <ul className="divide-y divide-border rounded-md border border-border">
          {chain.map((key, index) => (
            <li key={key} className="flex items-center gap-2 px-2 py-1.5 text-xs">
              <button
                type="button"
                onClick={() => onChange(chain, key)}
                title={t("tasks.llmChainActive")}
                aria-pressed={active === key}
                disabled={disabled}
                className={`size-2 shrink-0 rounded-full ${
                  active === key ? "bg-emerald-500" : "bg-muted-foreground/40"
                }`}
              />
              <span className="min-w-0 flex-1 truncate">{chainLabel(key)}</span>
              <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
                #{index + 1}
              </span>
              <Button
                size="icon"
                variant="ghost"
                className="size-6"
                disabled={disabled || index === 0}
                onClick={() => moveEntry(index, -1)}
                aria-label={t("tasks.llmChainUp")}
                title={t("tasks.llmChainUp")}
              >
                <ArrowUp className="size-3" />
              </Button>
              <Button
                size="icon"
                variant="ghost"
                className="size-6"
                disabled={disabled || index === chain.length - 1}
                onClick={() => moveEntry(index, 1)}
                aria-label={t("tasks.llmChainDown")}
                title={t("tasks.llmChainDown")}
              >
                <ArrowDown className="size-3" />
              </Button>
              <Button
                size="icon"
                variant="ghost"
                className="size-6"
                disabled={disabled}
                onClick={() => removeEntry(key)}
                aria-label={t("tasks.llmChainRemove")}
                title={t("tasks.llmChainRemove")}
              >
                <X className="size-3" />
              </Button>
            </li>
          ))}
        </ul>
      )}
      <p className="text-[11px] text-muted-foreground">
        {t("tasks.llmChainHint")}
      </p>
    </div>
  );
}
