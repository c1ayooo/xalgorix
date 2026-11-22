// Run 文件浏览器 —— 后端 scan_files.go 的前端（只读）。
//
// 左侧：当前目录的单层列表（后端保证 dirs-first；目录懒展开，不递归）。
// 右侧：选中文件的预览 —— 图片走 /files/preview（<img> 直连），文本走
// /files/content（JSON 内联，二进制由后端标记），二进制/超限给出下载卡。
// 工具行：面包屑 + 当前目录 ZIP 导出 + 刷新；扫描运行中当前目录每 5s 轮询。
// 已知系统产物（scan.json / ledger.json / tool-outputs …）显示 i18n 友好名，
// 原始名始终以 mono 为主文本 —— 机器数据可扫读优先。
import { useEffect, useState } from "react";
import {
  ChevronRight,
  Download,
  File as FileIcon,
  FileArchive,
  FileText,
  Folder,
  Image as ImageIcon,
  Link2,
  RefreshCw,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState, ErrorState } from "@/components/states";
import { useI18n } from "@/i18n";
import { api } from "@/api/client";
import { useScanFileContent, useScanFiles } from "@/api/queries";
import { cn } from "@/lib/utils";
import type { ScanFileEntry } from "@/types/api";

// 已知系统产物的友好名（i18n key）。只覆盖 Run 目录的固定输出，
// 扫描器/代理随手写的文件保持原名 —— 不给未知的名字编造语义。
const KNOWN_LABEL_KEYS: Record<string, string> = {
  "scan.json": "scanFiles.known.scanJson",
  "scan.meta.json": "scanFiles.known.scanMeta",
  "events.jsonl": "scanFiles.known.events",
  "ledger.json": "scanFiles.known.ledger",
  "domain_journal.jsonl": "scanFiles.known.domainJournal",
  "coverage.json": "scanFiles.known.coverage",
  "token-usage.jsonl": "scanFiles.known.tokenRecords",
  "token-usage.json": "scanFiles.known.tokenSummary",
  "tool-outputs": "scanFiles.known.toolOutputs",
  tmp: "scanFiles.known.tmp",
  "findings.json": "scanFiles.known.findings",
  "execution.json": "scanFiles.known.execution",
  "notes.json": "scanFiles.known.notes",
  "advisory-replay": "scanFiles.known.advisoryReplay",
  "session.json": "scanFiles.known.browserSession",
};

// 与后端 preview 白名单一致的客户端判断：决定走 <img> 还是文本读取。
const IMAGE_EXTS = new Set(["png", "jpg", "jpeg", "gif", "webp", "bmp"]);

