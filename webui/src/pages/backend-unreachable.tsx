import { useState } from "react";
import { AlertTriangle, RotateCw, Terminal } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/store/auth";
import { useI18n } from "@/i18n";

/**
 * Rendered by `AuthBootstrap` when `/api/auth/status` returns either a
 * network-level failure (HttpError.status === 0) or 404 — i.e. there is
 * no Xalgorix backend on the other side of `/api/*`. The login screen
 * would be misleading here (signing in would just produce another 404),
 * so we replace the entire app with a remediation page that:
 *
 *   - Names the cause in plain language.
 *   - Shows the raw probe error so a developer can copy/paste it.
 *   - Offers a "Retry" button that re-probes `/api/auth/status` without
 *     a full page reload, so an operator who just started the Go server
 *     in another shell can recover instantly.
 *   - Lists the most common fixes (run `xalgorix serve` /
 *     run the mock backend).
 */
export function BackendUnreachable() {
  const { t } = useI18n();
  const refresh = useAuth((s) => s.refresh);
  const probeError = useAuth((s) => s.probeError);
  const [retrying, setRetrying] = useState(false);

  async function onRetry() {
    setRetrying(true);
    try {
      await refresh();
    } finally {
      setRetrying(false);
    }
  }

  return (
    <div className="grid min-h-screen w-full place-items-center bg-background p-6">
      <div className="w-full max-w-lg">
        <Card>
          <CardHeader>
            <div className="flex items-center gap-3">
              <div className="grid h-9 w-9 place-items-center rounded-md border border-destructive/40 bg-destructive/10 text-destructive">
                <AlertTriangle className="h-4 w-4" />
              </div>
              <div>
                <CardTitle className="text-balance">{t("unreachable.title")}</CardTitle>
                <CardDescription>
                  {t("unreachable.subtitle")}
                </CardDescription>
              </div>
            </div>
          </CardHeader>
          <CardContent className="space-y-5">
            <p className="text-sm leading-relaxed text-muted-foreground">
              {t("unreachable.bodyBefore")}{" "}
              <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">
                /api/auth/status
              </code>{" "}
              {t("unreachable.bodyAfter")}
            </p>

            {probeError && (
              <div className="rounded-md border border-border bg-muted/40 p-3">
                <div className="mb-1 flex items-center gap-2 text-xs font-medium uppercase tracking-wider text-muted-foreground">
                  <Terminal className="h-3.5 w-3.5" />
                  {t("unreachable.probeError")}
                </div>
                <pre className="overflow-x-auto whitespace-pre-wrap break-words font-mono text-xs text-foreground">
                  {probeError}
                </pre>
              </div>
            )}

            <div className="space-y-2">
              <h3 className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
                {t("unreachable.commonFixes")}
              </h3>
              <ul className="space-y-1.5 text-sm leading-relaxed text-foreground">
                <li className="flex gap-2">
                  <span aria-hidden className="text-muted-foreground">
                    1.
                  </span>
                  <span>
                    {t("unreachable.fixServeBefore")}{" "}
                    <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">
                      go run ./cmd/xalgorix serve
                    </code>
                  </span>
                </li>
                <li className="flex gap-2">
                  <span aria-hidden className="text-muted-foreground">
                    2.
                  </span>
                  <span>
                    {t("unreachable.fixMockBefore")}{" "}
                    <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">
                      node webui/mock-backend.mjs
                    </code>
                  </span>
                </li>
                <li className="flex gap-2">
                  <span aria-hidden className="text-muted-foreground">
                    3.
                  </span>
                  <span>
                    {t("unreachable.fixTargetBefore")}{" "}
                    <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">
                      VITE_API_TARGET=http://&lt;host&gt;:&lt;port&gt;
                    </code>
                  </span>
                </li>
              </ul>
            </div>

            <div className="flex items-center gap-2 border-t border-border pt-4">
              <Button onClick={onRetry} disabled={retrying} className="gap-2">
                <RotateCw
                  className={`h-4 w-4 ${retrying ? "animate-spin" : ""}`}
                  aria-hidden
                />
                {retrying ? t("unreachable.retrying") : t("unreachable.retry")}
              </Button>
              <p className="text-xs text-muted-foreground">
                {t("unreachable.retryHint")}
              </p>
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
