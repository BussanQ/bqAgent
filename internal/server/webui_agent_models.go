package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"bqagent/internal/extagent"
	"bqagent/internal/session"
)

func (channel *WebUIChannel) handleAgentModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	query := r.URL.Query()
	svc, _, err := channel.resolveService(query.Get("workspace_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, chatResponse{Error: "workspace not found"})
		return
	}
	id := query.Get("session_id")
	if id != "" {
		id, err = session.CanonicalID(id)
		if err != nil {
			writeError(w, http.StatusBadRequest, chatResponse{Error: err.Error()})
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	name := extagent.AgentName(query.Get("agent"))
	if r.Method == http.MethodPost {
		body := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		body.DisallowUnknownFields()
		// An empty model is an explicit request to restore the native default.
		var mutation *struct {
			Model  *string `json:"model"`
			Custom bool    `json:"custom,omitempty"`
			Effort *string `json:"effort,omitempty"`
		}
		if body.Decode(&mutation) != nil || mutation == nil || mutation.Model == nil || body.Decode(&struct{}{}) != io.EOF {
			writeError(w, 400, chatResponse{Error: "无效的模型偏好"})
			return
		}
		pick := extagent.ModelPick{Model: *mutation.Model, Custom: mutation.Custom, Effort: mutation.Effort}
		if err := svc.externalBroker.RememberWebModelPick(ctx, name, id, pick); err != nil {
			writeError(w, 400, chatResponse{Error: err.Error()})
			return
		}
		writeJSON(w, 200, struct{}{})
		return
	}
	var menu extagent.ModelCatalog
	if query.Get("view") == "capabilities" {
		menu, err = svc.externalBroker.ModelCapabilities(ctx, name, id, svc.workspaceRoot)
	} else if model := query.Get("model"); model != "" {
		menu, err = svc.externalBroker.PreviewModels(ctx, name, id, svc.workspaceRoot, model)
	} else {
		menu, err = svc.externalBroker.Models(ctx, name, id, svc.workspaceRoot)
	}
	if err != nil && menu.Selected == "" && !menu.AllowCustom {
		writeError(w, http.StatusBadGateway, chatResponse{Error: err.Error()})
		return
	}
	result := struct {
		extagent.ModelCatalog
		Preferences extagent.ModelPreferences `json:"preferences"`
		Error       string                    `json:"error,omitempty"`
	}{ModelCatalog: menu}
	if err != nil {
		result.Error = err.Error()
	}
	result.Preferences, err = svc.externalBroker.WebModelPreferences(name, id)
	if err != nil {
		// The agent's own catalog is still usable when a saved preference
		// cannot be read. Allow a manual choice and expose the reason on retry.
		result.Preferences = extagent.ModelPreferences{}
		if result.Error != "" {
			result.Error += "。"
		}
		result.Error += "无法读取模型偏好：" + err.Error()
	}
	writeJSON(w, http.StatusOK, result)
}
