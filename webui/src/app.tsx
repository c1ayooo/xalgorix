import { useEffect, type ReactNode } from "react";
import { Navigate, useLocation } from "react-router-dom";
import { useAuth, isAdmin, type AuthState } from "@/store/auth";
import { AppShell } from "@/layout/app-shell";
import { BackendUnreachable } from "@/pages/backend-unreachable";

export function AuthBootstrap({ children }: { children: ReactNode }) {
  const refresh = useAuth((s: AuthState) => s.refresh);
  const status = useAuth((s: AuthState) => s.status);
  useEffect(() => {
    void refresh();
  }, [refresh]);
  if (status === "loading") {
    return (
      <div className="grid min-h-screen place-items-center bg-background">
        <div className="font-mono text-xs uppercase tracking-wider text-muted-foreground">
          Loading…
        </div>
      </div>
    );
  }
  // When the API isn't reachable at all, render a remediation page in
  // place of every route — the login form would be misleading because
  // submitting it would just produce another 404. We render this above
  // the router so it covers /, /login, and every protected page alike.
  if (status === "no-backend") {
    return <BackendUnreachable />;
  }
  return <>{children}</>;
}

export function RequireAuth() {
  const status = useAuth((s: AuthState) => s.status);
  const location = useLocation();

  if (status === "anon") {
    return (
      <Navigate
        to="/login"
        state={{ from: location.pathname + location.search }}
        replace
      />
    );
  }
  return <AppShell />;
}

/**
 * Route-level RBAC guard. `roles` lists the roles that may enter; a
 * signed-in user with a different role is bounced to the overview page
 * (their nav already hides the entry — this is the backstop, and it also
 * covers direct URL entry).
 */
export function RequireRole({
  roles,
  children,
}: {
  roles: string[];
  children: ReactNode;
}) {
  const user = useAuth((s: AuthState) => s.user);
  if (user && !roles.includes(user.role)) {
    return <Navigate to="/" replace />;
  }
  return <>{children}</>;
}

/** Admin-only convenience wrapper (settings, account management). */
export function RequireAdmin({ children }: { children: ReactNode }) {
  return (
    <RequireRole roles={["admin"]}>
      {children}
    </RequireRole>
  );
}

// Kept for readability at use sites that assert adminhood explicitly.
export { isAdmin };

export function RedirectIfAuthed({ children }: { children: ReactNode }) {
  const status = useAuth((s: AuthState) => s.status);
  const needsSetup = useAuth((s: AuthState) => s.needsSetup);
  // Auth disabled + no first admin yet: the login page is the bootstrap
  // form, so do NOT bounce the operator back to the open dashboard.
  if (status === "disabled" && needsSetup) {
    return <>{children}</>;
  }
  if (status === "authed" || status === "disabled") {
    return <Navigate to="/" replace />;
  }
  return <>{children}</>;
}
