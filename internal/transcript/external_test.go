package transcript

import (
	"reflect"
	"testing"
)

func TestFoldExternalRecordsKeepsFirstObservationAndUserOffsets(t *testing.T) {
	patch := func(kind, id, text string, appendText bool) map[string]any {
		return map[string]any{"role": "assistant", "name": "pi", ExternalPatchKey: map[string]any{
			"segment": "seg", "kind": kind, "id": id, "text": text, "append": appendText,
		}}
	}
	records := []map[string]any{
		{"role": "user", "content": "question"}, patch("text", "a", "I'll ", true), patch("tool", "read", "", false),
		patch("text", "b", "waiting", true), patch("text", "a", "inspect", true),
		{"role": "user", "content": "correction"},
	}
	records[2][ExternalToolsKey] = []any{map[string]any{"id": "read", "status": "running"}}
	var first Records
	for i, m := range records {
		first.Add(m, int64(i*10))
	}
	rows, offsets := first.Messages()
	if len(rows) != 3 || !reflect.DeepEqual(offsets, []int64{0, 10, 50}) || rows[1]["content"] != "I'll inspect\n\nwaiting" {
		t.Fatal(rows, offsets)
	}
	h, ok := ReadExternalHistory(rows[1][ExternalHistoryKey])
	if !ok || !reflect.DeepEqual(h.Parts, []Part{{Kind: "text", ID: "a", Text: "I'll inspect"}, {Kind: "tool", ID: "read"}, {Kind: "text", ID: "b", Text: "waiting"}}) {
		t.Fatal(h)
	}
	commit := func(text string) map[string]any {
		return map[string]any{"role": "assistant", "name": "pi", "content": text, ExternalHistoryKey: map[string]any{"id": "seg", "parts": []any{map[string]any{"kind": "text", "id": "a", "text": text}}}}
	}
	records = append(records, commit("completed"), commit("native revision"))
	var revised Records
	for _, m := range records {
		revised.Add(m, -1)
	}
	rows, _ = revised.Messages()
	if len(rows) != 3 || rows[1]["content"] != "native revision" || rows[2]["role"] != "user" {
		t.Fatal(rows)
	}
}
