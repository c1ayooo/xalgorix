import { useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Search, Wrench } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { EmptyState, ErrorState } from "@/components/states";
import { InvocationRecords } from "@/components/invocation-records";
import { Button } from "@/components/ui/button";
import { useI18n } from "@/i18n";
import { useToolCatalog, useToolInvocations } from "@/api/queries";
import type { InvocationListParams, ToolCatalogEntry } from "@/types/api";

/**
 * 内置工具页：引擎注册的工具面目录（只读）+ 跨扫描调用记录。
 * 目录是 GET /api/tools 的静态投影（不代表单个 Run 的启用状态）；
 * 记录是 web 层 best-effort JSONL 投影（与 MCP/技能/Agent 记录同构）。
 */
export default function ToolsPage() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = searchParams.get("tab") === "records" ? "records" : "available";
  const setTab = (v: string) =>
    setSearchParams(v === "available" ? {} : { tab: v }, { replace: true });

  return (
    <div className="space-y-6">
      <header>
        <h1 className="font-sans text-2xl font-semibold tracking-tight">{t("tools.title")}</h1>
        <p className="text-sm text-muted-foreground">{t("tools.subtitle")}</p>
      </header>

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="available">{t("tools.tabAvailable")}</TabsTrigger>
          <TabsTrigger value="records">{t("tools.tabRecords")}</TabsTrigger>
        </TabsList>
        <TabsContent value="available">
          <AvailableToolsTab />
        </TabsContent>
        <TabsContent value="records">
          <ToolsRecordsTab />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function AvailableToolsTab() {
  const { t } = useI18n();
  const { data, isLoading, isError, error, refetch } = useToolCatalog();
  const [query, setQuery] = useState("");
  const [category, setCategory] = useState<string>("");

  const tools = data?.tools ?? [];
  const categories = data?.categories ?? [];

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return tools.filter((entry) => {
      if (category && entry.category !== category) return false;
      if (!q) return true;
      return (
        entry.name.toLowerCase().includes(q) ||
        entry.description.toLowerCase().includes(q) ||
        (entry.parameters ?? []).some((p) => p.name.toLowerCase().includes(q))
      );
    });
  }, [tools, category, query]);

  const grouped = useMemo(() => {
    const groups = new Map<string, ToolCatalogEntry[]>();
    for (const entry of filtered) {
      const list = groups.get(entry.category) ?? [];
      list.push(entry);
      groups.set(entry.category, list);
    }
    // 保持后端 categories 顺序（注册顺序），未知分类排尾部。
    const order = [...categories];
    return [...groups.entries()].sort((a, b) => {
      const ia = order.indexOf(a[0]);
      const ib = order.indexOf(b[0]);
      return (ia < 0 ? Number.MAX_SAFE_INTEGER : ia) - (ib < 0 ? Number.MAX_SAFE_INTEGER : ib);
    });
  }, [filtered, categories]);

  if (isError) {
    return (
      <ErrorState
        title={t("tools.loadFailed")}
        description={error instanceof Error ? error.message : ""}
        action={
          <Button size="sm" variant="outline" onClick={() => refetch()}>
            {t("common.retry")}
          </Button>
        }
      />
    );
  }

  if (isLoading) {
    return (
      <div className="space-y-2">
        {Array.from({ length: 6 }).map((_, i) => (
          <Skeleton key={i} className="h-16 w-full" />
        ))}
      </div>
    );
  }

  if (tools.length === 0) {
    return (
      <EmptyState
        icon={<Wrench className="h-6 w-6" />}
        title={t("tools.emptyTitle")}
        description={t("tools.emptyDescription")}
      />
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t("tools.searchPlaceholder")}
            className="h-8 w-64 pl-8"
          />
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <CategoryChip active={category === ""} onClick={() => setCategory("")}>
            {t("tools.categoryAll")}
          </CategoryChip>
          {categories.map((key) => (
            <CategoryChip
              key={key}
              active={category === key}
              onClick={() => setCategory(category === key ? "" : key)}
            >
              {t(`tools.category.${key}`)}
            </CategoryChip>
          ))}
        </div>
        <span className="ml-auto text-xs text-muted-foreground tabular-nums">
          {filtered.length} / {tools.length}
        </span>
      </div>

      {filtered.length === 0 ? (
        <EmptyState
          icon={<Search className="h-6 w-6" />}
          title={t("tools.noMatchTitle")}
          description={t("tools.noMatchDescription")}
        />
      ) : (
        <div className="space-y-6">
          {grouped.map(([key, entries]) => (
            <section key={key} className="space-y-2">
              <h2 className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
                {t(`tools.category.${key}`)}
              </h2>
              <div className="overflow-hidden rounded-md border border-border">
                {entries.map((entry) => (
                  <ToolRow key={entry.name} entry={entry} />
                ))}
              </div>
            </section>
          ))}
        </div>
      )}
    </div>
  );
}

function ToolRow({ entry }: { entry: ToolCatalogEntry }) {
  const { t } = useI18n();
  return (
    <div className="border-b border-border/60 p-4 last:border-0 hover:bg-muted/30">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-sm font-medium">{entry.name}</span>
        {entry.approval ? (
          <Badge variant="warning">
            {t(`tools.approval.${entry.approval}`)}
          </Badge>
        ) : null}
      </div>
      <p className="mt-1 text-xs text-muted-foreground">{entry.description}</p>
      {(entry.parameters ?? []).length > 0 ? (
        <div className="mt-2 flex flex-wrap gap-1">
          {(entry.parameters ?? []).map((p) => (
            <Badge key={p.name} variant="outline" className="font-mono text-[10px]">
              {p.name}
              {p.required ? <span className="ml-0.5 text-primary">*</span> : null}
            </Badge>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function CategoryChip({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        active
          ? "rounded-full border border-border bg-accent px-2.5 py-0.5 text-[11px] text-accent-foreground"
          : "rounded-full border border-border px-2.5 py-0.5 text-[11px] text-muted-foreground hover:bg-accent/40 hover:text-foreground"
      }
    >
      {children}
    </button>
  );
}

function ToolsRecordsTab() {
  const { t } = useI18n();
  const [params, setParams] = useState<InvocationListParams>({ page: 1, size: 25 });
  const query = useToolInvocations(params);
  return (
    <InvocationRecords
      query={query}
      params={params}
      onParamsChange={setParams}
      namePlaceholder={t("tools.filterName")}
    />
  );
}
