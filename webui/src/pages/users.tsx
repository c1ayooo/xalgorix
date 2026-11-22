import { useEffect, useState } from "react";
import {
  useUsers,
  useCreateUser,
  useUpdateUser,
  useDeleteUser,
} from "@/api/queries";
import { useAuth } from "@/store/auth";
import { useI18n } from "@/i18n";
import { toast } from "sonner";
import { KeyRound, Plus, ShieldCheck, Trash2 } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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
import { EmptyState, ErrorState } from "@/components/states";
import { Skeleton } from "@/components/ui/skeleton";
import { timeAgo } from "@/lib/utils";
import { cn } from "@/lib/utils";
import type { ManagedUser, Role } from "@/types/api";

const ROLES: Role[] = ["admin", "operator", "viewer"];

/** Status-cap role badge (10% bg + 30% border + dual-shade text). */
function RoleBadge({ role }: { role: Role }) {
  const { t } = useI18n();
  const cls =
    role === "admin"
      ? "border-red-500/30 bg-red-500/10 text-red-600 dark:text-red-400"
      : role === "operator"
        ? "border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400"
        : "border-neutral-500/30 bg-neutral-500/10 text-neutral-600 dark:text-neutral-400";
  return (
    <span className={cn("rounded-full border px-2 py-0.5 text-[10px] font-medium", cls)}>
      {t(`role.${role}`)}
    </span>
  );
}

/**
 * Account management (admin-only; the server enforces the role boundary and
 * the route guard bounces non-admins before this renders). Create, change
 * role, reset password, enable/disable and delete dashboard accounts.
 */
