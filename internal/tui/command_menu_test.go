package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSlashMenuIncludesAllCommandsAndEffortOnFirstPage(t *testing.T) {
	backend := &fakeBackend{commands: []Command{{Name: "/effort", Description: "努力程度", Arguments: []CommandArgument{{Value: "max"}}}, {Name: "/custom", Description: "动态命令"}}}
	model := NewModel(backend, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.input.setValue("/")
	model.refreshSuggestions()
	view := model.renderSuggestions(100)
	if !strings.Contains(view, "/effort") || !strings.Contains(view, "↑/↓ 滚动选择") {
		t.Fatalf("effort not discoverable: %q", view)
	}
	for _, name := range []string{"/help", "/clear", "/exit", "/provider", "/run", "/ask", "/model", "/effort", "/fast", "/skill", "/memory", "/feedback", "/agent", "/claude", "/codex", "/cursor", "/opencode", "/pi", "/default", "/stop", "/custom"} {
		count := 0
		for i, item := range model.suggestions {
			if item.value != name {
				continue
			}
			count++
			for model.suggested != i {
				model.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
			}
			view := model.renderSuggestions(100)
			if !strings.Contains(view, "› "+name) || strings.Count(view, "\n") != maxSuggestions {
				t.Fatalf("command %s not selectable in fixed height menu: %q", name, view)
			}
		}
		if count != 1 {
			t.Fatalf("command %s appears %d times", name, count)
		}
	}
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if model.suggested != 0 {
		t.Fatal("down should wrap to first command")
	}
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if model.input.value() != "/custom" || model.active != nil {
		t.Fatal("last command should be completable without submitting")
	}
}

func TestSkillArgumentsSupportFuzzySearch(t *testing.T) {
	arguments := []CommandArgument{
		{Value: "khazix-writer"},
		{Value: "eli5", Label: "简单解释"},
		{Value: "wechat-title"},
	}
	for _, test := range []struct {
		name, input string
		want        []string
	}{
		{name: "empty", input: "/skill ", want: []string{"khazix-writer", "eli5", "wechat-title"}},
		{name: "prefix", input: "/skill khaz", want: []string{"khazix-writer"}},
		{name: "substring", input: "/skill writer", want: []string{"khazix-writer"}},
		{name: "subsequence", input: "/skill kzw", want: []string{"khazix-writer"}},
		{name: "case insensitive", input: "/SKILL KZW", want: []string{"khazix-writer"}},
		{name: "unicode label", input: "/skill 简释", want: []string{"eli5"}},
		{name: "multiple matches", input: "/skill wt", want: []string{"khazix-writer", "wechat-title"}},
		{name: "wrong order", input: "/skill wzk"},
		{name: "no match", input: "/skill missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := NewModel(&fakeBackend{commands: []Command{{Name: "/skill", Arguments: arguments}}}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
			model.input.setValue(test.input)
			model.refreshSuggestions()
			if len(model.suggestions) != len(test.want) || model.panelOpen != (len(test.want) > 0) {
				t.Fatalf("suggestions = %+v, panelOpen = %v, want %v", model.suggestions, model.panelOpen, test.want)
			}
			for i, want := range test.want {
				if model.suggestions[i].value != "/skill "+want {
					t.Fatalf("suggestion %d = %q, want %q", i, model.suggestions[i].value, "/skill "+want)
				}
			}
			if len(test.want) == 1 {
				for _, key := range []rune{tea.KeyTab, tea.KeyEnter} {
					model.input.setValue(test.input)
					model.refreshSuggestions()
					model.handleKey(tea.KeyPressMsg{Code: key})
					if model.input.value() != "/skill "+test.want[0] || model.active != nil {
						t.Fatalf("completion = %q, active = %v", model.input.value(), model.active)
					}
				}
			}
		})
	}
}

func TestCommandArgumentsBeyondFirstPageRemainReachable(t *testing.T) {
	var skills []CommandArgument
	for i := 0; i < 12; i++ {
		skills = append(skills, CommandArgument{Value: fmt.Sprintf("skill-%02d", i)})
	}
	for _, test := range []struct{ input, last string }{{"/agent ", "/agent cleanup"}, {"/skill ", "/skill skill-11"}} {
		t.Run(test.input, func(t *testing.T) {
			model := NewModel(&fakeBackend{commands: []Command{{Name: "/skill", Arguments: skills}}}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
			model.input.setValue(test.input)
			model.refreshSuggestions()
			model.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
			if !strings.Contains(model.renderSuggestions(100), "› "+test.last) {
				t.Fatalf("last argument missing: %+v", model.suggestions)
			}
			model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			if model.input.value() != test.last || model.active != nil {
				t.Fatal("non-picker command should still complete before submission")
			}
		})
	}
}
