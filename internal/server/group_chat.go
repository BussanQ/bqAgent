package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
	"bqagent/internal/session"
	"bqagent/internal/tools"
)

type ConversationType string

const (
	ConversationTypeDefault    ConversationType = "default"
	ConversationTypeGroup      ConversationType = "group"
	groupScheduler                              = "bqagent"
	groupConsultTool                            = "consult_group_agent"
	groupReplyKindCoordinator                   = "coordinator"
	groupReplyKindParticipants                  = "participant_results"
)

type GroupParticipant struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Kind      string                 `json:"kind"`
	Available bool                   `json:"available"`
	Transport extagent.TransportKind `json:"transport,omitempty"`
}

type GroupInfo struct {
	Scheduler    string             `json:"scheduler"`
	Participants []GroupParticipant `json:"participants"`
}

type GroupEvent struct {
	Kind        string `json:"kind"`
	CallID      string `json:"call_id"`
	Participant string `json:"participant"`
	Content     string `json:"content,omitempty"`
	Error       string `json:"error,omitempty"`
}

type GroupEventSink interface {
	EmitGroupEvent(GroupEvent)
}

type groupParticipantAddRequest struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	SessionID   string `json:"session_id"`
	Participant string `json:"participant"`
}

func parseConversationType(value string) (ConversationType, error) {
	switch ConversationType(strings.ToLower(strings.TrimSpace(value))) {
	case "", ConversationTypeDefault:
		return ConversationTypeDefault, nil
	case ConversationTypeGroup:
		return ConversationTypeGroup, nil
	default:
		return "", fmt.Errorf("conversation_type must be default or group")
	}
}

func storedConversationType(value string) ConversationType {
	conversationType, err := parseConversationType(value)
	if err != nil {
		return ConversationTypeDefault
	}
	return conversationType
}

func persistedConversationType(value ConversationType) string {
	if value == ConversationTypeGroup {
		return string(value)
	}
	return ""
}

func (service *Service) GroupParticipants() []GroupParticipant {
	participants := []GroupParticipant{{ID: groupScheduler, Name: groupScheduler, Kind: "builtin", Available: true}}
	if service == nil || service.externalBroker == nil {
		return participants
	}
	for _, name := range service.externalBroker.AvailableAgents() {
		detection := service.externalBroker.Detection(name)
		participant := GroupParticipant{ID: string(name), Name: string(name), Kind: "external", Available: detection.Preferred != nil}
		if detection.Preferred != nil {
			participant.Transport = detection.Preferred.Kind
		}
		participants = append(participants, participant)
	}
	return participants
}

func newGroupConfig(participants []GroupParticipant) session.GroupConfig {
	ids := make([]string, 0, len(participants))
	for _, participant := range participants {
		if participant.Available {
			ids = append(ids, participant.ID)
		}
	}
	return session.GroupConfig{Version: session.GroupConfigVersion, Scheduler: groupScheduler, Participants: ids}
}

func groupInfo(config session.GroupConfig, service *Service) GroupInfo {
	available := map[string]GroupParticipant{}
	for _, participant := range service.GroupParticipants() {
		available[participant.ID] = participant
	}
	participants := make([]GroupParticipant, 0, len(config.Participants))
	for _, id := range config.Participants {
		participant, ok := available[id]
		if !ok {
			participant = GroupParticipant{ID: id, Name: id, Kind: "external", Available: false}
		}
		participants = append(participants, participant)
	}
	return GroupInfo{Scheduler: firstNonEmpty(strings.TrimSpace(config.Scheduler), groupScheduler), Participants: participants}
}

func (service *Service) GroupInfoForSession(sessionID string) (GroupInfo, error) {
	canonicalID, err := session.CanonicalID(sessionID)
	if err != nil {
		return GroupInfo{}, err
	}
	saved, err := service.store.Open(canonicalID)
	if err != nil {
		return GroupInfo{}, err
	}
	if storedConversationType(saved.Meta().ConversationType) != ConversationTypeGroup {
		return GroupInfo{}, fmt.Errorf("conversation is not a group")
	}
	config, err := saved.LoadGroupConfig()
	if err != nil {
		return GroupInfo{}, err
	}
	return groupInfo(config, service), nil
}

