package runtime

import (
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/session"
)

func TestConfigFromEnvUsesContextDefaults(t *testing.T) {
	config := ConfigFromEnv(func(string) string { return "" })
	defaults := agent.DefaultContextConfig()
	if config.RunTraceEnabled {
		t.Fatal("RunTraceEnabled = true, want false by default")
	}

	if config.ContextManagementEnabled != defaults.Enabled {
		t.Fatalf("ContextManagementEnabled = %t, want %t", config.ContextManagementEnabled, defaults.Enabled)
	}
	if config.MaxIterations != agent.DefaultMaxIterations {
		t.Fatalf("MaxIterations = %d, want %d", config.MaxIterations, agent.DefaultMaxIterations)
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

func TestConfigFromEnvUsesSessionStorageDefaultsAndOverrides(t *testing.T) {
	defaults := ConfigFromEnv(func(string) string { return "" })
	if defaults.SessionTranscriptMode != session.TranscriptModeCompact || defaults.SessionOutputMaxBytes != session.DefaultOutputMaxBytes {
		t.Fatalf("session defaults = mode %q bytes %d", defaults.SessionTranscriptMode, defaults.SessionOutputMaxBytes)
	}
	overrides := ConfigFromEnv(func(key string) string {
		switch key {
		case "SESSION_TRANSCRIPT_MODE":
			return "full"
		case "SESSION_OUTPUT_MAX_BYTES":
			return "0"
		default:
			return ""
		}
	})
	if overrides.SessionTranscriptMode != session.TranscriptModeFull || overrides.SessionOutputMaxBytes != 0 {
		t.Fatalf("session overrides = mode %q bytes %d", overrides.SessionTranscriptMode, overrides.SessionOutputMaxBytes)
	}
	invalid := ConfigFromEnv(func(key string) string {
		switch key {
		case "SESSION_TRANSCRIPT_MODE":
			return "invalid"
		case "SESSION_OUTPUT_MAX_BYTES":
			return "-1"
		default:
			return ""
		}
	})
	if invalid.SessionTranscriptMode != session.TranscriptModeCompact || invalid.SessionOutputMaxBytes != session.DefaultOutputMaxBytes {
		t.Fatalf("invalid session config = mode %q bytes %d", invalid.SessionTranscriptMode, invalid.SessionOutputMaxBytes)
	}
}

func TestConfigFromEnvEnablesRunTrace(t *testing.T) {
	config := ConfigFromEnv(func(key string) string {
		if key == "RUN_TRACE_ENABLED" {
			return "true"
		}
		return ""
	})
	if !config.RunTraceEnabled {
		t.Fatal("RunTraceEnabled = false, want true")
	}
}

func TestConfigFromEnvFallsBackOnInvalidContextValues(t *testing.T) {
	config := ConfigFromEnv(func(key string) string {
		switch key {
		case "CONTEXT_MANAGEMENT_ENABLED":
			return "not-bool"
		case "AGENT_MAX_ITERATIONS":
			return "bad"
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
	if config.MaxIterations != agent.DefaultMaxIterations {
		t.Fatalf("MaxIterations = %d, want fallback %d", config.MaxIterations, agent.DefaultMaxIterations)
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

func TestConfigFromEnvUsesAgentMaxIterations(t *testing.T) {
	config := ConfigFromEnv(func(key string) string {
		if key == "AGENT_MAX_ITERATIONS" {
			return "200"
		}
		return ""
	})

	if config.MaxIterations != 200 {
		t.Fatalf("MaxIterations = %d, want 200", config.MaxIterations)
	}
}

// The iteration cap is a single canonical runaway safety valve (agent.DefaultMaxIterations),
// not a task limit: with auto-compaction the loop continues on a budget-bounded context,
// so the default is intentionally high and shared by every mode.
func TestDefaultMaxIterationsIsHighSafetyValve(t *testing.T) {
	if agent.DefaultMaxIterations != 1000 {
		t.Fatalf("agent.DefaultMaxIterations = %d, want 1000", agent.DefaultMaxIterations)
	}
}
