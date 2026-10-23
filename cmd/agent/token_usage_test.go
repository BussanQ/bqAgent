package main

import (
	"bytes"
	"testing"

	"bqagent/internal/agent"
)

func TestWriteTurnTokenUsage(t *testing.T) {
	for _, test := range []struct {
		name  string
		usage agent.LiveTokenUsage
		want  string
	}{
		{"no calls", agent.LiveTokenUsage{}, ""},
		{"exact including subsets", agent.LiveTokenUsage{InputTokens: 220, OutputTokens: 58, Calls: 2, CachedInputTokens: 80, CacheWriteTokens: 20, CacheUsageAvailable: true, ReasoningTokens: 30}, "[Tokens] input=220 output=58 total=278 calls=2 cache_read=80 cache_write=20 reasoning=30\n"},
		{"estimated output", agent.LiveTokenUsage{InputTokens: 100, OutputTokens: 8, OutputEstimated: true, Calls: 1}, "[Tokens] input=100 output=~8 total=~108 calls=1 (~ = estimated)\n"},
		{"estimated input", agent.LiveTokenUsage{InputTokens: 100, OutputTokens: 8, InputEstimated: true, Calls: 1}, "[Tokens] input=~100 output=8 total=~108 calls=1 (~ = estimated)\n"},
		{"reported zero", agent.LiveTokenUsage{Calls: 1, CacheUsageAvailable: true}, "[Tokens] input=0 output=0 total=0 calls=1 cache_read=0 cache_write=0\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			writeTurnTokenUsage(&output, test.usage)
			if output.String() != test.want {
				t.Fatalf("output = %q, want %q", output.String(), test.want)
			}
		})
	}
}
