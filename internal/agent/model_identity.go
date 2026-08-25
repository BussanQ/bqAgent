package agent

import "strings"

const modelIdentityPrefix = "Current runtime model:"

// EffectiveModel normalizes the configured model used for requests and runtime metadata.
func EffectiveModel(model string) string {
	return strings.TrimSpace(model)
}

// AppendModelIdentitySystemPrompt adds or replaces the current model identity
// in a system prompt. Keeping this operation idempotent prevents resumed
// sessions from retaining a stale model after the service restarts.
func AppendModelIdentitySystemPrompt(systemPrompt, model string, apiType APIType) string {
	model = singleLinePromptValue(EffectiveModel(model))
	identity := ""
	if model != "" {
		apiType = NormalizeAPIType(string(apiType))
		identity = modelIdentityPrefix + " " + model + " (API type: " + string(apiType) + "). When asked which model is in use, answer with this exact current-run value."
	}

	base := strings.TrimSpace(systemPrompt)

	lines := strings.Split(strings.ReplaceAll(base, "\r\n", "\n"), "\n")
	replaced := false
	output := make([]string, 0, len(lines)+2)
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), modelIdentityPrefix) {
			if identity != "" && !replaced {
				output = append(output, identity)
				replaced = true
			}
			continue
		}
		output = append(output, line)
	}
	prompt := strings.TrimSpace(strings.Join(output, "\n"))
	if identity == "" || replaced {
		return prompt
	}
	if prompt == "" {
		return identity
	}
	return prompt + "\n\n" + identity
}

func singleLinePromptValue(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
