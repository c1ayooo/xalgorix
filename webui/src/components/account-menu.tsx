import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import {
  CircleUserRound,
  KeyRound,
  LogOut,
  ShieldCheck,
  UserPlus,
} from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, HttpError } from "@/api/client";
import { useAuth } from "@/store/auth";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";

/** Role badge tint: status-cap pattern (10% bg + 30% border + dual text). */
const roleBadgeClass: Record<string, string> = {
  admin: "border-red-500/30 bg-red-500/10 text-red-600 dark:text-red-400",
  operator:
    "border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400",
  viewer:
    "border-neutral-500/30 bg-neutral-500/10 text-neutral-600 dark:text-neutral-400",
};

/**
 * Sidebar footer account chip: identity + role, self-service password
 * change, admin shortcut to account management, sign-out. In the
 * needs-setup state (auth not yet enabled, no accounts) it offers the
 * first-admin bootstrap entry instead.
 */
export function AccountMenu({ onNavigate }: { onNavigate?: () => void }) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const user = useAuth((s) => s.user);
  const needsSetup = useAuth((s) => s.needsSetup);
  const logout = useAuth((s) => s.logout);
  const [pwOpen, setPwOpen] = useState(false);
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [pwError, setPwError] = useState<string | null>(null);
  const [pwBusy, setPwBusy] = useState(false);

  async function signOut() {
    onNavigate?.();
    await logout();
    navigate("/login", { replace: true });
  }

  async function onChangePassword(e: FormEvent) {
    e.preventDefault();
    setPwError(null);
    setPwBusy(true);
    try {
      await api.changePassword(current, next);
      setPwOpen(false);
      setCurrent("");
      setNext("");
    } catch (err) {
      if (err instanceof HttpError) {
        const serverMsg =
          err.data && typeof err.data === "object" && "error" in (err.data as Record<string, unknown>)
            ? String((err.data as { error?: unknown }).error ?? "")
            : "";
        setPwError(serverMsg || t("account.pwFailed"));
      } else {
        setPwError(t("account.pwFailed"));
      }
    } finally {
      setPwBusy(false);
    }
  }

  // Bootstrap state: auth not enabled yet — point at /login (setup form).
  if (!user && needsSetup) {
    return (
      <div className="px-2 pb-2 pt-1">
        <Link
          to="/login"
          onClick={onNavigate}
          className="flex items-center justify-center gap-2 rounded-md border border-border bg-background px-3 py-2 text-sm font-medium text-muted-foreground transition-colors hover:border-brand/40 hover:text-foreground"
        >
          <UserPlus className="h-4 w-4" aria-hidden />
          <span>{t("account.setupEntry")}</span>
        </Link>
      </div>
    );
  }

  if (!user) {
    // Auth disabled and no setup pending: nothing to show (open mode).
    return null;
  }

  return (
    <>
      <div className="px-2 pb-2 pt-1">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              type="button"
              className="flex w-full items-center gap-2 rounded-md border border-border bg-background px-2.5 py-2 text-left transition-colors hover:border-brand/40 hover:text-foreground"
              title={t("account.menuTitle")}
            >
              <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-muted">
                <CircleUserRound className="h-4 w-4 text-muted-foreground" aria-hidden />
              </span>
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="truncate font-mono text-xs font-medium text-foreground">
                  {user.username}
                </span>
                <span
                  className={cn(
                    "mt-0.5 w-fit rounded-full border px-1.5 text-[10px] font-medium leading-4",
                    roleBadgeClass[user.role] ?? roleBadgeClass.viewer,
                  )}
                >
                  {t(`role.${user.role}`)}
                </span>
              </span>
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" side="top" className="w-52">
            <DropdownMenuLabel className="font-mono text-xs">
              {user.username}
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
            {user.role === "admin" && (
              <DropdownMenuItem asChild>
                <Link to="/users" onClick={onNavigate}>
                  <ShieldCheck className="h-4 w-4" aria-hidden />
                  {t("account.manageUsers")}
                </Link>
              </DropdownMenuItem>
            )}
            <DropdownMenuItem onSelect={() => setPwOpen(true)}>
              <KeyRound className="h-4 w-4" aria-hidden />
              {t("account.changePassword")}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => void signOut()}>
              <LogOut className="h-4 w-4" aria-hidden />
              {t("account.signOut")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      <Dialog open={pwOpen} onOpenChange={setPwOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("account.pwDialogTitle")}</DialogTitle>
            <DialogDescription>{t("account.pwDialogDesc")}</DialogDescription>
          </DialogHeader>
          <form onSubmit={onChangePassword} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="pw-current">{t("account.pwCurrent")}</Label>
              <Input
                id="pw-current"
                type="password"
                autoComplete="current-password"
                value={current}
                onChange={(e) => setCurrent(e.target.value)}
                required
                autoFocus
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="pw-next">{t("account.pwNew")}</Label>
              <Input
                id="pw-next"
                type="password"
                autoComplete="new-password"
                minLength={8}
                maxLength={72}
                value={next}
                onChange={(e) => setNext(e.target.value)}
                required
              />
            </div>
            {pwError && (
              <div className="rounded-md border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive">
                {pwError}
              </div>
            )}
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setPwOpen(false)}>
                {t("common.cancel")}
              </Button>
              <Button type="submit" disabled={pwBusy}>
                {t("account.pwSubmit")}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
