package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/extagent"
	"bqagent/internal/session"
	"bqagent/internal/taskstate"
	"bqagent/internal/transcript"
)

type conversationListItem struct {
	ActiveTurnID     string           `json:"active_turn_id,omitempty"`
	ID               string           `json:"id"`
	Title            string           `json:"title"`
	Status           string           `json:"status"`
	UpdatedAt        time.Time        `json:"updated_at"`
	ConversationType ConversationType `json:"conversation_type"`
}

type conversationMessage struct {
	ExternalTurn string              `json:"external_turn,omitempty"`
	Anchor       string              `json:"anchor,omitempty"`
	Fork         *forkAction         `json:"fork,omitempty"`
	Rewind       *rewindAction       `json:"rewind,omitempty"`
	Delivery     *queueDelivery      `json:"delivery,omitempty"`
	Parts        []transcript.Part   `json:"parts,omitempty"`
	Images       []string            `json:"images,omitempty"`
	Role         string              `json:"role"`
	Content      string              `json:"content"`
	Tools        []agent.HistoryTool `json:"tools,omitempty"`
	Files        []conversationFile  `json:"files,omitempty"`
	Sender       string              `json:"sender,omitempty"`
	Kind         string              `json:"kind,omitempty"`
}

type conversationFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// ConversationHistoryMessage is the provider-neutral text representation used by
// terminal and web clients. Raw tool and system messages are omitted; compact
// tool summaries are returned as structured Tools.
type ConversationHistoryMessage struct {
	ExternalTurn string      `json:"external_turn,omitempty"`
	Anchor       string      `json:"anchor,omitempty"`
	Fork         *forkAction `json:"fork,omitempty"`
	submission   string
	Rewind       *rewindAction       `json:"rewind,omitempty"`
	Delivery     *queueDelivery      `json:"delivery,omitempty"`
	Parts        []transcript.Part   `json:"parts,omitempty"`
	Images       []string            `json:"-"`
	Role         string              `json:"role"`
	Content      string              `json:"content"`
	Tools        []agent.HistoryTool `json:"tools,omitempty"`
	Sender       string              `json:"sender,omitempty"`
	Kind         string              `json:"kind,omitempty"`
}

type historyToolPosition struct{ message, tool int }

// A correction can arrive while a native tool is running. Later snapshots
// update its first card, even across user messages. Older unscoped records
// are independent because a provider may reuse its call IDs in later turns.
func mergeExternalToolSnapshots(messages []ConversationHistoryMessage, index map[[2]string]historyToolPosition, tools []agent.HistoryTool) []agent.HistoryTool {
	var added []agent.HistoryTool
	for _, tool := range tools {
		if tool.TurnID != "" && tool.ID != "" {
			key := [2]string{tool.TurnID, tool.ID}
			if pos, ok := index[key]; ok {
				if pos.message == len(messages) {
					added[pos.tool] = tool
				} else {
					messages[pos.message].Tools[pos.tool] = tool
				}
				continue
			}
			index[key] = historyToolPosition{len(messages), len(added)}
		}
		added = append(added, tool)
	}
	return added
}

// ConversationHistory is a read-only view of a persisted chat session.
type ConversationHistory struct {
	ForkedFrom       *session.ForkOrigin
	RewindReceipt    *rewindAction
	ContextNumber    int
	ActiveAgent      extagent.AgentName
	ID               string
	Title            string
	Mode             ChatMode
	ConversationType ConversationType
	Group            *GroupInfo
	Task             *taskstate.State
	Messages         []ConversationHistoryMessage
	Omitted          int
}

type conversationHistoryLoadError struct{ err error }

func (loadErr conversationHistoryLoadError) Error() string { return loadErr.err.Error() }
func (loadErr conversationHistoryLoadError) Unwrap() error { return loadErr.err }