func (service *Service) AddGroupParticipant(sessionID, participantID string) (GroupInfo, error) {
	canonicalID, err := session.CanonicalID(sessionID)
	if err != nil {
		return GroupInfo{}, err
	}
	participantID = strings.ToLower(strings.TrimSpace(participantID))
	if participantID == "" {
		return GroupInfo{}, fmt.Errorf("participant is required")
	}
	unlock := service.locker.Lock(canonicalID)
	defer unlock()
	saved, err := service.store.Open(canonicalID)
	if err != nil {
		return GroupInfo{}, err
	}
	if storedConversationType(saved.Meta().ConversationType) != ConversationTypeGroup {
		return GroupInfo{}, fmt.Errorf("conversation is not a group")
	}
	config, err := saved.LoadGroupConfig()
	if err != nil {
		return GroupInfo{}, err
	}
	for _, existing := range config.Participants {
		if strings.EqualFold(existing, participantID) {
			return groupInfo(config, service), nil
		}
	}
	available := map[string]GroupParticipant{}
	for _, participant := range service.GroupParticipants() {
		if participant.Available {
			available[strings.ToLower(participant.ID)] = participant
		}
	}
	participant, ok := available[participantID]
	if !ok || participant.ID == groupScheduler {
		return GroupInfo{}, fmt.Errorf("群聊成员 @%s 不存在或不可用", participantID)
	}
	config.Participants = append(config.Participants, participant.ID)
	if err := saved.SaveGroupConfig(config); err != nil {
		return GroupInfo{}, err
	}
	return groupInfo(config, service), nil
}

func (handler *handler) handleGroupParticipants(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	if request.Method == http.MethodGet {
		service, err := handler.serviceForWorkspace(request.URL.Query().Get("workspace_id"))
		if err != nil {
			writeError(writer, http.StatusNotFound, chatResponse{Error: err.Error()})
			return
		}
		writeJSON(writer, http.StatusOK, GroupInfo{Scheduler: groupScheduler, Participants: service.GroupParticipants()})
		return
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]))
	if contentType != "application/json" {
		writeError(writer, http.StatusUnsupportedMediaType, chatResponse{Error: "content-type must be application/json"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 64<<10)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var payload groupParticipantAddRequest
	if err := decoder.Decode(&payload); err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: "invalid JSON request: " + err.Error()})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: "request must contain exactly one JSON object"})
		return
	}
	service, err := handler.serviceForWorkspace(payload.WorkspaceID)
	if err != nil {
		writeError(writer, http.StatusNotFound, chatResponse{Error: err.Error()})
		return
	}
	info, err := service.AddGroupParticipant(payload.SessionID, payload.Participant)
	if err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, info)
}

func promptForGroup(prompt agent.PromptSnapshot, config session.GroupConfig) agent.PromptSnapshot {
	participants := strings.Join(config.Participants, ", ")
	stable := strings.TrimSpace(prompt.Stable)
	if stable != "" {
		stable += "\n\n"
	}
	stable += `# Group conversation

You are bqagent in a shared-workspace group conversation. When the user does not mention any participant, handle the task directly yourself without consulting external participants. When the user explicitly mentions @bqagent, act as the coordinator and final synthesizer.
The current participant roster is: ` + participants + `.
When consult_group_agent is available, use it when another participant's independent work would improve the answer. You may consult an allowed participant more than once when useful. Every consultation runs in the same workspace and returns into this conversation. Wait for requested consultations, read prior participant conclusions, reconcile conflicts, attribute important findings, and then provide the final consolidated answer yourself. Do not invent participant conclusions or claim a consultation that did not occur. Tasks addressed only to other participants are handled directly by the server and must not be analyzed or summarized by you.`
	return agent.NewFrozenPromptSnapshot(stable, prompt.SessionContext)
}

var groupMentionPattern = regexp.MustCompile(`(?:^|[\s])@([A-Za-z][A-Za-z0-9_-]*)`)

