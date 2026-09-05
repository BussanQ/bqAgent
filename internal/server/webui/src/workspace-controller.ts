import { APIClient, RequestScope, isCancellation } from "./api-client";
import { errorMessage } from "./api";
import { byId } from "./dom";
import { iconMarkup, setIconButtonLabel } from "./icons";
import { readJSONStorage } from "./storage";
import { loadWorkspaceSessions, migrateLegacySession, persistWorkspaceSession, workspaceURL } from "./workspace";
import type { GroupInfo, PendingFile, WorkspaceCurrentPreview, WorkspaceDirectoryPage, WorkspaceDirectoryResponse, WorkspaceDirectoryState, WorkspaceEntry, WorkspaceInfo, WorkspaceListResponse, WorkspacePreview, WorkspaceRoot, WorkspacesResponse } from "./types";

export interface WorkspaceDependencies {
  onSwitch: () => void;
  thread: HTMLDivElement;
  input: HTMLTextAreaElement;
  sendBtn: HTMLButtonElement;
  statusEl: HTMLDivElement;
  sessionId: string;
  busy: boolean;
  pendingFiles: PendingFile[];
  MAX_PENDING_FILES: number;
  MAX_PENDING_TOTAL_FILE_BYTES: number;
  setConversationType: (value: unknown, group?: GroupInfo | null) => void;
  refreshConversations: (loadCurrent: boolean) => Promise<void>;
  loadRuntimeModel: () => Promise<void>;
  formatBytes: (bytes: number) => string;
  pathBaseName: (value: string) => string;
  clearPendingAttachments: () => void;
  addWorkspacePath: (value: string, size: number) => boolean;
  setAttachmentMenu: (open: boolean) => void;
  setChatMode: (value: unknown) => void;
  emptyMarkup: () => string;
}
export function createWorkspaceController(api: APIClient, deps: WorkspaceDependencies) {
  let pickerRequestID = 0;
  const requests = new RequestScope(api);
  const lifetime = new AbortController();
  let explorerInitialized = false;
  const SESSION_KEY = "bqagent.webui.session";
  const WORKSPACE_KEY = "bqagent.webui.workspace";
  const WORKSPACE_SESSIONS_KEY = "bqagent.webui.workspace-sessions";
  const WORKSPACE_SIDEBAR_KEY = "bqagent.webui.workspace-sidebar";
  const appLayout = byId<HTMLDivElement>("app-layout");
  const workspaceSidebar = byId<HTMLElement>("workspace-sidebar");
  const workspaceToggle = byId<HTMLButtonElement>("workspace-toggle");
  const workspaceBackdrop = byId<HTMLButtonElement>("workspace-backdrop");
  const workspaceClose = byId<HTMLButtonElement>("workspace-close");
  const workspaceTreeView = byId<HTMLElement>("workspace-tree-view");
  const workspaceTreeScroll = byId<HTMLDivElement>("workspace-tree-scroll");
  const workspaceTreeStatus = byId<HTMLDivElement>("workspace-tree-status");
  const workspaceTree = byId<HTMLDivElement>("workspace-tree");
  const workspaceRefresh = byId<HTMLButtonElement>("workspace-refresh");
  const workspaceSelect = byId<HTMLButtonElement>("workspace-select");
  const workspaceCreateAgent = byId<HTMLButtonElement>("workspace-create-agent");
  const workspaceCurrentName = byId<HTMLElement>("workspace-current-name");
  const workspaceCurrentPath = byId<HTMLElement>("workspace-current-path");
  const workspacePreviewView = byId<HTMLElement>("workspace-preview-view");
  const workspacePreviewBack = byId<HTMLButtonElement>("workspace-preview-back");
  const workspacePreviewClose = byId<HTMLButtonElement>("workspace-preview-close");
  const workspacePreviewRefresh = byId<HTMLButtonElement>("workspace-preview-refresh");
  const workspacePreviewTitle = byId<HTMLElement>("workspace-preview-title");
  const workspacePreviewPath = byId<HTMLElement>("workspace-preview-path");
  const workspacePreviewScroll = byId<HTMLDivElement>("workspace-preview-scroll");
  const workspacePreviewMeta = byId<HTMLDivElement>("workspace-preview-meta");
  const workspacePreviewContent = byId<HTMLDivElement>("workspace-preview-content");
  const workspacePreviewAttach = byId<HTMLButtonElement>("workspace-preview-attach");
  const workspacePickerBackdrop = byId<HTMLDivElement>("workspace-picker-backdrop");
  const workspacePickerClose = byId<HTMLButtonElement>("workspace-picker-close");
  const workspacePickerRoot = byId<HTMLSelectElement>("workspace-picker-root");
  const workspacePickerUp = byId<HTMLButtonElement>("workspace-picker-up");
  const workspacePickerPath = byId<HTMLElement>("workspace-picker-path");
  const workspacePickerList = byId<HTMLDivElement>("workspace-picker-list");
  const workspacePickerCancel = byId<HTMLButtonElement>("workspace-picker-cancel");
  const workspacePickerConfirm = byId<HTMLButtonElement>("workspace-picker-confirm");
  var currentWorkspace: WorkspaceInfo | null = null;
  var workspaceRoots: WorkspaceRoot[] = [];
  var workspaceSessions: Record<string, string> = Object.create(null) as Record<string, string>;
  var pickerRootID = "";
  var pickerPath = "";
  var pickerNextOffset: number | null = null;
  var workspaceReady = false;
  var WORKSPACE_PREVIEW_IMAGE_TYPES: Record<string, boolean> = { "image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true };
  var workspaceDirectories: Record<string, WorkspaceDirectoryState> = Object.create(null) as Record<string, WorkspaceDirectoryState>;
  var workspaceExpanded: Record<string, boolean> = Object.create(null) as Record<string, boolean>;
  var workspaceTreeScrollTop = 0;
  var workspaceCurrentPreview: WorkspaceCurrentPreview | null = null;
  var workspacePreviewRequestID = 0;
  var workspaceRefreshInFlight = false;
  var workspaceDesktopQuery = window.matchMedia ? window.matchMedia("(min-width: 901px)") : null;
  workspaceExpanded[""] = true;
  function setCurrentWorkspaceSession(value: string): void {
    deps.sessionId = value || "";
    if (!currentWorkspace || !currentWorkspace.id) return;
    persistWorkspaceSession(WORKSPACE_SESSIONS_KEY, workspaceSessions, currentWorkspace.id, deps.sessionId);
  }

  function workspaceScopedURL(url: string): string {
    return workspaceURL(url, currentWorkspace ? currentWorkspace.id : "");
  }

  function resetWorkspaceExplorer(): void {
    workspacePreviewRequestID++;
    workspaceDirectories = Object.create(null);
    workspaceExpanded = Object.create(null);
    workspaceExpanded[""] = true;
    workspaceTreeScrollTop = 0;
    workspaceCurrentPreview = null;
    workspacePreviewView.hidden = true;
    workspaceTreeView.hidden = false;
    renderWorkspaceTree();
  }

  function applyWorkspace(info: WorkspaceInfo, switching: boolean): void {
    if (!info || !info.id) return;
    var changed = !currentWorkspace || currentWorkspace.id !== info.id;
    if (changed) { requests.cancel(); deps.onSwitch(); }
    currentWorkspace = info;
    workspaceCurrentName.textContent = info.name || "工作区";
    workspaceCurrentPath.textContent = info.path || "";
    workspaceCurrentPath.title = info.path || "";
    deps.sessionId = workspaceSessions[info.id] || "";
    deps.setConversationType("default", null);
    workspaceReady = true;
    deps.input.disabled = false;
    deps.sendBtn.disabled = false;
    workspaceSelect.disabled = false;
    workspaceCreateAgent.disabled = false;
    try {
      localStorage.setItem(WORKSPACE_KEY, JSON.stringify({ root_id: info.root_id, path: info.relative_path || "" }));
    } catch (_) {}
    if (switching && changed) {
      deps.setChatMode("run");
      deps.clearPendingAttachments();
      deps.setAttachmentMenu(false);
      deps.thread.innerHTML = deps.emptyMarkup();
      deps.statusEl.textContent = deps.sessionId ? "已切换，继续上次会话" : "已切换工作区";
      deps.statusEl.classList.remove("error", "busy");
    }
    resetWorkspaceExplorer();
    loadWorkspaceDirectory("", false, false);
    deps.loadRuntimeModel();
    deps.refreshConversations(true);
  }

  async function initializeWorkspaceSelection(): Promise<void> {
    workspaceSessions = loadWorkspaceSessions(WORKSPACE_SESSIONS_KEY);
    var response = await workspaceFetchJSON<WorkspacesResponse>("/api/v1/webui/workspaces");
    workspaceRoots = response && Array.isArray(response.roots) ? response.roots : [];
    var selected = response.default;
    var saved = readJSONStorage<{ root_id: string; path?: string } | null>(WORKSPACE_KEY, null);
    if (saved && saved.root_id) {
      try {
        selected = await workspaceFetchJSON<WorkspaceInfo>("/api/v1/webui/workspaces/open", {
          method: "POST",
          headers: { "Accept": "application/json", "Content-Type": "application/json" },
          body: JSON.stringify({ root_id: saved.root_id, path: saved.path || "" })
        });
      } catch (error) {
        if (isCancellation(error)) throw error;
        selected = response.default;
      }
    }
    migrateLegacySession(SESSION_KEY, WORKSPACE_SESSIONS_KEY, workspaceSessions, selected.id, response.default.id);
    applyWorkspace(selected, false);
  }

  async function createWorkspaceAgentConfig(): Promise<void> {
    if (deps.busy || !currentWorkspace) return;
    workspaceCreateAgent.disabled = true;
    try {
      var result = await workspaceFetchJSON<{ created: boolean }>(workspaceScopedURL("/api/v1/webui/workspace/config"), { method: "POST", headers: { "Accept": "application/json" } });
      deps.statusEl.textContent = result.created ? "已创建工作区 .agent 配置" : "工作区 .agent 配置已存在";
      deps.statusEl.classList.remove("error", "busy");
      refreshWorkspaceTree();
    } catch (error) { if (isCancellation(error)) return;
      deps.statusEl.textContent = "创建 .agent 失败：" + errorMessage(error);
      deps.statusEl.classList.add("error");
    } finally {
      workspaceCreateAgent.disabled = false;
    }
  }

  function isWorkspaceDesktop(): boolean {
    return !workspaceDesktopQuery || workspaceDesktopQuery.matches;
  }

  function setWorkspaceSidebarOpen(open: boolean, persist: boolean): void {
    open = Boolean(open);
    var desktop = isWorkspaceDesktop();
    appLayout.classList.toggle("workspace-closed", desktop && !open);
    appLayout.classList.toggle("workspace-open", !desktop && open);
    workspaceBackdrop.hidden = desktop || !open;
    workspaceToggle.setAttribute("aria-expanded", open ? "true" : "false");
    workspaceSidebar.setAttribute("aria-hidden", open ? "false" : "true");
    if ("inert" in workspaceSidebar) workspaceSidebar.inert = !open;
    if (persist && desktop) {
      try { localStorage.setItem(WORKSPACE_SIDEBAR_KEY, open ? "open" : "closed"); } catch (_) {}
    }
  }

  function syncWorkspaceSidebarForViewport(): void {
    if (isWorkspaceDesktop()) {
      var saved = "";
      try { saved = localStorage.getItem(WORKSPACE_SIDEBAR_KEY) || ""; } catch (_) {}
      setWorkspaceSidebarOpen(saved !== "closed", false);
    } else {
      setWorkspaceSidebarOpen(false, false);
    }
  }

  function workspaceDirectoryState(directoryPath: string): WorkspaceDirectoryState {
    if (!workspaceDirectories[directoryPath]) {
      workspaceDirectories[directoryPath] = {
        path: directoryPath,
        entries: [],
        nextOffset: null,
        loaded: false,
        loading: false,
        error: "",
        requestID: 0
      };
    }
    return workspaceDirectories[directoryPath];
  }

  async function workspaceFetchJSON<T extends object>(url: string, options: RequestInit = { headers: { "Accept": "application/json" } }): Promise<T> {
    var requestOptions: RequestInit = { ...options, cache: "no-store" };
    var response = await requests.request(url, requestOptions);
    if (!response.ok) {
      var message = "HTTP " + response.status;
      try {
        var errorPayload = await requests.readJSON<{ error?: string }>(response);
        if (errorPayload.error) message = errorPayload.error;
      } catch (_) {}
      throw new Error(message);
    }
    return requests.readJSON<T>(response);
  }

  async function requestWorkspaceDirectoryPage(directoryPath: string, offset: number): Promise<WorkspaceDirectoryPage> {
    var url = workspaceScopedURL("/api/v1/webui/workspace?path=" + encodeURIComponent(directoryPath) + "&offset=" + offset);
    var payload = await workspaceFetchJSON<WorkspaceListResponse>(url);
    return {
      entries: payload && Array.isArray(payload.entries) ? payload.entries : [],
      nextOffset: payload && typeof payload.next_offset === "number" ? payload.next_offset : null
    };
  }

  async function loadWorkspaceDirectory(directoryPath: string, append: boolean, preservePages: boolean): Promise<void> {
    var state = workspaceDirectoryState(directoryPath);
    if (state.loading && append) return;
    var priorEntries = state.entries.slice();
    var priorNextOffset = state.nextOffset;
    var pages = preservePages ? Math.max(1, Math.ceil(priorEntries.length / 250)) : 1;
    var requestID = ++state.requestID;
    state.loading = true;
    state.error = "";
    renderWorkspaceTree();
    try {
      var gathered = append ? state.entries.slice() : [];
      var nextOffset = append ? state.nextOffset : 0;
      if (append && typeof nextOffset !== "number") return;
      for (var pageIndex = 0; pageIndex < pages && typeof nextOffset === "number"; pageIndex++) {
        var page = await requestWorkspaceDirectoryPage(directoryPath, nextOffset);
        if (requestID !== state.requestID) return;
        var seen = Object.create(null);
        gathered.forEach(function (entry) { seen[entry.path] = true; });
        page.entries.forEach(function (entry) {
          if (!seen[entry.path]) {
            seen[entry.path] = true;
            gathered.push(entry);
          }
        });
        nextOffset = page.nextOffset;
        if (append) break;
      }
      if (requestID !== state.requestID) return;
      state.entries = gathered;
      state.nextOffset = nextOffset;
      state.loaded = true;
    } catch (error) { if (isCancellation(error)) return;
      if (requestID !== state.requestID) return;
      if (!append && priorEntries.length) {
        state.entries = priorEntries;
        state.nextOffset = priorNextOffset;
      }
      state.error = errorMessage(error);
    } finally {
      if (requestID === state.requestID) {
        state.loading = false;
        renderWorkspaceTree();
      }
    }
  }

  function createWorkspaceEntryIcon(type: string): HTMLSpanElement {
    var icon = document.createElement("span");
    icon.className = "workspace-entry-icon" + (type === "symlink" || type === "other" ? " muted" : "");
    icon.setAttribute("aria-hidden", "true");
    icon.innerHTML = iconMarkup(type === "directory" ? "folder" : (type === "symlink" ? "link" : "file"));
    return icon;
  }

  function appendWorkspaceInlineState(text: string, depth: number, error: boolean): void {
    var state = document.createElement("div");
    state.className = "workspace-inline-state" + (error ? " error" : "");
    state.style.setProperty("--tree-depth", String(depth));
    state.textContent = text;
    workspaceTree.appendChild(state);
  }

  function appendWorkspaceDirectory(directoryPath: string, depth: number): void {
    var state = workspaceDirectoryState(directoryPath);
    state.entries.forEach(function (entry) {
      var row = document.createElement("button");
      row.type = "button";
      row.className = "workspace-row" + (workspaceCurrentPreview && workspaceCurrentPreview.path === entry.path ? " active" : "");
      row.style.setProperty("--tree-depth", String(depth));
      row.setAttribute("role", "treeitem");
      row.setAttribute("aria-level", String(depth + 1));
      row.title = entry.path;

      var chevron = document.createElement("span");
      chevron.className = "workspace-chevron";
      if (entry.type === "directory") {
        var expanded = Boolean(workspaceExpanded[entry.path]);
        chevron.innerHTML = iconMarkup("chevron-right");
        chevron.classList.toggle("expanded", expanded);
        row.setAttribute("aria-expanded", expanded ? "true" : "false");
      }
      row.appendChild(chevron);
      row.appendChild(createWorkspaceEntryIcon(entry.type));

      var name = document.createElement("span");
      name.className = "workspace-entry-name";
      name.textContent = entry.name;
      row.appendChild(name);
      if (entry.type === "file") {
        var size = document.createElement("span");
        size.className = "workspace-entry-size";
        size.textContent = deps.formatBytes(entry.size);
        row.appendChild(size);
      }

      if (entry.type === "directory") {
        row.addEventListener("click", function () {
          workspaceExpanded[entry.path] = !workspaceExpanded[entry.path];
          var child = workspaceDirectoryState(entry.path);
          if (workspaceExpanded[entry.path] && !child.loaded && !child.loading) {
            loadWorkspaceDirectory(entry.path, false, false);
          } else {
            renderWorkspaceTree();
          }
        }, { signal: lifetime.signal });
      } else if (entry.type === "file") {
        row.addEventListener("click", function () { openWorkspacePreview(entry); }, { signal: lifetime.signal });
      } else {
        row.disabled = true;
        row.title = entry.path + "（符号链接或特殊文件不可浏览）";
      }
      workspaceTree.appendChild(row);

      if (entry.type === "directory" && workspaceExpanded[entry.path]) {
        var childState = workspaceDirectoryState(entry.path);
        if (childState.loaded) appendWorkspaceDirectory(entry.path, depth + 1);
        if (childState.loading) appendWorkspaceInlineState("加载中…", depth + 1, false);
        if (childState.error) appendWorkspaceInlineState("加载失败：" + childState.error, depth + 1, true);
      }
    });

    if (typeof state.nextOffset === "number") {
      var more = document.createElement("button");
      more.type = "button";
      more.className = "workspace-load-more";
      more.style.setProperty("--tree-depth", String(depth));
      more.textContent = state.loading ? "加载中…" : "加载更多";
      more.disabled = state.loading;
      more.addEventListener("click", function () { loadWorkspaceDirectory(directoryPath, true, false); }, { signal: lifetime.signal });
      workspaceTree.appendChild(more);
    }
  }

  function renderWorkspaceTree(): void {
    var scrollTop = workspaceTreeScroll.scrollTop;
    workspaceTree.textContent = "";
    workspaceTreeStatus.textContent = "";
    workspaceTreeStatus.classList.remove("error");
    var root = workspaceDirectoryState("");
    if (!root.loaded && root.loading) {
      workspaceTreeStatus.textContent = "正在读取工作区…";
    } else if (root.error && !root.entries.length) {
      workspaceTreeStatus.textContent = "读取失败：" + root.error;
      workspaceTreeStatus.classList.add("error");
    } else if (root.loaded && !root.entries.length) {
      workspaceTreeStatus.textContent = "工作区为空";
    } else {
      appendWorkspaceDirectory("", 0);
      if (root.error) {
        workspaceTreeStatus.textContent = "刷新失败，当前显示上次结果：" + root.error;
        workspaceTreeStatus.classList.add("error");
      }
    }
    workspaceTreeScroll.scrollTop = scrollTop;
  }

  async function refreshWorkspaceTree(): Promise<void> {
    if (workspaceRefreshInFlight) return;
    workspaceRefreshInFlight = true;
    workspaceRefresh.disabled = true;
    var paths = Object.keys(workspaceDirectories).filter(function (directoryPath) {
      return workspaceDirectories[directoryPath].loaded || directoryPath === "";
    });
    if (!paths.length) paths = [""];
    try {
      await Promise.all(paths.map(function (directoryPath) {
        return loadWorkspaceDirectory(directoryPath, false, true);
      }));
    } finally {
      workspaceRefreshInFlight = false;
      workspaceRefresh.disabled = false;
    }
  }

  function showWorkspaceTreeView(): void {
    workspacePreviewView.hidden = true;
    workspaceTreeView.hidden = false;
    renderWorkspaceTree();
    requestAnimationFrame(function () { workspaceTreeScroll.scrollTop = workspaceTreeScrollTop; });
  }

  function showWorkspacePreviewMessage(message: string, error: boolean): void {
    workspacePreviewContent.textContent = "";
    var notice = document.createElement("div");
    notice.className = "workspace-preview-message" + (error ? " error" : "");
    notice.textContent = message;
    workspacePreviewContent.appendChild(notice);
  }

  function updateWorkspacePreviewAttachState(): void {
    var current = workspaceCurrentPreview;
    var currentPath = current ? current.path : "";
    var duplicate = Boolean(current && deps.pendingFiles.some(function (file) {
      return file.kind === "path" && file.path === currentPath;
    }));
    var attachable = Boolean(current && current.payload && current.payload.attachable);
    var countLimited = Boolean(current && deps.pendingFiles.length >= deps.MAX_PENDING_FILES);
    var knownTotalBytes = deps.pendingFiles.reduce(function (sum, file) { return sum + (Number(file.size) || 0); }, 0);
    var totalLimited = Boolean(current && current.payload && current.payload.size && knownTotalBytes + current.payload.size > deps.MAX_PENDING_TOTAL_FILE_BYTES);
    workspacePreviewAttach.disabled = deps.busy || !attachable || duplicate || countLimited || totalLimited;
    if (duplicate) {
      setIconButtonLabel(workspacePreviewAttach, "paperclip", "已加入附件");
      workspacePreviewAttach.title = "该文件已在待发送附件中";
    } else if (countLimited) {
      setIconButtonLabel(workspacePreviewAttach, "paperclip", "已达 5 个附件上限");
      workspacePreviewAttach.title = "请先移除一个待发送文件";
    } else if (totalLimited) {
      setIconButtonLabel(workspacePreviewAttach, "paperclip", "超过 6 MiB 合计上限");
      workspacePreviewAttach.title = "请先移除部分待发送文件";
    } else if (current && current.payload && !current.payload.attachable) {
      setIconButtonLabel(workspacePreviewAttach, "paperclip", "超过 2 MiB 附件上限");
      workspacePreviewAttach.title = "工作区路径附件单文件不能超过 2 MiB";
    } else if (deps.busy) {
      setIconButtonLabel(workspacePreviewAttach, "paperclip", "生成期间不可添加");
      workspacePreviewAttach.title = "请等待当前回复结束";
    } else {
      setIconButtonLabel(workspacePreviewAttach, "paperclip", "加入附件");
      workspacePreviewAttach.title = "将此工作区文件加入当前消息";
    }
  }

  function renderWorkspacePreview(payload: WorkspacePreview): void {
    if (!workspaceCurrentPreview) return;
    workspaceCurrentPreview.payload = payload;
    workspacePreviewTitle.textContent = payload.name || deps.pathBaseName(payload.path);
    workspacePreviewPath.textContent = payload.path || workspaceCurrentPreview.path;
    workspacePreviewMeta.textContent = (payload.path || workspaceCurrentPreview.path) + "\n" + deps.formatBytes(payload.size) + " · " + (payload.mime_type || "未知类型");
    workspacePreviewContent.textContent = "";
    if (payload.preview_type === "text") {
      var code = document.createElement("pre");
      code.textContent = payload.content || "";
      workspacePreviewContent.appendChild(code);
      if (payload.truncated) {
        var truncated = document.createElement("div");
        truncated.className = "workspace-preview-truncated";
        truncated.textContent = "预览已截断，仅显示前 512 KiB。";
        workspacePreviewContent.appendChild(truncated);
      }
    } else if (payload.preview_type === "image") {
      var mimeType = String(payload.mime_type || "").split(";")[0].toLowerCase();
      if (WORKSPACE_PREVIEW_IMAGE_TYPES[mimeType] && payload.data_base64) {
        var image = document.createElement("img");
        image.alt = payload.name || "工作区图片预览";
        image.src = "data:" + mimeType + ";base64," + payload.data_base64;
        workspacePreviewContent.appendChild(image);
      } else {
        showWorkspacePreviewMessage("图片预览数据无效", true);
      }
    } else {
      showWorkspacePreviewMessage(payload.reason || "该文件无法在浏览器中预览", false);
    }
    updateWorkspacePreviewAttachState();
  }

  async function loadWorkspacePreview(): Promise<void> {
    if (!workspaceCurrentPreview) return;
    var currentPath = workspaceCurrentPreview.path;
    var requestID = ++workspacePreviewRequestID;
    workspacePreviewRefresh.disabled = true;
    workspacePreviewMeta.textContent = currentPath;
    showWorkspacePreviewMessage("正在读取文件…", false);
    workspaceCurrentPreview.payload = null;
    updateWorkspacePreviewAttachState();
    try {
      var payload = await workspaceFetchJSON<WorkspacePreview>(workspaceScopedURL("/api/v1/webui/workspace/preview?path=" + encodeURIComponent(currentPath)));
      if (requestID !== workspacePreviewRequestID || !workspaceCurrentPreview || workspaceCurrentPreview.path !== currentPath) return;
      renderWorkspacePreview(payload);
    } catch (error) { if (isCancellation(error)) return;
      if (requestID !== workspacePreviewRequestID || !workspaceCurrentPreview || workspaceCurrentPreview.path !== currentPath) return;
      workspacePreviewMeta.textContent = currentPath;
      showWorkspacePreviewMessage("预览失败：" + errorMessage(error), true);
      updateWorkspacePreviewAttachState();
    } finally {
      if (requestID === workspacePreviewRequestID) workspacePreviewRefresh.disabled = false;
    }
  }

  function openWorkspacePreview(entry: WorkspaceEntry): void {
    workspaceTreeScrollTop = workspaceTreeScroll.scrollTop;
    workspaceCurrentPreview = { path: entry.path, name: entry.name, size: entry.size, payload: null };
    workspaceTreeView.hidden = true;
    workspacePreviewView.hidden = false;
    workspacePreviewTitle.textContent = entry.name;
    workspacePreviewPath.textContent = entry.path;
    workspacePreviewScroll.scrollTop = 0;
    setWorkspaceSidebarOpen(true, false);
    renderWorkspaceTree();
    loadWorkspacePreview();
  }

  function closeWorkspacePicker(): void {
    pickerRequestID++;
    workspacePickerBackdrop.hidden = true;
    workspaceSelect.setAttribute("aria-expanded", "false");
    workspaceSelect.focus();
  }

  function renderWorkspacePickerState(message: string, error: boolean): void {
    workspacePickerList.textContent = "";
    var state = document.createElement("div");
    state.className = "workspace-picker-state" + (error ? " error" : "");
    state.textContent = message;
    workspacePickerList.appendChild(state);
  }

  async function loadWorkspacePickerDirectories(append: boolean): Promise<void> {
    const requestID = ++pickerRequestID;
    if (!pickerRootID) return;
    if (!append) {
      pickerNextOffset = 0;
      renderWorkspacePickerState("正在读取目录…", false);
    }
    var offset = append && typeof pickerNextOffset === "number" ? pickerNextOffset : 0;
    try {
      var payload = await workspaceFetchJSON<WorkspaceDirectoryResponse>("/api/v1/webui/workspaces/directories?root_id=" + encodeURIComponent(pickerRootID) + "&path=" + encodeURIComponent(pickerPath) + "&offset=" + offset);
      if (requestID !== pickerRequestID) return;
      if (!append) workspacePickerList.textContent = "";
      pickerPath = payload.path || "";
      pickerNextOffset = typeof payload.next_offset === "number" ? payload.next_offset : null;
      workspacePickerPath.textContent = payload.absolute_path || (payload.root && payload.root.path ? payload.root.path : "");
      workspacePickerUp.disabled = !pickerPath;
      var directories = payload && Array.isArray(payload.directories) ? payload.directories : [];
      directories.forEach(function (directory) {
        var row = document.createElement("button");
        row.type = "button";
        row.className = "workspace-picker-row";
        row.innerHTML = '<span class="workspace-entry-icon" aria-hidden="true">' + iconMarkup("folder") + '</span>';
        var label = document.createElement("span");
        label.textContent = directory.name;
        row.appendChild(label);
        row.addEventListener("click", function () {
          pickerPath = directory.path;
          loadWorkspacePickerDirectories(false);
        }, { signal: lifetime.signal });
        workspacePickerList.appendChild(row);
      });
      if (!directories.length && !append && pickerNextOffset === null) renderWorkspacePickerState("当前目录没有子目录", false);
      if (typeof pickerNextOffset === "number") {
        var more = document.createElement("button");
        more.type = "button";
        more.className = "workspace-picker-row";
        more.textContent = "加载更多";
        more.addEventListener("click", function () { loadWorkspacePickerDirectories(true); }, { signal: lifetime.signal });
        workspacePickerList.appendChild(more);
      }
    } catch (error) { if (isCancellation(error)) return;
      if (requestID === pickerRequestID) renderWorkspacePickerState("读取失败：" + errorMessage(error), true);
    }
  }

  function openWorkspacePicker(): void {
    if (deps.busy || !workspaceRoots.length) return;
    workspacePickerRoot.textContent = "";
    workspaceRoots.forEach(function (root) {
      var option = document.createElement("option");
      option.value = root.id;
      option.textContent = root.name + " - " + root.path;
      workspacePickerRoot.appendChild(option);
    });
    pickerRootID = currentWorkspace && currentWorkspace.root_id ? currentWorkspace.root_id : workspaceRoots[0].id;
    pickerPath = currentWorkspace && currentWorkspace.root_id === pickerRootID ? (currentWorkspace.relative_path || "") : "";
    workspacePickerRoot.value = pickerRootID;
    workspacePickerBackdrop.hidden = false;
    workspaceSelect.setAttribute("aria-expanded", "true");
    loadWorkspacePickerDirectories(false);
    workspacePickerRoot.focus();
  }

  async function confirmWorkspacePicker(): Promise<void> {
    if (deps.busy || !pickerRootID) return;
    workspacePickerConfirm.disabled = true;
    try {
      var info = await workspaceFetchJSON<WorkspaceInfo>("/api/v1/webui/workspaces/open", {
        method: "POST",
        headers: { "Accept": "application/json", "Content-Type": "application/json" },
        body: JSON.stringify({ root_id: pickerRootID, path: pickerPath })
      });
      closeWorkspacePicker();
      applyWorkspace(info, true);
      deps.input.focus();
    } catch (error) { if (isCancellation(error)) return;
      renderWorkspacePickerState("打开失败：" + errorMessage(error), true);
    } finally {
      workspacePickerConfirm.disabled = false;
    }
  }

  function initializeWorkspaceExplorer(): void {
    if (explorerInitialized) return;
    explorerInitialized = true;
    syncWorkspaceSidebarForViewport();
    if (workspaceDesktopQuery) {
      if (typeof workspaceDesktopQuery.addEventListener === "function") workspaceDesktopQuery.addEventListener("change", syncWorkspaceSidebarForViewport, { signal: lifetime.signal });
      else workspaceDesktopQuery.addListener(syncWorkspaceSidebarForViewport);
    }
  }

  workspaceSelect.addEventListener("click", openWorkspacePicker, { signal: lifetime.signal });
  workspaceCreateAgent.addEventListener("click", createWorkspaceAgentConfig, { signal: lifetime.signal });
  workspaceToggle.addEventListener("click", function () {
    setWorkspaceSidebarOpen(workspaceToggle.getAttribute("aria-expanded") !== "true", true);
  }, { signal: lifetime.signal });
  workspaceClose.addEventListener("click", function () { setWorkspaceSidebarOpen(false, true); }, { signal: lifetime.signal });
  workspacePreviewClose.addEventListener("click", function () { setWorkspaceSidebarOpen(false, true); }, { signal: lifetime.signal });
  workspaceBackdrop.addEventListener("click", function () { setWorkspaceSidebarOpen(false, false); }, { signal: lifetime.signal });
  workspaceRefresh.addEventListener("click", refreshWorkspaceTree, { signal: lifetime.signal });
  workspacePreviewBack.addEventListener("click", showWorkspaceTreeView, { signal: lifetime.signal });
  workspacePreviewRefresh.addEventListener("click", loadWorkspacePreview, { signal: lifetime.signal });
  workspacePreviewAttach.addEventListener("click", function () {
    if (!workspaceCurrentPreview || !workspaceCurrentPreview.payload) return;
    deps.addWorkspacePath(workspaceCurrentPreview.path, workspaceCurrentPreview.payload.size);
    updateWorkspacePreviewAttachState();
  }, { signal: lifetime.signal });
  workspacePickerClose.addEventListener("click", closeWorkspacePicker, { signal: lifetime.signal });
  workspacePickerCancel.addEventListener("click", closeWorkspacePicker, { signal: lifetime.signal });
  workspacePickerConfirm.addEventListener("click", confirmWorkspacePicker, { signal: lifetime.signal });
  workspacePickerBackdrop.addEventListener("click", function (event) {
    if (event.target === workspacePickerBackdrop) closeWorkspacePicker();
  }, { signal: lifetime.signal });
  workspacePickerRoot.addEventListener("change", function () {
    pickerRootID = workspacePickerRoot.value;
    pickerPath = "";
    loadWorkspacePickerDirectories(false);
  }, { signal: lifetime.signal });
  workspacePickerUp.addEventListener("click", function () {
    if (!pickerPath) return;
    var parts = pickerPath.split("/");
    parts.pop();
    pickerPath = parts.join("/");
    loadWorkspacePickerDirectories(false);
  }, { signal: lifetime.signal });

  return {
    initializeWorkspaceExplorer,
    initializeWorkspaceSelection,
    get workspaceToggle() { return workspaceToggle; },
    get workspaceSelect() { return workspaceSelect; },
    get workspaceCreateAgent() { return workspaceCreateAgent; },
    get workspacePickerBackdrop() { return workspacePickerBackdrop; },
    get currentWorkspace() { return currentWorkspace; },
    get workspaceReady() { return workspaceReady; },
    setCurrentWorkspaceSession,
    workspaceScopedURL,
    isWorkspaceDesktop,
    setWorkspaceSidebarOpen,
    refreshWorkspaceTree,
    updateWorkspacePreviewAttachState,
    closeWorkspacePicker,
    dispose() { lifetime.abort(); requests.cancel(); workspaceDesktopQuery?.removeListener(syncWorkspaceSidebarForViewport); },
    cancel() { requests.cancel(); pickerRequestID++; workspaceReady = false; resetWorkspaceExplorer(); workspacePickerBackdrop.hidden = true; workspacePreviewContent.textContent = ""; },
  };
}
