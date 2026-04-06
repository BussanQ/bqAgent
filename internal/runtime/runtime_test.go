package runtime

import (
	"testing"

	"bqagent/internal/agent"
)

func TestConfigFromEnvUsesContextDefaults(t *testing.T) {
	config := ConfigFromEnv(func(string) string { return "" })
	defaults := agent.DefaultContextConfig()

	if config.ContextManagementEnabled != defaults.Enabled {
		t.Fatalf("ContextManagementEnabled = %t, want %t", config.ContextManagementEnabled, defaults.Enabled)
	}
	if config.ContextMaxInputTokens != defaults.MaxInputTokens {
		t.Fatalf("ContextMaxInputTokens = %d, want %d", config.ContextMaxInputTokens, defaults.MaxInputTokens)
	}
	if config.ContextTargetInputTokens != defaults.TargetInputTokens {
		t.Fatalf("ContextTargetInputTokens = %d, want %d", config.ContextTargetInputTokens, defaults.TargetInputTokens)
	}
	if config.ContextResponseReserveTokens != defaults.ResponseReserveTokens {
		t.Fatalf("ContextResponseReserveTokens = %d, want %d", config.ContextResponseReserveTokens, defaults.ResponseReserveTokens)
	}
	if config.ContextKeepLastTurns != defaults.KeepLastTurns {
		t.Fatalf("ContextKeepLastTurns = %d, want %d", config.ContextKeepLastTurns, defaults.KeepLastTurns)
	}
	if config.ContextSummarizationEnabled != defaults.SummarizationEnabled {
		t.Fatalf("ContextSummarizationEnabled = %t, want %t", config.ContextSummarizationEnabled, defaults.SummarizationEnabled)
	}
	if config.ContextSummaryTriggerTokens != defaults.SummaryTriggerTokens {
		t.Fatalf("ContextSummaryTriggerTokens = %d, want %d", config.ContextSummaryTriggerTokens, defaults.SummaryTriggerTokens)
	}
	if config.ContextSummaryModel != defaults.SummaryModel {
		t.Fatalf("ContextSummaryModel = %q, want %q", config.ContextSummaryModel, defaults.SummaryModel)
	}
}

func TestConfigFromEnvFallsBackOnInvalidContextValues(t *testing.T) {
	config := ConfigFromEnv(func(key string) string {
		switch key {
		case "CONTEXT_MANAGEMENT_ENABLED":
			return "not-bool"
		case "CONTEXT_MAX_INPUT_TOKENS":
			return "bad"
		case "CONTEXT_TARGET_INPUT_TOKENS":
			return "bad"
		case "CONTEXT_RESPONSE_RESERVE_TOKENS":
			return "bad"
		case "CONTEXT_KEEP_LAST_TURNS":
			return "bad"
		case "CONTEXT_SUMMARIZATION_ENABLED":
			return "not-bool"
		case "CONTEXT_SUMMARY_TRIGGER_TOKENS":
			return "bad"
		default:
			return ""
		}
	})
	defaults := agent.DefaultContextConfig()

	if config.ContextManagementEnabled != defaults.Enabled {
		t.Fatalf("ContextManagementEnabled = %t, want fallback %t", config.ContextManagementEnabled, defaults.Enabled)
	}
	if config.ContextMaxInputTokens != defaults.MaxInputTokens {
		t.Fatalf("ContextMaxInputTokens = %d, want fallback %d", config.ContextMaxInputTokens, defaults.MaxInputTokens)
	}
	if config.ContextTargetInputTokens != defaults.TargetInputTokens {
		t.Fatalf("ContextTargetInputTokens = %d, want fallback %d", config.ContextTargetInputTokens, defaults.TargetInputTokens)
	}
	if config.ContextResponseReserveTokens != defaults.ResponseReserveTokens {
		t.Fatalf("ContextResponseReserveTokens = %d, want fallback %d", config.ContextResponseReserveTokens, defaults.ResponseReserveTokens)
	}
	if config.ContextKeepLastTurns != defaults.KeepLastTurns {
		t.Fatalf("ContextKeepLastTurns = %d, want fallback %d", config.ContextKeepLastTurns, defaults.KeepLastTurns)
	}
	if config.ContextSummarizationEnabled != defaults.SummarizationEnabled {
		t.Fatalf("ContextSummarizationEnabled = %t, want fallback %t", config.ContextSummarizationEnabled, defaults.SummarizationEnabled)
	}
	if config.ContextSummaryTriggerTokens != defaults.SummaryTriggerTokens {
		t.Fatalf("ContextSummaryTriggerTokens = %d, want fallback %d", config.ContextSummaryTriggerTokens, defaults.SummaryTriggerTokens)
	}
}
