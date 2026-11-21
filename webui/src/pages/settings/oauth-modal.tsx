// OAuthModal — handles all three `mode` shapes returned by
// POST /api/auth/profiles/oauth/start (loopback / device / paste)
// for the provider-catalog-and-oauth feature.
//
// The component opens, immediately calls the start endpoint with
// the provider's id, then renders the matching mode-specific UI:
//
//   • loopback — opens authURL in a new tab and polls
//     /api/auth/profiles, completing when a new OAuth profile
//     for this provider appears (the loopback callback handler
//     in internal/auth/driver_pkce.go finalizes the flow on the
//     server side).
//
//   • device — shows the user_code + verification_uri (with a
//     clickable link). The device-code driver's background poller
//     finalizes the profile; the dashboard observes the same
//     /api/auth/profiles endpoint and closes the modal when the
//     new profile shows up.
//
//   • paste — shows authURL plus a textarea where the operator
//     pastes the authorization code returned by the browser.
//     Submitting the textarea posts to
//     POST /api/auth/profiles/oauth/complete with the flowId,
//     code, and state from the start response.
//
// Validates: Requirements 5.1, 5.2, 14.1, 14.2, 14.5.
import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Input } from "@/components/ui/input";
import { ExternalLink } from "lucide-react";
import { HttpError } from "@/api/client";
import { useI18n } from "@/i18n";
import {
  useAuthProfiles,
  useOAuthComplete,
  useOAuthStart,
} from "@/api/queries";
import type { OAuthStartResponse } from "@/types/api";

interface OAuthModalProps {
  open: boolean;
  provider: string;
  displayName: string;
  // Existing profile keys at the time the modal opens. The polling
  // loop watches for any provider:profileId key that wasn't in this
  // set so a new OAuth handshake completes the modal.
  existingKeys: string[];
  onClose: () => void;
}

function errorMessage(err: unknown, fallback: string): string {
  if (err instanceof HttpError) {
    const data = err.data as { error?: string } | null | undefined;
    if (data?.error) return data.error;
    return err.message;
  }
  if (err instanceof Error) return err.message;
  return fallback;
}

