import type { SummarySection, SummaryView } from "./external-summary";

export type ReasoningEffort = "auto" | "low" | "medium" | "high" | "max";
export type ChatMode = "run" | "ask";
export type ConversationType = "default" | "group";

export interface GroupParticipant {
  id: string;
  name: string;
  kind: string;
  available: boolean;
  transport?: string;
  supports_images?: boolean;
}

export interface GroupInfo {
  scheduler: string;
  participants: GroupParticipant[];
}

export interface ProviderView {
  managed_by?: string;
  id: string;
  name: string;
  api_type: string;
  base_url: string;
  models: string[];
  model_display_names?: Record<string, string>;
  default_model: string;
  api_key?: string;
  api_key_configured?: boolean;
}

export interface ProviderSettings {
  active_provider: string;
  providers: ProviderView[];
}

export interface RuntimeLLMInfo {
  model_display_name?: string;
  provider_id?: string;
  api_type: string;
  model: string;
  mode: ChatMode;
}

export interface StatusResponse {
  status: string;
  llm: RuntimeLLMInfo;
}

export interface ConversationSummary {
  active_turn_id?: string;
  id: string;
  title: string;
  status: string;
  updated_at: string;
  conversation_type: ConversationType;
}

export interface ConversationHistoryTool {
  edits?: FileEdits;
  turn_id?: string;
  source?: string;
  id?: string;
  name: string;
  arguments?: Record<string, unknown>;
  result?: string;
  status?: string;
  call_status?: string;
  execution_status?: string;
  job_id?: string;
  exit_code?: number;
  result_delivered?: boolean;
  truncated?: boolean;
}

export interface AgentPlan {
  mode: "steps" | "proposal";
  steps?: { text: string; status: string; priority?: string }[];
}

export interface PromptQuestion {
  id: string;
  title: string;
  header?: string;
  multi_select?: boolean;
  options?: { id: string; label: string; description?: string; selected?: boolean }[];
}

export interface QuestionAnswer {
  question_id: string;
  option_ids: string[];
  other?: string;
}

export interface HistoricalPrompt {
  questions?: PromptQuestion[];
  detail?: string;
  header?: string;
  options?: { label: string; description?: string }[];
  title: string;
  method?: string;
  state: string;
  choice?: string;
}

export interface ConversationMessage {
  external_turn?: string;
  anchor?: string;
  fork?: ForkAction;
  rewind?: RewindAction;
  delivery?: { id: string; state: "unknown" | "returned"; attachments?: {kind: "image" | "file"; name?:string; path?:string}[] };
  parts?: { kind: string; id: string; text?: string; prompt?: HistoricalPrompt; plan?: AgentPlan; summary?: SummarySection }[];
  summary?: SummaryView;
  plan?: AgentPlan;
  prompt?: HistoricalPrompt;
  ordered?: boolean;
  images?: string[];
  role: string;
  content: string;
  tools?: ConversationHistoryTool[];
  files?: ConversationHistoryFile[];
  sender?: string;
  kind?: "message" | "error" | "context_clear" | "queue_delivery";
}

export interface ConversationHistoryFile {
  name: string;
  path: string;
}

export interface TaskCriterion {
  description: string;
  done: boolean;
  evidence?: string;
}

export interface TaskVerification {
  command: string;
  tool_call_id?: string;
  status: string;
  details: string;
}

export interface TaskState {
  goal: string;
  status: string;
  criteria: TaskCriterion[];
  remaining: string[];
  verification: TaskVerification[];
  reason?: string;
  updated_at?: string;
}

export interface ConversationHistory {
  forked_from?: ForkOrigin | null;
  rewind_receipt?: RewindAction | null;
  context_number?: number;
  active_agent?: string;
  id?: string;
  task?: TaskState | null;
  mode?: ChatMode;
  conversation_type?: ConversationType;
  group?: GroupInfo;
  messages: ConversationMessage[];
}

export interface WorkspaceRoot {
  id: string;
  name: string;
  path: string;
}

export interface WorkspaceInfo {
  id: string;
  name: string;
  path: string;
  root_id: string;
  relative_path: string;
}

export interface WorkspacesResponse {
  default: WorkspaceInfo;
  roots: WorkspaceRoot[];
}

export interface WorkspaceDirectoryEntry {
  name: string;
  path: string;
}

export interface WorkspaceDirectoryResponse {
  root: WorkspaceRoot;
  path: string;
  absolute_path: string;
  directories: WorkspaceDirectoryEntry[];
  next_offset?: number;
}

export interface WorkspaceEntry {
  name: string;
  path: string;
  type: string;
  size: number;
  attachable: boolean;
}

export interface WorkspaceListResponse {
  path: string;
  entries: WorkspaceEntry[];
  next_offset?: number;
}

export interface WorkspacePreview {
  name: string;
  path: string;
  size: number;
  mime_type: string;
  preview_type: string;
  content?: string;
  data_base64?: string;
  truncated?: boolean;
  reason?: string;
  attachable: boolean;
}