func (handler *handler) handleConversations(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	service, err := handler.serviceForWorkspace(request.URL.Query().Get("workspace_id"))
	if err != nil {
		writeError(writer, http.StatusNotFound, chatResponse{Error: err.Error()})
		return
	}
	metas, err := service.store.List(100)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, chatResponse{Error: err.Error()})
		return
	}
	items := make([]conversationListItem, 0, len(metas))
	active := service.webTurns.runningSessions()
	for _, meta := range metas {
		title := strings.TrimSpace(meta.Task)
		if title == "" {
			title = "新会话"
		}
		if runes := []rune(title); len(runes) > 48 {
			title = string(runes[:48]) + "…"
		}
		items = append(items, conversationListItem{ID: meta.ID, ActiveTurnID: active[meta.ID], Title: title, Status: string(meta.Status), UpdatedAt: meta.UpdatedAt, ConversationType: storedConversationType(meta.ConversationType)})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"conversations": items})
}

func (handler *handler) handleConversationHistory(writer http.ResponseWriter, request *http.Request) {
	id := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/v1/webui/conversations/"), "/")
	service, err := handler.serviceForWorkspace(request.URL.Query().Get("workspace_id"))
	if err != nil {
		writeError(writer, http.StatusNotFound, chatResponse{Error: err.Error()})
		return
	}
	if request.URL.Query().Has("image") {
		serveConversationImage(writer, request, service, id)
		return
	}
	if request.Method == http.MethodDelete {
		handler.deleteConversation(writer, service, id)
		return
	}
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	history, err := service.conversationHistory(id, 0, true)
	if errors.Is(err, os.ErrNotExist) {
		writeError(writer, http.StatusNotFound, chatResponse{Error: "conversation not found"})
		return
	}
	var loadErr conversationHistoryLoadError
	if errors.As(err, &loadErr) {
		writeError(writer, http.StatusInternalServerError, chatResponse{Error: err.Error()})
		return
	}
	if err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, webHistoryPayload(history))
}

func webHistoryPayload(history ConversationHistory) map[string]any {
	webMessages := make([]conversationMessage, 0, len(history.Messages))
	for _, message := range history.Messages {
		content := message.Content
		var files []conversationFile
		if message.Role == "user" && message.Delivery == nil {
			content, files = splitHistoryAttachments(content)
		}
		webMessages = append(webMessages, conversationMessage{
			ExternalTurn: message.ExternalTurn,
			Anchor:       message.Anchor, Fork: message.Fork,
			Rewind:   message.Rewind,
			Delivery: message.Delivery,
			Parts:    message.Parts,
			Role:     message.Role,
			Content:  content,
			Tools:    message.Tools,
			Files:    files,
			Images:   message.Images,
			Sender:   message.Sender,
			Kind:     message.Kind,
		})
	}
	return map[string]any{"id": history.ID, "context_number": history.ContextNumber, "forked_from": history.ForkedFrom, "rewind_receipt": history.RewindReceipt, "title": history.Title, "mode": history.Mode, "active_agent": history.ActiveAgent, "conversation_type": history.ConversationType, "group": history.Group, "task": history.Task, "messages": webMessages}
}

func splitHistoryAttachments(content string) (string, []conversationFile) {
	const opening = "\n\n<attachment name=\""
	const imagePlaceholder = "\n[图片]"
	attachmentContent := content
	hasImagePlaceholder := strings.HasSuffix(attachmentContent, imagePlaceholder)
	if hasImagePlaceholder {
		attachmentContent = strings.TrimSuffix(attachmentContent, imagePlaceholder)
	}
	for searchAt := 0; searchAt < len(attachmentContent); {
		relativeStart := strings.Index(attachmentContent[searchAt:], opening)
		if relativeStart < 0 {
			break
		}
		start := searchAt + relativeStart
		files, ok := parseHistoryAttachmentTail(attachmentContent[start:])
		if ok {
			text := strings.TrimSpace(attachmentContent[:start])
			if hasImagePlaceholder {
				if text != "" {
					text += "\n"
				}
				text += "[图片]"
			}
			return text, files
		}
		searchAt = start + len(opening)
	}
	return content, nil
}

