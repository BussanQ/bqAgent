package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"bqagent/internal/tools"
	apptrace "bqagent/internal/trace"
)

func DefaultContextConfig() ContextConfig {
	return ContextConfig{
		Enabled:                  true,
		MaxInputTokens:           DefaultContextMaxInputTokens,
		ResponseReserveTokens:    DefaultContextResponseReserveTokens,
		TargetInputTokens:        DefaultContextCompressionTokens,
		KeepLastTurns:            DefaultContextKeepLastTurns,
		ExactCountTriggerPercent: DefaultExactCountTriggerPercent,
		SummarizationEnabled:     true,
		SummaryTriggerTokens:     DefaultContextCompressionTokens,
	}
}

// BoundWorkingMessages creates a request-safe working snapshot without calling
// the model. Server paths use it for turns handled by external agents or command
// shortcuts that do not pass through runConversation's context manager.
func BoundWorkingMessages(messages []map[string]any, config ContextConfig) []map[string]any {
	config = normalizeContextConfig(config)
	working := sanitizeCompletedToolHistory(duplicateMessages(messages))
	if !config.Enabled || estimateMessagesTokens(working) <= config.TargetInputTokens {
		return working
	}
	working = pruneMessagesToBudget(working, config)
	return hardPruneMessagesToBudget(working, config.TargetInputTokens)
}

func normalizeContextConfig(config ContextConfig) ContextConfig {
	if config.MaxInputTokens <= 0 {
		config.MaxInputTokens = DefaultContextMaxInputTokens
	}
	if config.ResponseReserveTokens < 0 {
		config.ResponseReserveTokens = 0
	}
	if config.ResponseReserveTokens >= config.MaxInputTokens {
		config.ResponseReserveTokens = config.MaxInputTokens / 4
	}
	if config.TargetInputTokens <= 0 || config.TargetInputTokens >= config.MaxInputTokens {
		config.TargetInputTokens = config.MaxInputTokens - config.ResponseReserveTokens
	}
	if config.TargetInputTokens <= 0 {
		config.TargetInputTokens = config.MaxInputTokens
	}
	if config.KeepLastTurns < 0 {
		config.KeepLastTurns = DefaultContextKeepLastTurns
	}
	if config.ExactCountTriggerPercent <= 0 || config.ExactCountTriggerPercent > 100 {
		config.ExactCountTriggerPercent = DefaultExactCountTriggerPercent
	}
	if config.SummaryTriggerTokens <= 0 {
		config.SummaryTriggerTokens = config.TargetInputTokens
	}
	return config
}

