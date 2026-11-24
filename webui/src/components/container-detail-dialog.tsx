import { useMemo } from "react";

import { useInstanceContainer } from "@/api/queries";
import { useI18n } from "@/i18n";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { shortId } from "@/lib/utils";
import type { ScanInstance, ToolpackEntry } from "@/types/api";

/**
 * Read-only container detail for one scan instance.
 *
 * The backend owns the authority split: actual state comes from the container
 * runtime, desired state from PostgreSQL. This dialog only renders that
 * projection plus the image's own toolpack manifest — it never starts, stops,
 * or reconciles anything.
 */
export function ContainerDetailDialog({
  instance,
  onOpenChange,
}: {
  instance: ScanInstance | null;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useI18n();
  const { data, isLoading, error } = useInstanceContainer(instance?.id, !!instance);
  const groups = useMemo(() => groupTools(data?.tools.items ?? []), [data?.tools.items]);

  return (
    <Dialog open={!!instance} onOpenChange={(open) => !open && onOpenChange(false)}>
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
        {instance && (
          <>
            <DialogHeader>
              <DialogTitle className="truncate font-mono text-sm">
                {instance.name || instance.targets || shortId(instance.id)}
              </DialogTitle>
              <DialogDescription className="font-mono text-[11px]">
                {t("instances.container.title")} · {shortId(instance.id, 12)}
              </DialogDescription>
            </DialogHeader>

            {isLoading ? (
              <p className="text-sm text-muted-foreground">{t("instances.container.loading")}</p>
            ) : error ? (
              <div className="space-y-1 text-sm">
                <div className="text-destructive">{t("instances.container.loadError")}</div>
                <div className="text-xs text-muted-foreground">
                  {error instanceof Error ? error.message : t("common.unknownError")}
                </div>
              </div>
            ) : data ? (
              <div className="space-y-5 text-xs">
                <section className="space-y-1.5">
                  <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
                    {t("instances.container.metadata")}
                  </div>
                  <dl className="space-y-1">
                    <Row label={t("instances.container.project")} value={data.project_id} mono />
                    <Row label={t("instances.container.backend")} value={data.backend} mono />
                    <Row
                      label={t("instances.container.actualState")}
                      value={data.actual?.state || "—"}
                      mono
                    />
                    <Row
                      label={t("instances.container.containerId")}
                      value={data.actual?.container_id}
                      mono
                    />
                    <Row label={t("instances.container.image")} value={data.actual?.image} mono />
                    <Row
                      label={t("instances.container.desiredState")}
                      value={data.desired?.desired_state}
                      mono
                    />
                    <Row
                      label={t("instances.container.reconcile")}
                      value={data.desired?.reconcile_status}
                      mono
                    />
                    {data.desired && (
                      <Row
                        label={t("instances.container.generation")}
                        value={`${data.desired.applied_gen}/${data.desired.desired_gen}`}
                        mono
                      />
                    )}
                    <Row
                      label={t("instances.container.profile")}
                      value={data.desired?.profile || data.tools.profile}
                      mono
                    />
                    <Row
                      label={t("instances.container.netProfile")}
                      value={data.desired?.net_profile}
                      mono
                    />
                    <Row
                      label={t("instances.container.imageDigest")}
                      value={data.desired?.image_digest}
                      mono
                    />
                    <Row
                      label={t("instances.container.created")}
                      value={data.desired?.created_at}
                      mono
                    />
                    <Row
                      label={t("instances.container.updated")}
                      value={data.desired?.updated_at}
                      mono
                    />
                  </dl>
                  {!data.available && data.reason && (
                    <p className="text-[11px] text-muted-foreground">{data.reason}</p>
                  )}
                </section>

                {data.desired?.last_error && (
                  <section className="space-y-1">
                    <div className="text-[10px] uppercase tracking-wider text-destructive">
                      {t("instances.container.lastError")}
                    </div>
                    <p className="break-all rounded-md border border-destructive/30 bg-destructive/5 p-2 font-mono text-[11px] text-destructive">
                      {data.desired.last_error}
                    </p>
                  </section>
                )}

                <section className="space-y-1.5">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
                      {t("instances.container.tools")}
                      {data.tools.available ? ` · ${data.tools.items?.length ?? 0}` : ""}
                    </div>
                    {data.tools.available && data.tools.profile && (
                      <Badge variant="outline" className="font-mono text-[10px]">
                        {data.tools.profile}
                      </Badge>
                    )}
                  </div>
                  {!data.tools.available ? (
                    <p className="text-[11px] text-muted-foreground">
                      {t("instances.container.toolsUnavailable")}
                      {data.tools.reason ? ` · ${data.tools.reason}` : ""}
                    </p>
                  ) : groups.length === 0 ? (
                    <p className="text-[11px] text-muted-foreground">
                      {t("instances.container.noTools")}
                    </p>
                  ) : (
                    <div className="space-y-2">
                      {groups.map(([category, tools]) => (
                        <div key={category} className="space-y-1">
                          <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
                            {category}
                          </div>
                          <ul className="divide-y divide-border/60 rounded-md border border-border/60">
                            {tools.map((tool) => (
                              <li
                                key={tool.name}
                                title={tool.purpose}
                                className="flex items-center justify-between gap-3 px-2.5 py-1.5"
                              >
                                <span className="truncate font-mono text-[11px]">{tool.name}</span>
                                <span className="flex min-w-0 items-center gap-1.5 text-[10px] text-muted-foreground">
                                  {tool.capabilities?.map((capability) => (
                                    <Badge key={capability} variant="outline" className="text-[9px]">
                                      {capability}
                                    </Badge>
                                  ))}
                                  <span className="truncate font-mono">{tool.bin}</span>
                                </span>
                              </li>
                            ))}
                          </ul>
                        </div>
                      ))}
                    </div>
                  )}
                  {data.tools.available && (
                    <p className="text-[10px] text-muted-foreground">
                      {t("instances.container.toolsHint")}
                    </p>
                  )}
                </section>
              </div>
            ) : null}
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}

function Row({ label, value, mono }: { label: string; value?: string; mono?: boolean }) {
  if (!value) return null;
  return (
    <div className="flex gap-3">
      <span className="w-24 shrink-0 text-muted-foreground">{label}</span>
      <span className={`min-w-0 break-all ${mono ? "font-mono text-[11px]" : ""}`}>{value}</span>
    </div>
  );
}

function groupTools(tools: ToolpackEntry[]): Array<[string, ToolpackEntry[]]> {
  const groups = new Map<string, ToolpackEntry[]>();
  for (const tool of tools) {
    const category = tool.category || "other";
    const bucket = groups.get(category);
    if (bucket) {
      bucket.push(tool);
    } else {
      groups.set(category, [tool]);
    }
  }
  return Array.from(groups.entries());
}
