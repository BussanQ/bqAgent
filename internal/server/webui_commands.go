package server

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"bqagent/internal/chatcommand"
	"bqagent/internal/extagent"
	"bqagent/internal/session"
	"bqagent/internal/workspace"
)

func (handler *handler) handleWebUICommands(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	conversationType, err := parseConversationType(request.URL.Query().Get("conversation_type"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
		return
	}
	service, err := handler.serviceForWorkspace(request.URL.Query().Get("workspace_id"))
	if err != nil {
		writeError(writer, http.StatusNotFound, chatResponse{Error: err.Error()})
		return
	}
	query := request.URL.Query()
	if name := extagent.AgentName(query.Get("agent")); name != "" {
		if conversationType == ConversationTypeGroup || !slices.Contains(extagent.SupportedAgents(), name) {
			writeError(writer, http.StatusBadRequest, chatResponse{Error: "请选择有效的单聊 Agent"})
			return
		}
		id := query.Get("session_id")
		if id != "" {
			id, err = session.CanonicalID(id)
			if err != nil {
				writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
				return
			}
		}
		ctx, cancel := context.WithTimeout(request.Context(), 12*time.Second)
		defer cancel()
		rows, err := service.externalBroker.Commands(ctx, name, id, service.workspaceRoot)
		withClear := query.Get("qualified") == "1" && query.Get("mode") != "ask"
		if !withClear && err != nil && len(rows) == 0 {
			writeError(writer, http.StatusBadGateway, chatResponse{Error: err.Error()})
			return
		}
		commands := make([]webUIAgentCommand, 0, len(rows))
		if withClear {
			commands = append(commands, webUIAgentCommand{Command: chatcommand.Command{Name: "/clear", Description: "清空上下文，保留聊天记录"}, Agent: name, Insert: "/" + string(name) + " /clear", Kind: "host"})
		}
		for _, row := range rows {
			if strings.EqualFold(row.Name, "clear") {
				continue
			}
			commands = append(commands, webUIAgentCommand{
				Command: chatcommand.Command{Name: "/" + row.Name, Description: row.Description},
				Agent:   name, Insert: "/" + string(name) + " " + row.Text, Hint: row.Hint, Kind: row.Kind,
			})
		}
		var warning string
		if err != nil {
			warning = err.Error()
		}
		writeJSON(writer, http.StatusOK, struct {
			Commands []webUIAgentCommand `json:"commands"`
			Error    string              `json:"error,omitempty"`
		}{commands, warning})
		return
	}
	skills, err := (&workspace.Workspace{Root: service.workspaceRoot, GlobalAgentDir: service.agentDir}).LoadSkills()
	if err != nil {
		writeError(writer, http.StatusInternalServerError, chatResponse{Error: "读取 Skill 列表失败"})
		return
	}
	commands := make([]chatcommand.Command, 0)
	for _, command := range chatcommand.Builtins() {
		switch command.Name {
		// These actions already have WebUI controls, or are terminal-only.
		case "/help", "/exit", "/provider", "/model", "/effort", "/run", "/ask", "/feedback", "/stop":
			continue
		case "/clear":
			if conversationType == ConversationTypeGroup || query.Get("mode") == "ask" {
				continue
			}
			command.Description = "清空上下文，保留聊天记录"
		case "/agent", "/claude", "/codex", "/cursor", "/opencode", "/pi", "/default":
			if conversationType == ConversationTypeGroup {
				continue
			}
		case "/skill":
			for _, skill := range skills {
				command.Arguments = append(command.Arguments, chatcommand.Argument{Value: skill.ID, Description: skill.Description})
				for _, alias := range skill.Aliases {
					command.Arguments = append(command.Arguments, chatcommand.Argument{Value: alias, Description: "alias → " + skill.ID + " · " + skill.Description})
				}
			}
		}
		commands = append(commands, command)
	}
	writeJSON(writer, http.StatusOK, struct {
		Commands []chatcommand.Command `json:"commands"`
	}{commands})
}

type webUIAgentCommand struct {
	chatcommand.Command
	Agent  extagent.AgentName `json:"agent"`
	Insert string             `json:"insert"`
	Hint   string             `json:"hint,omitempty"`
	Kind   string             `json:"kind,omitempty"`
}
