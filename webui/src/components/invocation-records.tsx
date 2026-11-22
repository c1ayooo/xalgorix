import { useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  DEFAULT_PAGE_SIZE,
  Pagination,
  PAGE_SIZE_OPTIONS,
} from "@/components/Pagination";
import { EmptyState, ErrorState } from "@/components/states";
import { useI18n } from "@/i18n";
import type { InvocationList, InvocationListParams } from "@/types/api";

/**
 * 调用记录列表（三个集成模块共用的「调用记录」tab 渲染体）。
 * 行 = 一条跨扫描调用；展开行显示 args/outcome/error 的 mono 详情。
 * 数据获取由调用方（页面）完成，本组件只负责渲染 + 过滤/分页状态回传。
 */
export function InvocationRecords({
  query,
  params,
  onParamsChange,
  namePlaceholder,
  extraColumns,
}: {
  query: {
    data?: InvocationList;
    isLoading: boolean;
    isError: boolean;
    refetch: () => void;
  };
  params: InvocationListParams;
  onParamsChange: (next: InvocationListParams) => void;
  namePlaceholder: string;
  /** 模块特有列（如 MCP 的 server 列）。 */
  extraColumns?: (record: InvocationRecordLike) => React.ReactNode;
}) {
  const { t } = useI18n();
  const [openId, setOpenId] = useState<string | null>(null);

  if (query.isError) {
    return (
      <ErrorState
        title={t("invocations.errorTitle")}
        action={
          <button
            type="button"
            onClick={() => query.refetch()}
            className="text-xs font-medium text-primary underline-offset-4 hover:underline"
          >
            {t("common.retry")}
          </button>
        }
      />
    );
  }

  if (query.isLoading) {
    return (
      <div className="space-y-2">
        {Array.from({ length: 5 }).map((_, i) => (
          <Skeleton key={i} className="h-12 w-full" />
        ))}
      </div>
    );
  }

  const list = query.data;
  if (!list || list.items.length === 0) {
    return (
      <EmptyState
        title={t("invocations.emptyTitle")}
        description={t("invocations.emptyDescription")}
      />
    );
  }

  return (
    <div className="space-y-4">
      {/* 过滤条：name 子串 + status。分页参数由 Pagination 组件同步。 */}
      <div className="flex flex-wrap items-center gap-2">
        <Input
          value={params.name ?? ""}
          onChange={(e) =>
            onParamsChange({ ...params, name: e.target.value || undefined, page: 1 })
          }
          placeholder={namePlaceholder}
          className="h-8 w-56"
        />
        <Select
          value={params.status ?? "all"}
          onValueChange={(v) =>
            onParamsChange({
              ...params,
              status: v === "all" ? undefined : (v as "ok" | "error"),
              page: 1,
            })
          }
        >
          <SelectTrigger className="h-8 w-32">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("invocations.statusAll")}</SelectItem>
            <SelectItem value="ok">{t("invocations.statusOk")}</SelectItem>
            <SelectItem value="error">{t("invocations.statusError")}</SelectItem>
          </SelectContent>
        </Select>
        <span className="ml-auto text-xs text-muted-foreground tabular-nums">
          {`${t("invocations.total")} ${list.total}`}
        </span>
      </div>

      <div className="overflow-hidden rounded-md border border-border">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b bg-muted/30 text-left text-xs uppercase tracking-wide text-muted-foreground">
              <th className="px-4 py-3 font-medium"></th>
              <th className="px-4 py-3 font-medium">{t("invocations.colName")}</th>
              {extraColumns ? (
                <th className="hidden md:table-cell px-4 py-3 font-medium">
                  {t("invocations.colServer")}
                </th>
              ) : null}
              <th className="px-4 py-3 font-medium">{t("invocations.colStatus")}</th>
              <th className="hidden sm:table-cell px-4 py-3 font-medium">
                {t("invocations.colTime")}
              </th>
              <th className="hidden lg:table-cell px-4 py-3 font-medium">
                {t("invocations.colScan")}
              </th>
            </tr>
          </thead>
          <tbody>
            {list.items.map((rec) => {
              const open = openId === rec.id;
              return (
                <InvocationRow
                  key={rec.id}
                  record={rec}
                  open={open}
                  onToggle={() => setOpenId(open ? null : rec.id)}
                  extraColumns={extraColumns}
                />
              );
            })}
          </tbody>
        </table>
      </div>

      <Pagination
        totalItems={list.total}
        page={list.page}
        pageSize={list.size}
        onPageChange={(page) => onParamsChange({ ...params, page })}
        onPageSizeChange={(size) =>
          onParamsChange({ ...params, size, page: 1 })
        }
      />
    </div>
  );
}

export type InvocationRecordLike = InvocationList["items"][number];

function InvocationRow({
  record,
  open,
  onToggle,
  extraColumns,
}: {
  record: InvocationRecordLike;
  open: boolean;
  onToggle: () => void;
  extraColumns?: (record: InvocationRecordLike) => React.ReactNode;
}) {
  const { t } = useI18n();
  const hasDetail = Boolean(record.args || record.outcome || record.error);
  return (
    <>
      <tr
        className="border-b border-border/60 last:border-0 hover:bg-muted/30"
        aria-expanded={open}
      >
        <td className="px-4 py-3">
          {hasDetail ? (
            <button
              type="button"
              onClick={onToggle}
              className="text-muted-foreground hover:text-foreground"
              aria-label={open ? t("common.collapse") : t("common.expand")}
            >
              {open ? (
                <ChevronDown className="h-4 w-4" />
              ) : (
                <ChevronRight className="h-4 w-4" />
              )}
            </button>
          ) : null}
        </td>
        <td className="px-4 py-3">
          <span className="font-mono text-xs">{record.name}</span>
        </td>
        {extraColumns ? <td className="hidden md:table-cell px-4 py-3">{extraColumns(record)}</td> : null}
        <td className="px-4 py-3">
          {record.status === "ok" ? (
            <Badge variant="success">{t("invocations.statusOk")}</Badge>
          ) : (
            <Badge variant="destructive">{t("invocations.statusError")}</Badge>
          )}
        </td>
        <td className="hidden sm:table-cell px-4 py-3 font-mono text-xs text-muted-foreground tabular-nums">
          {record.ts}
        </td>
        <td className="hidden lg:table-cell px-4 py-3 font-mono text-xs text-muted-foreground">
          {record.instance_id || record.scan_id || "—"}
        </td>
      </tr>
      {open ? (
        <tr className="border-b border-border/60 last:border-0 bg-muted/20">
          <td colSpan={extraColumns ? 6 : 5} className="px-4 py-3">
            <div className="space-y-2">
              <DetailBlock label={t("invocations.args")} content={record.args} />
              <DetailBlock label={t("invocations.outcome")} content={record.outcome} />
              <DetailBlock label={t("invocations.errorLabel")} content={record.error} />
            </div>
          </td>
        </tr>
      ) : null}
    </>
  );
}

function DetailBlock({ label, content }: { label: string; content?: string }) {
  if (!content) return null;
  return (
    <div>
      <p className="text-[11px] uppercase tracking-wide text-muted-foreground">{label}</p>
      <pre className="mt-1 max-h-72 overflow-auto rounded bg-muted/40 p-2 font-mono text-xs whitespace-pre-wrap">
        {content}
      </pre>
    </div>
  );
}

/** 页面用它把「第 N 页 + 每页大小」拼进请求参数。 */
export function clampPage(page: number, size: number): InvocationListParams {
  return { page: Math.max(1, page), size: size || DEFAULT_PAGE_SIZE };
}

export { PAGE_SIZE_OPTIONS };
