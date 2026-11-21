// Current-project selection for the project-scoped UI.
//
// Mirrors the theme/i18n micro-store pattern (useSyncExternalStore): the
// choice persists to localStorage and every consumer re-renders on switch.
// The backend enforces the real isolation boundary (project_id on every
// cold-path query); this store only drives which project's data the UI
// requests and displays.
import { useSyncExternalStore } from "react";

const STORAGE_KEY = "xalgorix.project";

let currentProjectId: string | null = readStored();
const listeners = new Set<() => void>();

function readStored(): string | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage.getItem(STORAGE_KEY);
  } catch {
    return null;
  }
}

function emit() {
  for (const l of listeners) l();
}

/** Switch the active project. Null means "no choice yet" (auto-select). */
export function setCurrentProject(id: string | null) {
  if (id === currentProjectId) return;
  currentProjectId = id;
  try {
    if (id) window.localStorage.setItem(STORAGE_KEY, id);
    else window.localStorage.removeItem(STORAGE_KEY);
  } catch {
    /* storage unavailable; in-memory selection still works */
  }
  emit();
}

export function getProjectId(): string | null {
  return currentProjectId;
}

function subscribe(l: () => void): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

export interface ProjectSelection {
  projectId: string | null;
  setProjectId: (id: string | null) => void;
}

export function useProjectSelection(): ProjectSelection {
  const projectId = useSyncExternalStore(subscribe, getProjectId, () => null);
  return { projectId, setProjectId: setCurrentProject };
}
