package tui

import (
	"context"
	"strings"
	"testing"

	"bqagent/internal/turn"

	tea "charm.land/bubbletea/v2"
)

type inputFakeBackend struct{ imageFakeBackend }

func (*inputFakeBackend) SupportsTurnInput() bool { return true }

func newInputModel(t *testing.T, backend Backend) Model {
	t.Helper()
	model := NewModel(backend, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.width = 80
	_ = model.submit("第一轮")
	return model
}

func TestTurnInputEnterSteersAndAltQFollowsUpIndependently(t *testing.T) {
	model := newInputModel(t, &inputFakeBackend{})
	if model.active.inputs == nil {
		t.Fatal("supported backend turn has no input queue")
	}
	model.input.setValue("改用方案B")
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	model.input.setValue("之后补测试")
	model.handleKey(tea.KeyPressMsg{Code: 'q', Mod: tea.ModAlt})
	if model.queued != nil || len(model.active.pending) != 2 {
		t.Fatalf("queued=%#v pending=%#v", model.queued, model.active.pending)
	}
	steering := model.active.inputs.TakeSteering()
	if len(steering) != 1 || steering[0].Text != "改用方案B" || steering[0].Kind != turn.Steering {
		t.Fatalf("steering = %#v", steering)
	}
	if rest := model.active.inputs.Close(); len(rest) != 1 || rest[0].Kind != turn.FollowUp || rest[0].Text != "之后补测试" {
		t.Fatalf("follow-up = %#v", rest)
	}
	if !strings.Contains(strings.Join(model.auxiliaryLines(), "\n"), "跟进") {
		t.Fatal("pending state not shown")
	}
}

func TestTurnInputFollowUpShortcutEncodings(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"alt_q", tea.KeyPressMsg{Code: 'q', Mod: tea.ModAlt}},
		{"alt_q_with_text", tea.KeyPressMsg{Code: 'q', Mod: tea.ModAlt, Text: "q"}},
		{"alt_q_with_base_code", tea.KeyPressMsg{Code: 'œ', BaseCode: 'q', Mod: tea.ModAlt, Text: "œ"}},
		{"ctrl_s", tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := newInputModel(t, &inputFakeBackend{})
			model.input.setValue("之后补测试")
			model.handleKey(tc.key)
			if model.input.value() != "" || len(model.active.pending) != 1 {
				t.Fatalf("draft=%q pending=%#v", model.input.value(), model.active.pending)
			}
			if inputs := model.active.inputs.TakeSteering(); len(inputs) != 0 {
				t.Fatalf("follow-up consumed as steering: %#v", inputs)
			}
			if inputs := model.active.inputs.Finish(); len(inputs) != 1 || inputs[0].Kind != turn.FollowUp || inputs[0].Text != "之后补测试" {
				t.Fatalf("follow-up=%#v", inputs)
			}
		})
	}
}

func TestTurnInputFollowUpShortcutPreservesOrdinaryTextAndIdleDraft(t *testing.T) {
	for _, text := range []string{"q", "œ"} {
		model := newInputModel(t, &inputFakeBackend{})
		model.handleKey(tea.KeyPressMsg{Code: []rune(text)[0], Text: text})
		if model.input.value() != text || len(model.active.pending) != 0 {
			t.Fatalf("ordinary text %q: draft=%q pending=%#v", text, model.input.value(), model.active.pending)
		}
	}
	for _, key := range []tea.KeyPressMsg{{Code: 'q', Mod: tea.ModAlt}, {Code: 's', Mod: tea.ModCtrl}} {
		model := NewModel(&inputFakeBackend{}, Config{Context: context.Background(), NoColor: true})
		model.input.setValue("保留草稿")
		commands := model.handleKey(key)
		if model.input.value() != "保留草稿" || model.active != nil || len(commands) != 0 {
			t.Fatalf("idle key %s: draft=%q active=%#v commands=%d", key, model.input.value(), model.active, len(commands))
		}
		if !strings.Contains(model.inputNotice, "Enter") {
			t.Fatalf("idle shortcut needs a send hint: %q", model.inputNotice)
		}
	}
}

func TestTurnInputUnsupportedBackendAndAttachmentsKeepLegacyQueue(t *testing.T) {
	model := newInputModel(t, &imageFakeBackend{})
	if model.active.inputs != nil {
		t.Fatal("unsupported backend got an input queue")
	}
	model.input.setValue("下一轮")
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.queued == nil || model.queued.value != "下一轮" {
		t.Fatalf("legacy queue lost input: %#v", model.queued)
	}

	model = newInputModel(t, &inputFakeBackend{})
	model.queue("带图 [图片 #1]", []Image{testImage("[图片 #1]", 8)})
	if model.steer("带图 [图片 #1]", []Image{testImage("[图片 #1]", 8)}, turn.Steering) || len(model.active.pending) != 0 {
		t.Fatal("image input must not be steered")
	}
	if model.steer("/model x", nil, turn.Steering) {
		t.Fatal("slash must not be steered")
	}
}

