import type { ReactNode } from "react"
import { createBrowserRouter, Navigate, type RouteObject } from "react-router-dom"
import { AuthBootstrap, RequireAuth, RedirectIfAuthed, RequireAdmin } from "@/app"
import OverviewPage from "@/pages/overview"
import ScansPage from "@/pages/scans"
import ScanDetailPage from "@/pages/scan-detail"
// R1 起新建扫描直启退役：一切执行从任务发起（POST /api/tasks/{id}/runs）。
// import NewScanPage from "@/pages/new-scan"
import FindingsPage from "@/pages/findings"
import ReportsPage from "@/pages/reports"
import LivePage from "@/pages/live"
import InstancesPage from "@/pages/instances"
import SettingsPage from "@/pages/settings"
import LoginPage from "@/pages/login"
import NotFoundPage from "@/pages/not-found"
import SchedulesPage from "@/pages/schedules"
import ProjectsPage from "@/pages/projects"
import TasksPage from "@/pages/tasks"
import TaskDetailPage from "@/pages/task-detail"
import ApprovalsPage from "@/pages/approvals"
import ChatPage from "@/pages/chat"
import InfoSpacePage from "@/pages/info-space"
import McpServersPage from "@/pages/mcp-servers"
import SkillsPage from "@/pages/skills"
import AgentProfilesPage from "@/pages/agent-profiles"
import UsersPage from "@/pages/users"

function Root({ children }: { children: ReactNode }) {
  return <AuthBootstrap>{children}</AuthBootstrap>
}

const routes: RouteObject[] = [
  {
    path: "/login",
    element: (
      <Root>
        <RedirectIfAuthed>
          <LoginPage />
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
      { index: true, element: <OverviewPage /> },
      { path: "projects", element: <ProjectsPage /> },
      { path: "tasks", element: <TasksPage /> },
      { path: "tasks/:taskId", element: <TaskDetailPage /> },
      { path: "chat", element: <ChatPage /> },
      { path: "approvals", element: <ApprovalsPage /> },
      { path: "info", element: <InfoSpacePage /> },
      { path: "scans", element: <ScansPage /> },
      // 遗留直启入口重定向到任务页（新建任务 = 新的一次执行）。
      { path: "scans/new", element: <Navigate to="/tasks?create=1" replace /> },
      { path: "scans/:scanId", element: <ScanDetailPage /> },
      { path: "schedules", element: <SchedulesPage /> },
      { path: "findings", element: <FindingsPage /> },
      { path: "reports", element: <ReportsPage /> },
      { path: "mcp", element: <McpServersPage /> },
      { path: "skills", element: <SkillsPage /> },
      { path: "agents", element: <AgentProfilesPage /> },
      { path: "live", element: <LivePage /> },
      { path: "instances", element: <InstancesPage /> },
      { path: "settings", element: <SettingsPage /> },
      // RBAC account management — admin only (nav hides it for other roles;
      // this guard is the backstop for direct URL entry).
      {
        path: "users",
        element: (
          <RequireAdmin>
            <UsersPage />
          </RequireAdmin>
        ),
      },
      { path: "404", element: <NotFoundPage /> },
      { path: "*", element: <Navigate to="/404" replace /> },
    ],
  },
]

export const router: ReturnType<typeof createBrowserRouter> = createBrowserRouter(routes)
