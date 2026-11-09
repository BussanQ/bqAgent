export interface ModelDraft {
  draftMode?: string;
  draft?: string;
  draftEffort?: string;
  draftTier?: string;
  custom?: boolean;
  submittedTurn?: string;
}

interface AgentDraft {
  responder?: string;
  firstTurn?: string;
  models?: Record<string, ModelDraft>;
}

const prefix = "bqagent.web-agent-draft.";

export function readAgentDraft(scope: string): AgentDraft {
  try {
    const data = JSON.parse(sessionStorage.getItem(prefix + scope) || "{}");
    if (!data || typeof data !== "object") return {};
    const draft: AgentDraft = {};
    if (typeof data.firstTurn === "string") draft.firstTurn = data.firstTurn;
    if (typeof data.responder === "string") draft.responder = data.responder;
    if (data.models && typeof data.models === "object" && !Array.isArray(data.models)) {
      draft.models = Object.fromEntries(Object.entries(data.models).filter(([,entry]) => entry && typeof entry === "object")
        .map(([agent, entry]) => {
          const row = entry as Partial<Record<keyof ModelDraft,unknown>>;
          return [agent, {
            draft:typeof row.draft === "string" ? row.draft : undefined,
            draftEffort:typeof row.draftEffort === "string" ? row.draftEffort : undefined,
            draftTier:typeof row.draftTier === "string" ? row.draftTier : undefined,
            draftMode:row.draftMode === "plan" || row.draftMode === "default" ? row.draftMode : undefined,
            submittedTurn:typeof row.submittedTurn === "string" ? row.submittedTurn : undefined,
            custom:row.custom === true,
          }];
        }));
    }
    return draft;
  } catch { return {}; }
}

function store(scope: string, data: AgentDraft): void {
  try {
    if (data.firstTurn || data.responder || Object.keys(data.models || {}).length) sessionStorage.setItem(prefix + scope, JSON.stringify(data));
    else sessionStorage.removeItem(prefix + scope);
  } catch { /* The current composer still owns the unsent picks. */ }
}

export function saveResponderDraft(scope: string, responder?: string): void {
  store(scope, { ...readAgentDraft(scope), responder });
}

export function recordFirstAgentTurn(scope: string, firstTurn: string): void {
  store(scope, { ...readAgentDraft(scope), firstTurn });
}

export function saveModelDraft(scope: string, agent: string, menu: ModelDraft): void {
  const data = readAgentDraft(scope), models = data.models ?? {};
  const { draft, draftEffort, draftTier, draftMode, custom, submittedTurn } = menu;
  if (draft || draftEffort || draftTier || draftMode || custom) models[agent] = { draft, draftEffort, draftTier, draftMode, custom, submittedTurn };
  else delete models[agent];
  store(scope, { ...data, models });
}

export function bindAgentDraft(from: string, to: string): void {
  if (from === to) return;
  const old = readAgentDraft(from);
  if (old.responder || Object.keys(old.models || {}).length) store(to, { ...old, firstTurn: undefined });
  store(from, {});
}

export function clearAgentDraft(scope: string): void { store(scope, {}); }

// Capture the destination at click time. Each workspace/agent writes its deltas
// in order, including when the user opens another conversation before the reply.
export class AgentPreferenceWrites {
  private pending = new Map<string, Promise<void>>();

  write(key: string, save: () => Promise<void>): Promise<void> {
    const previous = this.pending.get(key);
    const next = previous ? previous.catch(() => {}).then(save) : save();
    this.pending.set(key, next);
    const release = () => { if (this.pending.get(key) === next) this.pending.delete(key); };
    void next.then(release, release);
    return next;
  }

  async settled(key: string): Promise<void> {
    await this.pending.get(key)?.catch(() => {});
  }
}
