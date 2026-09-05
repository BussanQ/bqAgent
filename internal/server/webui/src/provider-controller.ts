import { APIClient, RequestScope, isCancellation } from "./api-client";
import type WaOption from "@awesome.me/webawesome/dist/components/option/option.js";
import type WaSelect from "@awesome.me/webawesome/dist/components/select/select.js";
import { errorMessage } from "./api";
import { byId, eventElement } from "./dom";
import { iconMarkup, setIconButtonLabel } from "./icons";
import { filterProviderModels, providerAPIKeyPlaceholder, providerListSubtitle, providerModelOptions, providerPayload, remainingActiveProvider, resolveDefaultModel, resolveProviderEditorIndex, selectedProviderModelIDs, toggleProviderModel, upsertProviderModel, type ProviderModelOption } from "./providers";
import type { ProviderModelsResponse, ProviderSelectionResponse, ProviderSettings, StatusResponse } from "./types";

export interface ProviderDependencies {
  statusEl: HTMLDivElement;
  sessionId: string;
  busy: boolean;
  workspaceScopedURL: (url: string) => string;
  setChatMode: (value: unknown) => void;
}
export function createProviderController(api: APIClient, deps: ProviderDependencies) {
  const requests = new RequestScope(api);
  const lifetime = new AbortController();
  let discoveryController: AbortController | null = null;
  const modelSelect = byId<WaSelect>("model-select");
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
  var providerSettingsState: ProviderSettings = { active_provider: "", providers: [] };
  var editingProviderIndex = -1;
  var providerCatalogModels: ProviderModelOption[] = [];
  var providerDeleteArmed = false;
  function runtimeModelOptions(): WaOption[] {
    return Array.from(modelSelect.querySelectorAll<WaOption>("wa-option"));
  }

  function runtimeModelOption(providerID: string, model: string): WaOption | undefined {
    return runtimeModelOptions().find(function (option) {
      return option.dataset.providerId === providerID && option.dataset.model === model;
    });
  }

  function createRuntimeModelOption(providerID: string, model: string): WaOption {
    var option = document.createElement("wa-option");
    var value = encodeURIComponent(providerID || "runtime") + ":" + encodeURIComponent(model);
    option.setAttribute("value", value);
    option.dataset.providerId = providerID;
    option.dataset.model = model;
    option.textContent = model;
    return option;
  }

  function setRuntimeModelOption(option: WaOption): void {
    runtimeModelOptions().forEach(function (candidate) {
      candidate.toggleAttribute("selected", candidate === option);
    });
    modelSelect.handleDefaultSlotChange();
  }

  function selectedRuntimeModelOption(): WaOption | undefined {
    return modelSelect.selectedOptions[0] || runtimeModelOptions().find(function (option) { return option.getAttribute("aria-selected") === "true"; });
  }

  function updateRuntimeModel(apiType: string, model: string, providerID?: string): void {
    if (!model) return;
    var matched = runtimeModelOption(providerID || "", model);
    if (!matched) {
      matched = createRuntimeModelOption(providerID || "", model);
      modelSelect.appendChild(matched);
    }
    setRuntimeModelOption(matched);
    modelSelect.title = (providerID || apiType || "llm") + " / " + model;
  }

  function renderProviderSelectors(): void {
    modelSelect.innerHTML = "";
    providerSettingsState.providers.forEach(function (provider) {
      var heading = document.createElement("small");
      heading.className = "model-provider-heading";
      heading.textContent = provider.name;
      modelSelect.appendChild(heading);
      provider.models.forEach(function (model) {
        modelSelect.appendChild(createRuntimeModelOption(provider.id, model));
      });
    });
    var active = providerSettingsState.providers.find(function (provider) { return provider.id === providerSettingsState.active_provider; });
    var activeOption = active ? runtimeModelOption(active.id, active.default_model) : undefined;
    if (activeOption) setRuntimeModelOption(activeOption);
    else modelSelect.handleDefaultSlotChange();
    modelSelect.disabled = runtimeModelOptions().length === 0;
  }

  async function loadProviderSettings(): Promise<void> {
    try {
      var response = await requests.request(deps.workspaceScopedURL("/api/v1/webui/providers"), { headers: { "Accept": "application/json" } });
      if (!response.ok) return;
      providerSettingsState = await requests.readJSON<ProviderSettings>(response);
      renderProviderSelectors();
    } catch (error) { if (isCancellation(error)) throw error; }
  }

  async function selectRuntimeModel(): Promise<void> {
    var option = selectedRuntimeModelOption();
    if (!option || !option.dataset.providerId || !option.dataset.model) return;
    var providerID = option.dataset.providerId;
    var model = option.dataset.model;
    var response = await requests.request(deps.workspaceScopedURL("/api/v1/webui/provider-selection"), {
      method: "POST", headers: { "Content-Type": "application/json", "Accept": "application/json" },
      body: JSON.stringify({ provider_id: providerID, model: model, session_id: deps.sessionId })
    });
    var payload = await requests.readJSON<ProviderSelectionResponse>(response);
    if (!response.ok) throw new Error(payload.error || "切换模型失败");
    providerSettingsState.active_provider = providerID;
    var active = providerSettingsState.providers.find(function (provider) { return provider.id === providerID; });
    if (active) active.default_model = model;
    updateRuntimeModel(payload.api_type, payload.model, payload.provider_id);
  }

  function beginRuntimeModelSelection() {
    modelSelect.disabled = true;
    var selection = selectRuntimeModel().then(function () {
      return true;
    }, function (error: unknown) {
      if (isCancellation(error)) return false;
      deps.statusEl.textContent = errorMessage(error);
      deps.statusEl.classList.add("error");
      loadRuntimeModel();
      return false;
    });
    runtimeModelSelectionPromise = selection;
    selection.then(function () {
      if (runtimeModelSelectionPromise !== selection) return;
      runtimeModelSelectionPromise = null;
      modelSelect.disabled = deps.busy || runtimeModelOptions().length === 0;
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
    discoveryController?.abort();
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
    discoveryController?.abort();
    providerSettingsBackdrop.hidden = true;
    setProviderAPIKeyVisible(false);
    resetProviderDeleteArmed();
  }

  function addProviderModel(model: string, selected: boolean): void {
    providerCatalogModels = upsertProviderModel(providerCatalogModels, model, selected);
    renderProviderModelCatalog();
  }

  async function fetchAvailableProviderModels(): Promise<void> {
    discoveryController?.abort();
    const controller = new AbortController();
    discoveryController = controller;
    providerFetchModels.disabled = true;
    setIconButtonLabel(providerFetchModels, "download", "获取中…");
    try {
      var response = await requests.request(deps.workspaceScopedURL("/api/v1/webui/provider-models"), {
        signal: controller.signal,
        method: "POST", headers: { "Content-Type": "application/json", "Accept": "application/json" },
        body: JSON.stringify({ provider_id: providerIDInput.value.trim(), api_type: providerAPITypeInput.value, base_url: providerBaseURLInput.value.trim(), api_key: providerAPIKeyInput.value })
      });
      var payload = await requests.readJSON<ProviderModelsResponse>(response);
      if (!response.ok) throw new Error(payload.error || "获取模型失败");
      (payload.models || []).forEach(function (model) {
        providerCatalogModels = upsertProviderModel(providerCatalogModels, model, true);
      });
      renderProviderModelCatalog();
      deps.statusEl.textContent = "已获取 " + (payload.models || []).length + " 个模型";
      deps.statusEl.classList.remove("error");
    } finally {
      if (discoveryController === controller) {
        discoveryController = null;
        providerFetchModels.disabled = false;
        setIconButtonLabel(providerFetchModels, "download", "获取可用模型");
      }
    }
  }

  async function saveProviderSettings(): Promise<void> {
    var models = selectedProviderModels();
    var provider = { id: providerIDInput.value.trim(), name: providerNameInput.value.trim(), api_type: providerAPITypeInput.value, base_url: providerBaseURLInput.value.trim(), api_key: providerAPIKeyInput.value, models: models, default_model: providerDefaultModelInput.value || models[0] || "" };
    var index = editingProviderIndex;
    var providers = providerSettingsState.providers.map(providerPayload);
    if (index >= 0) providers[index] = provider; else providers.push(provider);
    var response = await requests.request(deps.workspaceScopedURL("/api/v1/webui/providers"), { method: "PUT", headers: { "Content-Type": "application/json", "Accept": "application/json" }, body: JSON.stringify({ active_provider: provider.id, providers: providers }) });
    var payload = await requests.readJSON<ProviderSettings & { error?: string }>(response);
    if (!response.ok) throw new Error(payload.error || "保存 Provider 失败");
    providerSettingsState = payload;
    renderProviderSelectors();
    closeProviderSettings();
    deps.statusEl.textContent = "Provider 设置已保存";
  }

  async function loadRuntimeModel(): Promise<void> {
    var loadID = ++runtimeModelLoadID;
    try {
      await loadProviderSettings();
      var url = deps.workspaceScopedURL("/api/v1/status" + (deps.sessionId ? "?session_id=" + encodeURIComponent(deps.sessionId) : ""));
      var response = await requests.request(url, { headers: { "Accept": "application/json" } });
      if (!response.ok) return;
      var payload = await requests.readJSON<StatusResponse>(response);
      if (loadID !== runtimeModelLoadID || !payload || !payload.llm) return;
      updateRuntimeModel(payload.llm.api_type, payload.llm.model, payload.llm.provider_id);
      deps.setChatMode(payload.llm.mode);
    } catch (_) {
      // Status display is best-effort and must never block chat startup.
    }
  }

  providerSettingsTrigger.addEventListener("click", function () {
    openProviderSettings().catch(function (error) { if (isCancellation(error)) return;
      deps.statusEl.textContent = errorMessage(error);
      deps.statusEl.classList.add("error");
    });
  }, { signal: lifetime.signal });
  providerSettingsClose.addEventListener("click", closeProviderSettings, { signal: lifetime.signal });
  providerSettingsCancel.addEventListener("click", closeProviderSettings, { signal: lifetime.signal });
  providerSettingsBackdrop.addEventListener("click", function (event) { if (event.target === providerSettingsBackdrop) closeProviderSettings(); }, { signal: lifetime.signal });
  providerSettingsList.addEventListener("click", function (event) {
    var item = eventElement(event.target)?.closest(".provider-settings-item") as HTMLElement | null;
    if (!item || !providerSettingsList.contains(item) || item.dataset.index == null) return;
    var index = Number(item.dataset.index);
    if (index === editingProviderIndex) return;
    renderProviderSettingsList(index);
  }, { signal: lifetime.signal });
  providerModelsInput.addEventListener("click", function (event) {
    var row = eventElement(event.target)?.closest(".provider-model-row") as HTMLElement | null;
    if (!row || !providerModelsInput.contains(row) || !row.dataset.model) return;
    providerCatalogModels = toggleProviderModel(providerCatalogModels, row.dataset.model, row.getAttribute("aria-selected") !== "true");
    renderProviderModelCatalog();
  }, { signal: lifetime.signal });
  providerModelFilter.addEventListener("input", function () { renderProviderModelCatalog(); }, { signal: lifetime.signal });
  providerAPIKeyToggle.addEventListener("click", function () {
    setProviderAPIKeyVisible(providerAPIKeyInput.type === "password");
  }, { signal: lifetime.signal });
  providerFetchModels.addEventListener("click", function () {
    fetchAvailableProviderModels().catch(function (error: unknown) { if (isCancellation(error)) return; deps.statusEl.textContent = errorMessage(error); deps.statusEl.classList.add("error"); });
  }, { signal: lifetime.signal });
  providerModelAdd.addEventListener("click", function () {
    addProviderModel(providerModelManual.value, true);
    providerModelManual.value = "";
    providerModelManual.focus();
  }, { signal: lifetime.signal });
  providerModelManual.addEventListener("keydown", function (event) {
    if (event.key === "Enter") { event.preventDefault(); providerModelAdd.click(); }
  }, { signal: lifetime.signal });
  providerSettingsNew.addEventListener("click", function () {
    if (editingProviderIndex < 0) {
      providerNameInput.focus();
      return;
    }
    renderProviderSettingsList(-1);
    providerNameInput.focus();
  }, { signal: lifetime.signal });
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
      var response = await requests.request(deps.workspaceScopedURL("/api/v1/webui/providers"), { method: "PUT", headers: { "Content-Type": "application/json", "Accept": "application/json" }, body: JSON.stringify({ active_provider: active, providers: providers }) });
      var payload = await requests.readJSON<ProviderSettings & { error?: string }>(response);
      if (!response.ok) throw new Error(payload.error || "删除 Provider 失败");
      providerSettingsState = payload;
      renderProviderSelectors();
      renderProviderSettingsList();
    } catch (error) { if (isCancellation(error)) return; deps.statusEl.textContent = errorMessage(error); deps.statusEl.classList.add("error"); }
  }, { signal: lifetime.signal });
  providerSettingsSave.addEventListener("click", function () {
    providerSettingsSave.disabled = true;
    saveProviderSettings().catch(function (error: unknown) { if (isCancellation(error)) return; deps.statusEl.textContent = errorMessage(error); deps.statusEl.classList.add("error"); }).finally(function () { providerSettingsSave.disabled = false; });
  }, { signal: lifetime.signal });
  modelSelect.addEventListener("change", beginRuntimeModelSelection, { signal: lifetime.signal });

  return {
    get modelSelect() { return modelSelect; },
    get providerSettingsTrigger() { return providerSettingsTrigger; },
    get providerSettingsBackdrop() { return providerSettingsBackdrop; },
    invalidateRuntimeModel() { runtimeModelLoadID++; },
    get runtimeModelSelectionPromise() { return runtimeModelSelectionPromise; },
    get providerSettingsState() { return providerSettingsState; },
    runtimeModelOptions,
    updateRuntimeModel,
    closeProviderSettings,
    loadRuntimeModel,
    dispose() { lifetime.abort(); discoveryController?.abort(); requests.cancel(); },
    cancel() { discoveryController?.abort(); requests.cancel(); runtimeModelLoadID++; runtimeModelSelectionPromise = null; providerSettingsBackdrop.hidden = true; providerAPIKeyInput.value = ""; },
  };
}
