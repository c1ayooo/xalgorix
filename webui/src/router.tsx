import type { ReactNode } from "react"
import { createBrowserRouter, Navigate, type RouteObject } from "react-router-dom"
import { AuthBootstrap, RequireAuth, RedirectIfAuthed } from "@/app"
import OverviewPage from "@/pages/overview"
import ScansPage from "@/pages/scans"
import ScanDetailPage from "@/pages/scan-detail"
import NewScanPage from "@/pages/new-scan"
import FindingsPage from "@/pages/findings"
import ReportsPage from "@/pages/reports"
import IntegrationsPage from "@/pages/integrations"
import LivePage from "@/pages/live"
import InstancesPage from "@/pages/instances"
import EmailTriagePage from "@/pages/email-triage"
import SettingsPage from "@/pages/settings"
import LoginPage from "@/pages/login"
import NotFoundPage from "@/pages/not-found"
import SchedulesPage from "@/pages/schedules"
import ProjectsPage from "@/pages/projects"
import TasksPage from "@/pages/tasks"
import TaskDetailPage from "@/pages/task-detail"
import ApprovalsPage from "@/pages/approvals"
import InfoSpacePage from "@/pages/info-space"

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
      { path: "approvals", element: <ApprovalsPage /> },
      { path: "info", element: <InfoSpacePage /> },
      { path: "scans", element: <ScansPage /> },
      { path: "scans/new", element: <NewScanPage /> },
      { path: "scans/:scanId", element: <ScanDetailPage /> },
      { path: "schedules", element: <SchedulesPage /> },
      { path: "findings", element: <FindingsPage /> },
      { path: "reports", element: <ReportsPage /> },
      { path: "integrations", element: <IntegrationsPage /> },
      { path: "live", element: <LivePage /> },
      { path: "instances", element: <InstancesPage /> },
      { path: "email", element: <EmailTriagePage /> },
      { path: "settings", element: <SettingsPage /> },
      { path: "404", element: <NotFoundPage /> },
      { path: "*", element: <Navigate to="/404" replace /> },
    ],
  },
]

export const router: ReturnType<typeof createBrowserRouter> = createBrowserRouter(routes)