func TestTurnInputAppliedPrintsUserMessageWithoutAssistantProjection(t *testing.T) {
	model := newInputModel(t, &inputFakeBackend{})
	model.steer("请改一下", nil, turn.Steering)
	id := model.active.pending[0].id
	model.stream = "旧的流式内容"
	model.printQueue = nil
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "message", message: turn.Event{TurnID: "t", ItemID: id, Seq: 1, Kind: "input.applied", Phase: "steering", Text: "请改一下"}})
	if len(model.active.pending) != 0 || model.messages.TurnID != "" || len(model.messages.Items) != 0 {
		t.Fatalf("pending=%#v messages=%#v", model.active.pending, model.messages)
	}
	joined := strings.Join(model.printQueue, "\n")
	if model.stream != "" || !strings.Contains(joined, "旧的流式内容") || !strings.Contains(joined, "请改一下") || strings.Index(joined, "旧的流式内容") > strings.Index(joined, "请改一下") {
		t.Fatalf("print queue = %q stream=%q", joined, model.stream)
	}
}

func TestTurnInputCancelRestoresUnconsumedWithBoundaries(t *testing.T) {
	model := newInputModel(t, &inputFakeBackend{})
	model.steer("第一条", nil, turn.Steering)
	model.steer("第二条", nil, turn.FollowUp)
	model.queued = &queuedPrompt{value: "/model fast"}
	model.active.cancelled = true
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "done", err: context.Canceled})
	if got := model.input.value(); got != "第一条\n第二条\n/model fast" || model.queued != nil {
		t.Fatalf("restored = %q queued=%#v", got, model.queued)
	}
	if model.toolMouse {
		t.Fatal("mouse capture must be released after turn end")
	}
}

func TestTurnInputClosedQueueFallsBackToNextTurn(t *testing.T) {
	model := newInputModel(t, &inputFakeBackend{})
	model.active.inputs.Finish()
	model.input.setValue("太晚了")
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.queued == nil || model.queued.value != "太晚了" || len(model.active.pending) != 0 {
		t.Fatalf("late input lost: queued=%#v", model.queued)
	}
}

func TestTurnInputDeclineNoticeSurvivesLegacyQueue(t *testing.T) {
	model := newInputModel(t, &inputFakeBackend{})
	model.input.setValue("带图 [图片 #1]")
	if err := model.input.attachImage(testImage("", 8)); err != nil {
		t.Fatal(err)
	}
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.queued == nil || len(model.queued.images) != 1 || !strings.Contains(model.statusNotice, "图片暂不支持运行中插入") {
		t.Fatalf("queued=%#v notice=%q", model.queued, model.statusNotice)
	}

	model = newInputModel(t, &inputFakeBackend{})
	model.active.inputs.Finish()
	model.input.setValue("太晚了")
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.queued == nil || !strings.Contains(model.statusNotice, "即将结束") {
		t.Fatalf("notice = %q", model.statusNotice)
	}
}

func TestTurnInputCancelPresentsConsumedButDroppedAppliedEvents(t *testing.T) {
	model := newInputModel(t, &inputFakeBackend{})
	model.steer("已消费", nil, turn.Steering)
	model.steer("未消费", nil, turn.Steering)
	// The agent consumed the first input, but its input.applied event was dropped.
	taken := model.active.inputs.TakeSteering()
	model.active.inputs = &turn.InputQueue{}
	_ = model.active.inputs.Add(turn.Input{ID: model.active.pending[1].id, Kind: turn.Steering, Text: "未消费"})
	if len(taken) != 2 {
		t.Fatal("setup")
	}
	model.printQueue = nil
	model.active.cancelled = true
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "done", err: context.Canceled})
	joined := strings.Join(model.printQueue, "\n")
	if !strings.Contains(joined, "已消费") || strings.Contains(joined, "未消费") || model.input.value() != "未消费" {
		t.Fatalf("print=%q input=%q", joined, model.input.value())
	}
}

func TestTurnInputAppliedArchivesToolsBeforeUserMessage(t *testing.T) {
	model := newInputModel(t, &inputFakeBackend{})
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "tool", tool: ToolEvent{Kind: "tool_start", Seq: 1, ID: "a", Name: "read_file"}})
	model.steer("改方向", nil, turn.Steering)
	model.printQueue = nil
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "message", message: turn.Event{TurnID: "t", ItemID: model.active.pending[0].id, Seq: 1, Kind: "input.applied", Text: "改方向"}})
	joined := strings.Join(model.printQueue, "\n")
	tool, user := strings.Index(joined, "read_file"), strings.Index(joined, "改方向")
	if tool < 0 || user < 0 || tool > user || !strings.Contains(joined, "❯❯") || len(model.tools.items) != 0 {
		t.Fatalf("order tool=%d user=%d tools=%d print=%q", tool, user, len(model.tools.items), joined)
	}
}