func parseHistoryAttachmentTail(tail string) ([]conversationFile, bool) {
	const opening = "\n\n<attachment name=\""
	const pathSeparator = "\" path=\""
	const closing = "\n</attachment>"
	files := make([]conversationFile, 0, 1)
	for tail != "" {
		if !strings.HasPrefix(tail, opening) {
			return nil, false
		}
		headerEnd := strings.IndexByte(tail[len(opening):], '\n')
		if headerEnd < 0 {
			return nil, false
		}
		headerEnd += len(opening)
		header := tail[len(opening):headerEnd]
		separatorAt := strings.Index(header, pathSeparator)
		if separatorAt < 0 || !strings.HasSuffix(header, "\">") {
			return nil, false
		}
		name := html.UnescapeString(header[:separatorAt])
		path := html.UnescapeString(header[separatorAt+len(pathSeparator) : len(header)-2])
		if name == "" || path == "" {
			return nil, false
		}

		next := strings.Index(tail[headerEnd+1:], opening)
		if next >= 0 {
			next += headerEnd + 1
			if !strings.HasSuffix(tail[:next], closing) {
				return nil, false
			}
			files = append(files, conversationFile{Name: name, Path: path})
			tail = tail[next:]
			continue
		}
		if !strings.HasSuffix(tail, closing) {
			return nil, false
		}
		files = append(files, conversationFile{Name: name, Path: path})
		return files, true
	}
	return nil, false
}

// ConversationHistory returns recent user and assistant text. When maxBytes is
// positive, complete messages are selected from newest to oldest until the
// budget is exhausted. Message size includes structured tool fields. A single
// oversized newest message is truncated (arguments/results first, then text)
// only in this returned view; the persisted session is never modified.
func (service *Service) ConversationHistory(id string, maxBytes int) (ConversationHistory, error) {
	return service.conversationHistory(id, maxBytes, false)
}

func (service *Service) conversationHistory(id string, maxBytes int, includeImageRefs bool) (ConversationHistory, error) {
	canonicalID, err := session.CanonicalID(id)
	if err != nil {
		return ConversationHistory{}, err
	}
	unlock := service.locker.Lock(canonicalID)
	defer unlock()
	saved, err := service.store.Open(canonicalID)
	if err != nil {
		return ConversationHistory{}, err
	}
	var messages []map[string]any
	var offsets []int64
	if includeImageRefs {
		messages, offsets, err = saved.LoadIndexedMessages()
	} else {
		messages, err = saved.LoadMessages()
	}
	if err != nil {
		return ConversationHistory{}, conversationHistoryLoadError{err: fmt.Errorf("load conversation history: %w", err)}
	}
	return service.projectConversationHistory(saved, messages, offsets, maxBytes, includeImageRefs, nil)
}