func parseGroupMentions(message string, config session.GroupConfig) ([]string, error) {
	allowed := make(map[string]bool, len(config.Participants))
	for _, participant := range config.Participants {
		allowed[strings.ToLower(participant)] = true
	}
	seen := map[string]bool{}
	mentions := make([]string, 0)
	for _, match := range groupMentionPattern.FindAllStringSubmatch(message, -1) {
		id := strings.ToLower(match[1])
		if !allowed[id] {
			return nil, fmt.Errorf("群聊成员 @%s 不存在或不可用", match[1])
		}
		if !seen[id] {
			seen[id] = true
			mentions = append(mentions, id)
		}
	}
	return mentions, nil
}

type groupTurnCoordinator struct {
	service      *Service
	sessionID    string
	allowed      map[string]bool
	baseMessages []map[string]any
	replies      []groupVisibleReply
	eventSink    GroupEventSink
	callSequence atomic.Uint64
}

type groupVisibleReply struct {
	Participant string
	Content     string
	Failed      bool
}

func newGroupTurnCoordinator(service *Service, sessionID string, config session.GroupConfig, mentions []string, messages []map[string]any, sink GroupEventSink) *groupTurnCoordinator {
	allowed := map[string]bool{}
	if len(mentions) != 1 || mentions[0] != groupScheduler {
		for _, participant := range mentions {
			if participant != groupScheduler {
				allowed[participant] = true
			}
		}
	} else {
		for _, participant := range config.Participants {
			if participant != groupScheduler {
				allowed[participant] = true
			}
		}
	}
	return &groupTurnCoordinator{service: service, sessionID: sessionID, allowed: allowed, baseMessages: messages, eventSink: sink}
}

func hasGroupMention(mentions []string, participant string) bool {
	for _, mention := range mentions {
		if mention == participant {
			return true
		}
	}
	return false
}

func (coordinator *groupTurnCoordinator) toolDefinition() (tools.Definition, bool) {
	participants := make([]string, 0, len(coordinator.allowed))
	for _, supported := range extagent.SupportedAgents() {
		if coordinator.allowed[string(supported)] {
			participants = append(participants, string(supported))
		}
	}
	if len(participants) == 0 {
		return tools.Definition{}, false
	}
	raw, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"participant": map[string]any{"type": "string", "enum": participants, "description": "Group participant to consult"},
			"task":        map[string]any{"type": "string", "description": "Concrete task or follow-up question for this participant"},
		},
		"required":             []string{"participant", "task"},
		"additionalProperties": false,
	})
	return tools.Definition{Type: "function", Function: tools.FunctionDefinition{
		Name: groupConsultTool, Description: "Ask an allowed group participant to work in the shared workspace and return their conclusion to the group.", RawParameters: raw,
	}}, true
}

func (coordinator *groupTurnCoordinator) function() tools.Function {
	return func(ctx context.Context, args map[string]any) (string, error) {
		participant, _ := args["participant"].(string)
		task, _ := args["task"].(string)
		return coordinator.consult(ctx, participant, task)
	}
}

func (coordinator *groupTurnCoordinator) consult(ctx context.Context, participant, task string) (string, error) {
	participant = strings.ToLower(strings.TrimSpace(participant))
	task = strings.TrimSpace(task)
	if !coordinator.allowed[participant] {
		return "", fmt.Errorf("participant @%s is not allowed for this turn", participant)
	}
	if task == "" {
		return "", fmt.Errorf("task is required")
	}
	callID := fmt.Sprintf("group-%d", coordinator.callSequence.Add(1))
	coordinator.emit(GroupEvent{Kind: "participant_start", CallID: callID, Participant: participant})
	result, err := coordinator.service.externalBroker.SendGroupTurn(ctx, extagent.TurnRequest{
		BQSessionID: coordinator.sessionID,
		Agent:       extagent.AgentName(participant),
		Prompt:      coordinator.participantPrompt(participant, task),
		CWD:         coordinator.service.workspaceRoot,
	})
	if err != nil {
		coordinator.replies = append(coordinator.replies, groupVisibleReply{Participant: participant, Content: err.Error(), Failed: true})
		coordinator.emit(GroupEvent{Kind: "participant_error", CallID: callID, Participant: participant, Error: err.Error()})
		return fmt.Sprintf("Group participant @%s failed: %v", participant, err), err
	}
	reply := strings.TrimSpace(result.Reply)
	coordinator.replies = append(coordinator.replies, groupVisibleReply{Participant: participant, Content: reply})
	coordinator.emit(GroupEvent{Kind: "participant_message", CallID: callID, Participant: participant, Content: reply})
	return fmt.Sprintf("Group participant @%s replied:\n%s", participant, reply), nil
}

