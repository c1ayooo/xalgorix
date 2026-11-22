import { NavLink } from "react-router-dom";
import {
  BadgeCheck,
  Clock,
  FolderKanban,
  Database,
  ListChecks,
  MessagesSquare,
  FileText,
  LayoutGrid,
  Plus,
  Radio,
  Server,
  Settings,
  ShieldAlert,
  Target,
  Cable,
  Sparkles,
  Bot,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { useVersion } from "@/api/queries";
import { useI18n } from "@/i18n";
import { ProjectSwitcher } from "@/components/project-switcher";
import { AccountMenu } from "@/components/account-menu";
import { useAuth } from "@/store/auth";

// Nav groups around the Project→Task→Run model:
// 1) unlabeled = the project-scoped work surfaces (what the operator lives in);
// 2) "execution" = run lifecycle views (scans ARE runs since R1, rename per
//    docs/REDESIGN.md D6; schedules is a transitional standalone page until
//    its backend becomes a task attribute; 新建扫描直启入口已摘除——一切执行
//    从任务发起（POST /api/tasks/{id}/runs）；
// 3) "system" = low-frequency management.
// minRole hides an entry from lower RBAC roles (server still enforces; this
// is presentation only). Unset = visible to every signed-in role.
const NAV_GROUPS: {
  labelKey?: string;
  items: { to: string; labelKey: string; icon: typeof LayoutGrid; end?: boolean; minRole?: "admin" }[];
}[] = [
  {
    // 主组顺序 = 实体层级：项目（作用域）→ 任务（目标）→ 审批（约束）→ 发现（产出）。
    items: [
      { to: "/", labelKey: "nav.overview", icon: LayoutGrid, end: true },
      { to: "/projects", labelKey: "nav.projects", icon: FolderKanban },
      { to: "/tasks", labelKey: "nav.tasks", icon: ListChecks },
      { to: "/chat", labelKey: "nav.chat", icon: MessagesSquare },
      { to: "/approvals", labelKey: "nav.approvals", icon: BadgeCheck },
      { to: "/findings", labelKey: "nav.findings", icon: ShieldAlert },
      { to: "/info", labelKey: "nav.info", icon: Database },
    ],
  },
  {
    labelKey: "nav.groupExecution",
    items: [
      { to: "/scans", labelKey: "nav.scans", icon: Target },
      { to: "/instances", labelKey: "nav.instances", icon: Server },
      { to: "/live", labelKey: "nav.live", icon: Radio },
    ],
  },
  {
    labelKey: "nav.groupSystem",
    items: [
      { to: "/schedules", labelKey: "nav.schedules", icon: Clock },
      { to: "/reports", labelKey: "nav.reports", icon: FileText },
      { to: "/mcp", labelKey: "nav.mcp", icon: Cable },
      { to: "/skills", labelKey: "nav.skills", icon: Sparkles },
      { to: "/agents", labelKey: "nav.agents", icon: Bot },
      // Settings edit provider keys / environment secrets — admin writes only.
      { to: "/settings", labelKey: "nav.settings", icon: Settings, minRole: "admin" },
    ],
  },
];

export function Sidebar({ onNavigate }: { onNavigate?: () => void }) {
  const { t } = useI18n();
  const { data: version } = useVersion();
  const user = useAuth((s) => s.user);
  return (
    <aside className="flex h-full w-60 shrink-0 flex-col overflow-hidden rounded-xl border border-border bg-card shadow-sm">
      <div className="flex h-14 items-center gap-2 border-b border-border px-4">
        <div className="flex h-8 w-8 items-center justify-center overflow-hidden rounded-md border border-border bg-background">
          <img src="/logo.png" alt="" className="h-full w-full object-cover" aria-hidden />
        </div>
        <div className="flex flex-col">
          <span className="text-sm font-semibold tracking-tight">Xalgorix</span>
          <span className="text-[10px] text-muted-foreground mono">
            {version?.version ? `v${version.version}` : t("sidebar.securityScanner")}
          </span>
        </div>
      </div>
      <ProjectSwitcher />
      <nav className="flex-1 overflow-y-auto py-2" aria-label={t("sidebar.navigation")}>
        {NAV_GROUPS.map((group, gi) => {
          // RBAC presentation filter: hide entries above the viewer's role
          // (the server enforces the real boundary; auth-disabled mode shows
          // everything, as before).
          const items = group.items.filter(
            (item) => !item.minRole || !user || user.role === "admin",
          );
          if (items.length === 0) return null;
          return (
          <div key={gi} className={cn(gi > 0 && "mt-3 border-t border-border pt-3")}>
            {group.labelKey && (
              <div className="px-4 pb-1 pt-0.5 text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
                {t(group.labelKey)}
              </div>
            )}
            <ul className="space-y-0.5 px-2">
              {items.map((item) => {
                const Icon = item.icon;
                return (
                  <li key={item.to}>
                    <NavLink
                      to={item.to}
                      end={item.end}
                      onClick={onNavigate}
                      className={({ isActive }) =>
                        cn(
                          "flex items-center gap-2.5 rounded-md px-2.5 py-1.5 text-sm transition-colors",
                          isActive
                            ? "bg-accent text-accent-foreground"
                            : "text-muted-foreground hover:bg-accent/40 hover:text-foreground",
                        )
                      }
                    >
                      <Icon className="h-3.5 w-3.5" aria-hidden />
                      <span>{t(item.labelKey)}</span>
                    </NavLink>
                  </li>
                );
              })}
            </ul>
          </div>
          );
        })}
      </nav>
      <AccountMenu onNavigate={onNavigate} />
    </aside>
  );
}
