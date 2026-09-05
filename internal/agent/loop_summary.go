package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func ensureSystemPromptMessage(messages []map[string]any, systemPrompt string) []map[string]any {
	return ensurePromptSnapshotMessages(messages, PromptSnapshot{Stable: systemPrompt, StableHash: hashPromptText(systemPrompt)})
}

func ensurePromptSnapshotMessages(messages []map[string]any, snapshot PromptSnapshot) []map[string]any {
	promptMessages := snapshot.Messages()
	if len(promptMessages) == 0 {
		return messages
	}
	if len(messages) == 0 {
		return promptMessages
	}
	role, _ := messages[0]["role"].(string)
	if role == "system" {
		messages[0] = promptMessages[0]
	} else {
		messages = append([]map[string]any{promptMessages[0]}, messages...)
	}
	if len(promptMessages) == 1 {
		return messages
	}
	if len(messages) > 1 {
		secondRole, _ := messages[1]["role"].(string)
		secondContent, _ := messages[1]["content"].(string)
		expectedContent, _ := promptMessages[1]["content"].(string)
		if secondRole == "system" && secondContent == expectedContent {
			return messages
		}
		if secondRole == "system" && !strings.HasPrefix(secondContent, EarlierConversationSummaryPrefix) {
			messages[1] = promptMessages[1]
			return messages
		}
	}
	return append(messages[:1], append([]map[string]any{promptMessages[1]}, messages[1:]...)...)
}

func (a *Agent) stageBoundaryReason(iteration int, explorationCtx context.Context) string {
	if a.stageConfig.MaxIterations > 0 && iteration >= a.stageConfig.MaxIterations {
		return fmt.Sprintf("stage iteration budget reached (%d)", a.stageConfig.MaxIterations)
	}
	if explorationCtx.Err() != nil && a.stageConfig.Timeout > 0 {
		return "stage time budget reached"
	}
	return ""
}

func (a *Agent) finishStageCheckpoint(ctx context.Context, messages []map[string]any, reason string, iterations int) (string, []map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return "", messages, err
	}
	a.writeStageProgress(fmt.Sprintf("Preparing stage summary after %d iterations", iterations))
	request, compacted := a.buildRequestMessages(ctx, messages, nil, ChatCompletionOptions{})
	if compacted != nil {
		messages = compacted
	}
	if err := ctx.Err(); err != nil {
		return "", messages, err
	}
	request = append(request, map[string]any{
		"role":    "user",
		"content": fmt.Sprintf("The current interactive analysis stage must stop now because %s. Based only on the work and tool results above, provide a concise checkpoint with exactly these sections: 已发现, 未完成, 建议下一步. State that the user can reply ‘继续’ to resume from this session. Do not call tools.", reason),
	})
	message, summaryErr := a.client.CreateChatCompletion(ctx, a.model, request, nil)
	if err := ctx.Err(); err != nil {
		return "", messages, err
	}
	summary := strings.TrimSpace(message.FinalContent())
	if summaryErr != nil || summary == "" {
		if err := ctx.Err(); err != nil {
			return "", messages, err
		}
		summary = fmt.Sprintf("阶段已暂停（%s）。\n\n已发现\n- 已完成 %d 轮探索，相关工具结果已保留在当前会话中。\n\n未完成\n- 仍需基于现有结果继续分析。\n\n建议下一步\n- 回复“继续”，我会沿用当前 session 和已保存的上下文继续。", reason, iterations)
	}
	if err := ctx.Err(); err != nil {
		return "", messages, err
	}
	checkpoint := map[string]any{"role": "assistant", "content": summary}
	messages = append(messages, checkpoint)
	if recordErr := a.recordMessages(checkpoint); recordErr != nil {
		return "", messages, recordErr
	}
	a.writeStageProgress("Stage summary completed; waiting for confirmation to continue")
	return summary, messages, nil
}

func shouldSummarize(estimatedTokens int, config ContextConfig) bool {
	return config.SummarizationEnabled && estimatedTokens > config.SummaryTriggerTokens
}