func (service *Service) projectConversationHistory(saved *session.Session, messages []map[string]any, offsets []int64, maxBytes int, includeImageRefs bool, snapshot *webTurn) (ConversationHistory, error) {
	var err error
	conversationType := storedConversationType(saved.Meta().ConversationType)
	var cutoff time.Time
	if snapshot != nil {
		cutoff = snapshot.baseAt
	}
	filtered := []ConversationHistoryMessage{}
	for i := 0; i < saved.ContextNumber(); i++ {
		if saved.BoundaryAfter(i).RewindTo != "" {
			continue
		}
		previous, _ := saved.ContextAt(i)
		var rows []map[string]any
		var positions []int64
		if includeImageRefs {
			rows, positions, err = previous.LoadIndexedMessages()
		} else {
			rows, err = previous.LoadMessages()
		}
		if err != nil {
			return ConversationHistory{}, conversationHistoryLoadError{err: err}
		}
		filtered = append(filtered, service.withQueueHandoffs(previous, projectContextMessages(previous, rows, positions, includeImageRefs), cutoff, false)...)
		filtered = append(filtered, ConversationHistoryMessage{Role: "assistant", Kind: "context_clear", Content: "上下文已清空"})
	}
	current := projectContextMessages(saved, messages, offsets, includeImageRefs)
	if includeImageRefs && conversationType != ConversationTypeGroup && storedChatMode(saved.Meta().CurrentMode) == ChatModeRun {
		annotateForks(saved, current, messages, snapshot == nil)
	}
	var active extagent.SessionState
	if snapshot == nil && conversationType != ConversationTypeGroup {
		active, err = service.externalBroker.ActiveSession(saved.ID())
		if err != nil {
			return ConversationHistory{}, conversationHistoryLoadError{err: err}
		}
		canRewind := active.Agent == extagent.AgentCodex && active.Transport == extagent.TransportAppServer
		if includeImageRefs && canRewind && storedChatMode(saved.Meta().CurrentMode) == ChatModeRun {
			annotateRewindTargets(saved, messages, current, active)
		}
	}
	filtered = append(filtered, service.withQueueHandoffs(saved, current, cutoff, snapshot == nil)...)
	var task *taskstate.State
	if service.taskToolEnabled {
		task, err = saved.LoadTaskState()
		if err != nil {
			return ConversationHistory{}, conversationHistoryLoadError{err: fmt.Errorf("load conversation task: %w", err)}
		}
	}
	result := ConversationHistory{ForkedFrom: saved.Meta().ForkedFrom, ID: saved.ID(), ContextNumber: saved.ContextNumber(), Title: saved.Meta().Task, Mode: storedChatMode(saved.Meta().CurrentMode), ConversationType: conversationType, Task: task, Messages: filtered}
	if includeImageRefs && snapshot == nil && saved.ContextNumber() > 0 {
		epoch := saved.ContextNumber() - 1
		if b := saved.BoundaryAfter(epoch); b.RewindTo != "" {
			result.RewindReceipt = &rewindAction{Agent: extagent.AgentName(b.Agent), Target: b.RewindTo, Epoch: epoch, Operation: b.Operation}
		}
	}
	if conversationType == ConversationTypeGroup {
		if config, configErr := saved.LoadGroupConfig(); configErr == nil {
			info := groupInfo(config, service)
			result.Group = &info
		}
	} else if snapshot != nil {
		result.ActiveAgent = snapshot.baseAgent
	} else {
		result.ActiveAgent = active.Agent
	}
	if maxBytes <= 0 || historyBytes(filtered) <= maxBytes {
		return result, nil
	}
	selected := make([]ConversationHistoryMessage, 0, len(filtered))
	used := 0
	for index := len(filtered) - 1; index >= 0; index-- {
		message := filtered[index]
		size := historyMessageBytes(message)
		if used+size > maxBytes {
			if len(selected) == 0 {
				selected = append(selected, truncateHistoryMessage(message, maxBytes))
				result.Omitted = index
			} else {
				result.Omitted = index + 1
			}
			break
		}
		selected = append(selected, message)
		used += size
	}
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	result.Messages = selected
	return result, nil
}

