import { lazy, Suspense, type ReactNode } from "react"
import { createBrowserRouter, Navigate, type RouteObject } from "react-router-dom"
import { AuthBootstrap, RequireAuth, RedirectIfAuthed, RequireAdmin } from "@/app"

// Route-level code splitting: the shell loads immediately and each page is a
// separate chunk fetched on first navigation. Eager imports put every page and
// its heavy dependencies into one ~1.5 MB entry bundle, which made the sessions
// page wait for the whole app before rendering.
const OverviewPage = lazy(() => import("@/pages/overview"))
const ScansPage = lazy(() => import("@/pages/scans"))
const ScanDetailPage = lazy(() => import("@/pages/scan-detail"))
const FindingsPage = lazy(() => import("@/pages/findings"))
const ReportsPage = lazy(() => import("@/pages/reports"))
const LivePage = lazy(() => import("@/pages/live"))
const InstancesPage = lazy(() => import("@/pages/instances"))
const SettingsPage = lazy(() => import("@/pages/settings"))
const LoginPage = lazy(() => import("@/pages/login"))
const NotFoundPage = lazy(() => import("@/pages/not-found"))
const SchedulesPage = lazy(() => import("@/pages/schedules"))
const ProjectsPage = lazy(() => import("@/pages/projects"))
const TasksPage = lazy(() => import("@/pages/tasks"))
const TaskDetailPage = lazy(() => import("@/pages/task-detail"))
const ApprovalsPage = lazy(() => import("@/pages/approvals"))
const ChatPage = lazy(() => import("@/pages/chat"))
const InfoSpacePage = lazy(() => import("@/pages/info-space"))
const AssetsPage = lazy(() => import("@/pages/assets"))
const McpServersPage = lazy(() => import("@/pages/mcp-servers"))
const ToolsPage = lazy(() => import("@/pages/tools"))
const SkillsPage = lazy(() => import("@/pages/skills"))
const AgentProfilesPage = lazy(() => import("@/pages/agent-profiles"))
const UsersPage = lazy(() => import("@/pages/users"))

function RouteFallback() {
  return (
    <div className="grid min-h-[40vh] place-items-center" aria-busy="true">
      <div className="font-mono text-xs uppercase tracking-wider text-muted-foreground">
        Loading…
      </div>
    </div>
  )
}

function page(node: ReactNode) {
  return <Suspense fallback={<RouteFallback />}>{node}</Suspense>
}

function Root({ children }: { children: ReactNode }) {
  return <AuthBootstrap>{children}</AuthBootstrap>
}

const routes: RouteObject[] = [
  {
    path: "/login",
    element: (
      <Root>
        <RedirectIfAuthed>
          {page(<LoginPage />)}
        </RedirectIfAuthed>
      </Root>
    ),
  },
  {
    path: "/",
    element: (
      <Root>
        <RequireAuth />
      </Root>
    ),
    children: [
      { index: true, element: page(<OverviewPage />) },
      { path: "projects", element: page(<ProjectsPage />) },
      { path: "tasks", element: page(<TasksPage />) },
      { path: "tasks/:taskId", element: page(<TaskDetailPage />) },
      { path: "chat", element: page(<ChatPage />) },
      { path: "approvals", element: page(<ApprovalsPage />) },
      { path: "info", element: page(<InfoSpacePage />) },
      { path: "assets", element: page(<AssetsPage />) },
      { path: "scans", element: page(<ScansPage />) },
      // 遗留直启入口重定向到任务页（新建任务 = 新的一次执行）。
      { path: "scans/new", element: <Navigate to="/tasks?create=1" replace /> },
      { path: "scans/:scanId", element: page(<ScanDetailPage />) },
      { path: "schedules", element: page(<SchedulesPage />) },
      { path: "findings", element: page(<FindingsPage />) },
      { path: "reports", element: page(<ReportsPage />) },
      { path: "mcp", element: page(<McpServersPage />) },
      { path: "tools", element: page(<ToolsPage />) },
      { path: "skills", element: page(<SkillsPage />) },
      { path: "agents", element: page(<AgentProfilesPage />) },
      { path: "live", element: page(<LivePage />) },
      { path: "instances", element: page(<InstancesPage />) },
      { path: "settings", element: page(<SettingsPage />) },
      // RBAC account management — admin only (nav hides it for other roles;
      // this guard is the backstop for direct URL entry).
      {
        path: "users",
        element: (
          <RequireAdmin>
            {page(<UsersPage />)}
          </RequireAdmin>
        ),
      },
      { path: "404", element: page(<NotFoundPage />) },
      { path: "*", element: <Navigate to="/404" replace /> },
    ],
  },
]

export const router: ReturnType<typeof createBrowserRouter> = createBrowserRouter(routes)
