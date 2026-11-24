import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useProxies } from "@/api/queries";
import { useI18n } from "@/i18n";

/**
 * 任务级出口代理选择器。"" = 跟随全局池（默认）；"direct" = 显式直连；
 * "N" = 全局池的第 N 项（1-based）。代理池本身仍是 Settings › Proxy 管理，
 * 保存即热生效；任务只是把某次 Run 钉在其中一项上。
 *
 * 直连在进程 `XALGORIX_PROXY_REQUIRED` 时被后端拒绝（fail-closed），这里也置灰；
 * 已保存的池索引在池变化后可能失效——行内警告，Run 启动时后端会再次校验。
 */
export function TaskProxySelect({
  value,
  onChange,
  disabled,
}: {
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
}) {
  const { t } = useI18n();
  const query = useProxies();
  const entries = query.data?.entries ?? [];
  const required = query.data?.required ?? false;
  const selected = value || "inherit";
  const stale = selected !== "inherit" && selected !== "direct" && !entries.some((e) => String(e.index) === selected);

  return (
    <div className="space-y-1.5">
      <Label>{t("tasks.fieldProxy")}</Label>
      <Select value={selected} onValueChange={(v) => onChange(v === "inherit" ? "" : v)} disabled={disabled}>
        <SelectTrigger>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="inherit">{t("tasks.proxyInherit")}</SelectItem>
          <SelectItem value="direct" disabled={required}>
            {t("tasks.proxyDirect")}
          </SelectItem>
          {entries.map((entry) => (
            <SelectItem key={entry.index} value={String(entry.index)}>
              {`#${entry.index} · ${entry.label}`}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {stale && (
        <p className="text-[11px] text-amber-600 dark:text-amber-400">
          {t("tasks.proxyStale")}
        </p>
      )}
      <p className="text-[11px] text-muted-foreground">
        {required ? t("tasks.proxyRequiredHint") : t("tasks.proxyHint")}
      </p>
    </div>
  );
}