func (a *Agent) buildRequestMessages(ctx context.Context, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (request []map[string]any, compacted []map[string]any) {
	sanitized := sanitizeCompletedToolHistory(duplicateMessages(messages))
	if !a.contextConfig.Enabled {
		return sanitized, nil
	}

	measurement := a.measureRequestTokens(ctx, sanitized, definitions, options, false)
	if measurement.tokens <= a.contextConfig.TargetInputTokens {
		a.logContextBudget(len(messages), len(sanitized), len(sanitized), measurement, false, false)
		return sanitized, nil
	}

	messageTarget := messageBudgetForRequest(sanitized, measurement.tokens, a.contextConfig.TargetInputTokens)
	pruned := pruneMessagesToBudget(sanitized, a.contextConfig)
	pruned = hardPruneMessagesToBudget(pruned, messageTarget)
	if !a.contextConfig.SummarizationEnabled || !shouldSummarize(measurement.tokens, a.contextConfig) {
		pruned, prunedMeasurement := a.finalizeRequestBudget(ctx, pruned, definitions, options, measurement.exact)
		a.logContextBudget(len(messages), len(sanitized), len(pruned), prunedMeasurement, true, false)
		return pruned, pruned
	}

	summarized, ok := a.summarizeMessages(ctx, sanitized, messageTarget)
	if !ok {
		pruned, prunedMeasurement := a.finalizeRequestBudget(ctx, pruned, definitions, options, measurement.exact)
		a.logContextBudget(len(messages), len(sanitized), len(pruned), prunedMeasurement, true, false)
		return pruned, pruned
	}
	summarized, summaryMeasurement := a.finalizeRequestBudget(ctx, summarized, definitions, options, measurement.exact)
	a.logContextBudget(len(messages), len(sanitized), len(summarized), summaryMeasurement, true, true)
	return summarized, summarized
}

func (a *Agent) finalizeRequestBudget(ctx context.Context, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions, forceExact bool) ([]map[string]any, requestTokenMeasurement) {
	measurement := a.measureRequestTokens(ctx, messages, definitions, options, forceExact)
	if measurement.tokens <= a.contextConfig.TargetInputTokens {
		return messages, measurement
	}

	messageTokens := estimateMessagesTokens(messages)
	overage := measurement.tokens - a.contextConfig.TargetInputTokens
	// Leave a small guard because removing one locally estimated token does not
	// always remove exactly one provider token after request framing.
	guard := maxInt(1, a.contextConfig.TargetInputTokens/100)
	messageTarget := maxInt(1, messageTokens-overage-guard)
	adjusted := hardPruneMessagesToBudget(messages, messageTarget)
	removedEstimate := messageTokens - estimateMessagesTokens(adjusted)
	measurement.tokens = maxInt(0, measurement.tokens-removedEstimate)
	measurement.source += "+adjusted"
	measurement.exact = false
	return adjusted, measurement
}

func messageBudgetForRequest(messages []map[string]any, requestTokens int, targetTokens int) int {
	staticAndFramingTokens := requestTokens - estimateMessagesTokens(messages)
	if staticAndFramingTokens < 0 {
		staticAndFramingTokens = 0
	}
	return maxInt(1, targetTokens-staticAndFramingTokens)
}

func (a *Agent) measureRequestTokens(ctx context.Context, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions, forceExact bool) requestTokenMeasurement {
	estimated, source := a.estimateRequestTokens(messages, definitions, options)
	measurement := requestTokenMeasurement{tokens: estimated, source: source}
	trigger := a.contextConfig.TargetInputTokens * a.contextConfig.ExactCountTriggerPercent / 100
	if !forceExact && estimated < trigger {
		return measurement
	}

	counter, ok := a.client.(InputTokenCounter)
	if !ok {
		return measurement
	}
	count, err := counter.CountInputTokens(ctx, a.model, messages, definitions, options)
	if err != nil {
		if !errors.Is(err, ErrInputTokenCountUnsupported) {
			a.logf("[Context] exact token count failed; using %s: %v\n", source, err)
		}
		return measurement
	}
	if count.Tokens <= 0 {
		return measurement
	}
	measurement.tokens = count.Tokens
	measurement.source = "provider"
	measurement.exact = count.Exact
	return measurement
}

func (a *Agent) estimateRequestTokens(messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (int, string) {
	shapeHash := requestShapeHash(definitions, options)
	messageHashes := hashMessages(messages)
	a.contextUsageMu.Lock()
	baseline := a.promptUsage
	a.contextUsageMu.Unlock()
	if baseline.promptTokens > 0 && baseline.requestShapeHash == shapeHash && hashesHavePrefix(messageHashes, baseline.messageHashes) {
		return baseline.promptTokens + estimateMessagesTokens(messages[len(baseline.messageHashes):]), "usage+estimate"
	}
	return estimateVisibleRequestTokens(messages, definitions, options), "estimate"
}

func (a *Agent) rememberServerPromptUsage(messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions, promptTokens int) {
	if promptTokens <= 0 {
		return
	}
	a.contextUsageMu.Lock()
	a.promptUsage = promptUsageBaseline{
		messageHashes:    hashMessages(messages),
		requestShapeHash: requestShapeHash(definitions, options),
		promptTokens:     promptTokens,
	}
	a.contextUsageMu.Unlock()
}

func estimateVisibleRequestTokens(messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) int {
	total := estimateMessagesTokens(messages)
	if len(definitions) > 0 {
		if encoded, err := json.Marshal(definitions); err == nil {
			total += estimateTextTokens(string(encoded))
		}
	}
	if len(options.ResponseFormat) > 0 {
		if encoded, err := json.Marshal(options.ResponseFormat); err == nil {
			total += estimateTextTokens(string(encoded))
		}
	}
	return total
}

func requestShapeHash(definitions []tools.Definition, options ChatCompletionOptions) string {
	return apptrace.HashJSON(struct {
		Definitions    []tools.Definition
		ResponseFormat map[string]any
		Reasoning      ReasoningEffort
	}{definitions, options.ResponseFormat, options.ReasoningEffort})
}

func hashMessages(messages []map[string]any) []string {
	hashes := make([]string, len(messages))
	for index, message := range messages {
		hashes[index] = apptrace.HashJSON(message)
	}
	return hashes
}

func hashesHavePrefix(values []string, prefix []string) bool {
	if len(values) < len(prefix) {
		return false
	}
	for index := range prefix {
		if values[index] != prefix[index] {
			return false
		}
	}
	return true
}

func pruneMessagesToBudget(messages []map[string]any, config ContextConfig) []map[string]any {
	if len(messages) <= 1 {
		return messages
	}

	systemEnd := leadingPromptMessageCount(messages)
	if systemEnd >= len(messages) {
		return messages
	}

	start := safeTailStart(messages, config.KeepLastTurns)
	if start < systemEnd {
		start = systemEnd
	}

	pruned := append([]map[string]any{}, messages[:systemEnd]...)
	pruned = append(pruned, messages[start:]...)
	return pruned
}

// hardPruneMessagesToBudget is the final request-size guard. Turn-based pruning
// can still exceed the token target when one recent turn contains many or very
// large tool results. This keeps a contiguous, valid tail and truncates the
// newest group only when that group alone would exceed the remaining budget.
func hardPruneMessagesToBudget(messages []map[string]any, targetTokens int) []map[string]any {
	if targetTokens <= 0 || len(messages) == 0 || estimateMessagesTokens(messages) <= targetTokens {
		return messages
	}

	protectedEnd := 0
	result := make([]map[string]any, 0, len(messages))
	remaining := targetTokens
	latestUserIndex := -1
	for index := len(messages) - 1; index >= 0; index-- {
		if role, _ := messages[index]["role"].(string); role == "user" {
			latestUserIndex = index
			break
		}
	}
	var reservedUser []map[string]any
	reservedUserTokens := 0
	if latestUserIndex >= 0 {
		userBudget := targetTokens * 3 / 4
		if userBudget < 1 {
			userBudget = 1
		}
		reservedUser = truncateMessageGroupToBudget(messages[latestUserIndex:latestUserIndex+1], userBudget)
		reservedUserTokens = estimateMessagesTokens(reservedUser)
	}
	protectedEnd = leadingPromptMessageCount(messages)
	if protectedEnd > 0 {
		result = append(result, messages[:protectedEnd]...)
		remaining -= estimateMessagesTokens(messages[:protectedEnd])
	}
	if protectedEnd < len(messages) {
		if content, _ := messages[protectedEnd]["content"].(string); strings.HasPrefix(content, EarlierConversationSummaryPrefix) {
			summaryBudget := remaining - reservedUserTokens
			if summaryBudget > 0 {
				clippedSummary := truncateMessageGroupToBudget(messages[protectedEnd:protectedEnd+1], summaryBudget)
				result = append(result, clippedSummary...)
				remaining -= estimateMessagesTokens(clippedSummary)
			}
			protectedEnd++
		}
	}
	if remaining <= 0 || protectedEnd >= len(messages) {
		return result
	}

	groups := messageTailGroups(messages, protectedEnd)
	selected := make([]messageGroup, 0, len(groups))
	latestUserGroup := -1
	for index, group := range groups {
		if group.start == latestUserIndex {
			latestUserGroup = index
			break
		}
	}
	for index := len(groups) - 1; index >= 0; index-- {
		group := groups[index]
		groupMessages := group.messages
		if index == latestUserGroup && len(reservedUser) > 0 {
			groupMessages = reservedUser
		}
		available := remaining
		if latestUserGroup >= 0 && index > latestUserGroup {
			available -= reservedUserTokens
		}
		if available <= 0 {
			continue
		}
		cost := estimateMessagesTokens(groupMessages)
		if cost > available {
			if index > latestUserGroup || (latestUserGroup < 0 && len(selected) == 0) {
				clipped := truncateMessageGroupToBudget(groupMessages, available)
				if len(clipped) > 0 {
					selected = append(selected, messageGroup{start: group.start, messages: clipped})
					remaining -= estimateMessagesTokens(clipped)
				}
				continue
			}
			break
		}
		selected = append(selected, messageGroup{start: group.start, messages: groupMessages})
		remaining -= cost
	}
	for index := len(selected) - 1; index >= 0; index-- {
		result = append(result, selected[index].messages...)
	}
	return result
}

type messageGroup struct {
	start    int
	messages []map[string]any
}

func messageTailGroups(messages []map[string]any, start int) []messageGroup {
	groups := make([]messageGroup, 0, len(messages)-start)
	for index := start; index < len(messages); {
		end := index + 1
		role, _ := messages[index]["role"].(string)
		if role == "assistant" && len(extractToolCallsFromMessageMap(messages[index])) > 0 {
			for end < len(messages) {
				nextRole, _ := messages[end]["role"].(string)
				if nextRole != "tool" {
					break
				}
				end++
			}
		}
		groups = append(groups, messageGroup{start: index, messages: messages[index:end]})
		index = end
	}
	return groups
}

func truncateMessageGroupToBudget(group []map[string]any, budgetTokens int) []map[string]any {
	cloned := duplicateMessages(group)
	for _, message := range cloned {
		if parts, ok := message["content"].([]any); ok {
			message["content"] = cloneContentParts(parts)
		}
	}
	if budgetTokens <= 0 {
		return nil
	}
	for estimateMessagesTokens(cloned) > budgetTokens {
		if dropOldestImagePart(cloned) {
			continue
		}
		largestIndex := -1
		largestContent := ""
		for index, message := range cloned {
			content, _ := message["content"].(string)
			if len(content) > len(largestContent) {
				largestIndex = index
				largestContent = content
			}
		}
		if largestIndex < 0 || len(largestContent) <= 16 {
			break
		}
		maxChars := len(largestContent) / 2
		if maxChars < 16 {
			maxChars = 16
		}
		cloned[largestIndex]["content"] = truncateTextMiddle(largestContent, maxChars)
	}
	return cloned
}

func cloneContentParts(parts []any) []any {
	cloned := make([]any, len(parts))
	for index, part := range parts {
		cloned[index] = cloneContentValue(part)
	}
	return cloned
}

func cloneContentValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = cloneContentValue(item)
		}
		return result
	case []any:
		return cloneContentParts(typed)
	default:
		return value
	}
}

