import { copyText } from "./clipboard";
import { TurnFileEdits } from "./file-edits";
import { renderQueueDelivery } from "./queue-delivery";
import { ClearContextRequests, clearDivider, clearRefusal, parseClear } from "./clear-context";
import { requestRewind, rewindButton } from "./rewind";
import { ForkRequests, forkSourceMarker, replyAnchor } from "./fork";
import type { SentAttachments } from "./attachments";
import type { ForkAction, ForkOrigin } from "./types";
import type { RewindAction } from "./types";
import { DEFAULT_REASONING_EFFORT, REASONING_EFFORT_VALUES, REASONING_EFFORT_LABELS, normalizeReasoningEffort } from "./reasoning-effort";
import "@awesome.me/webawesome/dist/styles/themes/default.css";
import "@awesome.me/webawesome/dist/components/select/select.js";
import "./styles.css";
import { initConversationLayout } from "./conversation-layout";
import { errorMessage, parseJSONEvent } from "./api";
import { hasBlockingPrompt, renderHistoricalPrompt, settleAgentPrompt, showAgentPrompt } from "./acp-permission";
import { ATTACHMENT_LIMITS, dataTransferHasFiles, restorePendingAttachments, showTemporaryError, snapshotPendingAttachments, splitDroppedFiles, takePendingAttachmentsForSend, validateFileAttachment, validateImageAttachment } from "./attachments";
import { complexTaskNotice, formatFirstTokenLatency, formatGenerationSpeed, historyAssistantView, normalizeHistoryMessages } from "./chat-rendering";
import { chatModePlaceholder, normalizeChatMode } from "./chat-mode";
import { builtinAgent, SingleAgentPicker } from "./single-agent";
import { AgentModelPicker, type AgentModelCatalog, type AgentTurnSelection, type ModelPick } from "./agent-models";
import { AgentPreferenceWrites, clearAgentDraft, readAgentDraft, recordFirstAgentTurn, saveResponderDraft } from "./agent-drafts";
import { formatConversationTime } from "./conversations";
import { addableGroupParticipants, canRemoveGroupParticipant, groupMentionQuery, matchingGroupParticipants, normalizeConversationType, replaceGroupMention, shouldCloseCoordinatorSegment, shouldRenderFinalReply, type MentionQuery } from "./group-chat";
import { byId, eventElement } from "./dom";
import { createIcon, iconMarkup, setIconButtonLabel } from "./icons";
import { renderMarkdown } from "./markdown";
import { waitForModelSelection } from "./model-selection";
import { initParticleField, redrawParticleField, refreshParticleCapability, refreshParticlePalette } from "./particles";
import { MessageSnapshots } from "./stream";
import { renderAgentPlan } from "./external-plan";
import { renderAgentSummary } from "./external-summary";
import { ExternalTimelineView, type ExternalPlanEvent, type ExternalSummaryEvent, type ExternalTextEvent } from "./external-timeline";
import { clearChatViewData, followTurn, readChatDraft, rememberTurn, saveChatDraft, turnBookmark, TurnViewer } from "./turn-replay";
import { StreamRenderQueue } from "./stream-render";
import { ContextProgress } from "./context-progress";
import { AgentContextMeter, type AgentContextUsage } from "./context-usage";
import { initialTheme } from "./theme";
import { isTaskUpdatedEvent, renderTaskPanel, TaskProgress } from "./task-progress";
import { busyInputActions, inputKindForKey, isInputAppliedEvent, nativeInputOption, TURN_INPUT_STATE_LABELS, TurnInputSession, type TurnInputKind } from "./turn-input";
import { restoredToolPayload } from "./tool-status";
import { updateToolTimeline, updateToolGroupSummary, type ToolCards, type ToolTimeline, type ToolTimelineSegment } from "./tool-timeline";
import type { SentFile, SentImage } from "./attachments";
import type { ACPPermissionPayload, ChatMode, ConversationHistory, ConversationMessage, ConversationSummary, ConversationsResponse, ConversationType, GenerationMetrics, GroupEventPayload, GroupInfo, MessageSnapshot, PendingFile, PendingInputPayload, PendingImage, ReasoningEffort, ToolEventPayload, WebUIDoneEvent } from "./types";
import { busyCommandContext, createSlashCommands } from "./slash-commands";
import { APIClient, isCancellation } from "./api-client";
import { CommandQueue, type CommandQueueSnapshot } from "./command-queue";
import { createAuthController } from "./auth-controller";
import { createProviderController } from "./provider-controller";
import { createWorkspaceController } from "./workspace-controller";
import { createDoctorController } from "./doctor-controller";
(function () {
  "use strict";
  const THEME_KEY = "bqagent.webui.theme";
  const REASONING_EFFORT_KEY = "bqagent.webui.reasoning-effort";
  const thread = byId<HTMLDivElement>("thread");
  const main = byId<HTMLElement>("main");
  const conversationList = byId<HTMLDivElement>("conversation-list");
  const conversationNew = byId<HTMLButtonElement>("conversation-new");
  const conversationNewWrap = byId<HTMLDivElement>("conversation-new-wrap");
  const conversationNewMenu = byId<HTMLDivElement>("conversation-new-menu");
  const conversationContextMenu = byId<HTMLDivElement>("conversation-context-menu");
  const conversationDelete = byId<HTMLButtonElement>("conversation-delete");
  const input = byId<HTMLTextAreaElement>("input");
  const agentPicker = new SingleAgentPicker(byId<HTMLSelectElement>("chat-agent"), () => {
    saveResponderDraft(modelScope(), agentPicker.pendingAgent);
    refreshAgentControls();
  });
  const agentPickerLabel = byId<HTMLLabelElement>("agent-picker");
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
  const uploadImageBtn = byId<HTMLButtonElement>("upload-image");
  const imageInput = byId<HTMLInputElement>("image-input");
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
  const turnInputQueue = byId<HTMLDivElement>("turn-input-queue");
  const turnInputActions = byId<HTMLElement>("turn-input-actions");
  const steerBtn = byId<HTMLButtonElement>("steer-btn");
  const followUpBtn = byId<HTMLButtonElement>("followup-btn");
  const queueSendBtn = byId<HTMLButtonElement>("queue-send-btn");
  const composerHints = byId<HTMLElement>("composer-hints");
  const themeToggle = byId<HTMLButtonElement>("theme-toggle");
  const statusEl = byId<HTMLDivElement>("status");
  var conversationLoadID = 0;
  let listRevision = 0;
  let listTimer: ReturnType<typeof setTimeout> | undefined;
  var conversationScopeEpoch = 0;
  const taskPanel = byId<HTMLElement>("task-panel");
  function taskScope(): string { return String(conversationScopeEpoch); }
  const taskProgress = new TaskProgress(taskScope());
  function clearTaskProgress(): void {
    taskProgress.reset(taskScope());
    renderTaskPanel(taskPanel, null);
  }
  var conversations: ConversationSummary[] = [];
  var conversationContextTarget: ConversationSummary | null = null;
  var sessionId = "";
  var reasoningEffort: ReasoningEffort = DEFAULT_REASONING_EFFORT;
  var chatMode: ChatMode = "run";
  var conversationType: ConversationType = "default";
  var currentGroup: GroupInfo | null = null;
  var mentionQuery: MentionQuery | null = null;
  var busy = false;
  var runningExternalAgent: string | undefined;
  let runningModelRevision: symbol | undefined;
  let runningSelection: AgentTurnSelection | undefined;
  const nativeInputKinds = new Set<TurnInputKind>();
  var streamRender: StreamRenderQueue | null = null;
  window.addEventListener("beforeunload", () => streamRender?.dispose(), { once: true });
  const viewer = new TurnViewer();
  let navigation = 0;
  var currentTurnId = "";
  var currentController: AbortController | null = null;
  var stopRequested = false;
  let webTurnStarted = false, stopOnStart = false;
  var turnInputs: TurnInputSession | null = null;
  var preparingSend = false;
  var pendingImages: PendingImage[] = [];
  var pendingImageReads: Promise<void>[] = [];
  var pendingFiles: PendingFile[] = [];
  var pendingFileReads: Promise<void>[] = [];
  var nextAttachmentID = 1;
  const attachmentPage = createTurnId() + "-";
  var attachmentErrorTimer: ReturnType<typeof setTimeout> | 0 = 0;
  var ATTACHMENT_ERROR_TIMEOUT_MS = 4000;
  var MAX_PENDING_FILES = ATTACHMENT_LIMITS.maxFiles;
  var MAX_PENDING_FILE_BYTES = ATTACHMENT_LIMITS.maxFileBytes;
  var MAX_PENDING_TOTAL_FILE_BYTES = ATTACHMENT_LIMITS.maxTotalFileBytes;

  const api = new APIClient();
  const contextMeter = new AgentContextMeter(byId<HTMLDetailsElement>("context-usage"));
  const preferenceAPI = new APIClient();
  preferenceAPI.onUnauthorized = () => api.onUnauthorized();
  const preferenceWrites = new AgentPreferenceWrites();
  let preferenceEpoch = new AbortController();
  const clearRequests = new ClearContextRequests(api, createTurnId);
  const forkRequests = new ForkRequests(api,createTurnId);
  const forkDraftMedia = new Map<string,SentAttachments>();
  let forkNavigation: { workspace: string; from: string; to: string } | undefined;
  let renderedContext: { workspace: string; id: string; number: number } | undefined;
  async function readAgentModels(agent: string, signal: AbortSignal, options?: Record<string, string>): Promise<AgentModelCatalog> {
    const scope = modelScope();
    const query = new URLSearchParams({ agent, session_id: sessionId, ...options });
    const url = workspaceController.workspaceScopedURL("/api/v1/webui/agent-models?" + query);
    await preferenceWrites.settled(JSON.stringify([workspaceController.currentWorkspace?.id,agent]));
    const result = await api.json<AgentModelCatalog>(url, { signal });
    if (!options?.model && !signal.aborted) contextMeter.observe(scope, agent, result.context_usage);
    return result;
  }
  function rememberAgentPick(agent: string, pick: ModelPick): void {
    const workspace = workspaceController.currentWorkspace?.id, signal = preferenceEpoch.signal;
    const url = workspaceController.workspaceScopedURL("/api/v1/webui/agent-models?" + new URLSearchParams({ agent, session_id: sessionId }));
    void preferenceWrites.write(JSON.stringify([workspace,agent]), async () => {
      await preferenceAPI.json(url, { method:"POST", headers:{"Content-Type":"application/json"}, body:JSON.stringify(pick), signal, keepalive:true });
    }).catch(error => {
      if (!isCancellation(error) && workspace === workspaceController.currentWorkspace?.id) setAttachmentError("模型偏好保存失败：" + errorMessage(error));
    });
  }
  const agentModels = new AgentModelPicker(byId("agent-model-controls"), byId("agent-model-select"), byId("agent-model-input"),
    byId("agent-model-retry"), byId("agent-effort-select"),
    (agent, signal, model) => readAgentModels(agent, signal, model ? { model } : undefined),
    (agent, signal) => readAgentModels(agent, signal, { view: "capabilities" }),
    () => { if (busy) renderTurnInputControls(true); }, rememberAgentPick, byId("agent-service-tier"), byId("agent-interaction-mode"));
  const providerController = createProviderController(api, {
    get statusEl() { return statusEl; },
    get sessionId() { return sessionId; },
    set sessionId(value) { sessionId = value; },
    get busy() { return busy; },
    workspaceScopedURL: (...args) => workspaceController.workspaceScopedURL(...args),
    setChatMode: (...args) => setChatMode(...args),
  });
  const workspaceController = createWorkspaceController(api, {
    onSwitch() { slashCommands.close(); api.abort(); providerController.cancel(); conversationLoadID++; conversationScopeEpoch++; clearTaskProgress(); },
    get thread() { return thread; },
    get input() { return input; },
    get sendBtn() { return sendBtn; },
    get statusEl() { return statusEl; },
    get sessionId() { return sessionId; },
    set sessionId(value) { sessionId = value; },
    get busy() { return busy; },
    get attachmentsEnabled() { return canAttach(); },
    get pendingFiles() { return pendingFiles; },
    get MAX_PENDING_FILES() { return MAX_PENDING_FILES; },
    get MAX_PENDING_TOTAL_FILE_BYTES() { return MAX_PENDING_TOTAL_FILE_BYTES; },
    setConversationType: (...args) => {
      void agentPicker.loadWorkspace(loadAvailableGroup).then(refreshAgentControls);
      if (!sessionId) {
        agentPicker.restore(undefined, readAgentDraft(modelScope()).responder);
        input.value = readChatDraft(workspaceController.currentWorkspace?.id || "", "new");
      }
      setConversationType(...args);
    },
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
  const commandQueue = new CommandQueue(byId("command-queue"), api, {
    turn: () => currentTurnId,
    draft: () => input.value,
    setDraft: text => { input.value = text; persistComposer(); autoGrow(); },
    chooseBuiltin: () => agentPicker.selectNext(builtinAgent),
    restoreSelection: (agent, picks) => agentModels.restoreQueuedSelection(agent, picks),
    newID: createTurnId, notify: setAttachmentError,
    watch: id => { if (!busy) void restoreTurn(id, conversationLoadID, true); },
    mediaIDs: () => [...pendingImages, ...pendingFiles].map(item => attachmentPage + item.id),
    captureMedia: () => {
      if ([...pendingImages, ...pendingFiles].some(item => item.loading)) throw new Error("正在读取附件，请稍后发送。");
      return snapshotPendingAttachments(pendingImages,pendingFiles);
    },
    removeMedia: ids => {
      pendingImages = pendingImages.filter(item => {
        if (!ids.includes(attachmentPage + item.id)) return true;
        URL.revokeObjectURL(item.objectURL);
        return false;
      });
      pendingFiles = pendingFiles.filter(item => !ids.includes(attachmentPage + item.id));
      renderPendingAttachments();
    },
    restoreMedia: payload => {
      const restored = restorePendingAttachments(payload, () => nextAttachmentID++);
      pendingImages.push(...restored.images);
      pendingFiles.push(...restored.files);
      renderPendingAttachments();
    },
  });
  let explorerInitialized = false;
  const doctorController = createDoctorController(api, url => workspaceController.workspaceScopedURL(url));
  const auth = createAuthController(api, async () => {
    preferenceAPI.authenticated();
    if (!explorerInitialized) { workspaceController.initializeWorkspaceExplorer(); initParticleField(); explorerInitialized = true; }
    input.disabled = true; sendBtn.disabled = true;
    await workspaceController.initializeWorkspaceSelection(); autoGrow();
  }, () => {
    preferenceAPI.suspend();
    preferenceEpoch.abort(); preferenceEpoch = new AbortController();
    clearChatViewData();
    contextMeter.configure("", "");
    forkDraftMedia.clear(); forkNavigation = undefined;
    commandQueue.reset();
    agentModels.configure("", "", false);
    slashCommands.close(); doctorController.cancel(); providerController.cancel(); workspaceController.cancel(); currentController?.abort(); conversationLoadID++;
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
    if (conversationType !== normalizeConversationType(value)) slashCommands.close();
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
    refreshAgentControls();
  }

  function setNewConversationMenu(open: boolean): void {
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
      if (conversation.active_turn_id) {
        const label = document.createElement("span");
        label.className = "conversation-type-badge";
        label.textContent = "生成中";
        button.appendChild(label);
      }
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
        clearAgentDraft(modelScope());
        agentPicker.restore();
        workspaceController.setCurrentWorkspaceSession("");
        agentPicker.restore(undefined, readAgentDraft(modelScope()).responder);
        input.value = readChatDraft(workspaceController.currentWorkspace?.id || "", "new");
        setChatMode("run");
        setConversationType("default", null);
        clearPendingAttachments();
        thread.innerHTML = emptyMarkup();
        clearTaskProgress();
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
    var loadID = loadCurrent ? ++conversationLoadID : conversationLoadID;
    const revision = ++listRevision;
    clearTimeout(listTimer);
    try {
      var response = await api.request(workspaceController.workspaceScopedURL("/api/v1/webui/conversations"), { headers: { "Accept": "application/json" } });
      var payload = await api.readJSON<ConversationsResponse>(response);
      if (!response.ok) throw new Error(payload.error || "读取对话列表失败");
      if (loadID !== conversationLoadID || revision !== listRevision) return;
      conversations = payload.conversations || [];
      renderConversationList();
      if (conversations.some(c => c.active_turn_id)) {
        listTimer = setTimeout(() => { if (!api.suspended && !document.hidden) void refreshConversations(false); }, 5000);
      }
      if (loadCurrent && !busy) {
        const workspace = workspaceController.currentWorkspace?.id || "";
        const bookmarked = turnBookmark(workspace);
        if (bookmarked && await restoreTurn(bookmarked, loadID)) return;
        if (loadID !== conversationLoadID) return;
      }
      if (loadCurrent && sessionId && conversations.some(function (conversation) { return conversation.id === sessionId; })) await openConversation(sessionId);
    } catch (error) { if (isCancellation(error)) return;
      if (loadID !== conversationLoadID) return;
      conversationList.innerHTML = '<div class="conversation-list-state">读取历史失败</div>';
    }
  }

  async function openConversation(id: string): Promise<void> {
    if (!id || preparingSend || (busy && sessionId === id)) return;
    const nav = ++navigation;
    await leaveVisibleTurn();
    if (nav !== navigation) return;
    conversationScopeEpoch++;
    clearTaskProgress();
    var loadID = ++conversationLoadID;
    statusEl.textContent = "正在读取历史";
    try {
      const active = await api.json<{turn_id: string}>(workspaceController.workspaceScopedURL("/api/v1/webui/turns?session_id=" + encodeURIComponent(id)));
      if (loadID !== conversationLoadID) return;
      if (active.turn_id && await restoreTurn(active.turn_id, loadID)) return;
      if (loadID !== conversationLoadID) return;
      var response = await api.request(workspaceController.workspaceScopedURL("/api/v1/webui/conversations/" + encodeURIComponent(id)), { headers: { "Accept": "application/json" } });
      var payload = await api.readJSON<ConversationHistory & { error?: string }>(response);
      if (!response.ok) throw new Error(payload.error || "读取历史失败");
      if (loadID !== conversationLoadID) return;
      renderHistory(payload, id);
      providerController.loadRuntimeModel();
      statusEl.textContent = "已恢复历史会话";
      statusEl.classList.remove("error");
      scrollToBottom();
    } catch (error) { if (isCancellation(error)) return;
      statusEl.textContent = errorMessage(error);
      statusEl.classList.add("error");
    }
  }

  function persistComposer(): void {
    saveChatDraft(workspaceController.currentWorkspace?.id || "", sessionId || (currentTurnId ? "turn:" + currentTurnId : "new"), input.value);
  }

  async function leaveVisibleTurn(): Promise<void> {
    if (!busy) return;
    persistComposer();
    const workspace = workspaceController.currentWorkspace?.id || "";
    if (turnBookmark(workspace) === currentTurnId) rememberTurn(workspace,"");
    await viewer.detach();
  }

  function deliveryBubble(id: string) {
    return Array.from(thread.querySelectorAll<HTMLElement>("[data-queue-delivery]")).find(bubble => bubble.dataset.queueDelivery === id);
  }

  function showQueueDelivery(message: ConversationMessage, session: string) {
    if (!message.delivery) return;
    const bubble = deliveryBubble(message.delivery.id) || addMessage("user");
    renderQueueDelivery(bubble,message,(id,index) => workspaceController.workspaceScopedURL("/api/v1/webui/command-queue?" +
      new URLSearchParams({ session_id:session, id, view:"attachment", source:"handoff", index:String(index) })));
  }

  function renderHistory(payload: ConversationHistory, id: string, preservePicker = false): void {
    const sameChat = sessionId === (payload.id || id);
    const sameContext = sameChat && renderedContext?.number === (payload.context_number || 0);
    renderedContext = { workspace: workspaceController.currentWorkspace?.id || "", id: payload.id || id, number: payload.context_number || 0 };
    if (!sameChat) {
      const ws = workspaceController.currentWorkspace?.id || "", to = payload.id || id;
      if (forkNavigation?.workspace === ws && forkNavigation.from === sessionId && forkNavigation.to === to) {
        if ([...pendingImages,...pendingFiles].some(item => item.loading)) throw new Error("附件仍在读取，请稍后从对话列表打开分支");
        persistComposer();
        if (pendingImages.length || pendingFiles.length) forkDraftMedia.set(JSON.stringify([ws,sessionId]),snapshotPendingAttachments(pendingImages,pendingFiles));
      }
      clearPendingAttachments();
      const key = JSON.stringify([ws,to]), media = forkDraftMedia.get(key);
      if (media) {
        const restored = restorePendingAttachments(media,() => nextAttachmentID++);
        pendingImages.push(...restored.images); pendingFiles.push(...restored.files);
        forkDraftMedia.delete(key); renderPendingAttachments();
      }
    }
    workspaceController.setCurrentWorkspaceSession(payload.id || id);
    input.value = readChatDraft(workspaceController.currentWorkspace?.id || "",sessionId);
    autoGrow();
    if (preservePicker && sameContext) agentPicker.observe(payload.active_agent || builtinAgent);
    else agentPicker.restore(payload.active_agent || builtinAgent, readAgentDraft(modelScope()).responder);
    setConversationType(payload.conversation_type, payload.group || null);
    setChatMode(payload.mode);
    thread.innerHTML = "";
    taskProgress.restore(payload.task, taskScope());
    renderTaskPanel(taskPanel, taskProgress.state);
    const edits=new TurnFileEdits(), turnStacks=new Map<string,HTMLElement>();
    normalizeHistoryMessages(payload.messages || []).forEach(function (message) {
      if (message.kind === "context_clear") { thread.append(clearDivider(message.content)); return; }
      if (message.delivery) { showQueueDelivery(message,id); return; }
      var bubble = addMessage(message.role, message.sender, message.kind);
      if (message.role === "assistant") {
        for (const tool of message.tools || []) {
          edits.observe(tool);
          if (tool.turn_id && bubble.parentElement) turnStacks.set(tool.turn_id,bubble.parentElement);
        }
        if (message.external_turn && bubble.parentElement) turnStacks.set(message.external_turn,bubble.parentElement);
        renderRestoredAssistant(bubble, message);
        if (message.anchor) markReply(bubble,message.anchor,message.fork);
      } else {
        const images = (message.images || []).map(key => workspaceController.workspaceScopedURL("/api/v1/webui/conversations/" + encodeURIComponent(id) + "?image=" + encodeURIComponent(key)));
        const content = images.length ? message.content.replace(/(?:^|\n)\[图片\]$/, "") : message.content;
        renderUserMessage(bubble, content, [], message.files || [], images);
        if (message.rewind) addRewindControl(bubble.parentElement!, message.rewind);
      }
    });
    for (const [turn,stack] of turnStacks) edits.render(turn,stack);
    if (!payload.messages || !payload.messages.length) thread.innerHTML = emptyMarkup();
    if (payload.forked_from) {
      const marker = forkSourceMarker(payload.forked_from, origin => void jumpToForkSource(origin));
      const after = replyAnchor(thread,payload.forked_from.target)?.closest(".msg");
      if (after) after.after(marker); else thread.prepend(marker);
    }
    if (payload.rewind_receipt) addRewindControl(thread, payload.rewind_receipt, true);
    renderConversationList();
  }

  async function restoreTurn(id: string, loadID: number, preservePicker = false): Promise<boolean> {
    const workspace = workspaceController.currentWorkspace?.id || "";
    const url = workspaceController.workspaceScopedURL("/api/v1/webui/turns?view=history&turn_id=" + encodeURIComponent(id));
    try {
      const base = await api.json<{ history: ConversationHistory | null }>(url);
      if (loadID !== conversationLoadID || busy) return true;
      if (base.history) renderHistory(base.history, base.history.id || "", preservePicker);
      else thread.textContent = "";
      void watchTurn(id, "", undefined);
      return true;
    } catch (err) {
      if (![404,410].includes((err as {status?: number}).status || 0)) throw err;
      if (turnBookmark(workspace) === id) rememberTurn(workspace, "");
      return false;
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
    if (kind === "error") msg.classList.add("participant-error");
    var avatar = document.createElement("div");
    avatar.className = "avatar";
    avatar.setAttribute("aria-hidden", "true");
    var stack = document.createElement("div");
    stack.className = "message-stack";
    var label = document.createElement("div");
    label.className = "message-label";
    var bubble = document.createElement("div");
    bubble.className = "bubble";
    msg.appendChild(avatar);
    stack.appendChild(label);
    stack.appendChild(bubble);
    msg.appendChild(stack);
    setMessageSender(msg, role, sender);
    thread.appendChild(msg);
    if (streamRender) streamRender.schedule();
    else scrollToBottom();
    return bubble;
  }

  function setMessageSender(msg: HTMLElement, role: string, sender?: string): void {
    const user = role === "user";
    const name = user ? "你" : sender || builtinAgent;
    const external = !user && name !== builtinAgent;
    msg.classList.toggle("participant", external);
    msg.querySelector(".avatar")!.replaceChildren(user ? "你" : external ? name.slice(0, 2).toUpperCase() : createIcon("bot"));
    msg.querySelector(".message-label")!.textContent = !user && !external && conversationType === "group" ? "bqagent · 调度汇总" : name;
  }

  function addACPPermissionCard(payload: ACPPermissionPayload, pending: Record<string, HTMLElement>): void {
    const workspace = workspaceController.currentWorkspace?.id || "";
    showAgentPrompt(payload, pending, () => addMessage("assistant", payload.agent || "外部 Agent"), async (option, value, answers) => {
      const response = await api.json<{ accepted?: boolean; error?: string }>("/api/v1/webui/acp/permissions", {
        method: "POST", headers: { "Content-Type": "application/json", "Accept": "application/json" },
        body: JSON.stringify({ workspace_id: workspace, request_id: payload.request_id, option_id: option, value, answers }),
      });
      if (!response.accepted) throw new Error(response.error || "提交 Agent 回答失败");
    });
  }

  function expireACPPermissionCards(pending: Record<string, HTMLElement>): void {
    Object.keys(pending).forEach(id => settleAgentPrompt(pending, id));
  }

  function renderUserMessage(bubble: HTMLDivElement, text: string, images: SentImage[], files: SentFile[], restored: string[] = []): void {
    bubble.textContent = "";
    if (text) {
      var textNode = document.createElement("div");
      textNode.className = "user-message-text";
      textNode.textContent = text;
      bubble.appendChild(textNode);
    }
    if (images.length || restored.length) {
      var gallery = document.createElement("div");
      gallery.className = "user-images";
      const urls = images.map(image => "data:" + image.mime_type + ";base64," + image.data_base64).concat(restored);
      urls.forEach(function (url, index) {
        var preview = document.createElement("img");
        preview.className = "user-image";
        if (restored.length) preview.loading = "lazy";
        preview.src = url;
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
      remove.disabled = !canAttach();
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
      remove.disabled = !canAttach();
      remove.setAttribute("aria-label", "删除文件 " + file.name);
      remove.addEventListener("click", function () { removePendingFile(file.id); });
      item.appendChild(copy);
      item.appendChild(remove);
      attachmentTray.appendChild(item);
    });
    workspaceController.updateWorkspacePreviewAttachState();
    if (busy) {
      refreshModelControls();
      renderTurnInputControls(true);
      slashCommands.contextChanged();
    }
  }

  function canAttach(): boolean { return !busy || (conversationType !== "group" && chatMode === "run"); }

  function removePendingImage(id: number): void {
    if (!canAttach()) return;
    pendingImages = pendingImages.filter(function (image) {
      if (image.id !== id) return true;
      URL.revokeObjectURL(image.objectURL);
      return false;
    });
    renderPendingAttachments();
  }

  function removePendingFile(id: number): void {
    if (!canAttach()) return;
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
    if (!canAttach()) return false;
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
    if (!canAttach()) return;
    var value = serverFilePath.value.trim();
    if (!addWorkspacePath(value, 0)) return;
    serverFilePath.value = "";
    setAttachmentMenu(false);
    input.focus();
  }

  function setAttachmentMenu(open: boolean): void {
    open = Boolean(open) && canAttach();
    if (open) setReasoningEffortMenu(false);
    attachmentMenu.hidden = !open;
    addAttachmentBtn.setAttribute("aria-expanded", open ? "true" : "false");
    if (open) setTimeout(function () { (busy ? uploadImageBtn : chatMode === "ask" ? modeAskBtn : modeRunBtn).focus(); }, 0);
  }

  function setChatMode(value: unknown): void {
    chatMode = conversationType === "group" ? "run" : normalizeChatMode(value);
    thread.querySelectorAll<HTMLButtonElement>(".rewind-action, .fork-action").forEach(button => {
      button.hidden = chatMode !== "run" || conversationType === "group";
    });
    [modeRunBtn, modeAskBtn].forEach(function (button) {
      var selected = button.dataset.mode === chatMode;
      button.classList.toggle("selected", selected);
      button.setAttribute("aria-checked", selected ? "true" : "false");
    });
    input.placeholder = conversationType === "group" ? "群聊模式：无 @ 由 bqagent 处理，@成员可定向交互" : chatModePlaceholder(chatMode);
    addAttachmentBtn.title = chatMode === "ask" ? "Ask 模式与附件" : "Run 模式与附件";
    addAttachmentBtn.setAttribute("aria-label", chatMode === "ask" ? "Ask 模式：选择模式或添加文件" : "Run 模式：选择模式或添加文件");
    refreshAgentControls();
  }

  function refreshAgentControls(): void {
    commandQueue.show(workspaceController.currentWorkspace?.id || "", sessionId);
    agentPicker.configure(conversationType, chatMode, busy);
    agentPickerLabel.hidden = conversationType === "group";
    refreshModelControls();
    if (busy) renderTurnInputControls(true);
    slashCommands.contextChanged();
  }

  function modelScope(): string {
    const workspace = workspaceController.currentWorkspace?.id || "";
    return JSON.stringify([workspace, sessionId || null, sessionId ? displayedContextNumber(workspace, sessionId) : 0]);
  }

  function refreshModelControls(): void {
    const target = busy ? busyRoute(input.value).agent : agentPicker.externalResponder(input.value.trim());
    const agent = target === builtinAgent ? undefined : target;
    const external = !!agent;
    contextMeter.configure(modelScope(), agent || "", displayedContextNumber(workspaceController.currentWorkspace?.id || "",sessionId));
    agentModels.configure(modelScope(), agent || "", busy, !!agent && !sessionId && agentPicker.allowsCustomModel(agent));
    providerController.modelSelect.parentElement!.hidden = external;
    reasoningEffortControl.hidden = external;
    if (!busy) {
      input.placeholder = conversationType === "group" ? "群聊模式：无 @ 由 bqagent 处理，@成员可定向交互"
        : external ? "给 " + agent + " 发送消息" : chatModePlaceholder(chatMode);
    }
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
      '<p class="galaxy-hint">拖动探索四大星区 · 边界循环相连 · 双击归位</p>' +
      '<div class="prompts" aria-label="快捷提示">' +
      '<button class="prompt" type="button">' + iconMarkup("folder-tree") + '<strong>理解项目</strong><span>帮我梳理这个项目的结构</span></button>' +
      '<button class="prompt" type="button">' + iconMarkup("list-todo") + '<strong>制定计划</strong><span>把这个想法拆成执行步骤</span></button>' +
      '<button class="prompt" type="button">' + iconMarkup("code-xml") + '<strong>审查代码</strong><span>检查一段代码里的风险</span></button>' +
      '</div></div>';
  }

  function renderRestoredAssistant(bubble: HTMLDivElement, message: ConversationMessage): void {
    if (message.summary) { renderAgentSummary(bubble, message.summary, false); return; }
    if (message.plan) { renderAgentPlan(bubble,message.plan,message.content); return; }
    if (message.prompt) { renderHistoricalPrompt(bubble, message.sender || "外部 Agent", message.prompt); return; }
    var view = historyAssistantView(message);
    if (view.tools.length) {
      var cards: ToolCards = Object.create(null);
      view.tools.forEach(function (tool, index) {
        var payload = restoredToolPayload(tool, index);
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
    if (on) slashCommands.close();
    busy = on;
    commandQueue.setSendActivity(on, false);
    refreshAgentControls();
    sendBtn.disabled = !workspaceController.workspaceReady;
    sendBtn.classList.toggle("stop", on);
    sendBtn.title = on ? "停止生成" : "发送";
    sendBtn.setAttribute("aria-label", on ? "停止生成" : "发送");
    input.disabled = !workspaceController.workspaceReady;
    renderTurnInputControls(on);
    addAttachmentBtn.disabled = !canAttach();
    uploadFileBtn.disabled = !canAttach();
    uploadImageBtn.disabled = !canAttach();
    modeRunBtn.disabled = on;
    modeAskBtn.disabled = on;
    serverFilePath.disabled = !canAttach();
    addServerPathBtn.disabled = !canAttach();
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
    refreshModelControls();
    if (busy) renderTurnInputControls(true);
  }

  function addMessageMeta(bubble: HTMLDivElement, generation?: GenerationMetrics): void {
    var stack = bubble && bubble.parentElement;
    if (!stack) return;
    var previous = stack.querySelector(".message-meta");
    if (previous) previous.remove();
    if (!generation) return;

    var firstTokenLatency = formatFirstTokenLatency(generation.first_token_latency_ms);
    var generationSpeed = formatGenerationSpeed(generation.tokens_per_second);
    var parts: string[] = [];
    if (firstTokenLatency) {
      parts.push(firstTokenLatency);
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
    if (generationSpeed) {
      parts.push(generationSpeed);
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

  function turnInputKindLabel(kind: string): string {
    return kind === "follow_up" ? "后续任务" : "纠偏";
  }

  function createTurnInputSession(turnId: string): TurnInputSession {
    return new TurnInputSession(turnId, workspaceController.currentWorkspace ? workspaceController.currentWorkspace.id : "", {
      request: function (url, init) { return api.request(url, init); },
      scope: function () { return (workspaceController.currentWorkspace ? workspaceController.currentWorkspace.id : "") + ":" + conversationScopeEpoch; },
      takeDraft: function () {
        var text = input.value;
        if (text.trim() && (pendingImages.length || pendingFiles.length)) {
          setAttachmentError("附件暂不支持中途发送，已保留到下次发送；本次仅发送文字");
        }
        if (text.trim()) { input.value = ""; persistComposer(); autoGrow(); }
        return text;
      },
      getDraft: function () { return input.value; },
      setDraft: function (text) { input.value = text; persistComposer(); autoGrow(); },
      renderQueue: function (items) {
        turnInputQueue.textContent = "";
        items.forEach(function (item) {
          var row = document.createElement("div");
          row.className = "turn-input-queued";
          var label = document.createElement("span");
          label.textContent = TURN_INPUT_STATE_LABELS[item.state] + " · " + turnInputKindLabel(item.kind);
          var text = document.createElement("span");
          text.textContent = item.text;
          row.appendChild(label);
          row.appendChild(text);
          turnInputQueue.appendChild(row);
        });
        turnInputQueue.hidden = items.length === 0;
      },
      renderApplied: function (kind, text) {
        var bubble = addMessage("user");
        renderUserMessage(bubble, text, [], []);
        var chip = document.createElement("span");
        chip.className = "turn-input-chip";
        chip.textContent = TURN_INPUT_STATE_LABELS.applied + " · " + turnInputKindLabel(kind);
        bubble.appendChild(chip);
      },
      notify: setAttachmentError
    });
  }

  function busyRoute(text: string) {
    return agentPicker.busyContext(text,runningExternalAgent,pendingImages.length + pendingFiles.length > 0);
  }

  function currentInputActions() {
    const route = busyRoute(input.value.trimStart());
    if (parseClear(input.value)) return { ...route, steer: false, followUp: false, queue: undefined };
    const compact = agentModels.isCompactCommand(route.agent, route.query, agentPicker.supportsCompact(route.agent));
    const attachments = !!(pendingImages.length || pendingFiles.length);
    const nextTurnSelection = agentModels.hasNextTurnSelection(route.agent, runningModelRevision, runningSelection);
    return { ...route, ...busyInputActions(route.query, route.agent, !!route.qualified, runningExternalAgent,
      nativeInputKinds, compact, attachments, nextTurnSelection) };
  }

  let preparingQueuePick = false;
  async function enqueueDraft(): Promise<void> {
    if (preparingQueuePick || !busy || stopRequested || !currentTurnId) return;
    const { agent, query, queue } = currentInputActions();
    if (agent && agent !== builtinAgent && queue === "message") {
      const text = input.value, turn = currentTurnId;
      preparingQueuePick = true;
      try {
        if (!await agentModels.readyForSend(agent)) return;
      } finally { preparingQueuePick = false; }
      const current = currentInputActions();
      if (!busy || stopRequested || turn !== currentTurnId || text !== input.value || current.agent !== agent || current.queue !== queue) return;
    }
    const attachments = pendingImages.length + pendingFiles.length > 0;
    if (!agent || !queue || (!query.trim() && !attachments)) return;
    if (attachments && queue === "compact") {
      setAttachmentError("/compact 不接受附件，请先移除附件。");
      return;
    }
    if (pendingImages.length && agent !== builtinAgent && (agentModels.supportsImages(agent) ?? (!sessionId ? agentPicker.supportsImages(agent) : undefined)) === false) {
      setAttachmentError(agent + " 的当前接口不支持图片，请移除图片或切换 Agent。");
      return;
    }
    if (queue === "compact" && agent === "codex" && query.trim().toLowerCase() !== "/compact") {
      setAttachmentError("Codex 的 /compact 不接受附加说明。");
      return;
    }
    if (queue === "message" && !agentModels.validFor(agent)) return;
    const options = agent === builtinAgent ? { agent_effort: reasoningEffort === "auto" ? undefined : reasoningEffort } : agentModels.queueSelection(agent);
    void commandQueue.enqueue(agent, queue === "message" ? input.value.trim() : query.trim(), queue === "message" ? options : undefined);
  }

  async function pickPlanCommand(text: string, view: {query: string; agent?: string; qualified?: boolean}): Promise<boolean> {
    const scope = modelScope(), active = busy;
    const result = await agentModels.pickPlanCommand(view.agent,view.query, () => modelScope() === scope && busy === active &&
      input.value.trim() === text && (busy ? busyRoute(text) : agentPicker.commandContext(text)).agent === view.agent);
    if (!result) return false;
    if (result === "stale") return true;
    if (result === "unknown") {
      setAttachmentError("未能确认当前 Agent 的规划模式，请刷新模型目录。");
      return true;
    }
    if (result === "unavailable") {
      setAttachmentError("当前 Agent 接口未提供规划模式。");
      return true;
    }
    if (view.qualified && view.agent) agentPicker.selectNext(view.agent);
    input.value = "";
    input.dispatchEvent(new Event("input"));
    slashCommands.close();
    statusEl.classList.remove("error");
    statusEl.textContent = "下一条消息将使用规划模式";
    return true;
  }

  async function submitTurnInput(kind: TurnInputKind, nativeOnly = false): Promise<void> {
    if (!busy || !currentTurnId || !turnInputs || stopRequested) return;
    const route = busyRoute(input.value.trim());
    if (route.query.trim() === "/plan" && await pickPlanCommand(input.value.trim(),route)) return;
    if (!busy || !currentTurnId || !turnInputs || stopRequested) return;
    const clear = parseClear(input.value);
    if (clear) { setAttachmentError(clearRefusal(clear.args, chatMode, conversationType, !!(pendingImages.length || pendingFiles.length), true)!); return; }
    const actions = currentInputActions();
    if (!nativeOnly && actions.queue && (kind === "follow_up" || !actions.steer)) {
      enqueueDraft();
      return;
    }
    if (kind === "steering" ? actions.steer : actions.followUp) void turnInputs.submit(kind);
    else if (input.value.trim()) setAttachmentError("当前回复结束后可以发送这条命令。");
  }

  function renderTurnInputControls(on: boolean): void {
    const actions = currentInputActions();
    turnInputActions.hidden = !on || (!actions.queue && !actions.steer && !actions.followUp);
    steerBtn.hidden = !actions.steer;
    followUpBtn.hidden = !actions.followUp;
    followUpBtn.textContent = runningExternalAgent ? "本轮追加" : "排队后续";
    followUpBtn.title = runningExternalAgent ? "通过 Agent 原生接口追加到本轮" : "作为后续任务排队 (Ctrl/Cmd + Enter)";
    queueSendBtn.hidden = !actions.queue;
    queueSendBtn.disabled = stopRequested || (!actions.query.trim() && !pendingImages.length && !pendingFiles.length) ||
      [...pendingImages,...pendingFiles].some(item => item.loading);
    queueSendBtn.textContent = actions.queue === "compact" ? "排队压缩" : "排队发送";
    if (!on) {
      composerHints.textContent = "Enter 发送 · Shift + Enter 换行 · / 命令 · 支持 Markdown 与附件";
    } else if (parseClear(input.value)) {
      composerHints.textContent = "请先停止或等待回复结束后再清空上下文";
    } else if (actions.queue === "compact") {
      composerHints.textContent = "Enter 排队压缩 · Shift + Enter 换行";
    } else if (actions.queue && !actions.steer) {
      input.placeholder = "给 " + actions.agent + " 写下一条消息";
      composerHints.textContent = "Enter 排队发送 · Shift + Enter 换行";
    } else if (nativeInputKinds.size) {
      input.placeholder = "给正在回答的 " + runningExternalAgent + " 补充指令";
      composerHints.textContent = "Enter 纠偏 · Ctrl/Cmd + Enter 排队发送";
    } else if (runningExternalAgent) {
      input.placeholder = runningExternalAgent + " 正在回答，可先起草下一条消息";
      composerHints.textContent = "外部 Agent 回复期间可停止生成";
    } else {
      input.placeholder = "任务进行中：Enter 纠偏，Ctrl/Cmd + Enter 排队为后续任务";
      composerHints.textContent = "Enter 纠偏 · Ctrl/Cmd + Enter 排队后续 · 附件暂不支持中途发送";
    }
  }

  async function stopCurrentTurn(): Promise<void> {
    if (!busy || !currentTurnId || stopRequested) return;
    const stopping = currentTurnId;
    const starting = !webTurnStarted;
    stopRequested = true;
    commandQueue.setStopping(true);
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
      if (currentTurnId !== stopping) return;
      if (!result.stopped) {
        stopRequested = false;
        commandQueue.setStopping(false);
        sendBtn.disabled = false;
        statusEl.textContent = "正在读取回合状态";
        if (starting) {
          if (webTurnStarted) void stopCurrentTurn();
          else stopOnStart = true;
        }
      }
    } catch (error) {
      if (currentTurnId !== stopping) return;
      stopRequested = false;
      commandQueue.setStopping(false);
      sendBtn.disabled = false;
      setAttachmentError("未能送达停止请求，可再次点击停止：" + errorMessage(error));
    }
  }

  const clearUpdates = typeof BroadcastChannel === "function" ? new BroadcastChannel("bqagent.context-clear") : undefined;
  window.addEventListener("beforeunload", () => clearUpdates?.close(), { once:true });
  function displayedContextNumber(workspace: string, id: string) {
    return renderedContext?.id === id && renderedContext.workspace === workspace ? renderedContext.number : 0;
  }
  async function refreshClearedHistory(workspace: string, id: string) {
    const nav = navigation, load = conversationLoadID;
    const visible = () => workspace === (workspaceController.currentWorkspace?.id || "") && id === sessionId &&
      nav === navigation && load === conversationLoadID && !busy;
    if (!id || !visible()) return;
    const history = await api.json<ConversationHistory>(workspaceController.workspaceScopedURL("/api/v1/webui/conversations/" + encodeURIComponent(id)));
    if (visible() && (history.context_number || 0) >= displayedContextNumber(workspace, id)) {
      const draft = input.value;
      renderHistory(history, id, true);
      input.value = draft; autoGrow(); void commandQueue.refresh(); scrollToBottom();
    }
  }
  clearUpdates?.addEventListener("message", event => {
    const { workspace, id } = event.data || {};
    if (typeof workspace === "string" && typeof id === "string") void refreshClearedHistory(workspace, id).catch(() => {});
  });

  let checkingContext = false;
  async function checkContextBoundary() {
    if (checkingContext || busy || preparingSend || api.suspended || document.hidden || !sessionId || conversationType === "group") return;
    const workspace = workspaceController.currentWorkspace?.id || "", id = sessionId, nav = navigation;
    checkingContext = true;
    try {
      const agent = agentPicker.externalResponder(input.value.trim()), scope = modelScope();
      const before = agent && contextMeter.captured(scope,agent);
      const query = new URLSearchParams({view:"context",session_id:id});
      if (agent) query.set("agent",agent);
      const state = await api.json<{ context_number: number; turn_id: string; context_usage?:AgentContextUsage }>(workspaceController.workspaceScopedURL(
        "/api/v1/webui/turns?" + query));
      if (nav !== navigation || workspace !== (workspaceController.currentWorkspace?.id || "") || id !== sessionId || busy || preparingSend) return;
      if (state.context_number > displayedContextNumber(workspace, id)) {
        if (state.turn_id && await restoreTurn(state.turn_id, conversationLoadID, true)) return;
        await refreshClearedHistory(workspace, id);
      } else if (agent && !state.turn_id && state.context_number === displayedContextNumber(workspace,id)) {
        contextMeter.reconcileIdle(scope,agent,before || undefined,state.context_usage);
      }
    } catch { /* A visible idle chat checks again after reconnecting. */ }
    finally { checkingContext = false; }
  }
  const contextTimer = window.setInterval(() => void checkContextBoundary(), 10000);
  document.addEventListener("visibilitychange", () => { if (!document.hidden) void checkContextBoundary(); });
  window.addEventListener("beforeunload", () => window.clearInterval(contextTimer), { once:true });

  async function sendClear(text: string, clear: NonNullable<ReturnType<typeof parseClear>>) {
    const refusal = clearRefusal(clear.args, chatMode, conversationType, !!(pendingImages.length || pendingFiles.length), busy);
    if (refusal) { setAttachmentError(refusal); return; }
    const workspace = workspaceController.currentWorkspace?.id || "", id = sessionId, nav = navigation;
    const original = input.value;
    const visible = () => nav === navigation && id === sessionId && workspace === (workspaceController.currentWorkspace?.id || "");
    try {
      const finish = await clearRequests.clear(workspace, id, text, clear.agent || agentPicker.requestAgent);
      clearUpdates?.postMessage({ workspace, id });
      await refreshClearedHistory(workspace,id);
      if (visible()) {
        if (input.value === original) { input.value = ""; persistComposer(); autoGrow(); }
        statusEl.textContent = id ? "上下文已清空" : "当前对话还没有上下文";
        statusEl.classList.remove("error");
        finish();
      }
    } catch (error) {
      if (visible() && !isCancellation(error)) setAttachmentError(errorMessage(error));
    }
  }

  function addRewindControl(parent: HTMLElement, action: RewindAction, replay = false): void {
    if (!replay && action.agent === "claude") return;
    const button = rewindButton(action, replay, () => void rewindMessage(action, replay));
    button.hidden = chatMode !== "run" || conversationType === "group";
    parent.append(button);
  }

  function markReply(bubble: HTMLElement, target: string, fork?: ForkAction): void {
    const msg = bubble.closest<HTMLElement>(".msg");
    if (!msg) return;
    msg.dataset.replyAnchor = target;
    if (!fork || msg.querySelector(".fork-action")) return;
    const button = document.createElement("button");
    button.className = "fork-action";
    button.type = "button";
    button.textContent = "从这里创建分支";
    if (fork.tip) button.dataset.forkTip = "true";
    button.hidden = chatMode !== "run" || conversationType === "group";
    button.addEventListener("click", async () => {
      if (button.disabled || preparingSend) return;
      const id = sessionId, ws = workspaceController.currentWorkspace?.id || "", nav = navigation;
      button.disabled = true;
      button.textContent = "正在创建分支…";
      try {
        const child = await forkRequests.create(ws,id,fork);
        if (ws !== (workspaceController.currentWorkspace?.id || "")) return;
        if (nav === navigation && id === sessionId) {
          await openRelatedConversation(child);
        }
        void refreshConversations(false);
      } catch (error) {
        if (nav === navigation && !isCancellation(error)) setAttachmentError(errorMessage(error));
      } finally { button.disabled = false; button.textContent = "从这里创建分支"; }
    });
    bubble.parentElement?.append(button);
  }

  async function jumpToForkSource(origin: ForkOrigin): Promise<void> {
    await openRelatedConversation(origin.session);
    if (sessionId !== origin.session) return;
    const anchor = replyAnchor(thread,origin.target);
    if (!anchor) { statusEl.textContent = "已打开来源聊天，这条回复已不在当前历史中"; return; }
    anchor.tabIndex = -1;
    anchor.focus({preventScroll:true});
    anchor.scrollIntoView({block:"center"});
  }

  async function openRelatedConversation(id: string): Promise<void> {
    const relation = { workspace: workspaceController.currentWorkspace?.id || "", from:sessionId, to:id };
    forkNavigation = relation;
    try { await openConversation(id); } finally { if (forkNavigation === relation) forkNavigation = undefined; }
  }

  async function rewindMessage(action: RewindAction, replay: boolean): Promise<void> {
    if (chatMode !== "run" || conversationType === "group") return;
    if (busy || preparingSend) { setAttachmentError("请等当前操作结束后再回退。"); return; }
    const hasDraft = () => Boolean(input.value || pendingImages.length || pendingFiles.length);
    if (hasDraft()) { setAttachmentError("请先保存或清空输入框中的草稿和附件。"); return; }
    if (!replay && !action.operation && !window.confirm("回到这条消息发送之前？它和后续回复将从对话移除，原消息和附件将放回输入框。工作区文件会保留当前内容。")) return;
    const workspace = workspaceController.currentWorkspace?.id || "", id = sessionId, nav = navigation;
    const visible = () => nav === navigation && id === sessionId && workspace === (workspaceController.currentWorkspace?.id || "");
    preparingSend = true;
    try {
      const draft = await requestRewind(api, workspace, id, action, replay);
      if (!replay) clearUpdates?.postMessage({ workspace, id });
      if (!visible()) return;
      await refreshClearedHistory(workspace, id);
      if (!visible() || displayedContextNumber(workspace, id) !== draft.context_number) return;
      if (hasDraft()) { setAttachmentError("回退已完成。保存当前输入后，可点「取回回退草稿」。"); return; }
      const media = restorePendingAttachments(draft, () => nextAttachmentID++);
      pendingImages.push(...media.images);
      pendingFiles.push(...media.files);
      agentPicker.selectNext(draft.agent || action.agent || "codex");
      input.value = draft.text;
      persistComposer(); autoGrow(); renderPendingAttachments(); input.focus();
      statusEl.textContent = "已恢复原消息，可编辑后发送";
      statusEl.classList.remove("error");
    } catch (error) {
      if (visible() && !isCancellation(error)) setAttachmentError(errorMessage(error));
    } finally { preparingSend = false; }
  }

  async function send(): Promise<void> {
    if (!workspaceController.workspaceReady || busy || preparingSend) return;
    preparingSend = true;
    const sendScope = modelScope();
    const clear = parseClear(input.value);
    if (clear) {
      slashCommands.close();
      try { await sendClear(input.value.trim(), clear); } finally { preparingSend = false; }
      return;
    }
    if (!await commandQueue.reconcileDraft()) { preparingSend = false; return; }
    await agentPicker.readyForRoute(input.value);
    if (modelScope() !== sendScope) { preparingSend = false; return; }
    refreshModelControls();
    var pendingModelSelection = providerController.runtimeModelSelectionPromise;
    if (!agentPicker.externalResponder(input.value.trim()) && !await waitForModelSelection(pendingModelSelection)) {
      preparingSend = false;
      return;
    }
    slashCommands.close();
    var text = input.value.trim();
    const changedToClear = parseClear(text);
    if (changedToClear) {
      try { await sendClear(text, changedToClear); } finally { preparingSend = false; }
      return;
    }
    const externalResponder = agentPicker.externalResponder(text);
    const command = agentPicker.commandContext(text);
    if (command.query.trim() === "/plan" && await pickPlanCommand(text,command)) {
      preparingSend = false;
      return;
    }
    const compact = agentModels.isCompactCommand(externalResponder, command.query, agentPicker.supportsCompact(externalResponder));
    if (compact && (pendingImages.length || pendingFiles.length)) {
      setAttachmentError("/compact 不接受附件，请先移除附件。");
      preparingSend = false;
      return;
    }
    if (compact && externalResponder === "codex" && command.query.trim().toLowerCase() !== "/compact") {
      setAttachmentError("Codex 的 /compact 不接受附加说明。");
      preparingSend = false;
      return;
    }
    if (!compact && !await agentModels.readyForSend(externalResponder)) {
      preparingSend = false;
      return;
    }
    try {
      // More attachments can arrive while a provider refreshes its inventory.
      while (pendingImageReads.length || pendingFileReads.length) {
        await Promise.all([...pendingImageReads.splice(0), ...pendingFileReads.splice(0)]);
      }
    } catch {
      preparingSend = false;
      return;
    }
    if (modelScope() !== sendScope || text !== input.value.trim() || externalResponder !== agentPicker.externalResponder(text)) {
      preparingSend = false;
      return;
    }
    const supportsImages = externalResponder && (agentModels.supportsImages(externalResponder) ??
      (sessionId ? undefined : agentPicker.supportsImages(externalResponder)));
    if (externalResponder && pendingImages.length && supportsImages === false) {
      setAttachmentError(externalResponder + " 的当前接口不支持图片，请移除图片或切换 Agent");
      preparingSend = false;
      return;
    }
    var sentAttachments = takePendingAttachmentsForSend(pendingImages, pendingFiles);
    var sentImages = sentAttachments.images;
    var sentFiles = sentAttachments.files;
    if (!text && !sentImages.length && !sentFiles.length) {
      renderPendingAttachments();
      preparingSend = false;
      return;
    }

    conversationLoadID++;
    const id = createTurnId();
    if (!sessionId && conversationType !== "group") recordFirstAgentTurn(modelScope(), id);
    if (!compact) agentModels.submitted(externalResponder, id);
    const body = JSON.stringify({
      workspace_id: workspaceController.currentWorkspace ? workspaceController.currentWorkspace.id : "",
      message: text,
      ...agentModels.turnOptions(agentPicker.requestAgent, externalResponder, compact),
      images: sentImages,
      files: sentFiles,
      session_id: sessionId,
      turn_id: id,
      mode: chatMode,
      conversation_type: conversationType,
      reasoning_effort: reasoningEffort === "auto" ? "" : reasoningEffort
    });
    const sentBubble = addMessage("user");
    sentBubble.dataset.rootTurn = id;
    renderUserMessage(sentBubble, text, sentImages, sentFiles);
    input.value = "";
    persistComposer();
    autoGrow();
    renderPendingAttachments();
    clearAttachmentError();

    agentPicker.beginSend(externalResponder);
    preparingSend = false;
    await watchTurn(id, text, externalResponder, body);
  }

  async function watchTurn(id: string, text: string, externalResponder: string | undefined, body?: string): Promise<void> {
    const nav = navigation, workspace = workspaceController.currentWorkspace?.id || "";
    const next = await viewer.observe(id, controller => renderTurn(controller, id, text, externalResponder, body));
    if (next && nav === navigation && workspace === (workspaceController.currentWorkspace?.id || "") && !api.suspended && !busy) {
      const loaded = await restoreTurn(next, conversationLoadID, true);
      if (!loaded && nav === navigation && !busy && sessionId) await openConversation(sessionId);
    } else if (nav === navigation && !api.suspended && !busy) {
      commandQueue.afterTurn();
    }
  }

  async function renderTurn(controller: AbortController, id: string, text: string, externalResponder: string | undefined, body?: string): Promise<string | undefined> {
    const sentModelRevision = body === undefined ? undefined : agentModels.draftRevision(externalResponder);
    let nextTurn: string | undefined;
    const workspace = workspaceController.currentWorkspace?.id || "";
    rememberTurn(workspace, id);
    let recoverHistory = "";
    currentTurnId = id;
    var inputSession = createTurnInputSession(currentTurnId);
    turnInputs = inputSession;
    var pendingFromServer: PendingInputPayload[] = [];
    currentController = controller;
    stopRequested = false;
    webTurnStarted = false;
    stopOnStart = false;

    runningExternalAgent = externalResponder;
    runningModelRevision = sentModelRevision;
    var bubble: HTMLDivElement | null = conversationType === "group" || body === undefined || externalResponder ? null : addMessage("assistant", externalResponder);
    var toolCards: ToolCards = Object.create(null);
    // Closed segments of this turn only, so background job results can settle
    // cards left running in earlier segments without touching other sessions.
    var closedToolSegments: ToolTimelineSegment[] = [];
    var participantBubbles: Record<string, HTMLDivElement> = Object.create(null) as Record<string, HTMLDivElement>;
    var pendingPermissions: Record<string, HTMLElement> = Object.create(null) as Record<string, HTMLElement>;
    preparingSend = false;
    var caret = document.createElement("span");
    caret.className = "caret";
    function ensureCoordinatorBubble(): HTMLDivElement {
      if (!bubble) bubble = addMessage("assistant", runningExternalAgent || "bqagent");
      bubble.classList.add("is-streaming");
      if (!caret.parentElement) bubble.appendChild(caret);
      return bubble;
    }
    if (bubble) ensureCoordinatorBubble();
    var snapshots = new MessageSnapshots(currentTurnId);
    var streamTaskScope = taskScope();
    var streamTurnId = currentTurnId;
    var streamed = "";
    var progress = "";
    var contextProgress = new ContextProgress(() => addMessage("assistant", "bqagent"));
    var renders = new StreamRenderQueue(main);
    streamRender = renders;
    const fileEdits=new TurnFileEdits();
    let editTurn="";
    const native = new ExternalTimelineView(sender => {
      if (bubble) closeCoordinatorSegment();
      return addMessage("assistant", sender);
    }, (target, value, streaming) => {
      renders.schedule(target, () => {
        target.classList.toggle("is-streaming", streaming);
        target.classList.toggle("rendered", !streaming);
        if (streaming) target.textContent = value;
        else renderReply(target, value);
      });
    }, (target,plan,text) => renders.schedule(target, () => renderAgentPlan(target,plan,text)),
       (target,view) => renders.schedule(target, () => renderAgentSummary(target,view)));
    function queueText(target: HTMLDivElement, text: string, notice = false): void {
      renders.schedule(target, () => {
        caret.remove();
        target.classList.toggle("notice", notice);
        target.textContent = text;
        target.appendChild(caret);
      });
    }
    function archiveToolSegment(segmentBubble: HTMLDivElement | null): void {
      if (!Object.keys(toolCards).length) return;
      var archivedCards = toolCards;
      var stack = segmentBubble ? segmentBubble.parentElement : null;
      var timeline = (stack ? stack.querySelector(".tool-timeline") : null) as ToolTimeline | null;
      closedToolSegments.push({ cards: archivedCards, refresh: function () { if (timeline) updateToolGroupSummary(timeline, archivedCards); } });
    }
    function closeCoordinatorSegment(): void {
      renders.flush();
      archiveToolSegment(bubble);
      if (!bubble) {
        streamed = "";
        progress = "";
        toolCards = Object.create(null);
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
      toolCards = Object.create(null);
    }

    setBusy(true);
    try {
      const signal = currentController.signal;
      const streamURL = workspaceController.workspaceScopedURL("/api/v1/webui/turns?compact=1&turn_id=" + encodeURIComponent(id));
      const start = body === undefined ? undefined : () => api.request("/api/v1/webui/chat?compact=1", {
        method: "POST", headers: { "Content-Type": "application/json" }, body, signal,
      });
      for await (const evt of followTurn(start, cursor => api.request(streamURL + "&after=" + cursor, {signal}), signal,
        () => { statusEl.textContent = "连接中断，正在接回原回合"; })) {
        if (statusEl.textContent === "连接中断，正在接回原回合") statusEl.textContent = "已重新连接，正在生成";
        if (evt.event === "turn_start") {
          webTurnStarted = true;
          thread.querySelectorAll("[data-fork-tip]").forEach(button => button.remove());
          if (stopOnStart) { stopOnStart = false; void stopCurrentTurn(); }
          const initial = parseJSONEvent<AgentTurnSelection & { message: string; mode?: string; conversation_type?: ConversationType;
            images?: SentImage[]; files?: SentFile[] }>(evt.data);
          runningSelection = { agent: initial.agent, agent_model: initial.agent_model, agent_effort: initial.agent_effort,
            agent_service_tier: initial.agent_service_tier, agent_interaction_mode: initial.agent_interaction_mode };
          if (body === undefined) {
            text = initial.message || "";
            if (initial.agent) agentPicker.observe(initial.agent);
            if (initial.conversation_type) setConversationType(initial.conversation_type, currentGroup);
            if (initial.mode) setChatMode(initial.mode);
            runningExternalAgent = agentPicker.externalResponder(text, initial.agent || agentPicker.reportedAgent);
            refreshModelControls();
            renderTurnInputControls(true);
            const sentBubble = addMessage("user");
            sentBubble.dataset.rootTurn = id;
            renderUserMessage(sentBubble, text, initial.images || [], initial.files || []);
          }
          continue;
        }
        if (evt.event === "turn_session") {
          const bound = parseJSONEvent<{ session_id: string }>(evt.data);
          if (bound.session_id && sessionId !== bound.session_id) {
            const newChat = !sessionId;
            const bindPicks = newChat && (body !== undefined || readAgentDraft(modelScope()).firstTurn === id);
            const draft = input.value || readChatDraft(workspace, "turn:" + id);
            workspaceController.setCurrentWorkspaceSession(bound.session_id);
            if (bindPicks) agentModels.bindDraft(modelScope());
            refreshModelControls();
            input.value = draft;
            persistComposer();
            saveChatDraft(workspace, "turn:" + id, "");
          }
          commandQueue.show(workspace,sessionId);
          void refreshConversations(false);
          continue;
        }
        if (evt.event === "external_context_usage") {
          const {agent,usage} = parseJSONEvent<{agent:string; usage:AgentContextUsage}>(evt.data);
          contextMeter.observe(modelScope(), agent, usage);
          continue;
        }
        if (evt.event === "command_queue") { commandQueue.apply(workspace,parseJSONEvent<CommandQueueSnapshot>(evt.data)); continue; }
        if (evt.event === "queue_error") { setAttachmentError(parseJSONEvent<{error:string}>(evt.data).error); continue; }
        if (evt.event === "queue_delivery") {
          native.boundary(); closeCoordinatorSegment();
          showQueueDelivery(parseJSONEvent<ConversationMessage>(evt.data),sessionId);
          continue;
        }
        if (evt.event === "queued_input_applied") {
          const sent = parseJSONEvent<{id:string;message:string;images?:SentImage[];files?:SentFile[]}>(evt.data);
          deliveryBubble(sent.id)?.closest(".msg")?.remove();
          native.boundary(); closeCoordinatorSegment();
          renderUserMessage(addMessage("user"), sent.message || "", sent.images || [], sent.files || []);
          continue;
        }
        if (evt.event === "input_queued") {
          inputSession.onQueued(parseJSONEvent<PendingInputPayload>(evt.data));
          continue;
        }
        if (evt.event === "done" || evt.event === "stopped" || evt.event === "error") renders.flush();
        if (evt.event === "message_snapshot" || evt.event === "message_patch") {
          var snapshot = evt.event === "message_patch"
            ? snapshots.expand(parseJSONEvent<MessageSnapshot & {base_seq: number}>(evt.data))
            : parseJSONEvent<MessageSnapshot>(evt.data);
          if (isTaskUpdatedEvent(snapshot)) {
            if (taskProgress.apply(snapshot, streamTaskScope, streamTurnId)) renderTaskPanel(taskPanel, taskProgress.state);
          } else if (snapshot.kind === "input.available") {
            const option = nativeInputOption(snapshot, streamTurnId);
            if (option && !stopRequested) {
              runningExternalAgent = option.agent;
              nativeInputKinds.add(option.kind);
              renderTurnInputControls(true);
            }
          } else if (isInputAppliedEvent(snapshot)) {
            if (snapshot.turn_id === streamTurnId) inputSession.onApplied(snapshot, () => { native.boundary(); closeCoordinatorSegment(); });
          } else if (snapshots.apply(snapshot) && snapshots.latest) {
            streamed = snapshots.latest.text;
            var snapshotBubble = ensureCoordinatorBubble();
            queueText(snapshotBubble, streamed);
          }
        } else if (evt.event === "external_command") {
          const event = parseJSONEvent<{agent:string; command:string}>(evt.data);
          if (event.command === "compact") {
            runningExternalAgent = event.agent;
            statusEl.textContent = event.agent + " 正在压缩当前会话…";
          }
        } else if (evt.event === "external_summary") {
          const event = parseJSONEvent<ExternalSummaryEvent>(evt.data);
          runningExternalAgent = event.agent;
          native.summary(event);
        } else if (evt.event === "external_plan") {
          const event = parseJSONEvent<ExternalPlanEvent>(evt.data);
          runningExternalAgent = event.agent;
          native.plan(event);
        } else if (evt.event === "external_text") {
          const event = parseJSONEvent<ExternalTextEvent>(evt.data);
          runningExternalAgent = event.agent;
          native.text(event);
        } else if (evt.event === "tool_start" || evt.event === "tool_result") {
          var toolPayload = parseJSONEvent<ToolEventPayload>(evt.data);
          if (toolPayload.turn_id && toolPayload.source==="external") {
            editTurn=toolPayload.turn_id; fileEdits.observe(toolPayload);
          }
          if (conversationType !== "group" && toolPayload.source === "external" && toolPayload.id) {
            const sender = toolPayload.sender || runningExternalAgent || "外部 Agent";
            runningExternalAgent = sender;
            native.tool(evt.event, toolPayload, sender);
          } else if (conversationType !== "group" || toolPayload.name !== "consult_group_agent") {
            updateToolTimeline(ensureCoordinatorBubble(), toolCards, evt.event, toolPayload, closedToolSegments);
          }
        } else if (evt.event === "acp_permission") {
          var permission = parseJSONEvent<ACPPermissionPayload>(evt.data);
          if (!permission.expired && !permission.selected) native.boundary();
          addACPPermissionCard(permission, pendingPermissions);
          commandQueue.setSendActivity(true,hasBlockingPrompt(pendingPermissions));
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
          var done = parseJSONEvent<WebUIDoneEvent>(evt.data);
          if (done.rewind) {
            const root = Array.from(thread.querySelectorAll<HTMLElement>("[data-root-turn]")).find(el => el.dataset.rootTurn === id);
            if (root) addRewindControl(root.parentElement!, done.rewind);
          } else {
            thread.querySelectorAll(".msg .rewind-action").forEach(button => button.remove());
          }
          nextTurn = done.next_turn_id;
          pendingFromServer = done.pending_inputs || [];
          if (text.toLowerCase() === "/default" && done.mode === "run" && done.conversation_type !== "group") {
            contextMeter.configure("", "");
            clearAgentDraft(modelScope());
            agentModels.configure("", "", busy);
          }
          agentModels.confirm(done.reply_agent, done.reply_agent_model, done.reply_agent_effort, done.reply_agent_command, sentModelRevision, id,
            done.reply_agent_service_tier, done.reply_agent_preferred_tier, done.reply_agent_interaction_mode);
          if (done.session_id) {
            workspaceController.setCurrentWorkspaceSession(done.session_id);
          }
          if (done.model) {
            providerController.invalidateRuntimeModel();
            providerController.updateRuntimeModel(done.api_type, done.model, providerController.providerSettingsState.active_provider);
          }
          setConversationType(done.conversation_type, done.group || currentGroup);
          agentPicker.observe(done.active_agent || builtinAgent);
          if (!agentPicker.pendingAgent || agentPicker.pendingAgent === (done.active_agent || builtinAgent)) saveResponderDraft(modelScope());
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
          const nativeFinal = native.finish();
          renders.flush();
          const finalPlan = native.planBubble(done.reply_item_id);
          var finalBubble = finalPlan || nativeFinal?.bubble || ensureCoordinatorBubble();
          if (conversationType !== "group") setMessageSender(finalBubble.closest<HTMLElement>(".msg")!, "assistant", done.reply_agent);
          clearStreamingState(finalBubble);
          if (done.fork) markReply(finalBubble,done.fork.target,done.fork);
          finalBubble.classList.add("rendered");
          var finalReply = typeof done.reply === "string" ? done.reply : "";
          if (!finalReply.trim()) {
            finalBubble.classList.add("error");
            finalBubble.textContent = "出错：服务端未返回有效回复";
            statusEl.textContent = "回复无效";
            statusEl.classList.remove("busy");
            statusEl.classList.add("error");
          } else {
            if (finalPlan && done.outcome?.notice) {
              const notice = document.createElement("p");
              notice.textContent = done.outcome.notice;
              finalPlan.append(notice);
            } else if (!finalPlan && (!nativeFinal || done.outcome?.notice)) {
              renderReply(finalBubble, (nativeFinal?.text || finalReply) + (done.outcome?.notice ? "\n\n> " + done.outcome.notice : ""));
            }
            addMessageMeta(finalBubble, done.generation);
            addMessageControls(finalBubble, done.run_id || "", finalReply);
          }
        } else if (evt.event === "stopped") {
          const stopped = parseJSONEvent<{ session_id?: string; pending_inputs?: PendingInputPayload[]; next_turn_id?: string }>(evt.data);
          nextTurn = stopped.next_turn_id;
          pendingFromServer = stopped.pending_inputs || [];
          if (stopped.session_id) { workspaceController.setCurrentWorkspaceSession(stopped.session_id); refreshConversations(false); }
          expireACPPermissionCards(pendingPermissions);
          const stoppedItem = native.finish();
          renders.flush();
          var stoppedBubble = stoppedItem?.text === "已停止生成。" ? stoppedItem.bubble : ensureCoordinatorBubble();
          clearStreamingState(stoppedBubble);
          caret.remove();
          stoppedBubble.classList.add("rendered", "notice");
          var stoppedReply = streamed ? streamed + "\n\n> 已停止生成。" : "已停止生成。";
          renderReply(stoppedBubble, stoppedReply);
          addMessageControls(stoppedBubble, "", streamed);
          statusEl.textContent = "已停止";
          statusEl.classList.remove("busy", "error");
        } else if (evt.event === "error") {
          expireACPPermissionCards(pendingPermissions);
          var errPayload = parseJSONEvent<{ error?: string; session_id?: string; pending_inputs?: PendingInputPayload[]; next_turn_id?: string }>(evt.data);
          nextTurn = errPayload.next_turn_id;
          const failedItem = native.finish();
          renders.flush();
          var errorBubble = failedItem && failedItem.text === "执行失败：" + errPayload.error ? failedItem.bubble : ensureCoordinatorBubble();
          clearStreamingState(errorBubble);
          caret.remove();
          if (errPayload.session_id) { workspaceController.setCurrentWorkspaceSession(errPayload.session_id); refreshConversations(false); }
          pendingFromServer = errPayload.pending_inputs || [];
          errorBubble.classList.add("error");
          errorBubble.textContent = (streamed ? streamed + "\n\n" : "") + "出错：" + (errPayload.error || "未知错误");
          statusEl.textContent = "请求失败";
          statusEl.classList.remove("busy");
          statusEl.classList.add("error");
        } else if (evt.event === "speed") {
          // 服务端在生成满 1 秒后推送当前调用速率（约每 3 秒一次），调用结束时推送
          // 与 done 一致的值；0 表示撤销此前的实时值。
          var liveSpeed = formatGenerationSpeed(parseJSONEvent<{ tokens_per_second?: number }>(evt.data).tokens_per_second);
          if (busy) statusEl.textContent = liveSpeed ? "处理中 · " + liveSpeed : "处理中";
        } else if (evt.event === "progress") {
          var progressPayload = parseJSONEvent<{ message?: string }>(evt.data);
          if (progressPayload.message) {
            // Handle compaction before the ordinary progress visibility filters.
            var contextNotice = contextProgress.apply(progressPayload.message);
            if (!contextNotice && conversationType !== "group" && !streamed && !native.used) {
              var progressBubble = ensureCoordinatorBubble();
              progress = progressPayload.message;
              queueText(progressBubble, progress, true);
            }
          }
        } else {
          var payload = parseJSONEvent<{ delta?: string }>(evt.data);
          if (payload.delta && !snapshots.latest && !native.used) {
            var streamBubble = ensureCoordinatorBubble();
            streamed += payload.delta;
            queueText(streamBubble, streamed);
          }
        }
        renders.schedule();
      }
      renders.flush();
      const lastStack=thread.lastElementChild?.querySelector<HTMLElement>(".message-stack");
      if (editTurn && lastStack) fileEdits.render(editTurn,lastStack);
      if (turnBookmark(workspace) === id) rememberTurn(workspace, "");
    } catch (err) { if (viewer.leaving(id) || (isCancellation(err) && api.suspended)) return;
      const status = (err as {status?: number}).status;
      if (status && status >= 400 && status < 500 && turnBookmark(workspace) === id) rememberTurn(workspace, "");
      if (status === 404 && sessionId) recoverHistory = sessionId;
      renders.flush();
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
      native.finish();
      contextProgress.finish();
      if (!api.suspended) renders.flush();
      renders.dispose();
      streamRender = null;
      clearStreamingState(bubble);
      if (!viewer.leaving(id) && !api.suspended) inputSession.finish(pendingFromServer);
      else { turnInputQueue.replaceChildren(); turnInputQueue.hidden = true; }
      runningExternalAgent = undefined;
      runningModelRevision = undefined;
      runningSelection = undefined;
      nativeInputKinds.clear();
      setBusy(false);
      currentTurnId = "";
      currentController = null;
      stopRequested = false;
      workspaceController.refreshWorkspaceTree();
      if (recoverHistory && !api.suspended) void openConversation(recoverHistory);
    }
    return nextTurn;
  }

  async function newChat(type: ConversationType = "default"): Promise<void> {
    if (preparingSend) return;
    const nav = ++navigation;
    await leaveVisibleTurn();
    if (nav !== navigation) return;
    conversationLoadID++;
    conversationScopeEpoch++;
    slashCommands.close();
    setNewConversationMenu(false);
    workspaceController.setCurrentWorkspaceSession("");
    input.value = readChatDraft(workspaceController.currentWorkspace?.id || "", "new");
    autoGrow();
    agentPicker.restore(undefined, type === "group" ? undefined : readAgentDraft(modelScope()).responder);
    setChatMode("run");
    setConversationType(type, type === "group" ? { scheduler: "bqagent", participants: [{ id: "bqagent", name: "bqagent", kind: "builtin", available: true }] } : null);
    clearPendingAttachments();
    setAttachmentMenu(false);
    setReasoningEffortMenu(false);
    thread.innerHTML = emptyMarkup();
    clearTaskProgress();
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
  steerBtn.addEventListener("click", function () { submitTurnInput("steering"); });
  followUpBtn.addEventListener("click", function () { submitTurnInput("follow_up", true); });
  queueSendBtn.addEventListener("click", enqueueDraft);
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
  uploadImageBtn.addEventListener("click", () => { setAttachmentMenu(false); imageInput.click(); });
  imageInput.addEventListener("change", () => {
    if (canAttach() && imageInput.files?.length) addClipboardImages(Array.from(imageInput.files));
    imageInput.value = "";
  });
  fileInput.addEventListener("change", function () {
    if (canAttach() && fileInput.files && fileInput.files.length) addLocalFiles(fileInput.files);
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
  const slashCommands = createSlashCommands(api, input, byId<HTMLDivElement>("slash-command-menu"), {
    url: () => workspaceController.workspaceScopedURL("/api/v1/webui/commands?conversation_type=" + conversationType + "&mode=" + chatMode),
    context: () => {
      if (busy) {
        const view = busyRoute(input.value);
        return busyCommandContext(view, agentModels.isCompactCommand(view.agent, "/compact", agentPicker.supportsCompact(view.agent)),
          !!agentModels.supportsInteractionMode(view.agent,"plan"));
      }
      const view = agentPicker.commandContext(input.value);
      if (!view.agent) return view;
      const query = new URLSearchParams({ agent: view.agent, session_id: sessionId, conversation_type: conversationType, mode: chatMode });
      if (view.qualified) query.set("qualified", "1");
      return { ...view, nativeURL: workspaceController.workspaceScopedURL("/api/v1/webui/commands?" + query) };
    },
    disabled: () => stopRequested || preparingSend || !workspaceController.workspaceReady,
    onComplete: () => { autoGrow(); closeMentionMenu(); },
  });
  input.addEventListener("input", function () { autoGrow(); renderMentionMenu(); });
  input.addEventListener("paste", function (event) {
    if (!canAttach() || !event.clipboardData || !event.clipboardData.items) return;
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
  const composer = input.closest<HTMLElement>(".composer");
  if (composer) {
    var dragDepth = 0;
    var setDropActive = function (active: boolean) { composer.classList.toggle("drop-active", active); };
    composer.addEventListener("dragenter", function (event) {
      if (!dataTransferHasFiles(event.dataTransfer)) return;
      event.preventDefault();
      dragDepth++;
      setDropActive(canAttach());
    });
    composer.addEventListener("dragover", function (event) {
      if (!dataTransferHasFiles(event.dataTransfer)) return;
      event.preventDefault();
      if (event.dataTransfer) event.dataTransfer.dropEffect = canAttach() ? "copy" : "none";
    });
    composer.addEventListener("dragleave", function () {
      dragDepth = Math.max(0, dragDepth - 1);
      if (!dragDepth) setDropActive(false);
    });
    composer.addEventListener("drop", function (event) {
      if (!dataTransferHasFiles(event.dataTransfer)) return;
      event.preventDefault();
      dragDepth = 0;
      setDropActive(false);
      if (!canAttach() || !event.dataTransfer) return;
      var dropped = splitDroppedFiles(event.dataTransfer.files);
      if (dropped.images.length) addClipboardImages(dropped.images);
      if (dropped.files.length) addLocalFiles(dropped.files);
      input.focus();
    });
  }
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden && !api.suspended) {
      void commandQueue.refresh();
      if (conversations.some(c => c.active_turn_id)) void refreshConversations(false);
    }
  });
  input.addEventListener("input", persistComposer);
  input.addEventListener("keydown", function (e) {
    if (e.defaultPrevented) return;
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
    if (busy) {
      var turnInputKind = inputKindForKey(e);
      if (turnInputKind) {
        e.preventDefault();
        submitTurnInput(turnInputKind);
      }
      return;
    }
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      send();
    }
  });
  window.addEventListener("beforeunload", function () {
    pendingImages.forEach(function (image) { URL.revokeObjectURL(image.objectURL); });
  });

  setReasoningEffort(localStorage.getItem(REASONING_EFFORT_KEY) ?? DEFAULT_REASONING_EFFORT, false);
  setTheme(initialTheme(localStorage.getItem(THEME_KEY),
    Boolean(window.matchMedia && window.matchMedia("(prefers-color-scheme: light)").matches)));
  const disposeConversationLayout = initConversationLayout();
  window.addEventListener("beforeunload", disposeConversationLayout, { once: true });
  void auth.initialize();
})();
