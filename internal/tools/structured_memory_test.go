package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appmemory "bqagent/internal/memory"
)

func TestStructuredMemoryAddRoutesByTarget(t *testing.T) {
	workspaceDir := t.TempDir()
	globalDir := t.TempDir()
	workspaceStore := appmemory.NewStore(workspaceDir)
	globalStore := appmemory.NewStore(globalDir)
	memory := StructuredMemory(workspaceStore, globalStore)

	workspaceResult, err := memory(context.Background(), map[string]any{
		"action": "add", "kind": "user_preference", "content": "用户在工作区",
	})
	if err != nil {
		t.Fatalf("workspace add: %v", err)
	}
	if !strings.Contains(workspaceResult, `"target": "workspace"`) || !strings.Contains(workspaceResult, workspaceStore.EntriesPath()) {
		t.Fatalf("workspace add result = %s", workspaceResult)
	}

	globalResult, err := memory(context.Background(), map[string]any{
		"action": "add", "kind": "user_preference", "content": "用户在北京", "target": "global",
	})
	if err != nil {
		t.Fatalf("global add: %v", err)
	}
	if !strings.Contains(globalResult, `"target": "global"`) || !strings.Contains(globalResult, globalStore.EntriesPath()) {
		t.Fatalf("global add result = %s", globalResult)
	}

	if _, err := os.Stat(filepath.Join(workspaceDir, "entries.jsonl")); err != nil {
		t.Fatalf("workspace entries missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(globalDir, "entries.jsonl")); err != nil {
		t.Fatalf("global entries missing: %v", err)
	}

	workspaceEntries, err := workspaceStore.Active()
	if err != nil || len(workspaceEntries) != 1 || workspaceEntries[0].Content != "用户在工作区" {
		t.Fatalf("workspace entries = %+v err %v", workspaceEntries, err)
	}
	globalEntries, err := globalStore.Active()
	if err != nil || len(globalEntries) != 1 || globalEntries[0].Content != "用户在北京" {
		t.Fatalf("global entries = %+v err %v", globalEntries, err)
	}
}

func TestStructuredMemorySearchHonorsGlobalTarget(t *testing.T) {
	workspaceStore := appmemory.NewStore(t.TempDir())
	globalStore := appmemory.NewStore(t.TempDir())
	if _, err := globalStore.Add(appmemory.KindUserPreference, "用户在北京", "", .8, "normal", nil); err != nil {
		t.Fatal(err)
	}
	memory := StructuredMemory(workspaceStore, globalStore)

	result, err := memory(context.Background(), map[string]any{"action": "search", "query": "北京", "target": "global"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var payload struct {
		Target  string             `json:"target"`
		Results []appmemory.Result `json:"results"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("decode %s: %v", result, err)
	}
	if payload.Target != "global" || len(payload.Results) != 1 || payload.Results[0].Entry.Content != "用户在北京" {
		t.Fatalf("search payload = %+v", payload)
	}

	empty, err := memory(context.Background(), map[string]any{"action": "search", "query": "北京"})
	if err != nil {
		t.Fatalf("workspace search: %v", err)
	}
	if !strings.Contains(empty, `"results": []`) && !strings.Contains(empty, `"results":[]`) {
		t.Fatalf("workspace search should be empty: %s", empty)
	}
}

func TestStructuredMemoryRejectsInvalidTarget(t *testing.T) {
	memory := StructuredMemory(appmemory.NewStore(t.TempDir()), appmemory.NewStore(t.TempDir()))
	_, err := memory(context.Background(), map[string]any{"action": "add", "kind": "lesson", "content": "x", "target": "project"})
	if err == nil || !strings.Contains(err.Error(), "workspace or global") {
		t.Fatalf("error = %v, want target validation", err)
	}
}

func TestStructuredMemoryGlobalUnavailable(t *testing.T) {
	memory := StructuredMemory(appmemory.NewStore(t.TempDir()), nil)
	_, err := memory(context.Background(), map[string]any{"action": "list", "target": "global"})
	if err == nil || !strings.Contains(err.Error(), "global memory store is unavailable") {
		t.Fatalf("error = %v, want unavailable global store", err)
	}
}