export default function UsersPage() {
  const { t } = useI18n();
  const me = useAuth((s) => s.user);
  const { data, isLoading, error, refetch } = useUsers();
  const create = useCreateUser();
  const update = useUpdateUser();
  const del = useDeleteUser();

  const [formOpen, setFormOpen] = useState(false);
  const [resetting, setResetting] = useState<ManagedUser | null>(null);
  const [deleting, setDeleting] = useState<ManagedUser | null>(null);

  const users = data ?? [];

  function onToggleDisabled(u: ManagedUser, disabled: boolean) {
    update.mutate(
      { id: u.id, patch: { disabled } },
      {
        onSuccess: () => toast.success(t(disabled ? "toast.userDisabled" : "toast.userEnabled")),
        onError: (err: Error) => toast.error(`${t("toast.userFailed")}: ${err.message}`),
      },
    );
  }

  function onRoleChange(u: ManagedUser, role: Role) {
    update.mutate(
      { id: u.id, patch: { role } },
      {
        onSuccess: () => toast.success(t("toast.userUpdated")),
        onError: (err: Error) => toast.error(`${t("toast.userFailed")}: ${err.message}`),
      },
    );
  }

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="font-sans text-2xl font-semibold tracking-tight">{t("users.title")}</h1>
          <p className="text-sm text-muted-foreground">{t("users.subtitle")}</p>
        </div>
        <Button onClick={() => setFormOpen(true)}>
          <Plus className="mr-1 h-4 w-4" />
          {t("users.create")}
        </Button>
      </header>

      {error ? (
        <ErrorState
          title={t("users.loadFailed")}
          description={error instanceof Error ? error.message : ""}
          action={
            <Button size="sm" variant="outline" onClick={() => refetch()}>
              {t("common.retry")}
            </Button>
          }
        />
      ) : isLoading ? (
        <div className="space-y-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-16 w-full" />
          ))}
        </div>
      ) : users.length === 0 ? (
        <EmptyState
          title={t("users.emptyTitle")}
          description={t("users.emptyDescription")}
          action={
            <Button onClick={() => setFormOpen(true)}>
              <Plus className="mr-1 h-4 w-4" />
              {t("users.create")}
            </Button>
          }
        />
      ) : (
        <div className="space-y-3">
          {users.map((u) => {
            const isSelf = me?.username === u.username;
            return (
              <Card key={u.id}>
                <CardContent className="flex flex-col gap-3 p-4 lg:flex-row lg:items-center">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <ShieldCheck className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
                      <span className="truncate font-mono text-sm font-medium">{u.username}</span>
                      <RoleBadge role={u.role} />
                      {isSelf && (
                        <span className="rounded-sm border border-border bg-muted px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-muted-foreground">
                          {t("users.self")}
                        </span>
                      )}
                      {u.disabled && (
                        <span className="rounded-sm border border-neutral-500/30 bg-neutral-500/10 px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-neutral-600 dark:text-neutral-400">
                          {t("users.disabled")}
                        </span>
                      )}
                    </div>
                    <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
                      <span className="mono">{u.id}</span>
                      <span>{t("users.createdAt")} {timeAgo(u.created_at)}</span>
                    </div>
                  </div>
                  <div className="flex shrink-0 flex-wrap items-center gap-2">
                    <Select
                      value={u.role}
                      onValueChange={(v) => onRoleChange(u, v as Role)}
                      disabled={update.isPending || (isSelf && u.role === "admin")}
                    >
                      <SelectTrigger className="h-8 w-32 text-xs">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {ROLES.map((r) => (
                          <SelectItem key={r} value={r}>
                            {t(`role.${r}`)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => setResetting(u)}
                    >
                      <KeyRound className="mr-1 h-3.5 w-3.5" />
                      {t("users.resetPassword")}
                    </Button>
                    <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
                      <Switch
                        checked={!u.disabled}
                        onCheckedChange={(v) => onToggleDisabled(u, !v)}
                        disabled={update.isPending || (isSelf && !u.disabled)}
                        aria-label={t(u.disabled ? "users.enable" : "users.disable")}
                      />
                      {t(u.disabled ? "users.enable" : "users.disable")}
                    </label>
                    <Button
                      size="icon"
                      variant="ghost"
                      className="text-red-600 hover:text-red-700 dark:text-red-400"
                      onClick={() => setDeleting(u)}
                      disabled={isSelf}
                      aria-label={`${t("common.delete")} ${u.username}`}
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                </CardContent>
              </Card>
            );
          })}
        </div>
      )}

      <CreateUserDialog open={formOpen} onOpenChange={setFormOpen} />

      <ResetPasswordDialog user={resetting} onOpenChange={(v) => !v && setResetting(null)} />

      <AlertDialog open={!!deleting} onOpenChange={(v) => !v && setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("users.deleteTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("users.deleteDescriptionPrefix")} “{deleting?.username}”.{" "}
              {t("users.deleteDescriptionSuffix")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (!deleting) return;
                del.mutate(deleting.id, {
                  onSuccess: () => toast.success(t("toast.userDeleted")),
                  onError: (err: Error) =>
                    toast.error(`${t("toast.userFailed")}: ${err.message}`),
                });
                setDeleting(null);
              }}
            >
              {t("common.delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function CreateUserDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
}) {
  const { t } = useI18n();
  const create = useCreateUser();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Role>("operator");

  useEffect(() => {
    if (open) {
      setUsername("");
      setPassword("");
      setRole("operator");
    }
  }, [open]);

  function submit() {
    if (!username.trim() || password.length < 8) return;
    create.mutate(
      { username: username.trim(), password, role },
      {
        onSuccess: (u) => {
          toast.success(`${t("toast.userCreated")}: ${u.username}`);
          onOpenChange(false);
        },
        onError: (err: Error) =>
          toast.error(`${t("toast.userFailed")}: ${err.message}`),
      },
    );
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t("users.createTitle")}</DialogTitle>
          <DialogDescription>{t("users.formDescription")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="user-name">{t("users.fieldUsername")}</Label>
            <Input
              id="user-name"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="off"
              className="mono"
            />
            <p className="text-[11px] text-muted-foreground">{t("users.usernameHint")}</p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="user-password">{t("users.fieldPassword")}</Label>
            <Input
              id="user-password"
              type="password"
              autoComplete="new-password"
              minLength={8}
              maxLength={72}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
            <p className="text-[11px] text-muted-foreground">{t("users.passwordHint")}</p>
          </div>
          <div className="space-y-1.5">
            <Label>{t("users.fieldRole")}</Label>
            <Select value={role} onValueChange={(v) => setRole(v as Role)}>
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ROLES.map((r) => (
                  <SelectItem key={r} value={r}>
                    {t(`role.${r}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" size="sm" onClick={() => onOpenChange(false)} disabled={create.isPending}>
            {t("common.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={submit}
            disabled={create.isPending || !username.trim() || password.length < 8}
          >
            {t("common.create")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ResetPasswordDialog({
  user,
  onOpenChange,
}: {
  user: ManagedUser | null;
  onOpenChange: (v: boolean) => void;
}) {
  const { t } = useI18n();
  const update = useUpdateUser();
  const [password, setPassword] = useState("");

  useEffect(() => {
    if (user) setPassword("");
  }, [user]);

  function submit() {
    if (!user || password.length < 8) return;
    update.mutate(
      { id: user.id, patch: { password } },
      {
        onSuccess: () => {
          toast.success(`${t("toast.userPasswordReset")}: ${user.username}`);
          onOpenChange(false);
        },
        onError: (err: Error) =>
          toast.error(`${t("toast.userFailed")}: ${err.message}`),
      },
    );
  }

  return (
    <Dialog open={!!user} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t("users.resetTitle")}</DialogTitle>
          <DialogDescription>{t("users.resetDescription")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-1.5">
          <Label htmlFor="reset-password">{t("users.fieldNewPassword")}</Label>
          <Input
            id="reset-password"
            type="password"
            autoComplete="new-password"
            minLength={8}
            maxLength={72}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoFocus
          />
          <p className="text-[11px] text-muted-foreground">{t("users.passwordHint")}</p>
        </div>
        <DialogFooter>
          <Button variant="outline" size="sm" onClick={() => onOpenChange(false)} disabled={update.isPending}>
            {t("common.cancel")}
          </Button>
          <Button size="sm" onClick={submit} disabled={update.isPending || password.length < 8}>
            {t("users.resetSubmit")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