func dropOldestImagePart(messages []map[string]any) bool {
	for _, message := range messages {
		parts, ok := message["content"].([]any)
		if !ok {
			continue
		}
		for index, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok || !isImageContentPart(part) {
				continue
			}
			parts = append(parts[:index], parts[index+1:]...)
			if !hasTextContentPart(parts) {
				parts = append([]any{map[string]any{"type": "text", "text": "Earlier image attachment omitted to fit context budget."}}, parts...)
			}
			message["content"] = parts
			return true
		}
	}
	return false
}

func isImageContentPart(part map[string]any) bool {
	partType, _ := part["type"].(string)
	partType = strings.ToLower(strings.TrimSpace(partType))
	return strings.Contains(partType, "image") || part["image_url"] != nil
}

func hasTextContentPart(parts []any) bool {
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			continue
		}
		partType, _ := part["type"].(string)
		text, _ := part["text"].(string)
		if strings.Contains(strings.ToLower(partType), "text") && strings.TrimSpace(text) != "" {
			return true
		}
	}
	return false
}

func truncateTextMiddle(text string, maxChars int) string {
	if maxChars <= 0 || len(text) <= maxChars {
		return text
	}
	marker := "\n... [content truncated to fit context budget] ...\n"
	if maxChars <= len(marker)+2 {
		return text[:maxChars]
	}
	available := maxChars - len(marker)
	head := available * 2 / 3
	tail := available - head
	return text[:head] + marker + text[len(text)-tail:]
}