func (coordinator *groupTurnCoordinator) participantPrompt(participant, task string) string {
	contextText := sharedGroupContext(coordinator.baseMessages, coordinator.replies)
	return fmt.Sprintf(`You are @%s, a member of a bqagent-coordinated group conversation. Work directly in the shared workspace when the task requires it. The following bounded transcript is the authoritative current group context and may overlap with context from your external session. Read other members' conclusions before responding. Return a concise, self-contained conclusion for the group; do not try to dispatch another agent.

<shared_group_context>
%s
</shared_group_context>

<assigned_task>
%s
</assigned_task>`, participant, contextText, task)
}

func (coordinator *groupTurnCoordinator) emit(event GroupEvent) {
	if coordinator.eventSink != nil {
		coordinator.eventSink.EmitGroupEvent(event)
	}
}

func directGroupReply(replies []groupVisibleReply) string {
	parts := make([]string, 0, len(replies))
	for _, reply := range replies {
		heading := "@" + reply.Participant
		if reply.Failed {
			heading += " 执行失败"
		}
		parts = append(parts, heading+":\n"+strings.TrimSpace(reply.Content))
	}
	return strings.Join(parts, "\n\n")
}

func sharedGroupContext(messages []map[string]any, replies []groupVisibleReply) string {
	var lines []string
	for _, message := range messages {
		role, _ := message["role"].(string)
		content := conversationMessageText(message["content"])
		if strings.TrimSpace(content) == "" {
			continue
		}
		switch role {
		case "user":
			lines = append(lines, "user: "+content)
		case "assistant":
			lines = append(lines, "@bqagent: "+content)
		case "system":
			if strings.HasPrefix(content, agent.EarlierConversationSummaryPrefix) {
				lines = append(lines, "shared summary: "+strings.TrimPrefix(content, agent.EarlierConversationSummaryPrefix))
			}
		}
	}
	for _, reply := range replies {
		prefix := "@" + reply.Participant + ": "
		if reply.Failed {
			prefix = "@" + reply.Participant + " failed: "
		}
		lines = append(lines, prefix+reply.Content)
	}
	result := strings.Join(lines, "\n\n")
	runes := []rune(result)
	const maxRunes = 48 * 1024
	if len(runes) > maxRunes {
		result = "[earlier shared context omitted]\n" + string(runes[len(runes)-maxRunes:])
	}
	return result
}

func recordSyntheticGroupConsult(conversationMessages *[]map[string]any, savedSession *session.Session, callID, participant, result string) error {
	arguments, _ := json.Marshal(map[string]string{"participant": participant, "task": "Explicit user mention"})
	assistantMessage := agent.AssistantMessage{Role: "assistant", ToolCalls: []agent.ToolCall{{
		ID: callID, Type: "function", Function: agent.FunctionCall{Name: groupConsultTool, Arguments: string(arguments)},
	}}}.RequestMessage()
	toolMessage := map[string]any{"role": "tool", "tool_call_id": callID, "content": result}
	if err := savedSession.RecordMessages(assistantMessage, toolMessage); err != nil {
		return err
	}
	*conversationMessages = append(*conversationMessages, assistantMessage, toolMessage)
	return nil
}

func isExternalRouteCommand(message string) bool {
	command, _ := splitFirstToken(message)
	switch strings.ToLower(command) {
	case "/claude", "/codex", "/cursor", "/opencode", "/default":
		return true
	default:
		return false
	}
}
