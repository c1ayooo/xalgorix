import { useState } from "react";
import { useSearchParams } from "react-router-dom";
import { toast } from "sonner";
import { Cable, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { EmptyState, ErrorState } from "@/components/states";
import { InvocationRecords } from "@/components/invocation-records";
import { menuContentClass, menuItemClass } from "@/lib/utils";
import { useI18n } from "@/i18n";
import {
  useMcpServers,
  useCreateMcpServer,
  useUpdateMcpServer,
  useDeleteMcpServer,
  useRefreshMcpServer,
  useMcpInvocations,
} from "@/api/queries";
import type { InvocationListParams, McpServer } from "@/types/api";

/**
 * MCP 服务器管理（契约 §integration-mcp-servers）。
 * 两个 tab：配置管理（列表 + CRUD + refresh）与调用记录（跨扫描检索投影）。
 */
export default function McpServersPage() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = searchParams.get("tab") === "records" ? "records" : "config";
  const setTab = (v: string) =>
    setSearchParams(v === "config" ? {} : { tab: v }, { replace: true });

  return (
    <div className="space-y-6">
      <header>
        <h1 className="font-sans text-2xl font-semibold tracking-tight">{t("mcp.title")}</h1>
        <p className="text-sm text-muted-foreground">{t("mcp.subtitle")}</p>
      </header>

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="config">{t("mcp.tabConfig")}</TabsTrigger>
          <TabsTrigger value="records">{t("mcp.tabRecords")}</TabsTrigger>
        </TabsList>
        <TabsContent value="config">
          <McpConfigTab />
        </TabsContent>
        <TabsContent value="records">
          <McpRecordsTab />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function McpConfigTab() {
  const { t } = useI18n();
  const { data, isLoading, isError, error, refetch } = useMcpServers();
  const { toggleEnabled } = useMcpRowActions();
  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<McpServer | null>(null);
  const [deleting, setDeleting] = useState<McpServer | null>(null);

  const servers = data?.servers ?? [];

  return (
    <div className="space-y-4">
      <div className="flex justify-end">
        <Button
          onClick={() => {
            setEditing(null);
            setFormOpen(true);
          }}
        >
          <Plus className="mr-1 h-4 w-4" />
          {t("mcp.create")}
        </Button>
      </div>

      {isError ? (
        <ErrorState
          title={t("mcp.loadFailed")}
          description={error instanceof Error ? error.message : ""}
          action={
            <Button size="sm" variant="outline" onClick={() => refetch()}>
              {t("common.retry")}
            </Button>
          }
        />
      ) : isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-14 w-full" />
          ))}
        </div>
      ) : servers.length === 0 ? (
        <EmptyState
          icon={<Cable className="h-6 w-6" />}
          title={t("mcp.emptyTitle")}
          description={t("mcp.emptyDescription")}
          action={
            <Button
              onClick={() => {
                setEditing(null);
                setFormOpen(true);
              }}
            >
              <Plus className="mr-1 h-4 w-4" />
              {t("mcp.create")}
            </Button>
          }
        />
      ) : (
        <div className="overflow-hidden rounded-md border border-border">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b bg-muted/30 text-left text-xs uppercase tracking-wide text-muted-foreground">
                <th className="px-4 py-3 font-medium">{t("mcp.colName")}</th>
                <th className="px-4 py-3 font-medium">{t("mcp.colTransport")}</th>
                <th className="hidden md:table-cell px-4 py-3 font-medium">{t("mcp.colEndpoint")}</th>
                <th className="px-4 py-3 font-medium">{t("mcp.colTools")}</th>
                <th className="hidden sm:table-cell px-4 py-3 font-medium">{t("mcp.colHealth")}</th>
                <th className="px-4 py-3 font-medium">{t("mcp.colApproval")}</th>
                <th className="px-4 py-3 text-right font-medium">{t("common.actions")}</th>
              </tr>
            </thead>
            <tbody>
              {servers.map((m) => (
                <tr key={m.id} className="border-b border-border/60 last:border-0 hover:bg-muted/30">
                  <td className="px-4 py-3">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium">{m.name}</span>
                      <Switch
                        checked={m.enabled}
                        aria-label={`${t("mcp.colName")}: ${m.name}`}
                        onCheckedChange={(v) => toggleEnabled(m.id, v)}
                      />
                    </div>
                  </td>
                  <td className="px-4 py-3">
                    <Badge variant="muted">{m.transport}</Badge>
                  </td>
                  <td className="hidden md:table-cell px-4 py-3">
                    <span className="font-mono text-xs text-muted-foreground">
                      {m.transport === "stdio" ? [m.command, ...(m.args ?? [])].join(" ") : m.url}
                    </span>
                  </td>
                  <td className="px-4 py-3 font-mono text-xs tabular-nums">{m.tool_count}</td>
                  <td className="hidden sm:table-cell px-4 py-3">
                    {m.last_error ? (
                      <Badge variant="destructive">{t("mcp.healthError")}</Badge>
                    ) : m.last_healthy_at ? (
                      <Badge variant="success">{t("mcp.healthOk")}</Badge>
                    ) : (
                      <Badge variant="muted">{t("mcp.healthUnknown")}</Badge>
                    )}
                  </td>
                  <td className="px-4 py-3">
                    <Badge variant={m.require_approval ? "warning" : "muted"}>
                      {m.require_approval ? t("mcp.approvalRequired") : t("mcp.approvalExempt")}
                    </Badge>
                  </td>
                  <td className="px-4 py-3 text-right">
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button size="sm" variant="outline">
                          {t("common.actions")}
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end" className={menuContentClass}>
                        <DropdownMenuItem className={menuItemClass} onClick={() => setEditing(m)}>
                          <Pencil className="mr-2 h-3.5 w-3.5" />
                          {t("common.edit")}
                        </DropdownMenuItem>
                        <McpRefreshItem id={m.id} />
                        <DropdownMenuItem
                          className={`${menuItemClass} text-red-600 focus:text-red-600 dark:text-red-400`}
                          onClick={() => setDeleting(m)}
                        >
                          <Trash2 className="mr-2 h-3.5 w-3.5" />
                          {t("common.delete")}
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <McpFormDialog open={formOpen} onOpenChange={setFormOpen} editing={editing} />
      <McpDeleteDialog target={deleting} onClear={() => setDeleting(null)} />
    </div>
  );
}

/** enabled 开关 + refresh 都走行级 mutation（失败 toast，成功刷新缓存）。 */
function useMcpRowActions() {
  const { t } = useI18n();
  const update = useUpdateMcpServer();
  const refresh = useRefreshMcpServer();
  const toggleEnabled = (id: string, enabled: boolean) => {
    update.mutate(
      { id, patch: { enabled } },
      {
        onSuccess: () => toast.success(t("toast.mcpUpdated")),
        onError: (err: Error) => toast.error(`${t("toast.mcpFailed")}: ${err.message}`),
      },
    );
  };
  const refreshServer = (id: string) => {
    refresh.mutate(id, {
      onSuccess: (server) => {
        if (server.last_error) {
          toast.error(`${t("toast.mcpRefreshFailed")}: ${server.last_error}`);
        } else {
          toast.success(`${t("toast.mcpRefreshed")} (${server.tool_count})`);
        }
      },
      onError: (err: Error) => toast.error(`${t("toast.mcpFailed")}: ${err.message}`),
    });
  };
  return { toggleEnabled, refreshServer, pending: update.isPending || refresh.isPending };
}

function McpRefreshItem({ id }: { id: string }) {
  const { t } = useI18n();
  const { refreshServer } = useMcpRowActions();
  return (
    <DropdownMenuItem className={menuItemClass} onClick={() => refreshServer(id)}>
      <RefreshCw className="mr-2 h-3.5 w-3.5" />
      {t("mcp.refresh")}
    </DropdownMenuItem>
  );
}

function McpRecordsTab() {
  const { t } = useI18n();
  const [params, setParams] = useState<InvocationListParams>({ page: 1, size: 25 });
  const query = useMcpInvocations(params);
  return (
    <InvocationRecords
      query={query}
      params={params}
      onParamsChange={setParams}
      namePlaceholder={t("mcp.filterName")}
      extraColumns={(rec) => (
        <span className="font-mono text-xs text-muted-foreground">{rec.server || "—"}</span>
      )}
    />
  );
}

function McpFormDialog({
  open,
  onOpenChange,
  editing,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  editing: McpServer | null;
}) {
  const { t } = useI18n();
  const create = useCreateMcpServer();
  const update = useUpdateMcpServer();
  const [name, setName] = useState("");
  const [transport, setTransport] = useState<"stdio" | "http">("stdio");
  const [command, setCommand] = useState("");
  const [args, setArgs] = useState("");
  const [url, setUrl] = useState("");
  const [envText, setEnvText] = useState("");
  const [headersText, setHeadersText] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [requireApproval, setRequireApproval] = useState(true);
  const [timeoutSeconds, setTimeoutSeconds] = useState("120");
  const [formError, setFormError] = useState("");

  const reset = (m: McpServer | null) => {
    setName(m?.name ?? "");
    setTransport(m?.transport ?? "stdio");
    setCommand(m?.command ?? "");
    setArgs((m?.args ?? []).join(" "));
    setUrl(m?.url ?? "");
    // env/headers 是打码回显；原样提交（"****" 前缀）= 未修改，后端保现值。
    setEnvText(linesOfEntries(m?.env));
    setHeadersText(linesOfEntries(m?.headers));
    setEnabled(m?.enabled ?? true);
    setRequireApproval(m?.require_approval ?? true);
    setTimeoutSeconds(String(m?.timeout_seconds ?? 120));
    setFormError("");
  };

  // open 时按 editing 重置（react-router Tabs 之下 Dialog 保持挂载）。
  const [wasOpen, setWasOpen] = useState(false);
  if (open && !wasOpen) {
    reset(editing);
    setWasOpen(true);
  } else if (!open && wasOpen) {
    setWasOpen(false);
  }

  function parseEntries(text: string): Record<string, string> | undefined {
    const entries: Record<string, string> = {};
    for (const line of text.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      const idx = trimmed.indexOf("=");
      if (idx <= 0) {
        setFormError(t("mcp.entryFormatError"));
        return undefined;
      }
      entries[trimmed.slice(0, idx).trim()] = trimmed.slice(idx + 1).trim();
    }
    return entries;
  }

  function submit() {
    setFormError("");
    if (!name.trim()) {
      setFormError(t("mcp.nameRequired"));
      return;
    }
    if (transport === "stdio" && !command.trim()) {
      setFormError(t("mcp.commandRequired"));
      return;
    }
    if (transport === "http" && !url.trim()) {
      setFormError(t("mcp.urlRequired"));
      return;
    }
    const env = parseEntries(envText);
    if (env === undefined) return;
    const headers = parseEntries(headersText);
    if (headers === undefined) return;
    const timeout = Number.parseInt(timeoutSeconds, 10);
    const body = {
      name: name.trim(),
      transport,
      command: transport === "stdio" ? command.trim() : undefined,
      args: transport === "stdio"
        ? args.split(/\s+/).filter(Boolean)
        : undefined,
      url: transport === "http" ? url.trim() : undefined,
      headers: transport === "http" && headers ? headers : undefined,
      env,
      enabled,
      require_approval: requireApproval,
      timeout_seconds: Number.isFinite(timeout) && timeout > 0 ? timeout : undefined,
    };
    const onSaved = (serverName: string, isEdit: boolean) => {
      toast.success(isEdit ? `${t("toast.mcpUpdated")}: ${serverName}` : `${t("toast.mcpCreated")}: ${serverName}`);
      onOpenChange(false);
    };
    if (editing) {
      update.mutate(
        { id: editing.id, patch: body },
        {
          onSuccess: () => onSaved(editing.name, true),
          onError: (err: Error) => toast.error(`${t("toast.mcpFailed")}: ${err.message}`),
        },
      );
    } else {
      create.mutate(body, {
        onSuccess: (m) => onSaved(m.name, false),
        onError: (err: Error) => toast.error(`${t("toast.mcpFailed")}: ${err.message}`),
      });
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>{editing ? t("mcp.editTitle") : t("mcp.createTitle")}</DialogTitle>
          <DialogDescription>{t("mcp.formDescription")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="mcp-name">{t("mcp.fieldName")}</Label>
            <Input id="mcp-name" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="mcp-transport">{t("mcp.fieldTransport")}</Label>
            <select
              id="mcp-transport"
              value={transport}
              onChange={(e) => setTransport(e.target.value === "http" ? "http" : "stdio")}
              disabled={!!editing}
              className="h-9 w-full rounded-md border border-border bg-card px-3 text-sm"
            >
              <option value="stdio">stdio</option>
              <option value="http">http</option>
            </select>
            {editing ? <p className="text-[11px] text-muted-foreground">{t("mcp.transportLocked")}</p> : null}
          </div>
          {transport === "stdio" ? (
            <>
              <div className="space-y-1.5">
                <Label htmlFor="mcp-command">{t("mcp.fieldCommand")}</Label>
                <Input id="mcp-command" value={command} onChange={(e) => setCommand(e.target.value)} placeholder="npx" className="font-mono" />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="mcp-args">{t("mcp.fieldArgs")}</Label>
                <Input id="mcp-args" value={args} onChange={(e) => setArgs(e.target.value)} placeholder={"-y @example/recon-mcp"} className="font-mono" />
              </div>
            </>
          ) : (
            <div className="space-y-1.5">
              <Label htmlFor="mcp-url">{t("mcp.fieldUrl")}</Label>
              <Input id="mcp-url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://mcp.example.com/sse" className="font-mono" />
            </div>
          )}
          {transport === "http" ? (
            <div className="space-y-1.5">
              <Label htmlFor="mcp-headers">{t("mcp.fieldHeaders")}</Label>
              <Textarea id="mcp-headers" rows={2} value={headersText} onChange={(e) => setHeadersText(e.target.value)} placeholder={"Authorization=****abcd1234"} className="font-mono" />
            </div>
          ) : null}
          <div className="space-y-1.5">
            <Label htmlFor="mcp-env">{t("mcp.fieldEnv")}</Label>
            <Textarea id="mcp-env" rows={2} value={envText} onChange={(e) => setEnvText(e.target.value)} placeholder={"API_TOKEN=****abcd1234"} className="font-mono" />
            <p className="text-[11px] text-muted-foreground">{t("mcp.secretHint")}</p>
          </div>
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-1.5">
              <Label htmlFor="mcp-timeout">{t("mcp.fieldTimeout")}</Label>
              <Input id="mcp-timeout" type="number" min={5} max={600} value={timeoutSeconds} onChange={(e) => setTimeoutSeconds(e.target.value)} className="tabular-nums" />
            </div>
            <div className="flex items-end gap-6 pb-1">
              <label className="flex items-center gap-2 text-sm">
                <Switch checked={enabled} onCheckedChange={setEnabled} />
                {t("mcp.fieldEnabled")}
              </label>
              <label className="flex items-center gap-2 text-sm">
                <Switch checked={requireApproval} onCheckedChange={setRequireApproval} />
                {t("mcp.fieldApproval")}
              </label>
            </div>
          </div>
          {formError ? <p className="text-xs text-red-600 dark:text-red-400">{formError}</p> : null}
        </div>
        <DialogFooter>
          <Button variant="outline" size="sm" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button size="sm" onClick={submit} disabled={create.isPending || update.isPending}>
            {editing ? t("common.save") : t("common.create")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function McpDeleteDialog({ target, onClear }: { target: McpServer | null; onClear: () => void }) {
  const { t } = useI18n();
  const del = useDeleteMcpServer();
  return (
    <AlertDialog open={!!target} onOpenChange={(v) => !v && onClear()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("mcp.deleteTitle")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("mcp.deleteDescriptionPrefix")} “{target?.name}”.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            onClick={() => {
              if (!target) return;
              del.mutate(target.id, {
                onSuccess: () => toast.success(t("toast.mcpDeleted")),
                onError: (err: Error) => toast.error(`${t("toast.mcpFailed")}: ${err.message}`),
              });
              onClear();
            }}
          >
            {t("common.delete")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

/** KEY=VALUE 多行文本 ↔ 记录（顺序稳定，输出可再编辑）。 */
function linesOfEntries(entries?: Record<string, string>): string {
  if (!entries) return "";
  return Object.entries(entries)
    .map(([k, v]) => `${k}=${v}`)
    .join("\n");
}
