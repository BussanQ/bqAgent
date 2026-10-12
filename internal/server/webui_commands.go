package server

import (
	"net/http"

	"bqagent/internal/chatcommand"
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
	skills, err := (&workspace.Workspace{Root: service.workspaceRoot, GlobalAgentDir: service.agentDir}).LoadSkills()
	if err != nil {
		writeError(writer, http.StatusInternalServerError, chatResponse{Error: "读取 Skill 列表失败"})
		return
	}
	commands := make([]chatcommand.Command, 0)
	for _, command := range chatcommand.Builtins() {
		switch command.Name {
		// These actions already have WebUI controls, or are terminal-only.
		case "/help", "/clear", "/exit", "/provider", "/model", "/effort", "/run", "/ask", "/feedback", "/stop":
			continue
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
