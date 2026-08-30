package agent

import (
	"testing"
)

func TestParseCompletedToolActivityExtractsCallsAndResults(t *testing.T) {
	content := CompletedToolActivityLead + "\n" +
		assistantNoteLead + "先记下位置\n" +
		`Calls: [{"id":"call-1","type":"function","function":{"name":"memory","arguments":"{\"action\":\"add\",\"content\":\"用户在北京\",\"target\":\"global\"}"}}]` + "\n" +
		"Result call-1:\n" +
		`{"target":"global","path":"/Users/haomi/.agent/memory/entries.jsonl"}`

	note, tools, ok := ParseCompletedToolActivity(content)
	if !ok {
		t.Fatal("expected completed tool activity")
	}
	if note != "先记下位置" {
		t.Fatalf("note = %q", note)
	}
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
	if tools[0].ID != "call-1" || tools[0].Name != "memory" || tools[0].Status != "succeeded" {
		t.Fatalf("tool = %#v", tools[0])
	}
	if tools[0].Arguments["content"] != "用户在北京" || tools[0].Arguments["target"] != "global" {
		t.Fatalf("arguments = %#v", tools[0].Arguments)
	}
	if tools[0].Result != `{"target":"global","path":"/Users/haomi/.agent/memory/entries.jsonl"}` {
		t.Fatalf("result = %q", tools[0].Result)
	}
}

func TestParseCompletedToolActivityMarksFailedResults(t *testing.T) {
	content := CompletedToolActivityLead + "\n" +
		`Calls: [{"id":"bash-1","function":{"name":"execute_bash","arguments":"{\"command\":\"false\"}"}}]` + "\n" +
		"Result bash-1:\n" +
		"Error: exit status 1"

	_, tools, ok := ParseCompletedToolActivity(content)
	if !ok || len(tools) != 1 || tools[0].Status != "failed" {
		t.Fatalf("tools = %#v ok=%t", tools, ok)
	}
}

func TestParseCompletedToolActivityMatchesMultilineCallIDs(t *testing.T) {
	id := "call-1\nfc_abc"
	content := CompletedToolActivityLead + "\n" +
		`Calls: [{"id":"call-1\nfc_abc","function":{"name":"glob","arguments":"{\"pattern\":\"*\"}"}}]` + "\n" +
		"Result " + id + ":\n" +
		".DS_Store\nbuild.sh"

	_, tools, ok := ParseCompletedToolActivity(content)
	if !ok || len(tools) != 1 || tools[0].Name != "glob" || tools[0].Result != ".DS_Store\nbuild.sh" {
		t.Fatalf("tools = %#v ok=%t", tools, ok)
	}
}

func TestParseCompletedToolActivityIgnoresOrdinaryReplies(t *testing.T) {
	note, tools, ok := ParseCompletedToolActivity("已经写入全局记忆。")
	if ok || note != "已经写入全局记忆。" || tools != nil {
		t.Fatalf("note=%q tools=%#v ok=%t", note, tools, ok)
	}
}
