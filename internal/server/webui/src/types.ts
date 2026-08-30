export type ReasoningEffort = "auto" | "low" | "medium" | "high";
export type ChatMode = "run" | "ask";

export interface ProviderView {
  id: string;
  name: string;
  api_type: string;
  base_url: string;
  models: string[];
  default_model: string;
  api_key?: string;
  api_key_configured?: boolean;
}

export interface ProviderSettings {
  active_provider: string;
  providers: ProviderView[];
}

export interface RuntimeLLMInfo {
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
  id: string;
  title: string;
  status: string;
  updated_at: string;
}

export interface ConversationHistoryTool {
  id?: string;
  name: string;
  arguments?: Record<string, unknown>;
  result?: string;
  status?: string;
  truncated?: boolean;
}

export interface ConversationMessage {
  role: string;
  content: string;
  tools?: ConversationHistoryTool[];
  files?: ConversationHistoryFile[];
}

export interface ConversationHistoryFile {
  name: string;
  path: string;
}

export interface ConversationHistory {
  id?: string;
  mode?: ChatMode;
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

export interface WebUIDoneEvent {
  workspace_id?: string;
  session_id: string;
  run_id?: string;
  reply: string;
  api_type: string;
  model: string;
  mode: ChatMode;
  generation?: GenerationMetrics;
}

export interface ToolEventPayload {
  kind?: string;
  id?: string;
  seq?: number;
  name?: string;
  status?: string;
  arguments?: Record<string, unknown>;
  duration_ms?: number;
  truncated?: boolean;
  preview?: string;
}

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

export interface ProviderModelsResponse {
  models: string[];
  error?: string;
}

export interface ProviderSelectionResponse {
  api_type: string;
  model: string;
  provider_id: string;
  error?: string;
}

export interface ConversationsResponse {
  conversations: ConversationSummary[];
  error?: string;
}

export interface Particle {
  x: number;
  y: number;
  vx: number;
  vy: number;
  baseVx: number;
  baseVy: number;
  radius: number;
}

export interface ParticleTrailPoint {
  x: number;
  y: number;
  createdAt: number;
}

export interface ParticlePulse {
  x: number;
  y: number;
  createdAt: number;
}

export interface SSEEvent {
  event: string;
  data: string;
}

export type JsonObject = Record<string, unknown>;
