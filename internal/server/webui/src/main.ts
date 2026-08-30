import "./styles.css";
import { errorMessage, parseJSONEvent, responseJSON } from "./api";
import { ATTACHMENT_LIMITS, showTemporaryError, takePendingAttachmentsForSend, validateFileAttachment, validateImageAttachment } from "./attachments";
import { complexTaskNotice, historyAssistantView, normalizeHistoryMessages } from "./chat-rendering";
import { formatConversationTime } from "./conversations";
import { byId, eventElement } from "./dom";
import { createIcon, iconMarkup, setIconButtonLabel } from "./icons";
import { renderMarkdown } from "./markdown";
import { waitForModelSelection } from "./model-selection";
import { initParticleField, redrawParticleField, refreshParticleCapability, refreshParticlePalette } from "./particles";
import { filterProviderModels, providerAPIKeyPlaceholder, providerListSubtitle, providerModelOptions, providerPayload, remainingActiveProvider, resolveDefaultModel, resolveProviderEditorIndex, selectedProviderModelIDs, toggleProviderModel, upsertProviderModel, type ProviderModelOption } from "./providers";
import { readJSONStorage } from "./storage";
import { SSEBuffer } from "./stream";
import { initialTheme } from "./theme";
import { shouldGroupToolCalls } from "./tool-groups";
import { loadWorkspaceSessions, migrateLegacySession, persistWorkspaceSession, workspaceURL } from "./workspace";
import type { SentFile, SentImage } from "./attachments";
import type { ConversationHistory, ConversationMessage, ConversationSummary, ConversationsResponse, GenerationMetrics, PendingFile, PendingImage, ProviderModelsResponse, ProviderSelectionResponse, ProviderSettings, ReasoningEffort, StatusResponse, ToolEventPayload, WorkspaceCurrentPreview, WorkspaceDirectoryPage, WorkspaceDirectoryResponse, WorkspaceDirectoryState, WorkspaceEntry, WorkspaceInfo, WorkspaceListResponse, WorkspacePreview, WorkspaceRoot, WorkspacesResponse, WebUIDoneEvent } from "./types";

