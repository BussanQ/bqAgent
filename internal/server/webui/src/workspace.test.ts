import { beforeEach, describe, expect, it } from "vitest";
import { loadWorkspaceSessions, migrateLegacySession, persistWorkspaceSession, workspaceURL } from "./workspace";

describe("工作区 URL 与会话持久化", () => {
  beforeEach(() => localStorage.clear());

  it("保留已有查询参数并编码 workspace_id", () => {
    expect(workspaceURL("/api/v1/status", "")).toBe("/api/v1/status");
    expect(workspaceURL("/api/v1/status", "workspace/a b")).toBe("/api/v1/status?workspace_id=workspace%2Fa%20b");
    expect(workspaceURL("/api/v1/status?session_id=s1", "w1")).toBe("/api/v1/status?session_id=s1&workspace_id=w1");
  });

  it("按工作区保存、删除并过滤损坏的 session", () => {
    const sessions: Record<string, string> = {};
    persistWorkspaceSession("sessions", sessions, "w1", "s1");
    expect(loadWorkspaceSessions("sessions")).toEqual({ w1: "s1" });
    persistWorkspaceSession("sessions", sessions, "w1", "");
    expect(loadWorkspaceSessions("sessions")).toEqual({});
    localStorage.setItem("sessions", JSON.stringify({ valid: "s2", invalid: 42 }));
    expect(loadWorkspaceSessions("sessions")).toEqual({ valid: "s2" });
  });

  it("仅把旧 session 迁移到默认工作区一次", () => {
    localStorage.setItem("legacy", "old-session");
    const sessions: Record<string, string> = {};
    migrateLegacySession("legacy", "sessions", sessions, "default", "default");
    expect(sessions.default).toBe("old-session");
    expect(localStorage.getItem("legacy")).toBeNull();
  });
});
