import { useProjects } from "@/api/queries";
import { useProjectSelection } from "@/store/project";

/**
 * Single source of truth for "which project is the UI scoped to".
 *
 * Falls back to the first project when the user has not picked one yet, so
 * pages, queries and the switcher always agree on the effective scope.
 * Returns null only while the project list is still loading / empty.
 */
export function useActiveProjectId(): string | null {
  const { projectId } = useProjectSelection();
  const { data } = useProjects();
  const projects = data?.projects ?? [];
  const explicit = projects.find((p) => p.id === projectId);
  if (explicit) return explicit.id;
  return projects[0]?.id ?? null;
}