function extOf(name: string): string {
  const i = name.lastIndexOf(".");
  return i >= 0 ? name.slice(i + 1).toLowerCase() : "";
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = bytes / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 100 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

function formatTime(ms: number): string {
  if (!ms) return "";
  return new Date(ms).toLocaleString();
}

function joinPath(dir: string, name: string): string {
  return dir ? `${dir}/${name}` : name;
}

function entryIcon(entry: ScanFileEntry) {
  if (entry.type === "dir") return <Folder className="h-3.5 w-3.5 text-muted-foreground" />;
  if (entry.type === "link") return <Link2 className="h-3.5 w-3.5 text-muted-foreground" />;
  if (IMAGE_EXTS.has(extOf(entry.name)))
    return <ImageIcon className="h-3.5 w-3.5 text-muted-foreground" />;
  if (/\.(json|jsonl|txt|log|md|csv|xml|yaml|yml|sh|py|go)$/.test(entry.name))
    return <FileText className="h-3.5 w-3.5 text-muted-foreground" />;
  return <FileIcon className="h-3.5 w-3.5 text-muted-foreground" />;
}

export function ScanFilesBrowser({
  scanId,
  running,
}: {
  scanId: string;
  running: boolean;
}) {
  const { t } = useI18n();
  const [dir, setDir] = useState("");
  const [selected, setSelected] = useState<string | null>(null);
  // 图片加载失败（扩展名在但内容坏了）时退回下载卡，而不是裂图。
  const [imageFailed, setImageFailed] = useState(false);

  const listing = useScanFiles(scanId, dir, running);
  const content = useScanFileContent(scanId, selected);

  useEffect(() => {
    setImageFailed(false);
  }, [selected]);

  const segments = dir ? dir.split("/") : [];
  const openDir = (path: string) => {
    setDir(path);
    setSelected(null);
  };
  const selectedName = selected ? selected.split("/").pop() ?? "" : "";
  const selectedIsImage = selected !== null && IMAGE_EXTS.has(extOf(selectedName));

  const entries = listing.data?.entries ?? [];

  return (
    <div className="overflow-hidden rounded-md border border-border bg-card">
      {/* 工具行：面包屑 + 条目数 + 刷新 + 当前目录导出 */}
      <div className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2">
        <nav
          aria-label={t("scanFiles.breadcrumb")}
          className="flex min-w-0 items-center gap-0.5 overflow-hidden font-mono text-xs"
        >
          <button
            onClick={() => openDir("")}
            className="shrink-0 text-muted-foreground transition-colors hover:text-foreground"
          >
            {t("scanFiles.root")}
          </button>
          {segments.map((seg, i) => (
            <span key={i} className="flex min-w-0 items-center">
              <ChevronRight className="h-3 w-3 shrink-0 text-muted-foreground/60" />
              <button
                onClick={() => openDir(segments.slice(0, i + 1).join("/"))}
                className={cn(
                  "truncate transition-colors hover:text-foreground",
                  i === segments.length - 1
                    ? "text-foreground"
                    : "text-muted-foreground",
                )}
              >
                {seg}
              </button>
            </span>
          ))}
        </nav>
        <div className="ml-auto flex items-center gap-1">
          {!listing.isLoading && (
            <span className="font-mono text-xs tabular-nums text-muted-foreground">
              <span className="text-foreground">{entries.length}</span>{" "}
              {t("scanFiles.itemCount")}
            </span>
          )}
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7"
            onClick={() => listing.refetch()}
            title={t("scanFiles.refresh")}
          >
            <RefreshCw
              className={cn("h-3.5 w-3.5", listing.isFetching && "animate-pulse")}
            />
            <span className="sr-only">{t("scanFiles.refresh")}</span>
          </Button>
          <Button variant="ghost" size="sm" className="h-7 gap-1.5 px-2" asChild>
            <a
              href={api.scanFilesArchiveUrl(scanId, dir)}
              title={t("scanFiles.exportDir")}
            >
              <FileArchive className="h-3.5 w-3.5" />
              <span className="hidden sm:inline">{t("scanFiles.exportDir")}</span>
            </a>
          </Button>
        </div>
      </div>

      <div className="grid md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        {/* 左：目录列表 */}
        <div className="min-h-0 max-h-[420px] overflow-y-auto border-b border-border md:border-b-0 md:border-r">
          {listing.isLoading ? (
            <div className="space-y-1 p-3">
              {Array.from({ length: 6 }).map((_, i) => (
                <Skeleton key={i} className="h-7 w-full" />
              ))}
            </div>
          ) : listing.isError ? (
            <div className="p-3">
              <ErrorState description={t("scanFiles.loadFailed")} />
            </div>
          ) : entries.length === 0 ? (
            <div className="p-3">
              <EmptyState
                title={t("scanFiles.emptyTitle")}
                description={t("scanFiles.emptyHint")}
              />
            </div>
          ) : (
            <div className="py-1">
              {entries.map((entry) => {
                const path = joinPath(dir, entry.name);
                const openable = entry.type === "dir" || entry.type === "file";
                const labelKey = KNOWN_LABEL_KEYS[entry.name];
                return (
                  <button
                    key={entry.name}
                    disabled={!openable}
                    onClick={() => {
                      if (entry.type === "dir") openDir(path);
                      else setSelected(path);
                    }}
                    className={cn(
                      "flex w-full items-center gap-2 px-3 py-1.5 text-left",
                      openable
                        ? "transition-colors hover:bg-accent/40"
                        : "cursor-default",
                      selected === path && "bg-accent/40",
                    )}
                  >
                    {entryIcon(entry)}
                    <span className="truncate font-mono text-xs text-foreground">
                      {entry.name}
                    </span>
                    {labelKey && (
                      <span className="shrink-0 text-[10px] text-muted-foreground">
                        {t(labelKey)}
                      </span>
                    )}
                    {entry.type === "link" && (
                      <span className="shrink-0 text-[10px] text-muted-foreground">
                        {t("scanFiles.symlink")}
                      </span>
                    )}
                    <span className="ml-auto shrink-0 font-mono text-xs tabular-nums text-muted-foreground">
                      {entry.type === "file" ? formatSize(entry.size) : ""}
                    </span>
                  </button>
                );
              })}
              {listing.data?.truncated && (
                <p className="border-t border-border px-3 py-1.5 text-[10px] text-muted-foreground">
                  {t("scanFiles.listTruncated")}
                </p>
              )}
            </div>
          )}
        </div>

        {/* 右：预览 */}
        <div className="min-h-0 max-h-[420px] overflow-y-auto p-3">
          {!selected ? (
            <div className="flex h-full min-h-24 items-center justify-center">
              <p className="text-xs text-muted-foreground">
                {t("scanFiles.selectHint")}
              </p>
            </div>
          ) : selectedIsImage && !imageFailed ? (
            <figure className="space-y-2">
              <PreviewHeader
                name={selectedName}
                meta={formatTime(content.data?.mtime ?? 0)}
                downloadUrl={api.scanFileDownloadUrl(scanId, selected)}
                downloadLabel={t("scanFiles.download")}
              />
              <img
                src={api.scanFilePreviewUrl(scanId, selected)}
                alt={selectedName}
                loading="lazy"
                onError={() => setImageFailed(true)}
                className="max-h-[340px] w-auto max-w-full rounded-md border border-border"
              />
            </figure>
          ) : content.isLoading ? (
            <div className="space-y-2">
              <Skeleton className="h-5 w-1/2" />
              {Array.from({ length: 8 }).map((_, i) => (
                <Skeleton key={i} className="h-4 w-full" />
              ))}
            </div>
          ) : content.isError ? (
            <ErrorState
              title={t("scanFiles.previewFailed")}
              description={t("scanFiles.binaryHint")}
            />
          ) : content.data?.binary || imageFailed ? (
            <div className="flex h-full min-h-24 flex-col items-center justify-center gap-3 text-center">
              <FileIcon className="h-6 w-6 text-muted-foreground" />
              <div>
                <p className="font-mono text-xs text-foreground">{selectedName}</p>
                <p className="mt-1 text-xs text-muted-foreground">
                  {t("scanFiles.binaryHint")}
                  {content.data && ` · ${formatSize(content.data.size)}`}
                </p>
              </div>
              <Button variant="outline" size="sm" className="gap-1.5" asChild>
                <a href={api.scanFileDownloadUrl(scanId, selected)}>
                  <Download className="h-3.5 w-3.5" />
                  {t("scanFiles.download")}
                </a>
              </Button>
            </div>
          ) : (
            <div className="space-y-2">
              <PreviewHeader
                name={selectedName}
                meta={
                  [
                    content.data ? formatSize(content.data.size) : "",
                    formatTime(content.data?.mtime ?? 0),
                  ]
                    .filter(Boolean)
                    .join(" · ")
                }
                downloadUrl={api.scanFileDownloadUrl(scanId, selected)}
                downloadLabel={t("scanFiles.download")}
              />
              <pre className="max-h-[300px] overflow-auto whitespace-pre-wrap break-all rounded-md bg-muted/50 p-3 font-mono text-xs leading-relaxed">
                {content.data?.content ?? ""}
              </pre>
              {content.data?.truncated && (
                <p className="text-[10px] text-muted-foreground">
                  {t("scanFiles.contentTruncated")}
                </p>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function PreviewHeader({
  name,
  meta,
  downloadUrl,
  downloadLabel,
}: {
  name: string;
  meta: string;
  downloadUrl: string;
  downloadLabel: string;
}) {
  return (
    <div className="flex items-center gap-2">
      <div className="min-w-0 flex-1">
        <p className="truncate font-mono text-xs text-foreground">{name}</p>
        {meta && (
          <p className="font-mono text-[10px] tabular-nums text-muted-foreground">
            {meta}
          </p>
        )}
      </div>
      <Button variant="ghost" size="icon" className="h-7 w-7" asChild>
        <a href={downloadUrl} title={downloadLabel}>
          <Download className="h-3.5 w-3.5" />
          <span className="sr-only">{downloadLabel}</span>
        </a>
      </Button>
    </div>
  );
}
