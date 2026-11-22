import { useCallback, useEffect, useState } from "react";
import { CheckCircle2, Key, XCircle, Zap, Search } from "lucide-react";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  CardDescription,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";
import { api, type ProviderKeyStatus } from "@/api/client";
import { useI18n } from "@/i18n";

interface ProviderKeysManagerProps {
  className?: string;
}

export default function ProviderKeysManager({ className }: ProviderKeysManagerProps) {
  const { t } = useI18n();

  const [providers, setProviders] = useState<ProviderKeyStatus[]>([]);
  const [configuredCount, setConfiguredCount] = useState(0);
  const [routerEnabled, setRouterEnabled] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);

  // Key input state — keyed by provider_id
  const [keyInputs, setKeyInputs] = useState<Record<string, string>>({});

  // Model test
  const [testModel, setTestModel] = useState("");
  const [testResult, setTestResult] = useState<{
    resolved: boolean;
    provider_id?: string;
    display_name?: string;
    bare_model?: string;
    has_key?: boolean;
    error?: string;
  } | null>(null);
  const [testing, setTesting] = useState(false);

  const fetchKeys = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await api.providerKeys();
      setProviders(data.providers);
      setConfiguredCount(data.configured_count);
      setRouterEnabled(data.router_enabled);
    } catch (err) {
      setError(err instanceof Error ? err.message : t("providerKeys.loadFailed"));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchKeys();
  }, [fetchKeys]);

  function updateKeyInput(providerId: string, value: string) {
    setKeyInputs((prev) => ({ ...prev, [providerId]: value }));
  }

  async function saveKeys() {
    setSaving(true);
    setSaved(false);
    try {
      const keys = Object.entries(keyInputs)
        .filter(([, v]) => v.trim() !== "")
        .map(([providerId, apiKey]) => ({
          provider_id: providerId,
          api_key: apiKey,
        }));

      if (keys.length === 0) return;
      await api.saveProviderKeys(keys);
      setSaved(true);
      setKeyInputs({}); // clear inputs
      await fetchKeys(); // refresh
      setTimeout(() => setSaved(false), 2500);
    } catch (err) {
      setError(err instanceof Error ? err.message : t("providerKeys.saveFailed"));
    } finally {
      setSaving(false);
    }
  }

  async function deleteKey(providerId: string) {
    try {
      await api.deleteProviderKey(providerId);
      await fetchKeys();
    } catch (err) {
      setError(err instanceof Error ? err.message : t("providerKeys.deleteFailed"));
    }
  }

  async function testRoute() {
    if (!testModel.trim()) return;
    setTesting(true);
    setTestResult(null);
    try {
      const result = await api.testModelRoute(testModel.trim());
      setTestResult(result);
    } catch (err) {
      setTestResult({
        resolved: false,
        error: err instanceof Error ? err.message : t("providerKeys.testFailed"),
      });
    } finally {
      setTesting(false);
    }
  }

  if (loading) {
    return (
      <Card className={className}>
        <CardContent className="p-6">
          <div className="flex items-center gap-2 text-muted-foreground">
            <div className="h-4 w-4 animate-spin rounded-full border-2 border-primary border-t-transparent" />
            {t("providerKeys.loading")}
          </div>
        </CardContent>
      </Card>
    );
  }

  if (error && providers.length === 0) {
    return (
      <Card className={className}>
        <CardContent className="p-6">
          <div className="text-sm text-destructive">{error}</div>
          <Button size="sm" variant="outline" onClick={fetchKeys} className="mt-2">
            {t("common.retry")}
          </Button>
        </CardContent>
      </Card>
    );
  }

  // Split into configured and unconfigured
  const configured = providers.filter((p) => p.has_key);
  const unconfigured = providers.filter((p) => !p.has_key);

  return (
    <Card className={className}>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Zap className="h-5 w-5 text-amber-500" />
          {t("providerKeys.title")}
        </CardTitle>
        <CardDescription>
          {t("providerKeys.descBefore")}
          <code className="text-xs">gpt-5.6</code>,{" "}
          <code className="text-xs">claude-fable-5</code>,{" "}
          {t("providerKeys.descOr")}{" "}
          <code className="text-xs">gemini-3.5-flash</code>
          {t("providerKeys.descAfter")}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        {/* Status bar */}
        <div className="flex flex-wrap items-center gap-3">
          <Badge
            variant={routerEnabled ? "default" : "muted"}
            className="gap-1"
          >
            {routerEnabled ? (
              <CheckCircle2 className="h-3 w-3" />
            ) : (
              <XCircle className="h-3 w-3" />
            )}
            {routerEnabled ? t("providerKeys.routerActive") : t("providerKeys.routerInactive")}
          </Badge>
          <span className="text-xs text-muted-foreground">
            {configuredCount} {t("providerKeys.providersConfigured")}
          </span>
        </div>

        {/* Configured providers */}
        {configured.length > 0 && (
          <div className="space-y-2">
            <Label className="text-xs uppercase tracking-wider text-muted-foreground">
              {t("providerKeys.configuredLabel")}
            </Label>
            <div className="divide-y divide-border rounded-md border border-border">
              {configured.map((provider) => (
                <div
                  key={provider.provider_id}
                  className="flex items-center gap-3 px-3 py-2"
                >
                  <CheckCircle2 className="h-4 w-4 shrink-0 text-success" />
                  <div className="min-w-0 flex-1">
                    <div className="text-sm font-medium">
                      {provider.display_name}
                    </div>
                    <div className="font-mono text-xs text-muted-foreground">
                      {provider.masked_key}
                    </div>
                  </div>
                  <Input
                    className="max-w-48 font-mono text-xs"
                    placeholder={t("providerKeys.replacePlaceholder")}
                    value={keyInputs[provider.provider_id] || ""}
                    onChange={(e) =>
                      updateKeyInput(provider.provider_id, e.target.value)
                    }
                  />
                  <Button
                    size="sm"
                    variant="ghost"
                    className="text-destructive"
                    onClick={() => deleteKey(provider.provider_id)}
                  >
                    {t("providerKeys.remove")}
                  </Button>
                </div>
              ))}
            </div>
          </div>
        )}

        {/* Unconfigured — top providers only */}
        {unconfigured.length > 0 && (
          <div className="space-y-2">
            <Label className="text-xs uppercase tracking-wider text-muted-foreground">
              {t("providerKeys.addLabel")}
            </Label>
            <div className="divide-y divide-border rounded-md border border-border">
              {unconfigured.map((provider) => (
                <div
                  key={provider.provider_id}
                  className="flex items-center gap-3 px-3 py-2"
                >
                  <Key className="h-4 w-4 shrink-0 text-muted-foreground" />
                  <div className="min-w-0 flex-1 text-sm">
                    {provider.display_name}
                  </div>
                  <Input
                    className="max-w-64 font-mono text-xs"
                    placeholder={`${provider.display_name}${t("providerKeys.apiKeyForAfter")}`}
                    value={keyInputs[provider.provider_id] || ""}
                    onChange={(e) =>
                      updateKeyInput(provider.provider_id, e.target.value)
                    }
                  />
                </div>
              ))}
            </div>
          </div>
        )}

        {/* Save button */}
        <div className="flex items-center justify-end gap-3">
          {saved && <span className="text-xs text-success">{t("settings.savedIndicator")}</span>}
          {error && <span className="text-xs text-destructive">{error}</span>}
          <Button
            onClick={saveKeys}
            disabled={
              saving ||
              Object.values(keyInputs).every((v) => !v.trim())
            }
          >
            {saving ? t("settings.saving") : t("providerKeys.save")}
          </Button>
        </div>

        <Separator />

        {/* Model route tester */}
        <div className="space-y-3">
          <Label className="text-xs uppercase tracking-wider text-muted-foreground">
            {t("providerKeys.testLabel")}
          </Label>
          <div className="flex gap-2">
            <Input
              className="max-w-sm font-mono text-sm"
              placeholder={t("providerKeys.modelPlaceholder")}
              value={testModel}
              onChange={(e) => setTestModel(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && testRoute()}
            />
            <Button
              size="sm"
              variant="outline"
              onClick={testRoute}
              disabled={testing || !testModel.trim()}
            >
              <Search className="mr-1 h-3.5 w-3.5" />
              {testing ? t("providerKeys.testing") : t("providerKeys.resolve")}
            </Button>
          </div>

          {testResult && (
            <div
              className={`rounded-md border p-3 text-sm ${
                testResult.resolved
                  ? "border-success/30 bg-success/5"
                  : "border-destructive/30 bg-destructive/5"
              }`}
            >
              {testResult.resolved ? (
                <div className="space-y-1">
                  <div className="flex items-center gap-2">
                    <CheckCircle2 className="h-4 w-4 text-success" />
                    <span className="font-medium">
                      → {testResult.display_name}
                    </span>
                  </div>
                  <div className="text-xs text-muted-foreground">
                    {t("settings.llm.model")}: <code>{testResult.bare_model}</code> •{" "}
                    {t("settings.llm.provider")}: <code>{testResult.provider_id}</code> •{" "}
                    {t("providerKeys.keyWord")}: {testResult.has_key ? t("providerKeys.configured") : t("providerKeys.missing")}
                  </div>
                </div>
              ) : (
                <div className="flex items-center gap-2">
                  <XCircle className="h-4 w-4 text-destructive" />
                  <span>{testResult.error}</span>
                </div>
              )}
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
