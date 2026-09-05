import "@awesome.me/webawesome/dist/styles/themes/default.css";
import "@awesome.me/webawesome/dist/components/select/select.js";
import "./styles.css";
import { errorMessage, parseJSONEvent } from "./api";
import { acpPermissionTitle, formatACPPermissionInput, isRejectPermissionOption } from "./acp-permission";
import { ATTACHMENT_LIMITS, showTemporaryError, takePendingAttachmentsForSend, validateFileAttachment, validateImageAttachment } from "./attachments";
import { complexTaskNotice, historyAssistantView, normalizeHistoryMessages } from "./chat-rendering";
import { chatModePlaceholder, normalizeChatMode } from "./chat-mode";
import { formatConversationTime } from "./conversations";
import { addableGroupParticipants, canRemoveGroupParticipant, groupMentionQuery, matchingGroupParticipants, normalizeConversationType, replaceGroupMention, shouldCloseCoordinatorSegment, shouldRenderFinalReply, type MentionQuery } from "./group-chat";
import { byId, eventElement } from "./dom";
import { createIcon, iconMarkup, setIconButtonLabel } from "./icons";
import { renderMarkdown } from "./markdown";
import { waitForModelSelection } from "./model-selection";
import { initParticleField, redrawParticleField, refreshParticleCapability, refreshParticlePalette } from "./particles";
import { SSEBuffer } from "./stream";
import { initialTheme } from "./theme";
import { shouldGroupToolCalls } from "./tool-groups";
import type { SentFile, SentImage } from "./attachments";
import type { ACPPermissionPayload, ChatMode, ConversationHistory, ConversationMessage, ConversationSummary, ConversationsResponse, ConversationType, GenerationMetrics, GroupEventPayload, GroupInfo, PendingFile, PendingImage, ReasoningEffort, ToolEventPayload, WebUIDoneEvent } from "./types";
import { APIClient, isCancellation } from "./api-client";
import { createAuthController } from "./auth-controller";
import { createProviderController } from "./provider-controller";
import { createWorkspaceController } from "./workspace-controller";
import { createDoctorController } from "./doctor-controller";
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
  const THEME_KEY = "bqagent.webui.theme";
  const REASONING_EFFORT_KEY = "bqagent.webui.reasoning-effort";
  const REASONING_EFFORT_VALUES: ReasoningEffort[] = ["auto", "low", "medium", "high"];
  const REASONING_EFFORT_LABELS: Record<ReasoningEffort, string> = { auto: "自动", low: "低", medium: "中", high: "高" };
  const thread = byId<HTMLDivElement>("thread");
  const main = byId<HTMLElement>("main");
  const conversationList = byId<HTMLDivElement>("conversation-list");
  const conversationNew = byId<HTMLButtonElement>("conversation-new");
  const conversationNewWrap = byId<HTMLDivElement>("conversation-new-wrap");
  const conversationNewMenu = byId<HTMLDivElement>("conversation-new-menu");
  const conversationContextMenu = byId<HTMLDivElement>("conversation-context-menu");
  const conversationDelete = byId<HTMLButtonElement>("conversation-delete");
  const input = byId<HTMLTextAreaElement>("input");
  const groupBar = byId<HTMLDivElement>("group-bar");
  const groupParticipants = byId<HTMLDivElement>("group-participants");
  const groupMemberControls = byId<HTMLDivElement>("group-member-controls");
  const groupAddWrap = byId<HTMLDivElement>("group-add-wrap");
  const groupAddBtn = byId<HTMLButtonElement>("group-add");
  const groupAddMenu = byId<HTMLDivElement>("group-add-menu");
  const mentionMenu = byId<HTMLDivElement>("mention-menu");
  const attachmentTray = byId<HTMLDivElement>("attachment-tray");
  const attachmentError = byId<HTMLDivElement>("attachment-error");
  const attachmentActions = byId<HTMLDivElement>("attachment-actions");
  const addAttachmentBtn = byId<HTMLButtonElement>("add-attachment");
  const attachmentMenu = byId<HTMLDivElement>("attachment-menu");
  const modeRunBtn = byId<HTMLButtonElement>("mode-run");
  const modeAskBtn = byId<HTMLButtonElement>("mode-ask");
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
  var conversationLoadID = 0;
  var conversations: ConversationSummary[] = [];
  var conversationContextTarget: ConversationSummary | null = null;
  var sessionId = "";
  var reasoningEffort: ReasoningEffort = "auto";
  var chatMode: ChatMode = "run";
  var conversationType: ConversationType = "default";
  var currentGroup: GroupInfo | null = null;
  var mentionQuery: MentionQuery | null = null;
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

  const api = new APIClient();
  let toolDisclosureSequence = 0;
  const providerController = createProviderController(api, {
    get statusEl() { return statusEl; },
    get sessionId() { return sessionId; },
    set sessionId(value) { sessionId = value; },
    get busy() { return busy; },
    workspaceScopedURL: (...args) => workspaceController.workspaceScopedURL(...args),
    setChatMode: (...args) => setChatMode(...args),
  });
  const workspaceController = createWorkspaceController(api, {
    onSwitch() { api.abort(); providerController.cancel(); conversationLoadID++; },
    get thread() { return thread; },
    get input() { return input; },
    get sendBtn() { return sendBtn; },
    get statusEl() { return statusEl; },
    get sessionId() { return sessionId; },
    set sessionId(value) { sessionId = value; },
    get busy() { return busy; },
    get pendingFiles() { return pendingFiles; },
    get MAX_PENDING_FILES() { return MAX_PENDING_FILES; },
    get MAX_PENDING_TOTAL_FILE_BYTES() { return MAX_PENDING_TOTAL_FILE_BYTES; },
    setConversationType: (...args) => setConversationType(...args),
    refreshConversations: (...args) => refreshConversations(...args),
    loadRuntimeModel: (...args) => providerController.loadRuntimeModel(...args),
    formatBytes: (...args) => formatBytes(...args),
    pathBaseName: (...args) => pathBaseName(...args),
    clearPendingAttachments: (...args) => clearPendingAttachments(...args),
    addWorkspacePath: (...args) => addWorkspacePath(...args),
    setAttachmentMenu: (...args) => setAttachmentMenu(...args),
    setChatMode: (...args) => setChatMode(...args),
    emptyMarkup: (...args) => emptyMarkup(...args),
  });
  let explorerInitialized = false;
  const doctorController = createDoctorController(api, url => workspaceController.workspaceScopedURL(url));
  const auth = createAuthController(api, async () => {
    if (!explorerInitialized) { workspaceController.initializeWorkspaceExplorer(); initParticleField(); explorerInitialized = true; }
    input.disabled = true; sendBtn.disabled = true;
    await workspaceController.initializeWorkspaceSelection(); autoGrow();
  }, () => {
    doctorController.cancel(); providerController.cancel(); workspaceController.cancel(); currentController?.abort(); conversationLoadID++;
    clearPendingAttachments(); thread.textContent = ""; conversationList.textContent = ""; input.value = "";
    input.disabled = true; sendBtn.disabled = true;
  });

  async function loadAvailableGroup(): Promise<GroupInfo> {
    var response = await api.request(workspaceController.workspaceScopedURL("/api/v1/webui/group/participants"), { headers: { "Accept": "application/json" } });
    var payload = await api.readJSON<GroupInfo & { error?: string }>(response);
    if (!response.ok) throw new Error(payload.error || "读取群聊成员失败");
    return payload;
  }

  function closeGroupAddMenu(): void {
    groupAddMenu.hidden = true;
    groupAddBtn.setAttribute("aria-expanded", "false");
  }

  function renderGroupAddMenu(available: GroupInfo): void {
    var candidates = addableGroupParticipants(available, currentGroup);
    groupAddMenu.textContent = "";
    if (!candidates.length) {
      var empty = document.createElement("div");
      empty.className = "group-add-empty";
      empty.textContent = "暂无可添加成员";
      groupAddMenu.appendChild(empty);
      return;
    }
    candidates.forEach(function (participant) {
      var button = document.createElement("button");
      button.type = "button";
      button.className = "group-add-option";
      button.setAttribute("role", "menuitem");
      var name = document.createElement("strong");
      name.textContent = "@" + participant.id;
      var transport = document.createElement("span");
      transport.textContent = participant.transport || participant.kind;
      button.appendChild(name);
      button.appendChild(transport);
      button.addEventListener("click", function () { addGroupParticipant(participant.id); });
      groupAddMenu.appendChild(button);
    });
  }

  async function toggleGroupAddMenu(): Promise<void> {
    if (busy || conversationType !== "group" || !sessionId) return;
    if (!groupAddMenu.hidden) {
      closeGroupAddMenu();
      return;
    }
    groupAddBtn.disabled = true;
    try {
      var available = await loadAvailableGroup();
      renderGroupAddMenu(available);
      groupAddMenu.hidden = false;
      groupAddBtn.setAttribute("aria-expanded", "true");
    } catch (error) { if (isCancellation(error)) return;
      statusEl.textContent = errorMessage(error);
      statusEl.classList.add("error");
    } finally {
      groupAddBtn.disabled = busy || !sessionId;
    }
  }

  async function addGroupParticipant(participant: string): Promise<void> {
    if (busy || !sessionId) return;
    groupAddBtn.disabled = true;
    groupAddMenu.querySelectorAll<HTMLButtonElement>("button").forEach(function (button) { button.disabled = true; });
    try {
      var response = await api.request("/api/v1/webui/group/participants", {
        method: "POST",
        headers: { "Content-Type": "application/json", "Accept": "application/json" },
        body: JSON.stringify({ workspace_id: workspaceController.currentWorkspace ? workspaceController.currentWorkspace.id : "", session_id: sessionId, participant: participant }),
      });
      var payload = await api.readJSON<GroupInfo & { error?: string }>(response);
      if (!response.ok) throw new Error(payload.error || "添加群聊成员失败");
      currentGroup = payload;
      closeGroupAddMenu();
      renderGroupBar();
      statusEl.textContent = "已添加 @" + participant;
      statusEl.classList.remove("error");
    } catch (error) { if (isCancellation(error)) return;
      statusEl.textContent = errorMessage(error);
      statusEl.classList.add("error");
      groupAddMenu.querySelectorAll<HTMLButtonElement>("button").forEach(function (button) { button.disabled = false; });
    } finally {
      groupAddBtn.disabled = busy || !sessionId;
    }
  }

  async function removeGroupParticipant(participant: string): Promise<void> {
    if (busy || !sessionId) return;
    closeGroupAddMenu();
    groupAddBtn.disabled = true;
    groupParticipants.querySelectorAll<HTMLButtonElement>("button").forEach(function (button) { button.disabled = true; });
    try {
      var response = await api.request("/api/v1/webui/group/participants", {
        method: "DELETE",
        headers: { "Content-Type": "application/json", "Accept": "application/json" },
        body: JSON.stringify({ workspace_id: workspaceController.currentWorkspace ? workspaceController.currentWorkspace.id : "", session_id: sessionId, participant: participant }),
      });
      var payload = await api.readJSON<GroupInfo & { error?: string }>(response);
      if (!response.ok) throw new Error(payload.error || "删除群聊成员失败");
      currentGroup = payload;
      renderGroupBar();
      statusEl.textContent = "已移除 @" + participant;
      statusEl.classList.remove("error");
    } catch (error) { if (isCancellation(error)) return;
      statusEl.textContent = errorMessage(error);
      statusEl.classList.add("error");
      groupParticipants.querySelectorAll<HTMLButtonElement>("button").forEach(function (button) { button.disabled = false; });
    } finally {
      groupAddBtn.disabled = busy || !sessionId;
    }
  }

  function renderGroupBar(): void {
    groupBar.hidden = conversationType !== "group";
    groupParticipants.textContent = "";
    groupMemberControls.hidden = conversationType !== "group";
    groupAddBtn.disabled = busy || !sessionId;
    if (conversationType !== "group" || !currentGroup) {
      closeGroupAddMenu();
      return;
    }
    currentGroup.participants.forEach(function (participant) {
      var chip = document.createElement("span");
      chip.className = "group-participant" + (participant.id === currentGroup!.scheduler ? " scheduler" : "") + (participant.available ? "" : " unavailable");
      var name = document.createElement("span");
      name.textContent = "@" + participant.id;
      chip.appendChild(name);
      chip.title = participant.available ? (participant.transport || participant.kind) : "当前不可用";
      if (canRemoveGroupParticipant(currentGroup, participant)) {
        chip.classList.add("removable");
        var remove = document.createElement("button");
        remove.type = "button";
        remove.className = "group-participant-remove";
        remove.textContent = "×";
        remove.title = "移除 @" + participant.id;
        remove.setAttribute("aria-label", "移除群聊成员 @" + participant.id);
        remove.disabled = busy || !sessionId;
        remove.addEventListener("click", function (event) {
          event.stopPropagation();
          removeGroupParticipant(participant.id);
        });
        chip.appendChild(remove);
      }
      groupParticipants.appendChild(chip);
    });
  }

  function setConversationType(value: unknown, group?: GroupInfo | null): void {
    conversationType = normalizeConversationType(value);
    currentGroup = conversationType === "group" ? (group || currentGroup) : null;
    closeGroupAddMenu();
    if (conversationType === "group") {
      setChatMode("run");
      modeAskBtn.hidden = true;
      input.placeholder = "群聊模式：无 @ 由 bqagent 处理，@成员可定向交互";
    } else {
      modeAskBtn.hidden = false;
      input.placeholder = chatModePlaceholder(chatMode);
      closeMentionMenu();
    }
    renderGroupBar();
  }

  function setNewConversationMenu(open: boolean): void {
    open = Boolean(open) && !busy;
    conversationNewMenu.hidden = !open;
    conversationNew.setAttribute("aria-expanded", open ? "true" : "false");
  }

  function closeMentionMenu(): void {
    mentionMenu.hidden = true;
    mentionMenu.textContent = "";
    mentionQuery = null;
  }

  function renderMentionMenu(): void {
    if (conversationType !== "group" || !currentGroup || busy) {
      closeMentionMenu();
      return;
    }
    var query = groupMentionQuery(input.value, input.selectionStart || 0);
    if (!query) {
      closeMentionMenu();
      return;
    }
    var matches = matchingGroupParticipants(currentGroup.participants, query.query);
    if (!matches.length) {
      closeMentionMenu();
      return;
    }
    mentionQuery = query;
    mentionMenu.textContent = "";
    matches.forEach(function (participant, index) {
      var button = document.createElement("button");
      button.type = "button";
      button.setAttribute("role", "option");
      button.className = index === 0 ? "active" : "";
      button.dataset.participant = participant.id;
      button.textContent = "@" + participant.id + (participant.id === currentGroup!.scheduler ? " · 调度员" : "");
      mentionMenu.appendChild(button);
    });
    mentionMenu.hidden = false;
  }

  function chooseMention(participant: string): void {
    if (!mentionQuery) return;
    var replacement = replaceGroupMention(input.value, mentionQuery, participant);
    input.value = replacement.text;
    input.setSelectionRange(replacement.cursor, replacement.cursor);
    closeMentionMenu();
    autoGrow();
    input.focus();
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
      if (conversation.conversation_type === "group") {
        var badge = document.createElement("span");
        badge.className = "conversation-type-badge";
        badge.textContent = "群聊";
        button.appendChild(badge);
      }
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
      var response = await api.request(workspaceController.workspaceScopedURL("/api/v1/webui/conversations/" + encodeURIComponent(conversation.id)), {
        method: "DELETE", headers: { "Accept": "application/json" }
      });
      var payload = await api.readJSON<{ error?: string }>(response);
      if (!response.ok) throw new Error(payload.error || "删除对话失败");
      conversationLoadID++;
      conversations = conversations.filter(function (item) { return item.id !== conversation.id; });
      if (sessionId === conversation.id) {
        workspaceController.setCurrentWorkspaceSession("");
        setChatMode("run");
        setConversationType("default", null);
        clearPendingAttachments();
        thread.innerHTML = emptyMarkup();
        providerController.loadRuntimeModel();
      }
      renderConversationList();
      statusEl.textContent = "已删除对话";
      statusEl.classList.remove("error", "busy");
    } catch (error) { if (isCancellation(error)) return;
      statusEl.textContent = errorMessage(error);
      statusEl.classList.add("error");
    } finally {
      conversationDelete.disabled = false;
    }
  }

  async function refreshConversations(loadCurrent: boolean): Promise<void> {
    var loadID = ++conversationLoadID;
    try {
      var response = await api.request(workspaceController.workspaceScopedURL("/api/v1/webui/conversations"), { headers: { "Accept": "application/json" } });
      var payload = await api.readJSON<ConversationsResponse>(response);
      if (!response.ok) throw new Error(payload.error || "读取对话列表失败");
      if (loadID !== conversationLoadID) return;
      conversations = payload.conversations || [];
      renderConversationList();
      if (loadCurrent && sessionId && conversations.some(function (conversation) { return conversation.id === sessionId; })) await openConversation(sessionId);
    } catch (error) { if (isCancellation(error)) return;
      if (loadID !== conversationLoadID) return;
      conversationList.innerHTML = '<div class="conversation-list-state">读取历史失败</div>';
    }
  }

  async function openConversation(id: string): Promise<void> {
    if (busy || !id) return;
    var loadID = ++conversationLoadID;
    statusEl.textContent = "正在读取历史";
    try {
      var response = await api.request(workspaceController.workspaceScopedURL("/api/v1/webui/conversations/" + encodeURIComponent(id)), { headers: { "Accept": "application/json" } });
      var payload = await api.readJSON<ConversationHistory & { error?: string }>(response);
      if (!response.ok) throw new Error(payload.error || "读取历史失败");
      if (loadID !== conversationLoadID) return;
      workspaceController.setCurrentWorkspaceSession(payload.id || id);
      setConversationType(payload.conversation_type, payload.group || null);
      setChatMode(payload.mode);
      thread.innerHTML = "";
      normalizeHistoryMessages(payload.messages || []).forEach(function (message) {
        var bubble = addMessage(message.role, message.sender, message.kind);
        if (message.role === "assistant") {
          renderRestoredAssistant(bubble, message);
        } else {
          renderUserMessage(bubble, message.content, [], message.files || []);
        }
      });
      if (!payload.messages || !payload.messages.length) thread.innerHTML = emptyMarkup();
      renderConversationList();
      providerController.loadRuntimeModel();
      statusEl.textContent = "已恢复历史会话";
      statusEl.classList.remove("error");
      scrollToBottom();
    } catch (error) { if (isCancellation(error)) return;
      statusEl.textContent = errorMessage(error);
      statusEl.classList.add("error");
    }
  }

  function scrollToBottom(): void {
    main.scrollTop = main.scrollHeight;
  }

  function removeEmpty(): void {
    var empty = document.getElementById("empty");
    if (empty) empty.remove();
  }

  function addMessage(role: string, sender?: string, kind?: string): HTMLDivElement {
    removeEmpty();
    var msg = document.createElement("div");
    msg.className = "msg " + role + " msg-enter";
    var displaySender = sender || (role === "user" ? "你" : "bqagent");
    if (role === "assistant" && displaySender !== "bqagent") msg.classList.add("participant");
    if (kind === "error") msg.classList.add("participant-error");
    var avatar = document.createElement("div");
    avatar.className = "avatar";
    avatar.setAttribute("aria-hidden", "true");
    if (role === "user") avatar.textContent = "你";
    else if (displaySender !== "bqagent") avatar.textContent = displaySender.slice(0, 2).toUpperCase();
    else avatar.appendChild(createIcon("bot"));
    var stack = document.createElement("div");
    stack.className = "message-stack";
    var label = document.createElement("div");
    label.className = "message-label";
    label.textContent = role === "user" ? "你" : (displaySender === "bqagent" && conversationType === "group" ? "bqagent · 调度汇总" : displaySender);
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

  function addACPPermissionCard(payload: ACPPermissionPayload, pending: Record<string, HTMLElement>): void {
    var bubble = addMessage("assistant", payload.agent || "外部 Agent");
    bubble.classList.add("rendered", "acp-permission-card");

    var heading = document.createElement("strong");
    heading.className = "acp-permission-heading";
    heading.textContent = "需要你的权限";
    bubble.appendChild(heading);

    var title = document.createElement("div");
    title.className = "acp-permission-title";
    title.textContent = acpPermissionTitle(payload.tool_call);
    bubble.appendChild(title);

    var inputText = formatACPPermissionInput(payload.tool_call && payload.tool_call.rawInput);
    if (inputText) {
      var inputPreview = document.createElement("pre");
      inputPreview.className = "acp-permission-input";
      inputPreview.textContent = inputText;
      bubble.appendChild(inputPreview);
    }

    var actions = document.createElement("div");
    actions.className = "acp-permission-actions";
    var status = document.createElement("span");
    status.className = "acp-permission-status";
    payload.options.forEach(function (option) {
      var button = document.createElement("button");
      button.type = "button";
      button.className = "ui-btn ui-btn-sm " + (isRejectPermissionOption(option.kind) ? "ui-btn-danger" : "ui-btn-primary");
      button.textContent = option.name;
      button.addEventListener("click", async function () {
        var buttons = Array.prototype.slice.call(actions.querySelectorAll<HTMLButtonElement>("button")) as HTMLButtonElement[];
        buttons.forEach(function (item) { item.disabled = true; });
        status.textContent = "正在提交…";
        try {
          var response = await api.request("/api/v1/webui/acp/permissions", {
            method: "POST",
            headers: { "Content-Type": "application/json", "Accept": "application/json" },
            body: JSON.stringify({
              workspace_id: workspaceController.currentWorkspace ? workspaceController.currentWorkspace.id : "",
              request_id: payload.request_id,
              option_id: option.option_id
            })
          });
          var result = await api.readJSON<{ accepted?: boolean; error?: string }>(response);
          if (!response.ok || !result.accepted) throw new Error(result.error || "提交权限选择失败");
          delete pending[payload.request_id];
          bubble.classList.add("resolved");
          status.textContent = "已选择：" + option.name;
        } catch (error) { if (isCancellation(error)) return;
          buttons.forEach(function (item) { item.disabled = false; });
          status.textContent = errorMessage(error);
          status.classList.add("error");
        }
      });
      actions.appendChild(button);
    });
    bubble.appendChild(actions);
    bubble.appendChild(status);
    pending[payload.request_id] = bubble;
  }

  function expireACPPermissionCards(pending: Record<string, HTMLElement>): void {
    Object.keys(pending).forEach(function (requestID) {
      var bubble = pending[requestID];
      Array.prototype.forEach.call(bubble.querySelectorAll("button"), function (button: HTMLButtonElement) { button.disabled = true; });
      var status = bubble.querySelector<HTMLElement>(".acp-permission-status");
      if (status) status.textContent = "请求已取消或失效";
      bubble.classList.add("expired");
      delete pending[requestID];
    });
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
    workspaceController.updateWorkspacePreviewAttachState();
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
      var read = readAsBase64(file, attachment, "读取图片失败").catch(function (error: unknown) { if (isCancellation(error)) return;
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
      var read = readAsBase64(file, attachment, "读取文件失败").catch(function (error: unknown) { if (isCancellation(error)) return;
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
      workspaceController.updateWorkspacePreviewAttachState();
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
    if (open) setTimeout(function () { (chatMode === "ask" ? modeAskBtn : modeRunBtn).focus(); }, 0);
  }

  function setChatMode(value: unknown): void {
    chatMode = conversationType === "group" ? "run" : normalizeChatMode(value);
    [modeRunBtn, modeAskBtn].forEach(function (button) {
      var selected = button.dataset.mode === chatMode;
      button.classList.toggle("selected", selected);
      button.setAttribute("aria-checked", selected ? "true" : "false");
    });
    input.placeholder = conversationType === "group" ? "群聊模式：无 @ 由 bqagent 处理，@成员可定向交互" : chatModePlaceholder(chatMode);
    addAttachmentBtn.title = chatMode === "ask" ? "Ask 模式与附件" : "Run 模式与附件";
    addAttachmentBtn.setAttribute("aria-label", chatMode === "ask" ? "Ask 模式：选择模式或添加文件" : "Run 模式：选择模式或添加文件");
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
    sendBtn.disabled = !workspaceController.workspaceReady;
    sendBtn.classList.toggle("stop", on);
    sendBtn.title = on ? "停止生成" : "发送";
    sendBtn.setAttribute("aria-label", on ? "停止生成" : "发送");
    input.disabled = on || !workspaceController.workspaceReady;
    addAttachmentBtn.disabled = on;
    uploadFileBtn.disabled = on;
    modeRunBtn.disabled = on;
    modeAskBtn.disabled = on;
    serverFilePath.disabled = on;
    addServerPathBtn.disabled = on;
    reasoningEffortToggle.disabled = on;
    reasoningEffortRange.disabled = on;
    providerController.modelSelect.disabled = on || providerController.runtimeModelOptions().length === 0;
    providerController.providerSettingsTrigger.disabled = on;
    groupAddBtn.disabled = on || !sessionId;
    groupParticipants.querySelectorAll<HTMLButtonElement>("button").forEach(function (button) { button.disabled = on || !sessionId; });
    workspaceController.workspaceSelect.disabled = on || !workspaceController.workspaceReady;
    workspaceController.workspaceCreateAgent.disabled = on || !workspaceController.workspaceReady;
    if (on) {
      setAttachmentMenu(false);
      setReasoningEffortMenu(false);
      closeGroupAddMenu();
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
      } catch (error) { if (isCancellation(error)) return;
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
          await api.request(workspaceController.workspaceScopedURL("/api/v1/runs/" + encodeURIComponent(runId) + "/feedback"), {
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
      var response = await api.request("/api/v1/chat/stop", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ turn_id: currentTurnId, workspace_id: workspaceController.currentWorkspace ? workspaceController.currentWorkspace.id : "" })
      });
      if (!response.ok) throw new Error("HTTP " + response.status);
      var result = await api.readJSON<{ stopped: boolean }>(response);
      if (!result.stopped && currentController) currentController.abort();
    } catch (_) {
      if (currentController) currentController.abort();
    }
  }

  async function send(): Promise<void> {
    if (!workspaceController.workspaceReady || busy || preparingSend) return;
    preparingSend = true;
    var pendingModelSelection = providerController.runtimeModelSelectionPromise;
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

    var bubble: HTMLDivElement | null = conversationType === "group" ? null : addMessage("assistant");
    var toolCards: ToolCards = {};
    var participantBubbles: Record<string, HTMLDivElement> = Object.create(null) as Record<string, HTMLDivElement>;
    var pendingPermissions: Record<string, HTMLElement> = Object.create(null) as Record<string, HTMLElement>;
    preparingSend = false;
    var caret = document.createElement("span");
    caret.className = "caret";
    function ensureCoordinatorBubble(): HTMLDivElement {
      if (!bubble) bubble = addMessage("assistant", "bqagent");
      bubble.classList.add("is-streaming");
      if (!caret.parentElement) bubble.appendChild(caret);
      return bubble;
    }
    if (bubble) ensureCoordinatorBubble();
    var streamed = "";
    var progress = "";
    function closeCoordinatorSegment(): void {
      if (!bubble) {
        streamed = "";
        progress = "";
        toolCards = {};
        return;
      }
      clearStreamingState(bubble);
      caret.remove();
      var stack = bubble.parentElement;
      var hasToolTimeline = Boolean(stack && stack.querySelector(".tool-timeline"));
      if (streamed.trim()) {
        bubble.classList.add("rendered");
        renderReply(bubble, streamed);
      } else if (!hasToolTimeline) {
        var message = bubble.closest(".msg");
        if (message) message.remove();
      }
      bubble = null;
      streamed = "";
      progress = "";
      toolCards = {};
    }

    setBusy(true);
    try {
      var res = await api.request("/api/v1/webui/chat", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          workspace_id: workspaceController.currentWorkspace ? workspaceController.currentWorkspace.id : "",
          message: text,
          images: sentImages,
          files: sentFiles,
          session_id: sessionId,
          turn_id: currentTurnId,
          mode: chatMode,
          conversation_type: conversationType,
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
            var toolPayload = parseJSONEvent<ToolEventPayload>(evt.data);
            if (toolPayload.name !== "consult_group_agent") updateToolTimeline(ensureCoordinatorBubble(), toolCards, evt.event, toolPayload);
          } else if (evt.event === "acp_permission") {
            var permission = parseJSONEvent<ACPPermissionPayload>(evt.data);
            addACPPermissionCard(permission, pendingPermissions);
          } else if (evt.event === "participant_start" || evt.event === "participant_message" || evt.event === "participant_error") {
            var groupEvent = parseJSONEvent<GroupEventPayload>(evt.data);
            if (shouldCloseCoordinatorSegment(evt.event)) closeCoordinatorSegment();
            var participantBubble = participantBubbles[groupEvent.call_id];
            if (!participantBubble) {
              participantBubble = addMessage("assistant", groupEvent.participant, evt.event === "participant_error" ? "error" : "message");
              participantBubbles[groupEvent.call_id] = participantBubble;
            }
            if (evt.event === "participant_start") {
              participantBubble.classList.add("notice", "is-streaming");
              participantBubble.textContent = "正在思考并处理共享工作区…";
            } else if (evt.event === "participant_message") {
              participantBubble.classList.remove("notice", "is-streaming");
              participantBubble.classList.add("rendered");
              renderReply(participantBubble, groupEvent.content || "");
            } else {
              participantBubble.classList.remove("notice", "is-streaming");
              participantBubble.classList.add("rendered", "error");
              renderReply(participantBubble, "执行失败：" + (groupEvent.error || "未知错误"));
            }
          } else if (evt.event === "done") {
            finished = true;
            var done = parseJSONEvent<WebUIDoneEvent>(evt.data);
            if (done.session_id) {
              workspaceController.setCurrentWorkspaceSession(done.session_id);
            }
            if (done.model) {
              providerController.invalidateRuntimeModel();
              providerController.updateRuntimeModel(done.api_type, done.model, providerController.providerSettingsState.active_provider);
            }
            setConversationType(done.conversation_type, done.group || currentGroup);
            setChatMode(done.mode);
            refreshConversations(false);
            if (!shouldRenderFinalReply(done.conversation_type, done.reply_kind)) {
              caret.remove();
              if (bubble) {
                var directMessage = bubble.closest(".msg");
                if (directMessage) directMessage.remove();
              }
              bubble = null;
              continue;
            }
            var finalBubble = ensureCoordinatorBubble();
            clearStreamingState(finalBubble);
            finalBubble.classList.add("rendered");
            var finalReply = typeof done.reply === "string" ? done.reply : "";
            if (!finalReply.trim()) {
              finalBubble.classList.add("error");
              finalBubble.textContent = "出错：服务端未返回有效回复";
              statusEl.textContent = "回复无效";
              statusEl.classList.remove("busy");
              statusEl.classList.add("error");
            } else {
              renderReply(finalBubble, finalReply);
              addMessageMeta(finalBubble, done.generation);
              addMessageControls(finalBubble, done.run_id || "", finalReply);
            }
          } else if (evt.event === "stopped") {
            finished = true;
            expireACPPermissionCards(pendingPermissions);
            var stoppedBubble = ensureCoordinatorBubble();
            clearStreamingState(stoppedBubble);
            caret.remove();
            stoppedBubble.classList.add("rendered", "notice");
            var stoppedReply = streamed ? streamed + "\n\n> 已停止生成。" : "已停止生成。";
            renderReply(stoppedBubble, stoppedReply);
            addMessageControls(stoppedBubble, "", streamed);
            statusEl.textContent = "已停止";
            statusEl.classList.remove("busy", "error");
          } else if (evt.event === "error") {
            finished = true;
            expireACPPermissionCards(pendingPermissions);
            var errorBubble = ensureCoordinatorBubble();
            clearStreamingState(errorBubble);
            var errPayload = parseJSONEvent<{ error?: string }>(evt.data);
            errorBubble.classList.add("error");
            errorBubble.textContent = "出错：" + (errPayload.error || "未知错误");
            statusEl.textContent = "请求失败";
            statusEl.classList.remove("busy");
            statusEl.classList.add("error");
          } else if (evt.event === "progress") {
            var progressPayload = parseJSONEvent<{ message?: string }>(evt.data);
            if (conversationType !== "group" && progressPayload.message && !streamed) {
              var progressBubble = ensureCoordinatorBubble();
              progress = progressPayload.message;
              caret.remove();
              progressBubble.classList.add("notice");
              progressBubble.textContent = progress;
              progressBubble.appendChild(caret);
            }
          } else {
            var payload = parseJSONEvent<{ delta?: string }>(evt.data);
            if (payload.delta) {
              var streamBubble = ensureCoordinatorBubble();
              streamed += payload.delta;
              caret.remove();
              streamBubble.classList.remove("notice");
              streamBubble.textContent = streamed;
              streamBubble.appendChild(caret);
            }
          }
          scrollToBottom();
        }
      }
      eventBuffer.push(decoder.decode());
      finished = finished || eventBuffer.terminal;

      if (!finished) {
        var incompleteBubble = ensureCoordinatorBubble();
        clearStreamingState(incompleteBubble);
        caret.remove();
        incompleteBubble.classList.add("rendered", "error");
        var incompleteReply = streamed
          ? streamed + "\n\n> 响应在完成事件到达前中断。"
          : "出错：响应未正常完成。";
        renderReply(incompleteBubble, incompleteReply);
        statusEl.textContent = "响应中断";
        statusEl.classList.remove("busy");
        statusEl.classList.add("error");
      }
    } catch (err) { if (isCancellation(err) && api.suspended) return;
      expireACPPermissionCards(pendingPermissions);
      var caughtBubble = ensureCoordinatorBubble();
      clearStreamingState(caughtBubble);
      caret.remove();
      if (stopRequested || isCancellation(err)) {
        caughtBubble.classList.add("rendered", "notice");
        var partialReply = streamed ? streamed + "\n\n> 已停止生成。" : "已停止生成。";
        renderReply(caughtBubble, partialReply);
        addMessageControls(caughtBubble, "", streamed);
      } else {
        caughtBubble.classList.add("error");
        caughtBubble.textContent = "出错：" + errorMessage(err);
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
      workspaceController.refreshWorkspaceTree();
    }
  }

  function newChat(type: ConversationType = "default"): void {
    if (busy) return;
    setNewConversationMenu(false);
    workspaceController.setCurrentWorkspaceSession("");
    setChatMode("run");
    setConversationType(type, type === "group" ? { scheduler: "bqagent", participants: [{ id: "bqagent", name: "bqagent", kind: "builtin", available: true }] } : null);
    clearPendingAttachments();
    setAttachmentMenu(false);
    setReasoningEffortMenu(false);
    thread.innerHTML = emptyMarkup();
    renderConversationList();
    statusEl.textContent = "在线";
    statusEl.classList.remove("error", "busy");
    providerController.loadRuntimeModel();
    input.focus();
    if (type === "group") {
      loadAvailableGroup().then(function (group) {
        if (!sessionId && conversationType === "group") setConversationType("group", group);
      }).catch(function (error) { if (isCancellation(error)) return;
        statusEl.textContent = errorMessage(error);
        statusEl.classList.add("error");
      });
    }
  }

  sendBtn.addEventListener("click", function () {
    if (busy) stopCurrentTurn(); else send();
  });
  conversationNew.addEventListener("click", function () { setNewConversationMenu(Boolean(conversationNewMenu.hidden)); });
  conversationNewMenu.addEventListener("click", function (event) {
    var option = eventElement(event.target)?.closest<HTMLButtonElement>("[data-conversation-type]");
    if (!option) return;
    newChat(normalizeConversationType(option.dataset.conversationType));
  });
  addAttachmentBtn.addEventListener("click", function () {
    setAttachmentMenu(Boolean(attachmentMenu.hidden));
  });
  groupAddBtn.addEventListener("click", function () {
    toggleGroupAddMenu();
  });
  modeRunBtn.addEventListener("click", function () {
    setChatMode("run");
    setAttachmentMenu(false);
    input.focus();
  });
  modeAskBtn.addEventListener("click", function () {
    setChatMode("ask");
    setAttachmentMenu(false);
    input.focus();
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
    if (!conversationNewMenu.hidden && !conversationNewWrap.contains(target)) setNewConversationMenu(false);
    if (!mentionMenu.hidden && target !== input && !mentionMenu.contains(target)) closeMentionMenu();
    if (!groupAddMenu.hidden && !groupAddWrap.contains(target)) closeGroupAddMenu();
  });
  document.addEventListener("keydown", function (event) {
    if (event.key === "Escape" && !conversationContextMenu.hidden) {
      closeConversationContextMenu();
    } else if (event.key === "Escape" && !groupAddMenu.hidden) {
      closeGroupAddMenu();
      groupAddBtn.focus();
    } else if (event.key === "Escape" && !attachmentMenu.hidden) {
      setAttachmentMenu(false);
      addAttachmentBtn.focus();
    } else if (event.key === "Escape" && !reasoningEffortMenu.hidden) {
      setReasoningEffortMenu(false);
      reasoningEffortToggle.focus();
    } else if (event.key === "Escape" && !workspaceController.workspacePickerBackdrop.hidden) {
      workspaceController.closeWorkspacePicker();
    } else if (event.key === "Escape" && !providerController.providerSettingsBackdrop.hidden) {
      providerController.closeProviderSettings();
    } else if (event.key === "Escape" && !workspaceController.isWorkspaceDesktop() && workspaceController.workspaceToggle.getAttribute("aria-expanded") === "true") {
      workspaceController.setWorkspaceSidebarOpen(false, false);
      workspaceController.workspaceToggle.focus();
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
  mentionMenu.addEventListener("click", function (event) {
    var option = eventElement(event.target)?.closest<HTMLButtonElement>("[data-participant]");
    if (option && option.dataset.participant) chooseMention(option.dataset.participant);
  });
  input.addEventListener("input", function () { autoGrow(); renderMentionMenu(); });
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
    if (!mentionMenu.hidden) {
      var options = Array.prototype.slice.call(mentionMenu.querySelectorAll<HTMLButtonElement>("button")) as HTMLButtonElement[];
      var activeIndex = options.findIndex(function (option) { return option.classList.contains("active"); });
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        if (options.length) {
          options[Math.max(0, activeIndex)].classList.remove("active");
          activeIndex = e.key === "ArrowDown" ? (activeIndex + 1) % options.length : (activeIndex - 1 + options.length) % options.length;
          options[activeIndex].classList.add("active");
        }
        return;
      }
      if (e.key === "Enter" && !e.shiftKey && options.length) {
        e.preventDefault();
        chooseMention(options[Math.max(0, activeIndex)].dataset.participant || "");
        return;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        closeMentionMenu();
        return;
      }
    }
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
  void auth.initialize();
})();
