import { useProjects } from "@/api/queries";
import { useProjectSelection } from "@/store/project";
import type { Project } from "@/types/api";

/**
 * Resolve which project the UI is scoped to, from the stored selection and the
 * project list. This is the single fallback rule every consumer (pages,
 * queries, the switcher) must share — a second interpretation anywhere makes
 * the scope chip and the list disagree.
 *
 * Order:
 *   1. the stored selection, when it still exists;
 *   2. the project with the most active runs (a fresh profile must land on the
 *      project being worked on, not on whichever project happens to be first —
 *      an empty "Default" import project silently made every project-scoped
 *      page look empty while a run was in flight);
 *   3. the first project, so the UI still has a scope when nothing is running.
 */
export function resolveActiveProject(
  projects: Project[],
  projectId: string | null,
): Project | null {
  if (projects.length === 0) return null;
  const explicit = projects.find((p) => p.id === projectId);
  if (explicit) return explicit;
  let busiest: Project | null = null;
  for (const p of projects) {
    const runs = p.active_runs ?? 0;
    if (runs > 0 && (busiest === null || runs > (busiest.active_runs ?? 0))) {
      busiest = p;
    }
  }
  return busiest ?? projects[0];
}

/** Effective project id for every project-scoped query. */
export function resolveActiveProjectId(
  projects: Project[],
  projectId: string | null,
): string | null {
  return resolveActiveProject(projects, projectId)?.id ?? null;
}

/**
 * Single source of truth for "which project is the UI scoped to".
 *
 * Falls back to the project with active runs when the user has not picked one
 * yet, so pages, queries and the switcher always agree on the effective scope.
 * Returns null only while the project list is still loading / empty.
 */
export function useActiveProjectId(): string | null {
  const { projectId } = useProjectSelection();
  const { data } = useProjects();
  return resolveActiveProjectId(data?.projects ?? [], projectId);
}