func projectContextMessages(saved *session.Session, messages []map[string]any, offsets []int64, includeImageRefs bool) []ConversationHistoryMessage {
	conversationType := storedConversationType(saved.Meta().ConversationType)
	filtered := make([]ConversationHistoryMessage, 0, len(messages))
	pendingGroupCalls := map[string]string{}
	pendingTools := map[string]pendingHistoryTool{}
	externalTools := map[[2]string]historyToolPosition{}
	for index, message := range messages {
		role, _ := message["role"].(string)
		content := conversationMessageText(message["content"])
		if h, ok := transcript.ReadExternalHistory(message[transcript.ExternalHistoryKey]); ok && role == "assistant" {
			var tools []agent.HistoryTool
			if raw := message[agent.ExternalToolsMessageKey]; raw != nil {
				data, _ := json.Marshal(raw)
				_ = json.Unmarshal(data, &tools)
				tools = mergeExternalToolSnapshots(filtered, externalTools, tools)
			}
			if len(h.Parts) > 0 || len(tools) > 0 {
				sender, _ := message["name"].(string)
				filtered = append(filtered, ConversationHistoryMessage{Anchor: h.ID, ExternalTurn: h.TurnID, Role: role, Sender: sender, Content: content, Tools: tools, Parts: h.Parts})
			}
			continue
		}
		if raw, ok := message[agent.ExternalToolsMessageKey]; ok && role == "assistant" {
			var tools []agent.HistoryTool
			data, err := json.Marshal(raw)
			if err == nil && json.Unmarshal(data, &tools) == nil && len(tools) > 0 {
				sender, _ := message["name"].(string)
				tools = mergeExternalToolSnapshots(filtered, externalTools, tools)
				if len(tools) > 0 || content != "" {
					filtered = append(filtered, ConversationHistoryMessage{Role: role, Sender: sender, Content: content, Tools: tools})
				}
				continue
			}
		}
		ordinaryCalls := []agent.ToolCall{}
		if role == "assistant" {
			for _, call := range historyToolCalls(message) {
				if call.Function.Name != groupConsultTool {
					ordinaryCalls = append(ordinaryCalls, call)
					continue
				}
				var arguments map[string]any
				if json.Unmarshal([]byte(call.Function.Arguments), &arguments) == nil {
					participant, _ := arguments["participant"].(string)
					if strings.TrimSpace(call.ID) != "" && strings.TrimSpace(participant) != "" {
						pendingGroupCalls[call.ID] = strings.TrimSpace(participant)
					}
				}
			}
		}
		if role == "tool" {
			callID, _ := message["tool_call_id"].(string)
			if participant := pendingGroupCalls[callID]; participant != "" {
				text, failed := groupHistoryResult(participant, content)
				kind := "message"
				if failed {
					kind = "error"
				}
				filtered = append(filtered, ConversationHistoryMessage{Role: "assistant", Sender: participant, Kind: kind, Content: text})
				delete(pendingGroupCalls, callID)
			}
			if pending, ok := pendingTools[callID]; ok {
				resolved := agent.HistoryToolFromCall(pending.call, content)
				filtered[pending.message].Tools[pending.tool] = resolved
				delete(pendingTools, callID)
				// A bash_wait/bash_kill result also finishes the original running card.
				for i := range filtered {
					for j := range filtered[i].Tools {
						filtered[i].Tools[j].ResolveBashJob(resolved)
					}
				}
			}
			continue
		}
		if role == "developer" {
			// Background jobs resolved after their running tool result are
			// recorded as runtime data; fold the final state into that card.
			if outcomes, delivered, ok := agent.BashJobOutcomes(content); ok {
				for _, outcome := range outcomes {
					for i := range filtered {
						for j := range filtered[i].Tools {
							filtered[i].Tools[j].ApplyBashOutcome(outcome, delivered)
						}
					}
				}
			}
			continue
		}
		if len(ordinaryCalls) > 0 {
			// Structured tool batches stay in session history so model requests
			// keep a stable prefix; results arrive in the following tool messages.
			historyTools := make([]agent.HistoryTool, len(ordinaryCalls))
			for index, call := range ordinaryCalls {
				historyTools[index] = agent.HistoryToolFromCall(call, "")
				historyTools[index].Status = ""
				if call.ID != "" {
					pendingTools[call.ID] = pendingHistoryTool{message: len(filtered), tool: index, call: call}
				}
			}
			filtered = append(filtered, ConversationHistoryMessage{Role: role, Sender: groupHistorySender(conversationType, role), Content: content, Tools: historyTools})
			continue
		}
		if role == "assistant" {
			if note, historyTools, ok := agent.ParseCompletedToolActivity(content); ok {
				ordinaryTools := make([]agent.HistoryTool, 0, len(historyTools))
				for _, historyTool := range historyTools {
					if historyTool.Name != groupConsultTool {
						ordinaryTools = append(ordinaryTools, historyTool)
						continue
					}
					participant, _ := historyTool.Arguments["participant"].(string)
					text, failed := groupHistoryResult(participant, historyTool.Result)
					kind := "message"
					if failed {
						kind = "error"
					}
					filtered = append(filtered, ConversationHistoryMessage{Role: role, Sender: participant, Kind: kind, Content: text})
				}
				if strings.TrimSpace(note) != "" || len(ordinaryTools) > 0 {
					filtered = append(filtered, ConversationHistoryMessage{Role: role, Sender: groupHistorySender(conversationType, role), Content: note, Tools: ordinaryTools})
				}
				continue
			}
		}
		if agent.IsToolImagesMessage(message) {
			continue
		}
		if (role == "user" || role == "assistant") && strings.TrimSpace(content) != "" {
			sender := groupHistorySender(conversationType, role)
			if role == "assistant" && conversationType != ConversationTypeGroup {
				sender, _ = message["name"].(string)
			}
			item := ConversationHistoryMessage{Role: role, Sender: sender, Content: content}
			if includeImageRefs && role == "user" {
				item.submission, _ = message[transcript.SubmissionKey].(string)
				item.Images = conversationImageRefs(message, offsets[index], saved.ContextNumber())
			}
			filtered = append(filtered, item)
		}
	}
	for _, pos := range externalTools {
		call := &filtered[pos.message].Tools[pos.tool]
		if call.Status == "running" {
			call.Status = "unknown"
		}
	}
	return filtered
}

