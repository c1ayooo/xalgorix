import { useState, type FormEvent } from "react"
import { useNavigate, useLocation } from "react-router-dom"
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Button } from "@/components/ui/button"
import { useAuth } from "@/store/auth"
import { useI18n } from "@/i18n"
import { HttpError } from "@/api/client"
import { AlertCircle } from "lucide-react"

/**
 * Map a thrown error from `useAuth.login()` to a user-facing string.
 * The api client now throws an `HttpError` with status + parsed body, so
 * we can pick a friendly message per status code instead of leaking the
 * raw `HTTP 401 Unauthorized: {"error":"…"}` envelope.
 */
function describeLoginError(err: unknown, t: (k: string) => string): string {
  if (err instanceof HttpError) {
    const serverMsg =
      err.data && typeof err.data === "object" && "error" in (err.data as Record<string, unknown>)
        ? String((err.data as { error?: unknown }).error ?? "")
        : ""
    switch (err.status) {
      case 0:
        return t("login.err.network")
      case 400:
        return serverMsg || t("login.err.badRequest")
      case 401:
        return t("login.err.invalidCredentials")
      case 403:
        return serverMsg || t("login.err.forbidden")
      case 404:
        // The login endpoint is missing on the server. This is almost
        // always a stale Go binary — the SPA was rebuilt with the
        // /api/auth/login client but the embedded server wasn't.
        return t("login.err.notFound")
      case 405:
        return t("login.err.methodNotAllowed")
      case 409:
        return serverMsg || t("login.err.generic")
      case 429: {
        const wait = err.retryAfter ?? 0
        return wait > 0
          ? `${t("login.err.rateLimitedBefore")}${wait}${t("login.err.rateLimitedAfter")}`
          : serverMsg || t("login.err.rateLimited")
      }
      case 500:
      case 502:
      case 503:
      case 504:
        return t("login.err.server")
      default:
        return serverMsg || t("login.err.generic")
    }
  }
  if (err instanceof Error) return err.message
  return t("login.err.generic")
}

export default function LoginPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const location = useLocation()
  const login = useAuth((s) => s.login)
  const setup = useAuth((s) => s.setup)
  const needsSetup = useAuth((s) => s.needsSetup)
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [confirm, setConfirm] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const from = (location.state as { from?: string } | null)?.from ?? "/"

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setLoading(true)
    try {
      if (needsSetup) {
        if (password !== confirm) {
          setError(t("login.err.confirmMismatch"))
          return
        }
        await setup(username, password)
      } else {
        await login(username, password)
      }
      navigate(from, { replace: true })
    } catch (err) {
      setError(describeLoginError(err, t))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="grid min-h-screen w-full bg-background lg:grid-cols-2">
      <div className="hidden flex-col justify-between border-r border-border bg-muted/30 p-12 lg:flex">
        <div className="flex items-center gap-3">
          <img src="/logo.png" alt="Xalgorix" className="h-8 w-8 rounded-md" />
          <span className="font-mono text-sm font-semibold tracking-tight">XALGORIX</span>
        </div>
        <div className="space-y-6">
          <div className="space-y-2">
            <h1 className="font-sans text-4xl font-semibold tracking-tight text-foreground text-balance">
              {t("login.heroTitle")}
            </h1>
            <p className="max-w-md text-sm leading-relaxed text-muted-foreground">
              {t("login.heroSubtitle")}
            </p>
          </div>

        </div>
        <p className="text-xs text-muted-foreground">{t("login.footerInternal")}</p>
      </div>

      <div className="flex items-center justify-center p-6 sm:p-12">
        <div className="w-full max-w-sm">
          <div className="mb-8 flex items-center gap-3 lg:hidden">
            <img src="/logo.png" alt="Xalgorix" className="h-8 w-8 rounded-md" />
            <span className="font-mono text-sm font-semibold tracking-tight">XALGORIX</span>
          </div>
          <Card>
            <CardHeader>
              <CardTitle>
                {needsSetup ? t("login.setupTitle") : t("login.title")}
              </CardTitle>
              <CardDescription>
                {needsSetup ? t("login.setupSubtitle") : t("login.subtitle")}
              </CardDescription>
            </CardHeader>
            <CardContent>
              <form onSubmit={onSubmit} className="space-y-4">
                <div className="space-y-2">
                  <Label htmlFor="username">{t("login.username")}</Label>
                  <Input
                    id="username"
                    autoComplete="username"
                    value={username}
                    onChange={(e) => setUsername(e.target.value)}
                    required
                    autoFocus
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="password">{needsSetup ? t("login.setupPassword") : t("login.password")}</Label>
                  <Input
                    id="password"
                    type="password"
                    autoComplete={needsSetup ? "new-password" : "current-password"}
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    required
                  />
                </div>
                {needsSetup && (
                  <div className="space-y-2">
                    <Label htmlFor="confirm">{t("login.confirmPassword")}</Label>
                    <Input
                      id="confirm"
                      type="password"
                      autoComplete="new-password"
                      value={confirm}
                      onChange={(e) => setConfirm(e.target.value)}
                      required
                    />
                  </div>
                )}
                {error && (
                  <div className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive">
                    <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
                    <span>{error}</span>
                  </div>
                )}
                <Button type="submit" className="w-full" disabled={loading}>
                  {loading
                    ? t("login.signingIn")
                    : needsSetup
                      ? t("login.setupSubmit")
                      : t("login.signIn")}
                </Button>
              </form>
            </CardContent>
          </Card>
          <p className="mt-6 text-center text-xs text-muted-foreground">
            {needsSetup ? t("login.setupNote") : t("login.securityNote")}
          </p>
        </div>
      </div>
    </div>
  )
}