export interface GenerationMetrics {
  first_token_latency_ms?: number;
  prompt_tokens?: number;
  cached_prompt_tokens?: number;
  cache_usage_available?: boolean;
  completion_tokens?: number;
  reasoning_tokens?: number;
  generation_duration_ms?: number;
  tokens_per_second?: number;
  cache_metrics?: {
    available: boolean;
    calls: number;
    input_tokens: number;
    cache_read_tokens: number;
    cache_write_tokens: number;
    uncached_input_tokens: number;
    hit_rate: number;
  };
}

export interface TurnOutcome {
  status: "completed" | "failed" | "interrupted" | "budget_limited" | "blocked" | "awaiting_input";
  reason?: string;
  notice?: string;
  task_relation?: "continue" | "side_question";
  task_status?: string;
}

export interface MessageSnapshot {
  turn_id: string;
  item_id: string;
  seq: number;
  kind: "message.updated" | "message.completed" | "input.available" | "input.applied" | "task.updated";
  phase: string;
  text: string;
}

export interface PendingInputPayload {
  id: string;
  kind: string;
  text: string;
}

export interface WebUIDoneEvent {
  fork?: ForkAction;
  rewind?: RewindAction;
  next_turn_id?: string;
  active_agent?: string;
  reply_agent?: string;
  reply_agent_model?: string;
  reply_agent_effort?: string;
  reply_agent_service_tier?: string;
  reply_agent_interaction_mode?: string;
  reply_agent_preferred_tier?: string;
  reply_agent_command?: string;
  reply_item_id?: string;
  pending_inputs?: PendingInputPayload[];
  turn_id?: string;
  outcome?: TurnOutcome;
  workspace_id?: string;
  session_id: string;
  run_id?: string;
  reply: string;
  reply_kind?: "coordinator" | "participant_results";
  api_type: string;
  model: string;
  mode: ChatMode;
  conversation_type: ConversationType;
  group?: GroupInfo;
  generation?: GenerationMetrics;
}

export interface RewindAction {
  agent?: "codex" | "claude";
  target: string;
  expected_context: number;
  operation?: string;
}

export interface ForkAction { target: string; expected_context: number; tip?: boolean }
export interface ForkOrigin { session: string; epoch: number; target: string }

export interface GroupEventPayload {
  kind: string;
  call_id: string;
  participant: string;
  content?: string;
  error?: string;
}

export interface ACPPermissionOption {
  description?: string;
  option_id: string;
  name: string;
  kind: "allow_once" | "allow_always" | "reject_once" | "reject_always" | string;
}

export interface ACPPermissionToolCall {
  source?: string;
  server?: string;
  tool?: string;
  detail?: string;
  toolCallId?: string;
  title?: string;
  kind?: string;
  rawInput?: unknown;
  [key: string]: unknown;
}

export interface ACPPermissionPayload {
  dialog?: { method: "select" | "confirm" | "input" | "editor" | "question"; title: string; header?: string; message?: string; placeholder?: string; prefill?: string; free_text?: boolean; secret?: boolean; nonblocking?: boolean; questions?: PromptQuestion[] };
  expired?: boolean;
  selected?: string;
  request_id: string;
  session_id: string;
  agent: string;
  external_session_id: string;
  tool_call: ACPPermissionToolCall;
  options: ACPPermissionOption[];
}

export interface ToolEventPayload {
  edits?: FileEdits;
  turn_id?: string;
  sender?: string;
  source?: string;
  kind?: string;
  id?: string;
  seq?: number;
  name?: string;
  status?: string;
  call_status?: string;
  execution_status?: string;
  job_id?: string;
  exit_code?: number;
  result_delivered?: boolean;
  arguments?: Record<string, unknown>;
  duration_ms?: number;
  truncated?: boolean;
  preview?: string;
}

export interface EditLine { kind:"add"|"del"|"context"|"content"|"gap"; text?:string; old?:number; new?:number }
export interface EditFile {
  path:string; old_path?:string; kind:"added"|"deleted"|"edited"|"renamed"|"written";
  lines?:EditLine[]; added:number; removed:number; partial?:boolean; snippet?:boolean;
}
export interface FileEdits { files:EditFile[] | null; partial?:boolean }

export interface PendingImage {
  id: number;
  mimeType: string;
  size: number;
  dataBase64: string;
  objectURL: string;
  loading: boolean;
}

export interface PendingFile {
  id: number;
  kind: "upload" | "path";
  name: string;
  path?: string;
  size: number;
  dataBase64?: string;
  loading: boolean;
}

export interface WorkspaceDirectoryState {
  path: string;
  entries: WorkspaceEntry[];
  nextOffset: number | null;
  loaded: boolean;
  loading: boolean;
  error: string;
  requestID: number;
}

export interface WorkspaceCurrentPreview {
  path: string;
  name: string;
  size: number;
  payload: WorkspacePreview | null;
}

export interface WorkspaceDirectoryPage {
  entries: WorkspaceEntry[];
  nextOffset: number | null;
}

export interface ProviderModel {
  id: string;
  display_name?: string;
}

export interface ProviderModelsResponse {
  models: ProviderModel[];
  error?: string;
}

export interface ProviderSelectionResponse {
  model_display_name?: string;
  api_type: string;
  model: string;
  provider_id: string;
  error?: string;
}

export interface ConversationsResponse {
  conversations: ConversationSummary[];
  error?: string;
}

export interface SSEEvent {
  id?: number;
  event: string;
  data: string;
}

export type JsonObject = Record<string, unknown>;