export default function OAuthModal({
  open,
  provider,
  displayName,
  existingKeys,
  onClose,
}: OAuthModalProps) {
  const { t } = useI18n();
  const start = useOAuthStart();
  const complete = useOAuthComplete();
  const profiles = useAuthProfiles();

  const [startResult, setStartResult] = useState<OAuthStartResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [profileId, setProfileId] = useState("default");
  const [code, setCode] = useState("");
  const [state, setState] = useState("");
  // Snapshot of the existing keys at modal open. Captured in a ref
  // so the polling effect doesn't restart every time the parent's
  // memoized array changes identity.
  const baselineRef = useRef<Set<string>>(new Set(existingKeys));

  // Reset transient state every time the modal opens so the previous
  // session's start result and inputs don't bleed into the next one.
  useEffect(() => {
    if (!open) return;
    setStartResult(null);
    setError(null);
    setCode("");
    setState("");
    setProfileId("default");
    baselineRef.current = new Set(existingKeys);
  }, [open, existingKeys]);

  // Kick off the flow as soon as the modal becomes visible. Using a
  // ref guard prevents StrictMode double-invocation in dev from
  // firing two start calls at once (we only want one ephemeral
  // listener / one device-code request per modal open).
  const startedRef = useRef(false);
  useEffect(() => {
    if (!open) {
      startedRef.current = false;
      return;
    }
    if (startedRef.current) return;
    startedRef.current = true;
    setError(null);
    start
      .mutateAsync({ provider, profileId, preferPaste: false })
      .then((res) => setStartResult(res))
      .catch((err) => setError(errorMessage(err, t("common.somethingWrong"))));
    // We deliberately omit `start` and `profileId` from the deps —
    // the start call is keyed off `open` + `provider` only. The
    // operator-tweakable profileId is sent with the initial start
    // and the result already encodes whatever flowId the server
    // generated, so further changes to the input shouldn't refire.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, provider]);

  // Poll the profile list for completion. The PKCE loopback callback
  // and the device-code background poller both eventually persist
  // the new profile — when it shows up in the list we close.
  // H10: enforce the server-supplied expiresAt deadline so the
  // modal stops polling and surfaces a "flow expired" state
  // instead of running forever when the operator never completes
  // the flow.
  const [expired, setExpired] = useState(false);
  useEffect(() => {
    if (!open) return;
    if (!startResult) return;
    if (startResult.mode === "paste") return; // paste flow finalizes via mutateComplete

    const deadline = startResult.expiresAt
      ? new Date(startResult.expiresAt).getTime()
      : Number.POSITIVE_INFINITY;

    let stopped = false;
    let timeoutId: number | undefined;
    const tick = () => {
      if (stopped) return;
      if (Date.now() > deadline) {
        setExpired(true);
        return;
      }
      profiles.refetch();
      timeoutId = window.setTimeout(tick, 2500);
    };
    timeoutId = window.setTimeout(tick, 2500);
    return () => {
      stopped = true;
      if (timeoutId !== undefined) {
        window.clearTimeout(timeoutId);
      }
    };
  }, [open, startResult, profiles]);

  // Reset the expired flag when the modal opens or the start
  // result changes (e.g., the operator clicked "Try again").
  useEffect(() => {
    if (!open) return;
    setExpired(false);
  }, [open, startResult]);

  // Retry the start flow when the operator clicks the retry
  // button on the expired-state UI.
  function onRetryStart() {
    setError(null);
    setExpired(false);
    setStartResult(null);
    startedRef.current = false;
    start
      .mutateAsync({ provider, profileId, preferPaste: false })
      .then((res) => setStartResult(res))
      .catch((err) => setError(errorMessage(err, t("common.somethingWrong"))));
  }

  // Detect the newly persisted profile. Once we see a key for this
  // provider that wasn't present at modal open, close the modal.
  useEffect(() => {
    if (!open) return;
    if (!startResult) return;
    if (startResult.mode === "paste") return;
    const list = profiles.data ?? [];
    const baseline = baselineRef.current;
    const newKey = list.find((p) => {
      if (p.provider !== provider) return false;
      const k = `${p.provider}:${p.profileId}`;
      return !baseline.has(k);
    });
    if (newKey) {
      onClose();
    }
  }, [open, startResult, profiles.data, provider, onClose]);

  // Title translated at render time; displayName is data, the frame is chrome.
  const heading = `${t("oauth.titleBefore")}${displayName}${t("oauth.titleAfter")}`;

  async function onSubmitPaste(e: FormEvent) {
    e.preventDefault();
    if (!startResult) return;
    setError(null);
    try {
      await complete.mutateAsync({
        provider,
        flowId: startResult.flowId,
        code: code.trim(),
        state: state.trim() || undefined,
      });
      onClose();
    } catch (err) {
      setError(errorMessage(err, t("common.somethingWrong")));
    }
  }

  async function onSubmitSetupToken(e: FormEvent) {
    e.preventDefault();
    if (!startResult) return;
    setError(null);
    try {
      await complete.mutateAsync({
        provider,
        flowId: startResult.flowId,
        setupToken: code.trim(),
      });
      onClose();
    } catch (err) {
      setError(errorMessage(err, t("common.somethingWrong")));
    }
  }

  // confirm-and-import flows (claude_cli_reuse / codex_cli_reuse):
  // the credential file is already on disk, so Complete takes no
  // operator input — clicking Import just posts the flowId and the
  // driver reads the file directly.
  async function onSubmitImport() {
    if (!startResult) return;
    setError(null);
    try {
      await complete.mutateAsync({
        provider,
        flowId: startResult.flowId,
      });
      onClose();
    } catch (err) {
      setError(errorMessage(err, t("common.somethingWrong")));
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
    >
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>{heading}</DialogTitle>
          <DialogDescription>
            {t("oauth.descBefore")}
            <span className="ml-1 font-mono">
              {provider}:{profileId || "default"}
            </span>
            {t("oauth.descAfter")}
          </DialogDescription>
        </DialogHeader>

        {!startResult && !error && (
          <div className="rounded-md border border-border bg-muted/40 p-4 text-sm text-muted-foreground">
            {t("oauth.starting")}
          </div>
        )}

        {error && (
          <div className="space-y-1 rounded-md border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive">
            <p>{error}</p>
            {provider === "codex" &&
              error === "codex cli credentials not found" && (
                <p className="text-xs text-muted-foreground">
                  Run <code className="font-mono">codex login</code> as the
                  Xalgorix service user, or make the host file
                  <code className="mx-1 font-mono">~/.codex/auth.json</code>
                  available at
                  <code className="ml-1 font-mono">
                    /root/.codex/auth.json
                  </code>
                  .
                </p>
              )}
          </div>
        )}

        {expired && (
          <div className="space-y-3 rounded-md border border-amber-500/30 bg-amber-500/10 p-4 text-sm text-amber-900 dark:text-amber-200">
            <p className="font-medium">{t("oauth.expired.title")}</p>
            <p className="text-xs">
              {t("oauth.expired.desc")}
            </p>
            <div className="flex justify-end">
              <Button
                size="sm"
                onClick={onRetryStart}
                disabled={start.isPending}
              >
                {start.isPending ? t("oauth.startingShort") : t("common.retry")}
              </Button>
            </div>
          </div>
        )}

        {!expired && startResult?.mode === "loopback" && (
          <LoopbackBody startResult={startResult} />
        )}

        {!expired && startResult?.mode === "device" && (
          <DeviceBody startResult={startResult} />
        )}

        {/*
          H9: dispatch the paste-mode body purely on submode so
          the dashboard never has to infer flow shape from
          (mode, authURL) heuristics. Older servers omit submode
          entirely; we treat undefined / "" as the
          claude_cli_reuse confirm-and-import shape (the
          "no-input field" path), and the typed enum makes the
          two operator-supplied input flows ("paste_code" /
          "setup_token") explicit.
        */}
        {!expired && startResult?.mode === "paste" && startResult.submode === "paste_code" && (
          <PasteBody
            startResult={startResult}
            code={code}
            setCode={setCode}
            stateValue={state}
            setStateValue={setState}
            onSubmit={onSubmitPaste}
            submitting={complete.isPending}
          />
        )}

        {!expired && startResult?.mode === "paste" && startResult.submode === "setup_token" && (
          <SetupTokenBody
            code={code}
            setCode={setCode}
            onSubmit={onSubmitSetupToken}
            submitting={complete.isPending}
          />
        )}

        {/*
          The empty / unspecified Submode is the claude_cli_reuse
          confirm-and-import path. Today the dashboard renders
          this as the same paste body the PKCE OOB flow uses when
          authURL is non-empty, and as a no-op confirm pane when
          there is no authURL. Older servers (pre-H9) that route
          PKCE paste-fallback through here without populating
          submode still work because the branch above checks for
          "paste_code" explicitly.
        */}
        {!expired &&
          startResult?.mode === "paste" &&
          !startResult.submode &&
          startResult.authURL && (
            <PasteBody
              startResult={startResult}
              code={code}
              setCode={setCode}
              stateValue={state}
              setStateValue={setState}
              onSubmit={onSubmitPaste}
              submitting={complete.isPending}
            />
          )}

        {!expired &&
          startResult?.mode === "paste" &&
          !startResult.submode &&
          !startResult.authURL && (
            <div className="space-y-3 rounded-md border border-border bg-muted/30 p-4 text-sm">
              <p>
                {t("oauth.import.descBefore")}
                <code className="font-mono">
                  {provider}:{profileId || "default"}
                </code>
                {t("oauth.import.descAfter")}
              </p>
              {provider === "codex" && (
                <p className="text-xs text-muted-foreground">
                  {t("oauth.import.expectedFile")}
                  <code className="mx-1 font-mono">~/.codex/auth.json</code>.
                  {t("oauth.import.dockerBefore")}
                  <code className="ml-1 font-mono">
                    /root/.codex/auth.json
                  </code>
                  {t("oauth.import.dockerAfter")}
                </p>
              )}
              <div className="flex justify-end">
                <Button
                  size="sm"
                  onClick={onSubmitImport}
                  disabled={complete.isPending}
                >
                  {complete.isPending ? t("oauth.importing") : t("oauth.import.button")}
                </Button>
              </div>
            </div>
          )}

        <DialogFooter className="border-t border-border pt-3">
          <div className="flex w-full items-center justify-between text-xs text-muted-foreground">
            <span>
              {t("oauth.profileId")}&nbsp;
              <code className="font-mono">{profileId || "default"}</code>
            </span>
            <Button variant="ghost" size="sm" onClick={onClose}>
              {startResult?.mode === "paste" ? t("common.cancel") : t("oauth.close")}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function LoopbackBody({ startResult }: { startResult: OAuthStartResponse }) {
  const { t } = useI18n();
  // Open the authorization URL in a new tab on first render. Doing
  // it lazily inside a useEffect avoids triggering the popup blocker
  // on the very first render before React commits.
  useEffect(() => {
    if (!startResult.authURL) return;
    window.open(startResult.authURL, "_blank", "noopener,noreferrer");
  }, [startResult.authURL]);

  return (
    <div className="space-y-3 rounded-md border border-border bg-muted/30 p-4 text-sm">
      <p>
        {t("oauth.loopback.desc")}
      </p>
      {startResult.authURL && (
        <a
          href={startResult.authURL}
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex items-center gap-1 text-primary underline-offset-2 hover:underline"
        >
          {t("oauth.openAuthPage")} <ExternalLink className="h-3.5 w-3.5" />
        </a>
      )}
      <p className="text-xs text-muted-foreground">
        {t("oauth.loopback.waiting")}
      </p>
    </div>
  );
}

function DeviceBody({ startResult }: { startResult: OAuthStartResponse }) {
  const { t } = useI18n();
  return (
    <div className="space-y-3 rounded-md border border-border bg-muted/30 p-4 text-sm">
      <p>
        {t("oauth.device.desc")}
      </p>
      <div className="grid gap-3 sm:grid-cols-[auto_1fr]">
        <div className="space-y-1">
          <Label className="text-[10px]">{t("oauth.device.userCode")}</Label>
          <code className="block rounded-md border border-border bg-background px-3 py-2 font-mono text-base tracking-widest">
            {startResult.userCode || "—"}
          </code>
        </div>
        <div className="space-y-1">
          <Label className="text-[10px]">{t("oauth.device.verificationUri")}</Label>
          {startResult.verificationURI ? (
            <a
              href={startResult.verificationURI}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 break-all rounded-md border border-border bg-background px-3 py-2 font-mono text-xs text-primary hover:underline"
            >
              {startResult.verificationURI}
              <ExternalLink className="h-3.5 w-3.5 shrink-0" />
            </a>
          ) : (
            <span className="text-xs text-muted-foreground">—</span>
          )}
        </div>
      </div>
      {startResult.expiresAt && (
        <p className="text-xs text-muted-foreground">
          {t("oauth.device.expiresBefore")}{new Date(startResult.expiresAt).toLocaleString()}
        </p>
      )}
    </div>
  );
}

function PasteBody({
  startResult,
  code,
  setCode,
  stateValue,
  setStateValue,
  onSubmit,
  submitting,
}: {
  startResult: OAuthStartResponse;
  code: string;
  setCode: (v: string) => void;
  stateValue: string;
  setStateValue: (v: string) => void;
  onSubmit: (e: FormEvent) => void;
  submitting: boolean;
}) {
  const { t } = useI18n();
  return (
    <form onSubmit={onSubmit} className="space-y-3">
      {startResult.authURL && (
        <div className="rounded-md border border-border bg-muted/30 p-3 text-sm">
          <p className="mb-2 text-xs text-muted-foreground">
            {t("oauth.paste.desc")}
          </p>
          <a
            href={startResult.authURL}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 text-primary underline-offset-2 hover:underline"
          >
            {t("oauth.openAuthPage")} <ExternalLink className="h-3.5 w-3.5" />
          </a>
          <p className="mt-2 text-xs text-muted-foreground">
            {t("oauth.paste.expectedBefore")}
            <code className="font-mono">localhost:1455</code>
            {t("oauth.paste.expectedAfter")}
          </p>
        </div>
      )}
      <div className="space-y-2">
        <Label htmlFor="oauth-paste-code">{t("oauth.paste.urlLabel")}</Label>
        <Textarea
          id="oauth-paste-code"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          rows={3}
          required
          placeholder={t("oauth.paste.urlPlaceholder")}
          className="font-mono text-xs"
        />
        <p className="text-xs text-muted-foreground">
          {t("oauth.paste.urlHint")}
        </p>
      </div>
      <details className="text-xs">
        <summary className="cursor-pointer text-muted-foreground">
          {t("oauth.paste.advanced")}
        </summary>
        <div className="mt-2 space-y-2">
          <Label htmlFor="oauth-paste-state">{t("oauth.paste.stateLabel")}</Label>
          <Input
            id="oauth-paste-state"
            value={stateValue}
            onChange={(e) => setStateValue(e.target.value)}
            placeholder={t("oauth.paste.statePlaceholder")}
            className="font-mono text-xs"
          />
        </div>
      </details>
      <div className="flex justify-end">
        <Button
          type="submit"
          disabled={submitting || code.trim() === ""}
          size="sm"
        >
          {submitting ? t("oauth.paste.completing") : t("oauth.paste.complete")}
        </Button>
      </div>
    </form>
  );
}

function SetupTokenBody({
  code,
  setCode,
  onSubmit,
  submitting,
}: {
  code: string;
  setCode: (v: string) => void;
  onSubmit: (e: FormEvent) => void;
  submitting: boolean;
}) {
  const { t } = useI18n();
  return (
    <form onSubmit={onSubmit} className="space-y-3">
      <div className="space-y-2">
        <Label htmlFor="oauth-setup-token">{t("oauth.setupToken.label")}</Label>
        <Textarea
          id="oauth-setup-token"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          rows={3}
          required
          placeholder={t("oauth.setupToken.placeholder")}
          className="font-mono text-xs"
        />
        <p className="text-xs text-muted-foreground">
          {t("oauth.setupToken.hint")}
        </p>
      </div>
      <div className="flex justify-end">
        <Button
          type="submit"
          disabled={submitting || code.trim() === ""}
          size="sm"
        >
          {submitting ? t("oauth.setupToken.exchanging") : t("oauth.setupToken.exchange")}
        </Button>
      </div>
    </form>
  );
}
