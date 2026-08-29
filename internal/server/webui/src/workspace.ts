import { readJSONStorage, writeJSONStorage } from "./storage";

export function workspaceURL(url: string, workspaceID: string): string {
  if (!workspaceID) return url;
  return url + (url.includes("?") ? "&" : "?") + "workspace_id=" + encodeURIComponent(workspaceID);
}

export function loadWorkspaceSessions(key: string): Record<string, string> {
  const stored = readJSONStorage<unknown>(key, null);
  if (!stored || typeof stored !== "object" || Array.isArray(stored)) return Object.create(null) as Record<string, string>;
  const sessions: Record<string, string> = Object.create(null) as Record<string, string>;
  Object.entries(stored).forEach(([workspaceID, sessionID]) => {
    if (typeof sessionID === "string" && sessionID) sessions[workspaceID] = sessionID;
  });
  return sessions;
}

export function persistWorkspaceSession(
  storageKey: string,
  sessions: Record<string, string>,
  workspaceID: string,
  sessionID: string,
): void {
  if (sessionID) sessions[workspaceID] = sessionID;
  else delete sessions[workspaceID];
  writeJSONStorage(storageKey, sessions);
}

export function migrateLegacySession(
  legacyKey: string,
  sessionsKey: string,
  sessions: Record<string, string>,
  selectedWorkspaceID: string,
  defaultWorkspaceID: string,
): void {
  const legacy = localStorage.getItem(legacyKey) || "";
  if (legacy && selectedWorkspaceID === defaultWorkspaceID && !sessions[selectedWorkspaceID]) {
    sessions[selectedWorkspaceID] = legacy;
    writeJSONStorage(sessionsKey, sessions);
  }
  localStorage.removeItem(legacyKey);
}