(function () {
  "use strict";
  interface ToolCard {
    root: HTMLDivElement;
    status: HTMLSpanElement;
    arguments: HTMLPreElement;
    result: HTMLPreElement;
    state: "running" | "succeeded" | "failed";
  }
  type ToolCards = Record<string, ToolCard>;
  interface ToolGroup { root: HTMLDivElement; items: HTMLDivElement; status: HTMLSpanElement }
  interface ToolTimeline extends HTMLDivElement { toolGroup?: ToolGroup }
  const SESSION_KEY = "bqagent.webui.session";
  const WORKSPACE_KEY = "bqagent.webui.workspace";
  const WORKSPACE_SESSIONS_KEY = "bqagent.webui.workspace-sessions";
  const THEME_KEY = "bqagent.webui.theme";
  const WORKSPACE_SIDEBAR_KEY = "bqagent.webui.workspace-sidebar";
  const REASONING_EFFORT_KEY = "bqagent.webui.reasoning-effort";
  const REASONING_EFFORT_VALUES: ReasoningEffort[] = ["auto", "low", "medium", "high"];
  const REASONING_EFFORT_LABELS: Record<ReasoningEffort, string> = { auto: "自动", low: "低", medium: "中", high: "高" };
  let toolDisclosureSequence = 0;
  const thread = byId<HTMLDivElement>("thread");
  const main = byId<HTMLElement>("main");
  const appLayout = byId<HTMLDivElement>("app-layout");
  const conversationList = byId<HTMLDivElement>("conversation-list");
  const conversationNew = byId<HTMLButtonElement>("conversation-new");
  const conversationContextMenu = byId<HTMLDivElement>("conversation-context-menu");
  const conversationDelete = byId<HTMLButtonElement>("conversation-delete");
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
  const input = byId<HTMLTextAreaElement>("input");
  const attachmentTray = byId<HTMLDivElement>("attachment-tray");
  const attachmentError = byId<HTMLDivElement>("attachment-error");
  const attachmentActions = byId<HTMLDivElement>("attachment-actions");
  const addAttachmentBtn = byId<HTMLButtonElement>("add-attachment");
  const attachmentMenu = byId<HTMLDivElement>("attachment-menu");
  const uploadFileBtn = byId<HTMLButtonElement>("upload-file");
  const fileInput = byId<HTMLInputElement>("file-input");
  const serverFilePath = byId<HTMLInputElement>("server-file-path");
  const addServerPathBtn = byId<HTMLButtonElement>("add-server-path");
  const reasoningEffortControl = byId<HTMLDivElement>("reasoning-effort");
  const reasoningEffortToggle = byId<HTMLButtonElement>("reasoning-effort-toggle");
  const reasoningEffortMenu = byId<HTMLDivElement>("reasoning-effort-menu");
  const reasoningEffortLabel = byId<HTMLElement>("reasoning-effort-label");
  const reasoningEffortCurrent = byId<HTMLElement>("reasoning-effort-current");
  const reasoningEffortSlider = byId<HTMLDivElement>("reasoning-effort-slider");
  const reasoningEffortRange = byId<HTMLInputElement>("reasoning-effort-range");
  const reasoningEffortLabels = byId<HTMLDivElement>("reasoning-effort-labels");
  const sendBtn = byId<HTMLButtonElement>("send");
  const themeToggle = byId<HTMLButtonElement>("theme-toggle");
  const statusEl = byId<HTMLDivElement>("status");
  const modelSelect = byId<HTMLSelectElement>("model-select");
  const providerSettingsTrigger = byId<HTMLButtonElement>("provider-settings-trigger");
  const providerSettingsBackdrop = byId<HTMLDivElement>("provider-settings-backdrop");
  const providerSettingsClose = byId<HTMLButtonElement>("provider-settings-close");
  const providerSettingsCancel = byId<HTMLButtonElement>("provider-settings-cancel");
  const providerSettingsSave = byId<HTMLButtonElement>("provider-settings-save");
  const providerSettingsNew = byId<HTMLButtonElement>("provider-settings-new");
  const providerSettingsDelete = byId<HTMLButtonElement>("provider-settings-delete");
  const providerSettingsList = byId<HTMLDivElement>("provider-settings-list");
  const providerIDInput = byId<HTMLInputElement>("provider-id");
  const providerNameInput = byId<HTMLInputElement>("provider-name");
  const providerAPITypeInput = byId<HTMLSelectElement>("provider-api-type");
  const providerBaseURLInput = byId<HTMLInputElement>("provider-base-url");
  const providerAPIKeyInput = byId<HTMLInputElement>("provider-api-key");
  const providerAPIKeyToggle = byId<HTMLButtonElement>("provider-api-key-toggle");
  const providerAPIKeyStatus = byId<HTMLSpanElement>("provider-api-key-status");
  const providerModelsInput = byId<HTMLDivElement>("provider-models");
  const providerModelFilter = byId<HTMLInputElement>("provider-model-filter");
  const providerModelCount = byId<HTMLSpanElement>("provider-model-count");
  const providerFetchModels = byId<HTMLButtonElement>("provider-fetch-models");
  const providerModelManual = byId<HTMLInputElement>("provider-model-manual");
  const providerModelAdd = byId<HTMLButtonElement>("provider-model-add");
  const providerDefaultModelInput = byId<HTMLSelectElement>("provider-default-model");
  var runtimeModelLoadID = 0;
  var runtimeModelSelectionPromise: Promise<boolean> | null = null;
  var conversationLoadID = 0;
  var conversations: ConversationSummary[] = [];
  var conversationContextTarget: ConversationSummary | null = null;
  var providerSettingsState: ProviderSettings = { active_provider: "", providers: [] };
  var editingProviderIndex = -1;
  var providerCatalogModels: ProviderModelOption[] = [];
  var providerDeleteArmed = false;
  var sessionId = "";
  var currentWorkspace: WorkspaceInfo | null = null;
  var workspaceRoots: WorkspaceRoot[] = [];
  var workspaceSessions: Record<string, string> = Object.create(null) as Record<string, string>;
  var pickerRootID = "";
  var pickerPath = "";
  var pickerNextOffset: number | null = null;
  var workspaceReady = false;
  var reasoningEffort: ReasoningEffort = "auto";
  var busy = false;
  var currentTurnId = "";
  var currentController: AbortController | null = null;
  var stopRequested = false;
  var preparingSend = false;
  var pendingImages: PendingImage[] = [];
  var pendingImageReads: Promise<void>[] = [];
  var pendingFiles: PendingFile[] = [];
  var pendingFileReads: Promise<void>[] = [];
  var nextAttachmentID = 1;
  var attachmentErrorTimer: ReturnType<typeof setTimeout> | 0 = 0;
  var ATTACHMENT_ERROR_TIMEOUT_MS = 4000;
  var MAX_PENDING_FILES = ATTACHMENT_LIMITS.maxFiles;
  var MAX_PENDING_FILE_BYTES = ATTACHMENT_LIMITS.maxFileBytes;
  var MAX_PENDING_TOTAL_FILE_BYTES = ATTACHMENT_LIMITS.maxTotalFileBytes;
  var WORKSPACE_PREVIEW_IMAGE_TYPES: Record<string, boolean> = { "image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true };
  var workspaceDirectories: Record<string, WorkspaceDirectoryState> = Object.create(null) as Record<string, WorkspaceDirectoryState>;
  var workspaceExpanded: Record<string, boolean> = Object.create(null) as Record<string, boolean>;
  var workspaceTreeScrollTop = 0;
  var workspaceCurrentPreview: WorkspaceCurrentPreview | null = null;
  var workspacePreviewRequestID = 0;
  var workspaceRefreshInFlight = false;
  var workspaceDesktopQuery = window.matchMedia ? window.matchMedia("(min-width: 901px)") : null;
  workspaceExpanded[""] = true;

  function updateRuntimeModel(apiType: string, model: string, providerID?: string): void {
    if (!model) return;
    var matched = Array.prototype.find.call(modelSelect.options, function (option) { return option.dataset.providerId === (providerID || "") && option.dataset.model === model; });
    if (!matched) {
      matched = new Option(model, (providerID || "runtime") + "\u001f" + model);
      matched.dataset.providerId = providerID || "";
      matched.dataset.model = model;
      modelSelect.add(matched);
    }
    modelSelect.value = matched.value;
    modelSelect.title = (providerID || apiType || "llm") + " / " + model;
  }

  function renderProviderSelectors(): void {
    modelSelect.innerHTML = "";
    providerSettingsState.providers.forEach(function (provider) {
      var group = document.createElement("optgroup");
      group.label = provider.name;
      provider.models.forEach(function (model) {
        var option = new Option(model, provider.id + "\u001f" + model);
        option.dataset.providerId = provider.id;
        option.dataset.model = model;
        group.appendChild(option);
      });
      modelSelect.appendChild(group);
    });
    var active = providerSettingsState.providers.find(function (provider) { return provider.id === providerSettingsState.active_provider; });
    if (active) modelSelect.value = active.id + "\u001f" + active.default_model;
    modelSelect.disabled = modelSelect.options.length === 0;
  }

  async function loadProviderSettings(): Promise<void> {
    try {
      var response = await fetch(workspaceScopedURL("/api/v1/webui/providers"), { headers: { "Accept": "application/json" } });
      if (!response.ok) return;
      providerSettingsState = await responseJSON<ProviderSettings>(response);
      renderProviderSelectors();
    } catch (_) {}
  }

  async function selectRuntimeModel(): Promise<void> {
    var option = modelSelect.options[modelSelect.selectedIndex];
    if (!option || !option.dataset.providerId || !option.dataset.model) return;
    var response = await fetch(workspaceScopedURL("/api/v1/webui/provider-selection"), {
      method: "POST", headers: { "Content-Type": "application/json", "Accept": "application/json" },
      body: JSON.stringify({ provider_id: option.dataset.providerId, model: option.dataset.model, session_id: sessionId })
    });
    var payload = await responseJSON<ProviderSelectionResponse>(response);
    if (!response.ok) throw new Error(payload.error || "切换模型失败");
    providerSettingsState.active_provider = option.dataset.providerId;
    var active = providerSettingsState.providers.find(function (provider) { return provider.id === option.dataset.providerId; });
    if (active) active.default_model = option.dataset.model;
    updateRuntimeModel(payload.api_type, payload.model, payload.provider_id);
  }

  function beginRuntimeModelSelection() {
    modelSelect.disabled = true;
    var selection = selectRuntimeModel().then(function () {
      return true;
    }, function (error: unknown) {
      statusEl.textContent = errorMessage(error);
      statusEl.classList.add("error");
      loadRuntimeModel();
      return false;
    });
    runtimeModelSelectionPromise = selection;
    selection.then(function () {
      if (runtimeModelSelectionPromise !== selection) return;
      runtimeModelSelectionPromise = null;
      modelSelect.disabled = busy || modelSelect.options.length === 0;
    });
  }

  function selectedProviderModels(): string[] {
    return selectedProviderModelIDs(providerCatalogModels);
  }

  function refreshProviderDefaultModels(preferred = ""): void {
    var models = selectedProviderModels();
    var current = resolveDefaultModel(models, preferred);
    providerDefaultModelInput.innerHTML = "";
    if (!models.length) providerDefaultModelInput.add(new Option("请先启用模型", ""));
    models.forEach(function (model) { providerDefaultModelInput.add(new Option(model, model)); });
    providerDefaultModelInput.value = current;
    providerDefaultModelInput.disabled = models.length === 0;
    providerModelCount.textContent = models.length + " 已启用";
  }

  function renderProviderModelCatalog(): void {
    var visible = filterProviderModels(providerCatalogModels, providerModelFilter.value);
    providerModelsInput.innerHTML = "";
    if (!providerCatalogModels.length) {
      var empty = document.createElement("div");
      empty.className = "provider-model-empty";
      empty.textContent = "还没有模型。获取可用模型，或手工添加。";
      providerModelsInput.appendChild(empty);
      refreshProviderDefaultModels(providerDefaultModelInput.value);
      return;
    }
    if (!visible.length) {
      var unmatched = document.createElement("div");
      unmatched.className = "provider-model-empty";
      unmatched.textContent = "没有匹配的模型";
      providerModelsInput.appendChild(unmatched);
      refreshProviderDefaultModels(providerDefaultModelInput.value);
      return;
    }
    visible.forEach(function (item) {
      var row = document.createElement("button");
      row.type = "button";
      row.className = "provider-model-row" + (item.selected ? " selected" : "");
      row.setAttribute("role", "option");
      row.setAttribute("aria-selected", item.selected ? "true" : "false");
      row.dataset.model = item.id;
      var check = document.createElement("span");
      check.className = "provider-model-check";
      check.setAttribute("aria-hidden", "true");
      var label = document.createElement("code");
      label.textContent = item.id;
      row.appendChild(check);
      row.appendChild(label);
      providerModelsInput.appendChild(row);
    });
    refreshProviderDefaultModels(providerDefaultModelInput.value);
  }

  function setProviderAPIKeyVisible(visible: boolean): void {
    providerAPIKeyInput.type = visible ? "text" : "password";
    providerAPIKeyToggle.setAttribute("aria-pressed", visible ? "true" : "false");
    providerAPIKeyToggle.setAttribute("aria-label", visible ? "隐藏 API Key" : "显示 API Key");
    providerAPIKeyToggle.innerHTML = iconMarkup(visible ? "eye-off" : "eye");
  }

  function resetProviderDeleteArmed(): void {
    providerDeleteArmed = false;
    setIconButtonLabel(providerSettingsDelete, "trash-2", "删除");
    providerSettingsDelete.classList.remove("armed");
  }

  function createProviderNavItem(name: string, subtitle: string, index: number, selected: boolean, active: boolean): HTMLButtonElement {
    var button = document.createElement("button");
    button.type = "button";
    button.className = "provider-settings-item" + (selected ? " selected" : "");
    button.setAttribute("role", "option");
    button.setAttribute("aria-selected", selected ? "true" : "false");
    button.dataset.index = String(index);
    var title = document.createElement("strong");
    title.textContent = name;
    var meta = document.createElement("span");
    meta.textContent = subtitle;
    button.appendChild(title);
    button.appendChild(meta);
    if (active) {
      var badge = document.createElement("em");
      badge.textContent = "当前";
      button.appendChild(badge);
    }
    return button;
  }

  function syncProviderNavSelection(): void {
    Array.prototype.forEach.call(providerSettingsList.querySelectorAll(".provider-settings-item"), function (item: HTMLElement) {
      var selected = Number(item.dataset.index) === editingProviderIndex;
      item.classList.toggle("selected", selected);
      item.setAttribute("aria-selected", selected ? "true" : "false");
    });
  }

  function editProvider(index = -1): void {
    var provider = providerSettingsState.providers[index];
    editingProviderIndex = index;
    resetProviderDeleteArmed();
    setProviderAPIKeyVisible(false);
    providerSettingsDelete.disabled = index < 0;
    providerIDInput.value = provider ? provider.id : "";
    providerNameInput.value = provider ? provider.name : "";
    providerAPITypeInput.value = provider ? provider.api_type : "openai";
    providerBaseURLInput.value = provider ? provider.base_url || "" : "";
    providerAPIKeyInput.value = "";
    var configured = !!(provider && provider.api_key_configured);
    providerAPIKeyInput.placeholder = providerAPIKeyPlaceholder(configured);
    providerAPIKeyStatus.hidden = !configured;
    providerModelFilter.value = "";
    providerCatalogModels = providerModelOptions(provider ? provider.models : [], true);
    renderProviderModelCatalog();
    refreshProviderDefaultModels(provider ? provider.default_model : "");
    syncProviderNavSelection();
  }

  function renderProviderSettingsList(selected?: number): void {
    var index = resolveProviderEditorIndex(providerSettingsState.providers, providerSettingsState.active_provider, selected);
    providerSettingsList.innerHTML = "";
    if (index < 0) providerSettingsList.appendChild(createProviderNavItem("新 Provider", "填写后保存", -1, true, false));
    providerSettingsState.providers.forEach(function (provider, providerIndex) {
      providerSettingsList.appendChild(createProviderNavItem(provider.name || provider.id || "未命名", providerListSubtitle(provider), providerIndex, providerIndex === index, provider.id === providerSettingsState.active_provider));
    });
    if (!providerSettingsList.children.length) {
      var empty = document.createElement("div");
      empty.className = "provider-settings-list-empty";
      empty.textContent = "还没有 Provider";
      providerSettingsList.appendChild(empty);
    }
    editProvider(index);
  }

  async function openProviderSettings(): Promise<void> {
    await loadProviderSettings();
    renderProviderSettingsList();
    providerSettingsBackdrop.hidden = false;
    providerNameInput.focus();
  }

  function closeProviderSettings() {
    providerSettingsBackdrop.hidden = true;
    setProviderAPIKeyVisible(false);
    resetProviderDeleteArmed();
  }

  function addProviderModel(model: string, selected: boolean): void {
    providerCatalogModels = upsertProviderModel(providerCatalogModels, model, selected);
    renderProviderModelCatalog();
  }

  async function fetchAvailableProviderModels(): Promise<void> {
    providerFetchModels.disabled = true;
    setIconButtonLabel(providerFetchModels, "download", "获取中…");
    try {
      var response = await fetch(workspaceScopedURL("/api/v1/webui/provider-models"), {
        method: "POST", headers: { "Content-Type": "application/json", "Accept": "application/json" },
        body: JSON.stringify({ provider_id: providerIDInput.value.trim(), api_type: providerAPITypeInput.value, base_url: providerBaseURLInput.value.trim(), api_key: providerAPIKeyInput.value })
      });
      var payload = await responseJSON<ProviderModelsResponse>(response);
      if (!response.ok) throw new Error(payload.error || "获取模型失败");
      (payload.models || []).forEach(function (model) {
        providerCatalogModels = upsertProviderModel(providerCatalogModels, model, true);
      });
      renderProviderModelCatalog();
      statusEl.textContent = "已获取 " + (payload.models || []).length + " 个模型";
      statusEl.classList.remove("error");
    } finally {
      providerFetchModels.disabled = false;
      setIconButtonLabel(providerFetchModels, "download", "获取可用模型");
    }
  }

  async function saveProviderSettings(): Promise<void> {
    var models = selectedProviderModels();
    var provider = { id: providerIDInput.value.trim(), name: providerNameInput.value.trim(), api_type: providerAPITypeInput.value, base_url: providerBaseURLInput.value.trim(), api_key: providerAPIKeyInput.value, models: models, default_model: providerDefaultModelInput.value || models[0] || "" };
    var index = editingProviderIndex;
    var providers = providerSettingsState.providers.map(providerPayload);
    if (index >= 0) providers[index] = provider; else providers.push(provider);
    var response = await fetch(workspaceScopedURL("/api/v1/webui/providers"), { method: "PUT", headers: { "Content-Type": "application/json", "Accept": "application/json" }, body: JSON.stringify({ active_provider: provider.id, providers: providers }) });
    var payload = await responseJSON<ProviderSettings & { error?: string }>(response);
    if (!response.ok) throw new Error(payload.error || "保存 Provider 失败");
    providerSettingsState = payload;
    renderProviderSelectors();
    closeProviderSettings();
    statusEl.textContent = "Provider 设置已保存";
  }

  function setCurrentWorkspaceSession(value: string): void {
    sessionId = value || "";
    if (!currentWorkspace || !currentWorkspace.id) return;
    persistWorkspaceSession(WORKSPACE_SESSIONS_KEY, workspaceSessions, currentWorkspace.id, sessionId);
  }

  function workspaceScopedURL(url: string): string {
    return workspaceURL(url, currentWorkspace ? currentWorkspace.id : "");
  }

  function renderConversationList(): void {
    closeConversationContextMenu();
    conversationList.innerHTML = "";
    if (!conversations.length) {
      var empty = document.createElement("div");
      empty.className = "conversation-list-state";
      empty.textContent = "暂无历史会话";
      conversationList.appendChild(empty);
      return;
    }
    conversations.forEach(function (conversation) {
      var button = document.createElement("button");
      button.type = "button";
      button.className = "conversation-item" + (conversation.id === sessionId ? " active" : "");
      button.title = conversation.title;
      var title = document.createElement("span");
      title.className = "conversation-title";
      title.textContent = conversation.title || "新会话";
      var time = document.createElement("span");
      time.className = "conversation-time";
      time.textContent = formatConversationTime(conversation.updated_at);
      button.appendChild(title);
      button.appendChild(time);
      button.addEventListener("click", function () { openConversation(conversation.id); });
      button.addEventListener("contextmenu", function (event) { openConversationContextMenu(event, conversation); });
      conversationList.appendChild(button);
    });
  }

  function openConversationContextMenu(event: MouseEvent, conversation: ConversationSummary): void {
    event.preventDefault();
    if (busy) return;
    conversationContextTarget = conversation;
    conversationContextMenu.hidden = false;
    var rect = conversationContextMenu.getBoundingClientRect();
    var left = Math.max(8, Math.min(event.clientX, window.innerWidth - rect.width - 8));
    var top = Math.max(8, Math.min(event.clientY, window.innerHeight - rect.height - 8));
    conversationContextMenu.style.left = left + "px";
    conversationContextMenu.style.top = top + "px";
    conversationDelete.focus();
  }

  function closeConversationContextMenu(): void {
    conversationContextMenu.hidden = true;
    conversationContextTarget = null;
  }

  async function deleteConversation(conversation: ConversationSummary): Promise<void> {
    if (busy || !conversation || !conversation.id) return;
    if (!window.confirm("确定删除对话“" + (conversation.title || "新会话") + "”？此操作无法撤销。")) return;
    conversationDelete.disabled = true;
    try {
      var response = await fetch(workspaceScopedURL("/api/v1/webui/conversations/" + encodeURIComponent(conversation.id)), {
        method: "DELETE", headers: { "Accept": "application/json" }
      });
      var payload = await responseJSON<{ error?: string }>(response);
      if (!response.ok) throw new Error(payload.error || "删除对话失败");
      conversationLoadID++;
      conversations = conversations.filter(function (item) { return item.id !== conversation.id; });
      if (sessionId === conversation.id) {
        setCurrentWorkspaceSession("");
        clearPendingAttachments();
        thread.innerHTML = emptyMarkup();
        loadRuntimeModel();
      }
      renderConversationList();
      statusEl.textContent = "已删除对话";
      statusEl.classList.remove("error", "busy");
    } catch (error) {
      statusEl.textContent = errorMessage(error);
      statusEl.classList.add("error");
    } finally {
      conversationDelete.disabled = false;
    }
  }

  async function refreshConversations(loadCurrent: boolean): Promise<void> {
    var loadID = ++conversationLoadID;
    try {
      var response = await fetch(workspaceScopedURL("/api/v1/webui/conversations"), { headers: { "Accept": "application/json" } });
      var payload = await responseJSON<ConversationsResponse>(response);
      if (!response.ok) throw new Error(payload.error || "读取对话列表失败");
      if (loadID !== conversationLoadID) return;
      conversations = payload.conversations || [];
      renderConversationList();
      if (loadCurrent && sessionId && conversations.some(function (conversation) { return conversation.id === sessionId; })) await openConversation(sessionId);
    } catch (error) {
      if (loadID !== conversationLoadID) return;
      conversationList.innerHTML = '<div class="conversation-list-state">读取历史失败</div>';
    }
  }

  async function openConversation(id: string): Promise<void> {
    if (busy || !id) return;
    var loadID = ++conversationLoadID;
    statusEl.textContent = "正在读取历史";
    try {
      var response = await fetch(workspaceScopedURL("/api/v1/webui/conversations/" + encodeURIComponent(id)), { headers: { "Accept": "application/json" } });
      var payload = await responseJSON<ConversationHistory & { error?: string }>(response);
      if (!response.ok) throw new Error(payload.error || "读取历史失败");
      if (loadID !== conversationLoadID) return;
      setCurrentWorkspaceSession(payload.id || id);
      thread.innerHTML = "";
      normalizeHistoryMessages(payload.messages || []).forEach(function (message) {
        var bubble = addMessage(message.role);
        if (message.role === "assistant") {
          renderRestoredAssistant(bubble, message);
        } else {
          renderUserMessage(bubble, message.content, [], []);
        }
      });
      if (!payload.messages || !payload.messages.length) thread.innerHTML = emptyMarkup();
      renderConversationList();
      loadRuntimeModel();
      statusEl.textContent = "已恢复历史会话";
      statusEl.classList.remove("error");
      scrollToBottom();
    } catch (error) {
      statusEl.textContent = errorMessage(error);
      statusEl.classList.add("error");
    }
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
    currentWorkspace = info;
    workspaceCurrentName.textContent = info.name || "工作区";
    workspaceCurrentPath.textContent = info.path || "";
    workspaceCurrentPath.title = info.path || "";
    sessionId = workspaceSessions[info.id] || "";
    workspaceReady = true;
    input.disabled = false;
    sendBtn.disabled = false;
    workspaceSelect.disabled = false;
    workspaceCreateAgent.disabled = false;
    try {
      localStorage.setItem(WORKSPACE_KEY, JSON.stringify({ root_id: info.root_id, path: info.relative_path || "" }));
    } catch (_) {}
    if (switching && changed) {
      clearPendingAttachments();
      setAttachmentMenu(false);
      thread.innerHTML = emptyMarkup();
      statusEl.textContent = sessionId ? "已切换，继续上次会话" : "已切换工作区";
      statusEl.classList.remove("error", "busy");
    }
    resetWorkspaceExplorer();
    loadWorkspaceDirectory("", false, false);
    loadRuntimeModel();
    refreshConversations(true);
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
      } catch (_) {
        selected = response.default;
      }
    }
    migrateLegacySession(SESSION_KEY, WORKSPACE_SESSIONS_KEY, workspaceSessions, selected.id, response.default.id);
    applyWorkspace(selected, false);
  }

  async function createWorkspaceAgentConfig(): Promise<void> {
    if (busy || !currentWorkspace) return;
    workspaceCreateAgent.disabled = true;
    try {
      var result = await workspaceFetchJSON<{ created: boolean }>(workspaceScopedURL("/api/v1/webui/workspace/config"), { method: "POST", headers: { "Accept": "application/json" } });
      statusEl.textContent = result.created ? "已创建工作区 .agent 配置" : "工作区 .agent 配置已存在";
      statusEl.classList.remove("error", "busy");
      refreshWorkspaceTree();
    } catch (error) {
      statusEl.textContent = "创建 .agent 失败：" + errorMessage(error);
      statusEl.classList.add("error");
    } finally {
      workspaceCreateAgent.disabled = false;
    }
  }

  async function loadRuntimeModel(): Promise<void> {
    var loadID = ++runtimeModelLoadID;
    try {
      await loadProviderSettings();
      var url = workspaceScopedURL("/api/v1/status" + (sessionId ? "?session_id=" + encodeURIComponent(sessionId) : ""));
      var response = await fetch(url, { headers: { "Accept": "application/json" } });
      if (!response.ok) return;
      var payload = await responseJSON<StatusResponse>(response);
      if (loadID !== runtimeModelLoadID || !payload || !payload.llm) return;
      updateRuntimeModel(payload.llm.api_type, payload.llm.model, payload.llm.provider_id);
    } catch (_) {
      // Status display is best-effort and must never block chat startup.
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
    var response = await fetch(url, requestOptions);
    if (!response.ok) {
      var message = "HTTP " + response.status;
      try {
        var errorPayload = await responseJSON<{ error?: string }>(response);
        if (errorPayload.error) message = errorPayload.error;
      } catch (_) {}
      throw new Error(message);
    }
    return responseJSON<T>(response);
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
    } catch (error) {
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
        size.textContent = formatBytes(entry.size);
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
        });
      } else if (entry.type === "file") {
        row.addEventListener("click", function () { openWorkspacePreview(entry); });
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
      more.addEventListener("click", function () { loadWorkspaceDirectory(directoryPath, true, false); });
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
    var duplicate = Boolean(current && pendingFiles.some(function (file) {
      return file.kind === "path" && file.path === currentPath;
    }));
    var attachable = Boolean(current && current.payload && current.payload.attachable);
    var countLimited = Boolean(current && pendingFiles.length >= MAX_PENDING_FILES);
    var knownTotalBytes = pendingFiles.reduce(function (sum, file) { return sum + (Number(file.size) || 0); }, 0);
    var totalLimited = Boolean(current && current.payload && current.payload.size && knownTotalBytes + current.payload.size > MAX_PENDING_TOTAL_FILE_BYTES);
    workspacePreviewAttach.disabled = busy || !attachable || duplicate || countLimited || totalLimited;
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
    } else if (busy) {
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
    workspacePreviewTitle.textContent = payload.name || pathBaseName(payload.path);
    workspacePreviewPath.textContent = payload.path || workspaceCurrentPreview.path;
    workspacePreviewMeta.textContent = (payload.path || workspaceCurrentPreview.path) + "\n" + formatBytes(payload.size) + " · " + (payload.mime_type || "未知类型");
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
    } catch (error) {
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
    if (!pickerRootID) return;
    if (!append) {
      pickerNextOffset = 0;
      renderWorkspacePickerState("正在读取目录…", false);
    }
    var offset = append && typeof pickerNextOffset === "number" ? pickerNextOffset : 0;
    try {
      var payload = await workspaceFetchJSON<WorkspaceDirectoryResponse>("/api/v1/webui/workspaces/directories?root_id=" + encodeURIComponent(pickerRootID) + "&path=" + encodeURIComponent(pickerPath) + "&offset=" + offset);
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
        });
        workspacePickerList.appendChild(row);
      });
      if (!directories.length && !append && pickerNextOffset === null) renderWorkspacePickerState("当前目录没有子目录", false);
      if (typeof pickerNextOffset === "number") {
        var more = document.createElement("button");
        more.type = "button";
        more.className = "workspace-picker-row";
        more.textContent = "加载更多";
        more.addEventListener("click", function () { loadWorkspacePickerDirectories(true); });
        workspacePickerList.appendChild(more);
      }
    } catch (error) {
      renderWorkspacePickerState("读取失败：" + errorMessage(error), true);
    }
  }

  function openWorkspacePicker(): void {
    if (busy || !workspaceRoots.length) return;
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
    if (busy || !pickerRootID) return;
    workspacePickerConfirm.disabled = true;
    try {
      var info = await workspaceFetchJSON<WorkspaceInfo>("/api/v1/webui/workspaces/open", {
        method: "POST",
        headers: { "Accept": "application/json", "Content-Type": "application/json" },
        body: JSON.stringify({ root_id: pickerRootID, path: pickerPath })
      });
      closeWorkspacePicker();
      applyWorkspace(info, true);
      input.focus();
    } catch (error) {
      renderWorkspacePickerState("打开失败：" + errorMessage(error), true);
    } finally {
      workspacePickerConfirm.disabled = false;
    }
  }

  function initializeWorkspaceExplorer(): void {
    syncWorkspaceSidebarForViewport();
    if (workspaceDesktopQuery) {
      if (typeof workspaceDesktopQuery.addEventListener === "function") workspaceDesktopQuery.addEventListener("change", syncWorkspaceSidebarForViewport);
      else workspaceDesktopQuery.addListener(syncWorkspaceSidebarForViewport);
    }
  }

  function scrollToBottom(): void {
    main.scrollTop = main.scrollHeight;
  }

  function removeEmpty(): void {
    var empty = document.getElementById("empty");
    if (empty) empty.remove();
  }

  function addMessage(role: string): HTMLDivElement {
    removeEmpty();
    var msg = document.createElement("div");
    msg.className = "msg " + role + " msg-enter";
    var avatar = document.createElement("div");
    avatar.className = "avatar";
    avatar.setAttribute("aria-hidden", "true");
    if (role === "user") avatar.textContent = "你";
    else avatar.appendChild(createIcon("bot"));
    var stack = document.createElement("div");
    stack.className = "message-stack";
    var label = document.createElement("div");
    label.className = "message-label";
    label.textContent = role === "user" ? "你" : "bqagent";
    var bubble = document.createElement("div");
    bubble.className = "bubble";
    msg.appendChild(avatar);
    stack.appendChild(label);
    stack.appendChild(bubble);
    msg.appendChild(stack);
    thread.appendChild(msg);
    scrollToBottom();
    return bubble;
  }

  function renderUserMessage(bubble: HTMLDivElement, text: string, images: SentImage[], files: SentFile[]): void {
    bubble.textContent = "";
    if (text) {
      var textNode = document.createElement("div");
      textNode.className = "user-message-text";
      textNode.textContent = text;
      bubble.appendChild(textNode);
    }
    if (images && images.length) {
      var gallery = document.createElement("div");
      gallery.className = "user-images";
      images.forEach(function (image, index) {
        var preview = document.createElement("img");
        preview.className = "user-image";
        preview.src = "data:" + image.mime_type + ";base64," + image.data_base64;
        preview.alt = "已发送图片 " + (index + 1);
        gallery.appendChild(preview);
      });
      bubble.appendChild(gallery);
    }
    if (files && files.length) {
      var fileList = document.createElement("div");
      fileList.className = "user-files";
      files.forEach(function (file) {
        var chip = document.createElement("span");
        chip.className = "user-file";
        var icon = createFileIcon();
        icon.classList.remove("attachment-file-icon");
        var name = document.createElement("span");
        name.textContent = file.path ? pathBaseName(file.path) : (file.name || "");
        chip.appendChild(icon);
        chip.appendChild(name);
        fileList.appendChild(chip);
      });
      bubble.appendChild(fileList);
    }
  }

  function ensureToolTimeline(bubble: HTMLDivElement): ToolTimeline | null {
    var stack = bubble && bubble.parentElement;
    if (!stack) return null;
    var timeline = stack.querySelector<ToolTimeline>(".tool-timeline");
    if (!timeline) {
      timeline = document.createElement("div") as ToolTimeline;
      timeline.className = "tool-timeline";
      timeline.setAttribute("aria-label", "工具执行记录");
      stack.insertBefore(timeline, bubble);
    }
    return timeline;
  }

  function toolCardList(cards: ToolCards): ToolCard[] {
    return Object.keys(cards).map(function (id) { return cards[id]; });
  }

  function updateToolGroupSummary(timeline: ToolTimeline, cards: ToolCards): void {
    var group = timeline && timeline.toolGroup;
    if (!group) return;
    var cardList = toolCardList(cards);
    var running = 0;
    var failed = 0;
    cardList.forEach(function (card) {
      if (card.state === "running") running++;
      if (card.state === "failed") failed++;
    });
    group.root.classList.remove("succeeded", "failed");
    if (running === 0) group.root.classList.add(failed > 0 ? "failed" : "succeeded");
    if (running > 0) {
      group.status.textContent = cardList.length + " 次，" + running + " 运行中" + (failed > 0 ? "，" + failed + " 失败" : "");
    } else if (failed > 0) {
      group.status.textContent = cardList.length + " 次，" + failed + " 失败";
    } else {
      group.status.textContent = cardList.length + " 次，全部完成";
    }
  }

  function ensureToolGroup(timeline: ToolTimeline, cards: ToolCards): ToolGroup | undefined {
    var cardList = toolCardList(cards);
    if (timeline.toolGroup || !shouldGroupToolCalls(cardList.length)) {
      updateToolGroupSummary(timeline, cards);
      return timeline.toolGroup;
    }
    var root = document.createElement("div");
    root.className = "tool-group";
    var button = document.createElement("button");
    button.type = "button";
    button.className = "tool-group-toggle";
    var itemsID = "tool-group-items-" + (++toolDisclosureSequence);
    button.setAttribute("aria-expanded", "false");
    button.setAttribute("aria-controls", itemsID);
    var chevron = document.createElement("span");
    chevron.className = "tool-chevron";
    chevron.innerHTML = iconMarkup("chevron-right");
    var dot = document.createElement("span");
    dot.className = "tool-state-dot";
    var title = document.createElement("span");
    title.className = "tool-group-title";
    title.textContent = "工具调用";
    var status = document.createElement("span");
    status.className = "tool-status";
    status.setAttribute("role", "status");
    status.setAttribute("aria-live", "polite");
    status.setAttribute("aria-atomic", "true");
    var items = document.createElement("div");
    items.className = "tool-group-items";
    items.id = itemsID;
    button.appendChild(chevron);
    button.appendChild(dot);
    button.appendChild(title);
    button.appendChild(status);
    button.addEventListener("click", function () {
      var expanded = button.getAttribute("aria-expanded") === "true";
      button.setAttribute("aria-expanded", expanded ? "false" : "true");
      items.classList.toggle("open", !expanded);
    });
    root.appendChild(button);
    root.appendChild(items);
    timeline.appendChild(root);
    cardList.forEach(function (card) { items.appendChild(card.root); });
    timeline.toolGroup = { root: root, items: items, status: status };
    updateToolGroupSummary(timeline, cards);
    return timeline.toolGroup;
  }

  function updateToolTimeline(bubble: HTMLDivElement, cards: ToolCards, eventName: string, payload: ToolEventPayload): void {
    var timeline = ensureToolTimeline(bubble);
    if (!timeline || !payload) return;
    var cardID = payload.id || ("tool-" + (payload.seq || Object.keys(cards).length + 1));
    var card = cards[cardID];
    if (!card) {
      var root = document.createElement("div");
      root.className = "tool-card";
      var button = document.createElement("button");
      button.type = "button";
      button.className = "tool-toggle";
      var detailsID = "tool-details-" + (++toolDisclosureSequence);
      button.setAttribute("aria-expanded", "false");
      button.setAttribute("aria-controls", detailsID);
      var chevron = document.createElement("span");
      chevron.className = "tool-chevron";
      chevron.innerHTML = iconMarkup("chevron-right");
      var dot = document.createElement("span");
      dot.className = "tool-state-dot";
      var name = document.createElement("span");
      name.className = "tool-name";
      name.textContent = payload.name || "tool";
      var status = document.createElement("span");
      status.className = "tool-status";
      status.setAttribute("role", "status");
      status.setAttribute("aria-live", "polite");
      status.setAttribute("aria-atomic", "true");
      status.textContent = "运行中";
      button.appendChild(chevron);
      button.appendChild(dot);
      button.appendChild(name);
      button.appendChild(status);
      var details = document.createElement("div");
      details.className = "tool-details";
      details.id = detailsID;
      var argumentsSection = document.createElement("div");
      argumentsSection.className = "tool-section";
      var argumentsLabel = document.createElement("div");
      argumentsLabel.className = "tool-section-label";
      argumentsLabel.textContent = "arguments";
      var argumentsPre = document.createElement("pre");
      argumentsPre.className = "tool-json";
      argumentsPre.textContent = payload.arguments ? JSON.stringify(payload.arguments, null, 2) : "{}";
      argumentsSection.appendChild(argumentsLabel);
      argumentsSection.appendChild(argumentsPre);
      var resultSection = document.createElement("div");
      resultSection.className = "tool-section";
      var resultLabel = document.createElement("div");
      resultLabel.className = "tool-section-label";
      resultLabel.textContent = "result";
      var resultPre = document.createElement("pre");
      resultPre.className = "tool-preview";
      resultPre.textContent = "等待执行结果…";
      resultSection.appendChild(resultLabel);
      resultSection.appendChild(resultPre);
      details.appendChild(argumentsSection);
      details.appendChild(resultSection);
      button.addEventListener("click", function () {
        var expanded = button.getAttribute("aria-expanded") === "true";
        button.setAttribute("aria-expanded", expanded ? "false" : "true");
        details.classList.toggle("open", !expanded);
      });
      root.appendChild(button);
      root.appendChild(details);
      var group = timeline.toolGroup;
      (group ? group.items : timeline).appendChild(root);
      card = cards[cardID] = { root: root, status: status, arguments: argumentsPre, result: resultPre, state: "running" };
      ensureToolGroup(timeline, cards);
    }
    if (payload.arguments) card.arguments.textContent = JSON.stringify(payload.arguments, null, 2);
    if (eventName === "tool_result") {
      var failed = payload.status === "failed";
      card.root.classList.remove("succeeded", "failed");
      card.root.classList.add(failed ? "failed" : "succeeded");
      card.state = failed ? "failed" : "succeeded";
      var duration = payload.duration_ms ? "，" + payload.duration_ms + "ms" : "";
      var truncated = payload.truncated ? "，已截断" : "";
      card.status.textContent = (failed ? "失败" : "完成") + duration + truncated;
      card.result.textContent = payload.preview || (failed ? "工具执行失败" : "（无输出）");
    }
    updateToolGroupSummary(timeline, cards);
  }

  function clearAttachmentError(): void {
    if (attachmentErrorTimer) {
      clearTimeout(attachmentErrorTimer);
      attachmentErrorTimer = 0;
    }
    attachmentError.textContent = "";
  }

  function setAttachmentError(message: string): void {
    clearAttachmentError();
    attachmentErrorTimer = showTemporaryError(attachmentError, message || "", ATTACHMENT_ERROR_TIMEOUT_MS, function () {
      attachmentErrorTimer = 0;
    });
  }

  function formatBytes(bytes: number): string {
    if (!bytes) return "0 B";
    if (bytes < 1024) return bytes + " B";
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0) + " KiB";
    return (bytes / (1024 * 1024)).toFixed(1) + " MiB";
  }

  function pathBaseName(value: string): string {
    var parts = String(value || "").replace(/\\/g, "/").split("/");
    return parts[parts.length - 1] || value;
  }

  function createFileIcon(): SVGSVGElement {
    return createIcon("file", "attachment-file-icon");
  }

  function renderPendingAttachments(): void {
    attachmentTray.textContent = "";
    attachmentTray.hidden = pendingImages.length === 0 && pendingFiles.length === 0;
    pendingImages.forEach(function (image, index) {
      var item = document.createElement("div");
      item.className = "attachment-item" + (image.loading ? " loading" : "");
      var preview = document.createElement("img");
      preview.src = image.objectURL;
      preview.alt = "待发送图片 " + (index + 1);
      var remove = document.createElement("button");
      remove.type = "button";
      remove.className = "attachment-remove";
      remove.innerHTML = iconMarkup("x");
      remove.disabled = busy;
      remove.setAttribute("aria-label", "删除图片 " + (index + 1));
      remove.addEventListener("click", function () { removePendingImage(image.id); });
      var badge = document.createElement("span");
      badge.className = "attachment-badge";
      badge.textContent = image.loading ? "读取中" : image.mimeType.replace("image/", "");
      item.appendChild(preview);
      item.appendChild(remove);
      item.appendChild(badge);
      attachmentTray.appendChild(item);
    });
    pendingFiles.forEach(function (file) {
      var item = document.createElement("div");
      item.className = "attachment-file" + (file.loading ? " loading" : "");
      item.appendChild(createFileIcon());
      var copy = document.createElement("div");
      copy.className = "attachment-file-copy";
      var name = document.createElement("span");
      name.className = "attachment-file-name";
      name.textContent = file.name;
      name.title = file.path || file.name;
      var meta = document.createElement("span");
      meta.className = "attachment-file-meta";
      meta.textContent = file.loading ? "读取中" : (file.kind === "path" ? "服务器路径" + (file.size ? " · " + formatBytes(file.size) : "") : formatBytes(file.size));
      copy.appendChild(name);
      copy.appendChild(meta);
      var remove = document.createElement("button");
      remove.type = "button";
      remove.className = "attachment-remove";
      remove.innerHTML = iconMarkup("x");
      remove.disabled = busy;
      remove.setAttribute("aria-label", "删除文件 " + file.name);
      remove.addEventListener("click", function () { removePendingFile(file.id); });
      item.appendChild(copy);
      item.appendChild(remove);
      attachmentTray.appendChild(item);
    });
    updateWorkspacePreviewAttachState();
  }

  function removePendingImage(id: number): void {
    if (busy) return;
    pendingImages = pendingImages.filter(function (image) {
      if (image.id !== id) return true;
      URL.revokeObjectURL(image.objectURL);
      return false;
    });
    renderPendingAttachments();
  }

  function removePendingFile(id: number): void {
    if (busy) return;
    pendingFiles = pendingFiles.filter(function (file) { return file.id !== id; });
    renderPendingAttachments();
  }

  function clearPendingAttachments(): void {
    pendingImages.forEach(function (image) { URL.revokeObjectURL(image.objectURL); });
    pendingImages = [];
    pendingImageReads = [];
    pendingFiles = [];
    pendingFileReads = [];
    renderPendingAttachments();
    clearAttachmentError();
  }

  function readAsBase64(file: File, attachment: PendingImage | PendingFile, failureMessage: string): Promise<void> {
    return new Promise<void>(function (resolve, reject) {
      var reader = new FileReader();
      reader.onload = function () {
        var dataURL = String(reader.result || "");
        var comma = dataURL.indexOf(",");
        if (comma < 0) return reject(new Error(failureMessage));
        attachment.dataBase64 = dataURL.slice(comma + 1);
        attachment.loading = false;
        renderPendingAttachments();
        resolve();
      };
      reader.onerror = function () { reject(new Error(failureMessage)); };
      reader.readAsDataURL(file);
    });
  }

  function addClipboardImages(files: File[]): void {
    clearAttachmentError();
    var totalBytes = pendingImages.reduce(function (sum, image) { return sum + image.size; }, 0);
    files.forEach(function (file) {
      var validationError = validateImageAttachment(pendingImages.length, totalBytes, file);
      if (validationError) return setAttachmentError(validationError);
      totalBytes += file.size;
      var attachment: PendingImage = { id: nextAttachmentID++, mimeType: file.type, size: file.size, dataBase64: "", objectURL: URL.createObjectURL(file), loading: true };
      pendingImages.push(attachment);
      var read = readAsBase64(file, attachment, "读取图片失败").catch(function (error: unknown) {
        setAttachmentError(errorMessage(error));
        removePendingImage(attachment.id);
      });
      pendingImageReads.push(read);
    });
    renderPendingAttachments();
  }

  function addLocalFiles(files: FileList | File[]): void {
    clearAttachmentError();
    var totalBytes = pendingFiles.reduce(function (sum, file) { return sum + (Number(file.size) || 0); }, 0);
    Array.from(files).forEach(function (file) {
      var validationError = validateFileAttachment(pendingFiles.length, totalBytes, file.size);
      if (validationError) return setAttachmentError(validationError);
      totalBytes += file.size;
      var attachment: PendingFile = { id: nextAttachmentID++, kind: "upload", name: file.name || "未命名文件", size: file.size, dataBase64: "", loading: true };
      pendingFiles.push(attachment);
      var read = readAsBase64(file, attachment, "读取文件失败").catch(function (error: unknown) {
        setAttachmentError(errorMessage(error));
        removePendingFile(attachment.id);
      });
      pendingFileReads.push(read);
    });
    renderPendingAttachments();
  }

  function addWorkspacePath(value: string, size: number): boolean {
    if (busy) return false;
    clearAttachmentError();
    value = String(value || "").trim().replace(/\\/g, "/");
    if (!value) {
      setAttachmentError("请输入 workspace 内的文件路径");
      return false;
    }
    if (pendingFiles.some(function (file) { return file.kind === "path" && file.path === value; })) {
      setAttachmentError("该 workspace 文件已添加");
      updateWorkspacePreviewAttachState();
      return false;
    }
    if (pendingFiles.length >= MAX_PENDING_FILES) {
      setAttachmentError("每次最多添加 " + MAX_PENDING_FILES + " 个文件");
      return false;
    }
    size = Number(size) || 0;
    if (size > MAX_PENDING_FILE_BYTES) {
      setAttachmentError("单个文件不能超过 2 MiB");
      return false;
    }
    var knownTotalBytes = pendingFiles.reduce(function (sum, file) { return sum + (Number(file.size) || 0); }, 0);
    if (size && knownTotalBytes + size > MAX_PENDING_TOTAL_FILE_BYTES) {
      setAttachmentError("文件总大小不能超过 6 MiB");
      return false;
    }
    pendingFiles.push({ id: nextAttachmentID++, kind: "path", name: pathBaseName(value), path: value, size: size, loading: false });
    renderPendingAttachments();
    return true;
  }

  function addServerPath(): void {
    if (busy) return;
    var value = serverFilePath.value.trim();
    if (!addWorkspacePath(value, 0)) return;
    serverFilePath.value = "";
    setAttachmentMenu(false);
    input.focus();
  }

  function setAttachmentMenu(open: boolean): void {
    open = Boolean(open) && !busy;
    if (open) setReasoningEffortMenu(false);
    attachmentMenu.hidden = !open;
    addAttachmentBtn.setAttribute("aria-expanded", open ? "true" : "false");
    if (open) setTimeout(function () { uploadFileBtn.focus(); }, 0);
  }

  function normalizeReasoningEffort(value: unknown): ReasoningEffort {
    value = String(value || "").toLowerCase();
    return REASONING_EFFORT_VALUES.includes(value as ReasoningEffort) ? value as ReasoningEffort : "auto";
  }

  function setReasoningEffort(value: unknown, persist: boolean): void {
    reasoningEffort = normalizeReasoningEffort(value);
    var index = REASONING_EFFORT_VALUES.indexOf(reasoningEffort);
    var label = REASONING_EFFORT_LABELS[reasoningEffort];
    reasoningEffortRange.value = String(index);
    reasoningEffortRange.setAttribute("aria-valuetext", label);
    reasoningEffortToggle.setAttribute("aria-label", "推理强度：" + label);
    reasoningEffortLabel.textContent = label;
    reasoningEffortCurrent.textContent = label;
    reasoningEffortSlider.style.setProperty("--effort-fill", (index / (REASONING_EFFORT_VALUES.length - 1) * 100) + "%");
    Array.prototype.forEach.call(reasoningEffortLabels.children, function (item, itemIndex) {
      item.classList.toggle("active", itemIndex === index);
    });
    if (persist) localStorage.setItem(REASONING_EFFORT_KEY, reasoningEffort);
  }

  function setReasoningEffortMenu(open: boolean): void {
    open = Boolean(open) && !busy;
    if (open) setAttachmentMenu(false);
    reasoningEffortMenu.hidden = !open;
    reasoningEffortToggle.setAttribute("aria-expanded", open ? "true" : "false");
    if (open) setTimeout(function () { reasoningEffortRange.focus(); }, 0);
  }

  function clearStreamingState(bubble: HTMLDivElement | null): void {
    if (bubble) bubble.classList.remove("is-streaming");
  }

  function emptyMarkup(): string {
    return '' +
      '<div class="empty" id="empty">' +
      '<div class="empty-mark" aria-hidden="true">' + iconMarkup("bot") + '</div>' +
      '<div class="big"><span class="prompt-prefix">&gt;</span>今天想探索什么？</div>' +
      '<p>bqagent 已就绪，可以帮你拆解问题、写代码、查资料或整理复杂思路。</p>' +
      '<div class="prompts" aria-label="快捷提示">' +
      '<button class="prompt" type="button">' + iconMarkup("folder-tree") + '<strong>理解项目</strong><span>帮我梳理这个项目的结构</span></button>' +
      '<button class="prompt" type="button">' + iconMarkup("list-todo") + '<strong>制定计划</strong><span>把这个想法拆成执行步骤</span></button>' +
      '<button class="prompt" type="button">' + iconMarkup("code-xml") + '<strong>审查代码</strong><span>检查一段代码里的风险</span></button>' +
      '</div></div>';
  }

  function renderRestoredAssistant(bubble: HTMLDivElement, message: ConversationMessage): void {
    var view = historyAssistantView(message);
    if (view.tools.length) {
      var cards: ToolCards = {};
      view.tools.forEach(function (tool, index) {
        var payload: ToolEventPayload = {
          id: tool.id || ("history-tool-" + (index + 1)),
          name: tool.name,
          arguments: tool.arguments,
          status: tool.status,
          preview: tool.preview,
          truncated: tool.truncated,
        };
        updateToolTimeline(bubble, cards, "tool_call", payload);
        updateToolTimeline(bubble, cards, "tool_result", payload);
      });
    }
    if (view.content) {
      renderReply(bubble, view.content);
      bubble.classList.add("rendered");
      return;
    }
    if (view.tools.length) {
      bubble.hidden = true;
      bubble.classList.add("is-empty");
      return;
    }
    bubble.innerHTML = renderMarkdown(message.content || "");
    bubble.classList.add("rendered");
  }

  function renderReply(bubble: HTMLDivElement, reply: string): void {
    var notice = complexTaskNotice(reply);
    if (notice) {
      bubble.classList.add("notice");
      bubble.innerHTML = renderMarkdown(notice);
      return;
    }
    bubble.innerHTML = renderMarkdown(reply);
  }

  function setBusy(on: boolean): void {
    busy = on;
    sendBtn.disabled = !workspaceReady;
    sendBtn.classList.toggle("stop", on);
    sendBtn.title = on ? "停止生成" : "发送";
    sendBtn.setAttribute("aria-label", on ? "停止生成" : "发送");
    input.disabled = on || !workspaceReady;
    addAttachmentBtn.disabled = on;
    uploadFileBtn.disabled = on;
    serverFilePath.disabled = on;
    addServerPathBtn.disabled = on;
    reasoningEffortToggle.disabled = on;
    reasoningEffortRange.disabled = on;
    modelSelect.disabled = on || modelSelect.options.length === 0;
    providerSettingsTrigger.disabled = on;
    workspaceSelect.disabled = on || !workspaceReady;
    workspaceCreateAgent.disabled = on || !workspaceReady;
    if (on) {
      setAttachmentMenu(false);
      setReasoningEffortMenu(false);
    }
    if (on) {
      statusEl.textContent = "处理中";
      statusEl.classList.remove("error");
    } else if (!statusEl.classList.contains("error")) {
      statusEl.textContent = "在线";
    }
    statusEl.classList.toggle("busy", on);
    renderPendingAttachments();
    if (!on) input.focus();
  }

  function autoGrow(): void {
    input.style.height = "auto";
    input.style.height = Math.min(input.scrollHeight, 180) + "px";
  }

  function copyText(text: string): Promise<void> {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      return navigator.clipboard.writeText(text);
    }
    return new Promise<void>(function (resolve, reject) {
      var helper = document.createElement("textarea");
      helper.value = text;
      helper.style.position = "fixed";
      helper.style.opacity = "0";
      document.body.appendChild(helper);
      helper.select();
      try {
        document.execCommand("copy") ? resolve() : reject(new Error("copy failed"));
      } catch (error) {
        reject(error);
      } finally {
        helper.remove();
      }
    });
  }

  function addMessageMeta(bubble: HTMLDivElement, generation?: GenerationMetrics): void {
    var stack = bubble && bubble.parentElement;
    if (!stack) return;
    var previous = stack.querySelector(".message-meta");
    if (previous) previous.remove();
    if (!generation) return;

    var firstTokenLatency = Number(generation.first_token_latency_ms);
    var tokensPerSecond = Number(generation.tokens_per_second);
    var parts: string[] = [];
    if (isFinite(firstTokenLatency) && firstTokenLatency > 0) {
      parts.push("首字 " + Math.round(firstTokenLatency) + " ms");
    }
    var promptTokens = Number(generation.prompt_tokens);
    var cachedPromptTokens = Number(generation.cached_prompt_tokens || 0);
    var cacheMetrics = generation.cache_metrics || null;
    if (cacheMetrics && cacheMetrics.available === true && Number(cacheMetrics.input_tokens) > 0) {
      var cacheHitRate = Math.max(0, Math.min(100, Number(cacheMetrics.hit_rate || 0) * 100));
      parts.push("缓存命中 " + Math.round(cacheHitRate) + "%");
    } else if (generation.cache_usage_available === true && isFinite(promptTokens) && promptTokens > 0 && isFinite(cachedPromptTokens)) {
      var cacheHitRate = Math.max(0, Math.min(100, cachedPromptTokens * 100 / promptTokens));
      parts.push("缓存命中 " + Math.round(cacheHitRate) + "%");
    }
    if (isFinite(tokensPerSecond) && tokensPerSecond > 0) {
      parts.push("约 " + tokensPerSecond.toFixed(1).replace(/\.0$/, "") + " token/s");
    }
    if (!parts.length) return;

    var row = document.createElement("div");
    row.className = "message-meta";
    row.textContent = parts.join(" / ");
    row.setAttribute("aria-label", row.textContent);

    var details: string[] = [];
    var completionTokens = Number(generation.completion_tokens);
    var reasoningTokens = Number(generation.reasoning_tokens);
    var generationDuration = Number(generation.generation_duration_ms);
    if (isFinite(completionTokens) && completionTokens > 0) details.push("输出 " + Math.round(completionTokens) + " tokens");
    if (isFinite(reasoningTokens) && reasoningTokens > 0) details.push("其中推理 " + Math.round(reasoningTokens) + " tokens");
    if (cacheMetrics && cacheMetrics.available === true) {
      details.push("缓存汇总 " + Math.round(Number(cacheMetrics.calls || 0)) + " 次调用，输入 " + Math.round(Number(cacheMetrics.input_tokens || 0)) + " tokens，读取 " + Math.round(Number(cacheMetrics.cache_read_tokens || 0)) + "，写入 " + Math.round(Number(cacheMetrics.cache_write_tokens || 0)) + "，未缓存 " + Math.round(Number(cacheMetrics.uncached_input_tokens || 0)));
    } else if (generation.cache_usage_available === true && isFinite(promptTokens) && promptTokens > 0) details.push("输入 " + Math.round(promptTokens) + " tokens，缓存命中 " + Math.round(cachedPromptTokens) + " tokens");
    if (isFinite(generationDuration) && generationDuration > 0) details.push("生成区间 " + Math.round(generationDuration) + " ms");
    if (details.length) row.title = details.join(", ");
    stack.appendChild(row);
  }

  function addMessageControls(bubble: HTMLDivElement, runId: string, reply: string): void {
    var stack = bubble.parentElement;
    if (!stack) return;
    var previous = stack.querySelector(".message-actions");
    if (previous) previous.remove();
    var row = document.createElement("div");
    row.className = "message-actions";

    var copy = document.createElement("button");
    copy.type = "button";
    copy.className = "mini-action";
    setIconButtonLabel(copy, "copy", "复制");
    copy.title = "复制回复";
    copy.onclick = function () {
      copyText(reply || bubble.textContent || "").then(function () {
        setIconButtonLabel(copy, "check", "已复制");
        setTimeout(function () { setIconButtonLabel(copy, "copy", "复制"); }, 1200);
      });
    };
    row.appendChild(copy);

    if (!runId) {
      stack.appendChild(row);
      return;
    }
    ["up", "down"].forEach(function(rating) {
      var btn = document.createElement("button");
      btn.type = "button";
      btn.className = "mini-action";
      btn.appendChild(createIcon(rating === "up" ? "thumbs-up" : "thumbs-down"));
      btn.title = rating === "up" ? "有帮助" : "没有帮助";
      btn.onclick = async function() {
        try {
          await fetch(workspaceScopedURL("/api/v1/runs/" + encodeURIComponent(runId) + "/feedback"), {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ rating: rating })
          });
          row.querySelectorAll("button:not(:first-child)").forEach(function (item) { item.remove(); });
          var note = document.createElement("span");
          note.className = "feedback-note";
          note.textContent = "感谢反馈";
          row.appendChild(note);
        } catch (_) {}
      };
      row.appendChild(btn);
    });
    stack.appendChild(row);
  }

  function setTheme(theme: "dark" | "light"): void {
    document.documentElement.setAttribute("data-theme", theme);
    localStorage.setItem(THEME_KEY, theme);
    themeToggle.title = theme === "dark" ? "切换到浅色主题" : "切换到深色主题";
    var themeIcon = themeToggle.querySelector("use");
    if (themeIcon) themeIcon.setAttribute("href", theme === "dark" ? "#icon-sun" : "#icon-moon");
    var themeColor = document.querySelector('meta[name="theme-color"]');
    if (themeColor) themeColor.setAttribute("content", theme === "dark" ? "#030711" : "#ffffff");
    refreshParticlePalette();
    refreshParticleCapability();
    redrawParticleField();
  }

  function createTurnId(): string {
    if (window.crypto && typeof window.crypto.randomUUID === "function") {
      return window.crypto.randomUUID();
    }
    var random = Math.random().toString(36).slice(2);
    return "turn-" + Date.now().toString(36) + "-" + random;
  }

  async function stopCurrentTurn(): Promise<void> {
    if (!busy || !currentTurnId || stopRequested) return;
    stopRequested = true;
    sendBtn.disabled = true;
    statusEl.textContent = "正在停止";
    try {
      var response = await fetch("/api/v1/chat/stop", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ turn_id: currentTurnId, workspace_id: currentWorkspace ? currentWorkspace.id : "" })
      });
      if (!response.ok) throw new Error("HTTP " + response.status);
      var result = await responseJSON<{ stopped: boolean }>(response);
      if (!result.stopped && currentController) currentController.abort();
    } catch (_) {
      if (currentController) currentController.abort();
    }
  }

  async function send(): Promise<void> {
    if (!workspaceReady || busy || preparingSend) return;
    preparingSend = true;
    var pendingModelSelection = runtimeModelSelectionPromise;
    if (!await waitForModelSelection(pendingModelSelection)) {
      preparingSend = false;
      return;
    }
    try {
      await Promise.all(pendingImageReads.concat(pendingFileReads));
      pendingImageReads = [];
      pendingFileReads = [];
    } catch (_) {
      pendingImageReads = [];
      pendingFileReads = [];
      preparingSend = false;
      return;
    }
    var text = input.value.trim();
    var sentAttachments = takePendingAttachmentsForSend(pendingImages, pendingFiles);
    var sentImages = sentAttachments.images;
    var sentFiles = sentAttachments.files;
    if (!text && !sentImages.length && !sentFiles.length) {
      renderPendingAttachments();
      preparingSend = false;
      return;
    }

    currentTurnId = createTurnId();
    currentController = new AbortController();
    stopRequested = false;

    renderUserMessage(addMessage("user"), text, sentImages, sentFiles);
    input.value = "";
    autoGrow();
    renderPendingAttachments();
    clearAttachmentError();

    var bubble = addMessage("assistant");
    var toolCards = {};
    preparingSend = false;
    bubble.classList.add("is-streaming");
    var caret = document.createElement("span");
    caret.className = "caret";
    bubble.appendChild(caret);
    var streamed = "";
    var progress = "";

    setBusy(true);
    try {
      var res = await fetch("/api/v1/webui/chat", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          workspace_id: currentWorkspace ? currentWorkspace.id : "",
          message: text,
          images: sentImages,
          files: sentFiles,
          session_id: sessionId,
          turn_id: currentTurnId,
          reasoning_effort: reasoningEffort === "auto" ? "" : reasoningEffort
        }),
        signal: currentController.signal
      });
      if (!res.ok || !res.body) {
        var errorText = "";
        try { errorText = await res.text(); } catch (_) {}
        throw new Error("HTTP " + res.status + (errorText ? ": " + errorText : ""));
      }

      var reader = res.body.getReader();
      var decoder = new TextDecoder();
      var eventBuffer = new SSEBuffer();
      var finished = false;

      while (true) {
        var chunk = await reader.read();
        if (chunk.done) break;
        var events = eventBuffer.push(decoder.decode(chunk.value, { stream: true }));
        for (var eventIndex = 0; eventIndex < events.length; eventIndex++) {
          var evt = events[eventIndex];
          if (evt.event === "tool_start" || evt.event === "tool_result") {
            updateToolTimeline(bubble, toolCards, evt.event, parseJSONEvent<ToolEventPayload>(evt.data));
          } else if (evt.event === "done") {
            finished = true;
            clearStreamingState(bubble);
            var done = parseJSONEvent<WebUIDoneEvent>(evt.data);
            if (done.session_id) {
              setCurrentWorkspaceSession(done.session_id);
            }
            if (done.model) {
              runtimeModelLoadID++;
              updateRuntimeModel(done.api_type, done.model, providerSettingsState.active_provider);
            }
            refreshConversations(false);
            bubble.classList.add("rendered");
            var finalReply = typeof done.reply === "string" ? done.reply : "";
            if (!finalReply.trim()) {
              bubble.classList.add("error");
              bubble.textContent = "出错：服务端未返回有效回复";
              statusEl.textContent = "回复无效";
              statusEl.classList.remove("busy");
              statusEl.classList.add("error");
            } else {
              renderReply(bubble, finalReply);
              addMessageMeta(bubble, done.generation);
              addMessageControls(bubble, done.run_id || "", finalReply);
            }
          } else if (evt.event === "stopped") {
            finished = true;
            clearStreamingState(bubble);
            caret.remove();
            bubble.classList.add("rendered", "notice");
            var stoppedReply = streamed ? streamed + "\n\n> 已停止生成。" : "已停止生成。";
            renderReply(bubble, stoppedReply);
            addMessageControls(bubble, "", streamed);
            statusEl.textContent = "已停止";
            statusEl.classList.remove("busy", "error");
          } else if (evt.event === "error") {
            finished = true;
            clearStreamingState(bubble);
            var errPayload = parseJSONEvent<{ error?: string }>(evt.data);
            bubble.classList.add("error");
            bubble.textContent = "出错：" + (errPayload.error || "未知错误");
            statusEl.textContent = "请求失败";
            statusEl.classList.remove("busy");
            statusEl.classList.add("error");
          } else if (evt.event === "progress") {
            var progressPayload = parseJSONEvent<{ message?: string }>(evt.data);
            if (progressPayload.message && !streamed) {
              progress = progressPayload.message;
              caret.remove();
              bubble.classList.add("notice");
              bubble.textContent = progress;
              bubble.appendChild(caret);
            }
          } else {
            var payload = parseJSONEvent<{ delta?: string }>(evt.data);
            if (payload.delta) {
              streamed += payload.delta;
              caret.remove();
              bubble.classList.remove("notice");
              bubble.textContent = streamed;
              bubble.appendChild(caret);
            }
          }
          scrollToBottom();
        }
      }
      eventBuffer.push(decoder.decode());
      finished = finished || eventBuffer.terminal;

      if (!finished) {
        clearStreamingState(bubble);
        caret.remove();
        bubble.classList.add("rendered", "error");
        var incompleteReply = streamed
          ? streamed + "\n\n> 响应在完成事件到达前中断。"
          : "出错：响应未正常完成。";
        renderReply(bubble, incompleteReply);
        statusEl.textContent = "响应中断";
        statusEl.classList.remove("busy");
        statusEl.classList.add("error");
      }
    } catch (err) {
      clearStreamingState(bubble);
      caret.remove();
      if (stopRequested || (err instanceof Error && err.name === "AbortError")) {
        bubble.classList.add("rendered", "notice");
        var partialReply = streamed ? streamed + "\n\n> 已停止生成。" : "已停止生成。";
        renderReply(bubble, partialReply);
        addMessageControls(bubble, "", streamed);
      } else {
        bubble.classList.add("error");
        bubble.textContent = "出错：" + errorMessage(err);
        statusEl.textContent = "连接失败";
        statusEl.classList.remove("busy");
        statusEl.classList.add("error");
      }
    } finally {
      clearStreamingState(bubble);
      setBusy(false);
      currentTurnId = "";
      currentController = null;
      stopRequested = false;
      scrollToBottom();
      refreshWorkspaceTree();
    }
  }

  function newChat(): void {
    if (busy) return;
    setCurrentWorkspaceSession("");
    clearPendingAttachments();
    setAttachmentMenu(false);
    setReasoningEffortMenu(false);
    thread.innerHTML = emptyMarkup();
    renderConversationList();
    statusEl.textContent = "在线";
    statusEl.classList.remove("error", "busy");
    loadRuntimeModel();
    input.focus();
  }

  sendBtn.addEventListener("click", function () {
    if (busy) stopCurrentTurn(); else send();
  });
  conversationNew.addEventListener("click", newChat);
  providerSettingsTrigger.addEventListener("click", function () {
    openProviderSettings().catch(function (error) {
      statusEl.textContent = errorMessage(error);
      statusEl.classList.add("error");
    });
  });
  providerSettingsClose.addEventListener("click", closeProviderSettings);
  providerSettingsCancel.addEventListener("click", closeProviderSettings);
  providerSettingsBackdrop.addEventListener("click", function (event) { if (event.target === providerSettingsBackdrop) closeProviderSettings(); });
  providerSettingsList.addEventListener("click", function (event) {
    var item = eventElement(event.target)?.closest(".provider-settings-item") as HTMLElement | null;
    if (!item || !providerSettingsList.contains(item) || item.dataset.index == null) return;
    var index = Number(item.dataset.index);
    if (index === editingProviderIndex) return;
    renderProviderSettingsList(index);
  });
  providerModelsInput.addEventListener("click", function (event) {
    var row = eventElement(event.target)?.closest(".provider-model-row") as HTMLElement | null;
    if (!row || !providerModelsInput.contains(row) || !row.dataset.model) return;
    providerCatalogModels = toggleProviderModel(providerCatalogModels, row.dataset.model, row.getAttribute("aria-selected") !== "true");
    renderProviderModelCatalog();
  });
  providerModelFilter.addEventListener("input", function () { renderProviderModelCatalog(); });
  providerAPIKeyToggle.addEventListener("click", function () {
    setProviderAPIKeyVisible(providerAPIKeyInput.type === "password");
  });
  providerFetchModels.addEventListener("click", function () {
    fetchAvailableProviderModels().catch(function (error: unknown) { statusEl.textContent = errorMessage(error); statusEl.classList.add("error"); });
  });
  providerModelAdd.addEventListener("click", function () {
    addProviderModel(providerModelManual.value, true);
    providerModelManual.value = "";
    providerModelManual.focus();
  });
  providerModelManual.addEventListener("keydown", function (event) {
    if (event.key === "Enter") { event.preventDefault(); providerModelAdd.click(); }
  });
  providerSettingsNew.addEventListener("click", function () {
    if (editingProviderIndex < 0) {
      providerNameInput.focus();
      return;
    }
    renderProviderSettingsList(-1);
    providerNameInput.focus();
  });
  providerSettingsDelete.addEventListener("click", async function () {
    var index = editingProviderIndex;
    if (index < 0) { renderProviderSettingsList(); return; }
    if (!providerDeleteArmed) {
      providerDeleteArmed = true;
      setIconButtonLabel(providerSettingsDelete, "trash-2", "确认删除");
      providerSettingsDelete.classList.add("armed");
      return;
    }
    var providers = providerSettingsState.providers.filter(function (_, providerIndex) { return providerIndex !== index; }).map(providerPayload);
    var active = remainingActiveProvider(providers, providerSettingsState.active_provider);
    try {
      var response = await fetch(workspaceScopedURL("/api/v1/webui/providers"), { method: "PUT", headers: { "Content-Type": "application/json", "Accept": "application/json" }, body: JSON.stringify({ active_provider: active, providers: providers }) });
      var payload = await responseJSON<ProviderSettings & { error?: string }>(response);
      if (!response.ok) throw new Error(payload.error || "删除 Provider 失败");
      providerSettingsState = payload;
      renderProviderSelectors();
      renderProviderSettingsList();
    } catch (error) { statusEl.textContent = errorMessage(error); statusEl.classList.add("error"); }
  });
  providerSettingsSave.addEventListener("click", function () {
    providerSettingsSave.disabled = true;
    saveProviderSettings().catch(function (error: unknown) { statusEl.textContent = errorMessage(error); statusEl.classList.add("error"); }).finally(function () { providerSettingsSave.disabled = false; });
  });
  modelSelect.addEventListener("change", beginRuntimeModelSelection);
  workspaceSelect.addEventListener("click", openWorkspacePicker);
  workspaceCreateAgent.addEventListener("click", createWorkspaceAgentConfig);
  workspaceToggle.addEventListener("click", function () {
    setWorkspaceSidebarOpen(workspaceToggle.getAttribute("aria-expanded") !== "true", true);
  });
  workspaceClose.addEventListener("click", function () { setWorkspaceSidebarOpen(false, true); });
  workspacePreviewClose.addEventListener("click", function () { setWorkspaceSidebarOpen(false, true); });
  workspaceBackdrop.addEventListener("click", function () { setWorkspaceSidebarOpen(false, false); });
  workspaceRefresh.addEventListener("click", refreshWorkspaceTree);
  workspacePreviewBack.addEventListener("click", showWorkspaceTreeView);
  workspacePreviewRefresh.addEventListener("click", loadWorkspacePreview);
  workspacePreviewAttach.addEventListener("click", function () {
    if (!workspaceCurrentPreview || !workspaceCurrentPreview.payload) return;
    addWorkspacePath(workspaceCurrentPreview.path, workspaceCurrentPreview.payload.size);
    updateWorkspacePreviewAttachState();
  });
  workspacePickerClose.addEventListener("click", closeWorkspacePicker);
  workspacePickerCancel.addEventListener("click", closeWorkspacePicker);
  workspacePickerConfirm.addEventListener("click", confirmWorkspacePicker);
  workspacePickerBackdrop.addEventListener("click", function (event) {
    if (event.target === workspacePickerBackdrop) closeWorkspacePicker();
  });
  workspacePickerRoot.addEventListener("change", function () {
    pickerRootID = workspacePickerRoot.value;
    pickerPath = "";
    loadWorkspacePickerDirectories(false);
  });
  workspacePickerUp.addEventListener("click", function () {
    if (!pickerPath) return;
    var parts = pickerPath.split("/");
    parts.pop();
    pickerPath = parts.join("/");
    loadWorkspacePickerDirectories(false);
  });
  addAttachmentBtn.addEventListener("click", function () {
    setAttachmentMenu(Boolean(attachmentMenu.hidden));
  });
  reasoningEffortToggle.addEventListener("click", function () {
    setReasoningEffortMenu(Boolean(reasoningEffortMenu.hidden));
  });
  reasoningEffortRange.addEventListener("input", function () {
    var index = Math.max(0, Math.min(REASONING_EFFORT_VALUES.length - 1, Number(reasoningEffortRange.value) || 0));
    setReasoningEffort(REASONING_EFFORT_VALUES[index], true);
  });
  uploadFileBtn.addEventListener("click", function () {
    setAttachmentMenu(false);
    fileInput.click();
  });
  fileInput.addEventListener("change", function () {
    if (fileInput.files && fileInput.files.length) addLocalFiles(fileInput.files);
    fileInput.value = "";
  });
  addServerPathBtn.addEventListener("click", addServerPath);
  serverFilePath.addEventListener("keydown", function (event) {
    if (event.key === "Enter") {
      event.preventDefault();
      addServerPath();
    } else if (event.key === "Escape") {
      setAttachmentMenu(false);
      addAttachmentBtn.focus();
    }
  });
  document.addEventListener("click", function (event) {
    var target = eventElement(event.target);
    if (!attachmentMenu.hidden && !attachmentActions.contains(target)) setAttachmentMenu(false);
    if (!reasoningEffortMenu.hidden && !reasoningEffortControl.contains(target)) setReasoningEffortMenu(false);
    if (!conversationContextMenu.hidden && !conversationContextMenu.contains(target)) closeConversationContextMenu();
  });
  document.addEventListener("keydown", function (event) {
    if (event.key === "Escape" && !conversationContextMenu.hidden) {
      closeConversationContextMenu();
    } else if (event.key === "Escape" && !attachmentMenu.hidden) {
      setAttachmentMenu(false);
      addAttachmentBtn.focus();
    } else if (event.key === "Escape" && !reasoningEffortMenu.hidden) {
      setReasoningEffortMenu(false);
      reasoningEffortToggle.focus();
    } else if (event.key === "Escape" && !workspacePickerBackdrop.hidden) {
      closeWorkspacePicker();
    } else if (event.key === "Escape" && !providerSettingsBackdrop.hidden) {
      closeProviderSettings();
    } else if (event.key === "Escape" && !isWorkspaceDesktop() && workspaceToggle.getAttribute("aria-expanded") === "true") {
      setWorkspaceSidebarOpen(false, false);
      workspaceToggle.focus();
    }
  });
  conversationDelete.addEventListener("click", function (event) {
    event.stopPropagation();
    var conversation = conversationContextTarget;
    closeConversationContextMenu();
    if (conversation) deleteConversation(conversation);
  });
  window.addEventListener("resize", closeConversationContextMenu, { passive: true });
  conversationList.addEventListener("scroll", closeConversationContextMenu, { passive: true });
  themeToggle.addEventListener("click", function () {
    var current = document.documentElement.getAttribute("data-theme") || "dark";
    setTheme(current === "dark" ? "light" : "dark");
  });
  thread.addEventListener("click", function (e) {
    var target = eventElement(e.target);
    var copyButton = target ? target.closest<HTMLButtonElement>(".copy-code") : null;
    if (copyButton) {
      var activeCopyButton = copyButton;
      var codeBlock = copyButton.closest(".code-block");
      var code = codeBlock ? codeBlock.querySelector("code") : null;
      copyText(code ? code.textContent : "").then(function () {
        setIconButtonLabel(activeCopyButton, "check", "已复制");
        setTimeout(function () { setIconButtonLabel(activeCopyButton, "copy", "复制"); }, 1200);
      });
      return;
    }
    var prompt = target ? target.closest<HTMLButtonElement>(".prompt") : null;
    if (prompt) {
      var detail = prompt.querySelector("span");
      input.value = detail ? detail.textContent : (prompt.textContent || "");
      autoGrow();
      input.focus();
    }
  });
  input.addEventListener("input", autoGrow);
  input.addEventListener("paste", function (event) {
    if (busy || !event.clipboardData || !event.clipboardData.items) return;
    var files: File[] = [];
    Array.prototype.forEach.call(event.clipboardData.items, function (item) {
      if (item.kind === "file" && item.type.indexOf("image/") === 0) {
        var file = item.getAsFile();
        if (file) files.push(file);
      }
    });
    if (!files.length) return;
    event.preventDefault();
    var pastedText = event.clipboardData.getData("text/plain");
    if (pastedText) {
      input.setRangeText(pastedText, input.selectionStart, input.selectionEnd, "end");
      autoGrow();
    }
    addClipboardImages(files);
  });
  input.addEventListener("keydown", function (e) {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      send();
    }
  });
  window.addEventListener("beforeunload", function () {
    pendingImages.forEach(function (image) { URL.revokeObjectURL(image.objectURL); });
  });
  setReasoningEffort(localStorage.getItem(REASONING_EFFORT_KEY), false);
  setTheme(initialTheme(localStorage.getItem(THEME_KEY),
    Boolean(window.matchMedia && window.matchMedia("(prefers-color-scheme: light)").matches)));
  initializeWorkspaceExplorer();
  input.disabled = true;
  sendBtn.disabled = true;
  workspaceSelect.disabled = true;
  workspaceCreateAgent.disabled = true;
  initParticleField();
  initializeWorkspaceSelection().catch(function (error) {
    statusEl.textContent = "工作区初始化失败";
    statusEl.classList.add("error");
    workspaceTreeStatus.textContent = errorMessage(error);
    workspaceTreeStatus.classList.add("error");
  });
  autoGrow();
})();