func estimateMessagesTokens(messages []map[string]any) int {
	totalTokens := 0
	for _, message := range messages {
		for _, key := range []string{"role", "tool_call_id"} {
			if text, ok := message[key].(string); ok {
				totalTokens += estimateTextTokens(text)
			}
		}
		totalTokens += estimateContentTokens(message["content"])
		if toolCalls, ok := message["tool_calls"]; ok && toolCalls != nil {
			encoded, err := json.Marshal(toolCalls)
			if err == nil {
				totalTokens += estimateTextTokens(string(encoded))
			}
		}
	}
	return totalTokens
}

func estimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

func estimateContentTokens(content any) int {
	switch typed := content.(type) {
	case string:
		if strings.HasPrefix(typed, "data:image/") {
			return estimateDataURIImageTokens(typed)
		}
		return estimateTextTokens(typed)
	case []any:
		total := 0
		for _, part := range typed {
			total += estimateContentTokens(part)
		}
		return total
	case map[string]any:
		total := 0
		for key, value := range typed {
			switch key {
			case "text", "url", "image_url", "source":
				total += estimateContentTokens(value)
			}
		}
		return total
	default:
		return 0
	}
}

func estimateDataURIImageTokens(dataURI string) int {
	comma := strings.IndexByte(dataURI, ',')
	if comma < 0 {
		return 256
	}
	encodedLength := len(dataURI) - comma - 1
	rawBytes := encodedLength * 3 / 4
	return 256 + (rawBytes+1023)/1024
}

func (a *Agent) logContextBudget(rawCount, sanitizedCount, requestCount int, measurement requestTokenMeasurement, pruned bool, summarized bool) {
	if a.logWriter == nil {
		return
	}
	fmt.Fprintf(a.logWriter, "[Context] raw_messages=%d sanitized_messages=%d request_messages=%d input_tokens=%d token_source=%s exact=%t pruned=%t summarized=%t target_tokens=%d\n", rawCount, sanitizedCount, requestCount, measurement.tokens, measurement.source, measurement.exact, pruned, summarized, a.contextConfig.TargetInputTokens)
}