func (a *Agent) summarizeMessages(ctx context.Context, messages []map[string]any, targetTokens int) ([]map[string]any, bool) {
	prefix, tail, ok := splitMessagesForSummary(messages, a.contextConfig.KeepLastTurns)
	if !ok {
		return nil, false
	}
	summary, err := a.generateSummary(ctx, prefix)
	if err != nil || strings.TrimSpace(summary) == "" {
		return nil, false
	}

	promptMessageCount := leadingPromptMessageCount(prefix)
	summarized := make([]map[string]any, 0, len(tail)+promptMessageCount+1)
	if promptMessageCount > 0 {
		summarized = append(summarized, prefix[:promptMessageCount]...)
	}
	summarized = append(summarized, map[string]any{
		"role":    "system",
		"content": EarlierConversationSummaryPrefix + summary,
	})
	summarized = append(summarized, tail...)
	summarized = hardPruneMessagesToBudget(summarized, targetTokens)
	if a.checkpointSaver != nil {
		checkpointTail := summarized
		storedPromptCount := leadingPromptMessageCount(checkpointTail)
		if storedPromptCount > 0 {
			checkpointTail = checkpointTail[storedPromptCount:]
		}
		if len(checkpointTail) > 0 {
			if content, _ := checkpointTail[0]["content"].(string); strings.HasPrefix(content, EarlierConversationSummaryPrefix) {
				checkpointTail = checkpointTail[1:]
			}
		}
		var err error
		if saver, ok := a.checkpointSaver.(PromptContextCheckpointRecorder); ok {
			err = saver.SaveCheckpointSummaryWithPrompt(summary, checkpointTail, a.systemPrompt, a.prompt.StableHash, storedPromptCount)
		} else {
			err = a.checkpointSaver.SaveCheckpointSummary(summary, checkpointTail, a.systemPrompt)
		}
		if err != nil {
			a.logf("[Context] checkpoint save failed: %v\n", err)
		}
	}
	return summarized, true
}

func splitMessagesForSummary(messages []map[string]any, keepLastTurns int) ([]map[string]any, []map[string]any, bool) {
	if len(messages) <= 2 {
		return nil, nil, false
	}
	start := safeTailStart(messages, keepLastTurns)
	systemEnd := leadingPromptMessageCount(messages)
	if start <= systemEnd || start >= len(messages) {
		return nil, nil, false
	}
	return messages[:start], messages[start:], true
}

func leadingPromptMessageCount(messages []map[string]any) int {
	count := 0
	for _, message := range messages {
		role, _ := message["role"].(string)
		if role != "system" {
			break
		}
		content, _ := message["content"].(string)
		if strings.HasPrefix(content, EarlierConversationSummaryPrefix) {
			break
		}
		count++
	}
	return count
}

func (a *Agent) generateSummary(ctx context.Context, messages []map[string]any) (string, error) {
	client, ok := a.client.(chatCompletionOptionsClient)
	if !ok {
		return "", fmt.Errorf("chat completion options are not supported")
	}

	model := strings.TrimSpace(a.contextConfig.SummaryModel)
	if model == "" {
		model = a.model
	}
	messages = hardPruneMessagesToBudget(messages, a.contextConfig.TargetInputTokens)
	promptMessages := []map[string]any{
		{"role": "system", "content": "Summarize the earlier conversation for future continuation. Preserve goals, constraints, decisions, unresolved questions, and important factual context. Be concise."},
		{"role": "user", "content": buildSummaryInput(messages)},
	}
	response, err := client.CreateChatCompletionWithOptions(ctx, model, promptMessages, nil, ChatCompletionOptions{})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(response.FinalContent()), nil
}

func buildSummaryInput(messages []map[string]any) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		role, _ := message["role"].(string)
		content, _ := message["content"].(string)
		content = strings.TrimSpace(content)
		if role == "" || content == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", role, content))
	}
	return strings.Join(parts, "\n")
}

func safeTailStart(messages []map[string]any, keepLastTurns int) int {
	turns := 0
	for i := len(messages) - 1; i >= 0; i-- {
		role, _ := messages[i]["role"].(string)
		if role == "user" {
			turns++
			if turns >= keepLastTurns {
				return i
			}
		}
	}
	return 0
}

type legacyRunSkillCall struct {
	ID        string
	Arguments string
}

func normalizeLegacyRunSkillHistory(messages []map[string]any) []map[string]any {
	normalized := make([]map[string]any, 0, len(messages))
	for index := 0; index < len(messages); index++ {
		message := messages[index]
		role, _ := message["role"].(string)
		if role != "assistant" {
			normalized = append(normalized, message)
			continue
		}

		calls := extractToolCallsFromMessageMap(message)
		legacyCalls := make([]legacyRunSkillCall, 0)
		remainingCalls := make([]any, 0, len(calls))
		legacyIDs := make(map[string]bool)
		for _, rawCall := range calls {
			id, name, arguments := toolCallIdentity(rawCall)
			if name != "run_skill" {
				remainingCalls = append(remainingCalls, rawCall)
				continue
			}
			legacyCalls = append(legacyCalls, legacyRunSkillCall{ID: id, Arguments: arguments})
			if id != "" {
				legacyIDs[id] = true
			}
		}
		if len(legacyCalls) == 0 {
			normalized = append(normalized, message)
			continue
		}

		end := index + 1
		legacyResults := make(map[string]string)
		ordinaryResults := make([]map[string]any, 0)
		for end < len(messages) {
			result := messages[end]
			resultRole, _ := result["role"].(string)
			if resultRole != "tool" {
				break
			}
			toolCallID, _ := result["tool_call_id"].(string)
			if legacyIDs[toolCallID] {
				legacyResults[toolCallID], _ = result["content"].(string)
			} else {
				ordinaryResults = append(ordinaryResults, result)
			}
			end++
		}

		normalized = append(normalized, legacyRunSkillEvidence(legacyCalls, legacyResults))
		if len(remainingCalls) > 0 {
			copyMessage := make(map[string]any, len(message))
			for key, value := range message {
				copyMessage[key] = value
			}
			copyMessage["tool_calls"] = remainingCalls
			normalized = append(normalized, copyMessage)
			normalized = append(normalized, ordinaryResults...)
		} else if content, _ := message["content"].(string); strings.TrimSpace(content) != "" {
			normalized = append(normalized, map[string]any{"role": "assistant", "content": content})
		}
		index = end - 1
	}
	return normalized
}

