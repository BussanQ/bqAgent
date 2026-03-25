package server

import "testing"

func TestSanitizeChannelReplyRemovesThinkBlocks(t *testing.T) {
	got := sanitizeChannelReply("<think>internal reasoning</think>\nfinal answer")
	if got != "final answer" {
		t.Fatalf("sanitizeChannelReply = %q, want %q", got, "final answer")
	}
}

func TestSanitizeChannelReplyRemovesMultipleThinkBlocks(t *testing.T) {
	got := sanitizeChannelReply("before\n<think>a</think>\nmiddle\n<think>b</think>\nafter")
	if got != "before\n\nmiddle\n\nafter" {
		t.Fatalf("sanitizeChannelReply = %q, want %q", got, "before\n\nmiddle\n\nafter")
	}
}