type pendingHistoryTool struct {
	message int
	tool    int
	call    agent.ToolCall
}

func historyToolCalls(message map[string]any) []agent.ToolCall {
	raw := message["tool_calls"]
	if raw == nil {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var calls []agent.ToolCall
	if json.Unmarshal(encoded, &calls) != nil {
		return nil
	}
	return calls
}

func groupHistoryResult(participant, content string) (string, bool) {
	content = strings.TrimSpace(content)
	failedPrefix := fmt.Sprintf("Group participant @%s failed:", participant)
	replyPrefix := fmt.Sprintf("Group participant @%s replied:", participant)
	switch {
	case strings.HasPrefix(content, "Error:"):
		return strings.TrimSpace(strings.TrimPrefix(content, "Error:")), true
	case strings.HasPrefix(content, failedPrefix):
		return strings.TrimSpace(strings.TrimPrefix(content, failedPrefix)), true
	case strings.HasPrefix(content, replyPrefix):
		return strings.TrimSpace(strings.TrimPrefix(content, replyPrefix)), false
	default:
		return content, false
	}
}

func groupHistorySender(conversationType ConversationType, role string) string {
	if conversationType != ConversationTypeGroup {
		return ""
	}
	if role == "user" {
		return "user"
	}
	return groupScheduler
}

func historyBytes(messages []ConversationHistoryMessage) int {
	total := 0
	for _, message := range messages {
		total += historyMessageBytes(message)
	}
	return total
}

func historyMessageBytes(message ConversationHistoryMessage) int {
	total := len(message.Role) + len(message.Content) + len(message.Sender) + len(message.Kind) + len(message.ExternalTurn)
	if d := message.Delivery; d != nil {
		total += len(d.ID) + len(d.State)
		for _, file := range d.Attachments {
			total += len(file.Kind) + len(file.Name) + len(file.Path) + len(file.MIME)
		}
	}
	for _, part := range message.Parts {
		total += len(part.ID) + len(part.Kind) + len(part.Text)
		if p := part.Summary; p != nil {
			total += len(p.Item) + 8
		}
		if p := part.Plan; p != nil {
			total += len(p.Mode)
			for _, step := range p.Steps {
				total += len(step.Text) + len(step.Status) + len(step.Priority)
			}
		}
		if p := part.Prompt; p != nil {
			total += len(p.Title) + len(p.Method) + len(p.State) + len(p.Choice) + len(p.Header) + len(p.Detail)
			if len(p.Questions) > 0 {
				total += historyValueBytes(p.Questions)
			}
			for _, option := range p.Options {
				total += len(option.Label) + len(option.Description)
			}
		}
	}
	for _, tool := range message.Tools {
		total += historyToolBytes(tool)
	}
	return total
}

func historyToolBytes(tool agent.HistoryTool) int {
	size := len(tool.ID) + len(tool.TurnID) + len(tool.Name) + len(tool.Status) + len(tool.Result) + historyValueBytes(tool.Arguments)
	if tool.Edits != nil {
		size += historyValueBytes(tool.Edits)
	}
	return size
}

func historyValueBytes(value any) int {
	if value == nil {
		return 0
	}
	if object, ok := value.(map[string]any); ok && len(object) == 0 {
		return 0
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(encoded)
}

func truncateHistoryMessage(message ConversationHistoryMessage, budget int) ConversationHistoryMessage {
	if budget <= 0 {
		return ConversationHistoryMessage{Role: message.Role}
	}
	if historyMessageBytes(message) <= budget {
		return message
	}
	// Preserve the ordered answer and tool references before dropping a
	// message's entire timeline to fit the history response.
	if slices.ContainsFunc(message.Parts, func(p transcript.Part) bool {
		return p.Summary != nil || p.Prompt != nil && (p.Prompt.Detail != "" || len(p.Prompt.Questions) > 0)
	}) {
		message.Parts = slices.Clone(message.Parts)
		over := historyMessageBytes(message) - budget
		for i := len(message.Parts) - 1; i >= 0 && over > 0; i-- {
			part := &message.Parts[i]
			if part.Prompt != nil && len(part.Prompt.Questions) > 0 {
				p := *part.Prompt
				p.Questions = slices.Clone(p.Questions)
				for j := len(p.Questions) - 1; j >= 0 && over > 0; j-- {
					q := &p.Questions[j]
					before := historyValueBytes(q)
					var selected []transcript.PromptOption
					for _, opt := range q.Options {
						if opt.Selected {
							opt.Description = ""
							selected = append(selected, opt)
						}
					}
					q.Options = selected
					over -= before - historyValueBytes(q)
				}
				part.Prompt = &p
			}
			if part.Prompt != nil && part.Prompt.Detail != "" {
				p := *part.Prompt
				size := len(p.Detail)
				p.Detail = truncateHistoryText(p.Detail, max(0, size-over))
				over -= size - len(p.Detail)
				part.Prompt = &p
			}
			if part.Summary == nil || part.Text == "" {
				continue
			}
			preview := historyUTF8Prefix(part.Text, max(0, len(part.Text)-over))
			over -= len(part.Text) - len(preview)
			part.Text = preview
			meta := *part.Summary
			meta.Truncated = true
			part.Summary = &meta
		}
		if over <= 0 {
			return message
		}
		message.Parts = slices.DeleteFunc(message.Parts, func(p transcript.Part) bool { return p.Summary != nil })
		if historyMessageBytes(message) <= budget {
			return message
		}
	}
	message.Parts = nil
	if message.Delivery != nil {
		info := *message.Delivery
		info.Attachments = nil
		message.Delivery = &info
		if historyMessageBytes(message)-len(message.Content) > budget {
			message.Delivery = nil
			message.Kind = ""
		}
	}
	message.Tools = cloneHistoryTools(message.Tools)
	for i := range message.Tools {
		if message.Tools[i].Edits != nil {
			message.Tools[i].Edits = nil
			message.Tools[i].Truncated = true
			if historyMessageBytes(message) <= budget {
				return message
			}
		}
	}
	for index := range message.Tools {
		if len(message.Tools[index].Arguments) == 0 {
			continue
		}
		message.Tools[index].Arguments = nil
		message.Tools[index].Truncated = true
		if historyMessageBytes(message) <= budget {
			return message
		}
	}
	for index := len(message.Tools) - 1; index >= 0 && historyMessageBytes(message) > budget; index-- {
		over := historyMessageBytes(message) - budget
		message.Tools[index].Result = truncateHistoryText(message.Tools[index].Result, max(0, len(message.Tools[index].Result)-over))
		message.Tools[index].Truncated = true
	}
	for len(message.Tools) > 0 && historyMessageBytes(message) > budget {
		if len(message.Tools) == 1 {
			message.Tools = nil
			break
		}
		message.Tools = message.Tools[1:]
	}
	if historyMessageBytes(message) <= budget {
		return message
	}
	message.Content = truncateHistoryText(message.Content, max(0, budget-(historyMessageBytes(message)-len(message.Content))))
	return message
}

func cloneHistoryTools(tools []agent.HistoryTool) []agent.HistoryTool {
	if len(tools) == 0 {
		return nil
	}
	cloned := make([]agent.HistoryTool, len(tools))
	copy(cloned, tools)
	return cloned
}

func truncateHistoryText(value string, budget int) string {
	const suffix = "\n\n… [消息过长，显示已截断]"
	if budget <= len(suffix) {
		return historyUTF8Prefix(suffix, budget)
	}
	limit := budget - len(suffix)
	if len(value) <= limit {
		return value
	}
	for limit > 0 && limit < len(value) && value[limit]&0xc0 == 0x80 {
		limit--
	}
	return value[:limit] + suffix
}

func historyUTF8Prefix(value string, limit int) string {
	limit = min(max(0, limit), len(value))
	for limit > 0 && limit < len(value) && value[limit]&0xc0 == 0x80 {
		limit--
	}
	return value[:limit]
}

func (handler *handler) deleteConversation(writer http.ResponseWriter, service *Service, id string) {
	canonicalID, err := session.CanonicalID(id)
	if err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
		return
	}
	unlock := service.locker.Lock(canonicalID)
	defer unlock()
	saved, err := service.store.Open(canonicalID)
	if errors.Is(err, os.ErrNotExist) {
		writeError(writer, http.StatusNotFound, chatResponse{Error: "conversation not found"})
		return
	}
	if err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: err.Error()})
		return
	}
	if !saved.Meta().Chat {
		writeError(writer, http.StatusNotFound, chatResponse{Error: "conversation not found"})
		return
	}
	gate, err := saved.OpenContextLock(true)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, session.ErrContextBusy) {
			status = http.StatusConflict
		}
		writeError(writer, status, chatResponse{Error: err.Error()})
		return
	}
	defer gate.Close()
	service.webTurns.queueMu.Lock()
	defer service.webTurns.queueMu.Unlock()
	// Coordinate deletion with dispatch, including a queue owned by a different
	// server using this workspace. Damaged queue metadata can still be deleted.
	alreadyOwned := service.webTurns.queueLeases[canonicalID] != nil
	if err := service.webTurns.ownQueue(saved, context.Background()); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errQueueOwned) {
			status = http.StatusConflict
		}
		writeError(writer, status, chatResponse{Error: err.Error()})
		return
	}
	if !alreadyOwned {
		defer service.webTurns.releaseQueue(canonicalID)
	}
	if service.externalBroker != nil {
		var clearErr error
		if storedConversationType(saved.Meta().ConversationType) == ConversationTypeGroup {
			clearErr = service.externalBroker.ClearGroup(canonicalID)
		} else {
			clearErr = service.externalBroker.Clear(canonicalID)
		}
		if clearErr != nil {
			writeError(writer, http.StatusInternalServerError, chatResponse{Error: clearErr.Error()})
			return
		}
	}
	service.webTurns.forgetSession(canonicalID)
	if err := service.store.Delete(canonicalID); err != nil {
		writeError(writer, http.StatusInternalServerError, chatResponse{Error: err.Error()})
		return
	}
	service.webTurns.releaseQueue(canonicalID)
	_ = os.Remove(filepath.Join(filepath.Dir(saved.Dir()), queueLockName(saved)))
	_ = gate.Close()
	_ = os.Remove(gate.Name())
	writeJSON(writer, http.StatusOK, map[string]any{"deleted": true, "id": canonicalID})
}

func conversationMessageText(content any) string {
	if text, ok := content.(string); ok {
		return text
	}
	parts, ok := content.([]any)
	if !ok {
		return ""
	}
	texts := make([]string, 0, len(parts))
	images := 0
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch part["type"] {
		case "text":
			if text, ok := part["text"].(string); ok && strings.TrimSpace(text) != "" {
				texts = append(texts, text)
			}
		case "image_url", "image":
			images++
		}
	}
	if images > 0 {
		texts = append(texts, "[图片]")
	}
	return strings.Join(texts, "\n")
}
