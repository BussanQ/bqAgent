package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestToolPreviewBoundsPhysicalRows(t *testing.T) {
	for _, test := range []struct {
		name, value string
		width       int
		clipped     bool
	}{
		{name: "three lines", value: "first\nsecond\nthird", width: 20},
		{name: "CRLF", value: "first\r\nsecond\r\nthird\r\nfourth", width: 20, clipped: true},
		{name: "long Chinese line", value: strings.Repeat("工具错误", 30), width: 16, clipped: true},
		{name: "ANSI and tabs", value: "\x1b[31mfirst\tsecond\x1b[0m\nthird\tfourth\nfifth\tsixth\nlast", width: 12, clipped: true},
		{name: "carriage return", value: "first\rsecond\rthird\rlast", width: 20, clipped: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			preview, clipped := boundedPreview(test.value, test.width)
			if clipped != test.clipped {
				t.Fatalf("clipped = %v, want %v: %q", clipped, test.clipped, preview)
			}
			lines := strings.Split(preview, "\n")
			if len(lines) > 3 {
				t.Fatalf("preview exceeded three physical rows: %q", preview)
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > test.width {
					t.Fatalf("preview will wrap beyond its row budget: width=%d line=%q", test.width, line)
				}
			}
			if strings.ContainsAny(preview, "\x1b\r\t") {
				t.Fatalf("preview retained terminal controls: %q", preview)
			}
			if clipped && (len(lines) != 3 || !strings.HasSuffix(lines[2], "…")) {
				t.Fatalf("truncation marker must fit on the third row: %q", preview)
			}
			if !clipped && preview != test.value {
				t.Fatalf("short output changed: %q", preview)
			}
		})
	}
}

func TestFailedToolSummaryLimitsOutputAndColorsOnlyMarker(t *testing.T) {
	styles := newTheme(true)
	// Transform marks the error style even when the test terminal disables ANSI.
	styles.error = lipgloss.NewStyle().Transform(func(value string) string { return "ERROR[" + value + "]" })
	for _, status := range []string{"failed", "error"} {
		t.Run(status, func(t *testing.T) {
			event := ToolEvent{Kind: "tool_result", Seq: 1, Name: "bash", Status: status,
				Preview: "\x1b[31m" + strings.Repeat("长错误输出", 20) + "\x1b[0m\r\nERROR_TAIL"}
			rendered := toolSummary(event, styles, 30, false)
			header, preview, ok := strings.Cut(rendered, "\n")
			if !ok || !strings.Contains(header, "ERROR[✗]") || !strings.Contains(header, "输出已截断") {
				t.Fatalf("failure marker or truncation status missing: %q", rendered)
			}
			if strings.Contains(preview, "ERROR[") || strings.Contains(preview, "ERROR_TAIL") || strings.Contains(preview, "\x1b") {
				t.Fatalf("failure output was colored or not truncated: %q", preview)
			}
			lines := strings.Split(preview, "\n")
			if len(lines) > 3 {
				t.Fatalf("failed summary exceeded three output rows: %q", preview)
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > 30 {
					t.Fatalf("preview indentation causes an extra wrap: %q", line)
				}
			}
		})
	}
}

func TestToolGroupPreviewAccountsForBothIndents(t *testing.T) {
	for _, width := range []int{12, 24, 37} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			group := toolGroup{}
			group.add(ToolEvent{Kind: "tool_result", ID: "one", Name: "bash", Status: "failed", Preview: strings.Repeat("中文错误", 100)})
			rendered := group.render(newTheme(true), true, width)
			lines := strings.Split(rendered, "\n")
			// The group heading and tool summary precede the output rows.
			if len(lines) != 5 {
				t.Fatalf("expected three output rows after two headers: %q", rendered)
			}
			for _, line := range lines[2:] {
				if !strings.HasPrefix(line, "    ") || ansi.StringWidth(line) > width {
					t.Fatalf("group and preview indentation exceeded terminal width %d: %q", width, line)
				}
			}
		})
	}
}

func TestSingleToolPreviewToggleRetainsOriginalOutput(t *testing.T) {
	model := toolDrawerModel(t, 1)
	const original = "错误首行\n第二行\n第三行\n第四行\n完整错误尾部"
	model.tools.add(ToolEvent{Kind: "tool_result", ID: "call-1", Name: "read_file", Status: "failed", Preview: original})
	compact := model.View().Content
	if strings.Contains(compact, "完整错误尾部") || !strings.Contains(compact, "输出已截断") || !strings.Contains(compact, "Ctrl+O") {
		t.Fatalf("single tool did not expose a bounded, expandable preview: %q", compact)
	}
	for i := 0; i < 4; i++ {
		updated, command := model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
		model = updated.(Model)
		if command != nil || len(model.printQueue) != 0 {
			t.Fatal("toggling a preview must not print duplicate history")
		}
		expanded := i%2 == 0
		if model.tools.expanded != expanded || strings.Contains(model.View().Content, "完整错误尾部") != expanded {
			t.Fatalf("single tool preview toggle did not change the visible output: %q", model.View().Content)
		}
		if !expanded && model.View().Content != compact {
			t.Fatal("collapse did not restore the original compact preview")
		}
		if model.tools.items[0].result.Preview != original {
			t.Fatal("display truncation changed stored tool output")
		}
		assertLiveFrameFits(t, model)
	}
}

func TestToolHistoryAndArchiveKeepBoundedPreviews(t *testing.T) {
	const preview = "错误首行\n第二行\n第三行\n第四行\n完整错误尾部"
	for _, count := range []int{1, toolCollapseThreshold + 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			model := toolDrawerModel(t, count)
			tool := ToolEvent{Kind: "tool_result", ID: "call-1", Name: "read_file", Status: "failed", Preview: preview}
			model.tools.add(tool)
			model.tools.expanded = true
			model.archiveToolGroup()
			model.archiveToolGroup()
			printed := strings.Join(model.printQueue, "\n")
			if strings.Count(printed, "工具活动") != 1 || !strings.Contains(printed, "错误首行") || strings.Contains(printed, "完整错误尾部") {
				t.Fatalf("archiving expanded tools duplicated or exposed full output: %q", printed)
			}
			if !strings.Contains(printed, fmt.Sprintf("file-%03d.go", count)) {
				t.Fatal("archiving an expanded group lost the existing tool list")
			}
			if model.tools.items[0].result.Preview != preview {
				t.Fatal("archiving truncated stored output")
			}
			history := History{ID: "history", Messages: []HistoryMessage{{Role: "assistant", Tools: []ToolEvent{tool}}}}
			restored := strings.Join(model.renderHistory(history), "\n")
			if !strings.Contains(restored, "错误首行") || !strings.Contains(restored, "输出已截断") || strings.Contains(restored, "完整错误尾部") {
				t.Fatalf("restoring history bypassed preview truncation: %q", restored)
			}
		})
	}
}
