import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import { Check, ChevronsUpDown, FolderKanban, Plus } from "lucide-react";
import { Link } from "react-router-dom";
import { useProjects } from "@/api/queries";
import { useProjectSelection } from "@/store/project";
import { resolveActiveProject } from "@/lib/use-active-project";
import { useI18n } from "@/i18n";
import { cn, menuContentClass, menuItemClass } from "@/lib/utils";
import { Skeleton } from "@/components/ui/skeleton";

/**
 * Project switcher pinned to the top of the sidebar. Selection is a UI
 * preference (store/project.ts); the backend keeps enforcing the real
 * project isolation on every query.
 */
export function ProjectSwitcher() {
  const { t } = useI18n();
  const { data, isLoading } = useProjects();
  const { projectId, setProjectId } = useProjectSelection();

  const projects = data?.projects ?? [];
  // Same fallback rule as useActiveProjectId: the switcher must never show a
  // different project than the one the pages are querying.
  const active = resolveActiveProject(projects, projectId);

  if (isLoading && projects.length === 0) {
    return (
      <div className="px-2 pb-2">
        <Skeleton className="h-9 w-full" />
      </div>
    );
  }

  return (
    <div className="px-2 pb-2">
      <DropdownMenu.Root>
        <DropdownMenu.Trigger asChild>
          <button
            type="button"
            className="flex w-full items-center gap-2 rounded-md border border-border bg-background px-2.5 py-2 text-left text-sm transition-colors hover:bg-accent/40"
            aria-label={t("project.switcherLabel")}
          >
            <FolderKanban className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
            <span className="min-w-0 flex-1 truncate font-medium">
              {active ? active.name : t("project.none")}
            </span>
            <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden />
          </button>
        </DropdownMenu.Trigger>
        <DropdownMenu.Portal>
          <DropdownMenu.Content align="start" sideOffset={4} className={cn(menuContentClass, "w-56")}>
            <DropdownMenu.Label className="px-2 py-1.5 text-xs text-muted-foreground">
              {t("project.switcherLabel")}
            </DropdownMenu.Label>
            {projects.map((p) => (
              <DropdownMenu.Item
                key={p.id}
                className={menuItemClass}
                onSelect={() => setProjectId(p.id)}
              >
                <FolderKanban className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
                <span className="min-w-0 flex-1 truncate">{p.name}</span>
                {active?.id === p.id && (
                  <Check className="h-3.5 w-3.5 shrink-0" aria-hidden />
                )}
              </DropdownMenu.Item>
            ))}
            <DropdownMenu.Separator className="-mx-1 my-1 h-px bg-border" />
            <DropdownMenu.Item asChild className={menuItemClass}>
              <Link to="/projects">
                <Plus className="h-3.5 w-3.5" aria-hidden />
                {t("project.manage")}
              </Link>
            </DropdownMenu.Item>
          </DropdownMenu.Content>
        </DropdownMenu.Portal>
      </DropdownMenu.Root>
    </div>
  );
}
