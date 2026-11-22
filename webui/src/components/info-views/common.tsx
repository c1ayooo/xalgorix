import { Database } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState, ErrorState } from "@/components/states";
import { HttpError } from "@/api/client";
import { useI18n } from "@/i18n";

/**
 * Shared four-state vocabulary for the information-space views. The cold path
 * being absent (503 / available=false) is a *normal* degraded mode, not a
 * failure: it renders the disabled empty state and never a red error.
 */
export function isColdPathDisabled(error: unknown): boolean {
  return error instanceof HttpError && error.status === 503;
}

export function InfoNotice({
  truncated,
  incomplete,
  gapReason,
}: {
  truncated: boolean;
  incomplete: boolean;
  gapReason?: string;
}) {
  const { t } = useI18n();
  if (!truncated && !incomplete) return null;
  return (
    <div className="rounded-sm border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300">
      {truncated && <span>{t("info.truncated")}</span>}
      {truncated && incomplete && <span> · </span>}
      {incomplete && (
        <span>
          {t("info.incomplete")}
          {gapReason ? `: ${gapReason}` : ""}
        </span>
      )}
    </div>
  );
}

export function InfoDisabled() {
  const { t } = useI18n();
  return (
    <EmptyState
      icon={<Database className="h-6 w-6" />}
      title={t("info.disabled.title")}
      description={t("info.disabled.description")}
    />
  );
}

export function InfoQueryError({
  error,
  onRetry,
}: {
  error: unknown;
  onRetry: () => void;
}) {
  const { t } = useI18n();
  if (isColdPathDisabled(error)) return <InfoDisabled />;
  return (
    <ErrorState
      title={t("info.error.title")}
      description={error instanceof Error ? error.message : t("info.error.description")}
      action={
        <Button size="sm" variant="outline" onClick={onRetry}>
          {t("common.retry")}
        </Button>
      }
    />
  );
}

export function InfoLoading({ rows = 3 }: { rows?: number }) {
  return (
    <div className="space-y-3">
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} className="h-16 w-full" />
      ))}
    </div>
  );
}

export function InfoToolbarCard({ children }: { children: React.ReactNode }) {
  return (
    <Card className="rounded-md">
      <CardContent className="flex flex-wrap items-center gap-2 p-4">
        {children}
      </CardContent>
    </Card>
  );
}

export function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <h2 className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
      {children}
    </h2>
  );
}