func toolCallIdentity(raw any) (id, name, arguments string) {
	switch call := raw.(type) {
	case ToolCall:
		return call.ID, call.Function.Name, call.Function.Arguments
	case map[string]any:
		id, _ = call["id"].(string)
		function, _ := call["function"].(map[string]any)
		name, _ = function["name"].(string)
		arguments, _ = function["arguments"].(string)
		return id, name, arguments
	default:
		return "", "", ""
	}
}

func legacyRunSkillEvidence(calls []legacyRunSkillCall, results map[string]string) map[string]any {
	parts := []string{"Deprecated skill activity from an earlier runtime version was removed from tool history before contacting the model."}
	for _, call := range calls {
		detail := "Requested legacy run_skill arguments: " + call.Arguments
		if result := strings.TrimSpace(results[call.ID]); result != "" {
			detail += "\nArchived result:\n" + result
		} else {
			detail += "\nNo completed result was recorded. If this skill is still needed, use the current Skills index and read_file to load its SKILL.md."
		}
		parts = append(parts, detail)
	}
	return map[string]any{"role": "assistant", "content": strings.Join(parts, "\n")}
}

func sanitizeCompletedToolHistory(messages []map[string]any) []map[string]any {
	if len(messages) == 0 {
		return messages
	}
	messages = normalizeLegacyRunSkillHistory(messages)

	pendingAssistantIndex := -1
	pendingToolStart := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		role, _ := messages[i]["role"].(string)
		if role == "tool" {
			pendingToolStart = i
			continue
		}
		if role == "assistant" && len(extractToolCallsFromMessageMap(messages[i])) > 0 && pendingToolStart == i+1 {
			pendingAssistantIndex = i
		}
		break
	}

	sanitized := make([]map[string]any, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		message := messages[i]
		role, _ := message["role"].(string)
		if role == "tool" {
			if pendingAssistantIndex >= 0 && i >= pendingToolStart {
				sanitized = append(sanitized, message)
			} else {
				content, _ := message["content"].(string)
				sanitized = append(sanitized, map[string]any{"role": "assistant", "content": "Completed tool result:\n" + content})
			}
			continue
		}
		if role == "assistant" && len(extractToolCallsFromMessageMap(message)) > 0 {
			if i == pendingAssistantIndex {
				sanitized = append(sanitized, message)
				continue
			}
			end := i + 1
			for end < len(messages) {
				nextRole, _ := messages[end]["role"].(string)
				if nextRole != "tool" {
					break
				}
				end++
			}
			sanitized = append(sanitized, summarizeCompletedToolBatch(message, messages[i+1:end]))
			i = end - 1
			continue
		}
		sanitized = append(sanitized, message)
	}
	return sanitized
}

func summarizeCompletedToolBatch(assistant map[string]any, results []map[string]any) map[string]any {
	parts := []string{CompletedToolActivityLead}
	if content, _ := assistant["content"].(string); strings.TrimSpace(content) != "" {
		parts = append(parts, "Assistant note: "+strings.TrimSpace(content))
	}
	if calls, err := json.Marshal(assistant["tool_calls"]); err == nil {
		parts = append(parts, "Calls: "+string(calls))
	}
	for _, result := range results {
		id, _ := result["tool_call_id"].(string)
		content, _ := result["content"].(string)
		parts = append(parts, fmt.Sprintf("Result %s:\n%s", id, content))
	}
	return map[string]any{"role": "assistant", "content": strings.Join(parts, "\n")}
}

func extractToolCallsFromMessageMap(message map[string]any) []any {
	raw, ok := message["tool_calls"]
	if !ok || raw == nil {
		return nil
	}
	calls, ok := raw.([]any)
	if ok {
		return calls
	}
	if typed, ok := raw.([]ToolCall); ok && len(typed) > 0 {
		calls := make([]any, len(typed))
		for i, call := range typed {
			calls[i] = call
		}
		return calls
	}
	return nil
}
