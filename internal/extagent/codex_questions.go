package extagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

type codexQuestion struct {
	ID, Header, Question string
	IsOther, IsSecret    bool
	Options              []struct {
		Label, Description string
		IsOther            bool
	}
}

type codexQuestionParams struct {
	ThreadID, TurnID, ItemID string
	IsBlocking               *bool
	Questions                []codexQuestion
}

type codexAnswer struct {
	Answers []string `json:"answers"`
}

func (p *codexQuestionParams) valid() bool {
	if p.ThreadID == "" || p.TurnID == "" || p.ItemID == "" || len(p.Questions) == 0 || len(p.Questions) > 64 {
		return false
	}
	ids := make(map[string]bool, len(p.Questions))
	count := 0
	for _, q := range p.Questions {
		if strings.TrimSpace(q.ID) == "" || ids[q.ID] || firstNonEmpty(q.Question, q.Header) == "" {
			return false
		}
		ids[q.ID] = true
		count += len(q.Options)
		if count > 256 {
			return false
		}
		for _, o := range q.Options {
			if !o.IsOther && strings.TrimSpace(o.Label) == "" {
				return false
			}
		}
	}
	return true
}

func (c *codexAppServerClient) startQuestions(e rpcEnvelope) {
	var p codexQuestionParams
	if len(e.Params) > 128<<10 || json.Unmarshal(e.Params, &p) != nil || !p.valid() {
		c.invalidPrompt(e.ID)
		return
	}
	c.startPrompt(e.ID, codexPrompt{
		thread: p.ThreadID, turn: p.TurnID, nonblocking: p.IsBlocking != nil && !*p.IsBlocking,
		fallback: map[string]any{"answers": map[string]codexAnswer{}},
		ask: func(ctx context.Context, handler codexPromptHandler) any {
			answers := p.collect(ctx, handler)
			if answers == nil {
				return nil
			}
			return map[string]any{"answers": answers}
		},
		settled: func(_ bool, run *appServerRun) {
			if run != nil && run.questionsSettled != nil && run.ctx.Err() == nil {
				run.questionsSettled()
			}
		},
	})
}

// Publish in native order, but allow each card to be answered independently.
// Codex owns a single RPC for the whole group. Cancelling any question
// withdraws its siblings and discards the partially collected answers.
func (p *codexQuestionParams) collect(parent context.Context, handler codexPromptHandler) map[string]codexAnswer {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	answers := make(map[string]codexAnswer, len(p.Questions))
	var mu sync.Mutex
	var workers sync.WaitGroup
	for _, q := range p.Questions {
		if ctx.Err() != nil {
			break
		}
		ready := make(chan struct{})
		signal := sync.OnceFunc(func() { close(ready) })
		params, choices := p.prompt(q)
		params.published = signal
		workers.Add(1)
		go func() {
			defer workers.Done()
			outcome := handler(ctx, params)
			signal()
			answer, ok := choices[outcome.OptionID]
			if outcome.OptionID == "submit" && params.Dialog.FreeText && outcome.Value != nil {
				answer, ok = *outcome.Value, strings.TrimSpace(*outcome.Value) != "" && len(*outcome.Value) <= 64<<10
			}
			if outcome.Outcome != "selected" || !ok {
				cancel()
				return
			}
			mu.Lock()
			answers[q.ID] = codexAnswer{Answers: []string{answer}}
			mu.Unlock()
		}()
		select {
		case <-ready:
		case <-ctx.Done():
		}
	}
	workers.Wait()
	if ctx.Err() != nil || len(answers) != len(p.Questions) {
		return nil
	}
	return answers
}

func (p *codexQuestionParams) prompt(q codexQuestion) (acpPermissionParams, map[string]string) {
	dialog := &AgentDialog{Method: "question", Title: firstNonEmpty(q.Question, q.Header), Header: q.Header,
		FreeText: q.IsOther || len(q.Options) == 0, Secret: q.IsSecret, NonBlocking: p.IsBlocking != nil && !*p.IsBlocking}
	params := acpPermissionParams{SessionID: p.ThreadID, Dialog: dialog}
	choices := make(map[string]string)
	for i, o := range q.Options {
		if o.IsOther {
			dialog.FreeText = true
			continue
		}
		id := fmt.Sprintf("choice-%d", i)
		params.Options = append(params.Options, ACPPermissionOption{OptionID: id, Name: o.Label, Description: o.Description})
		choices[id] = o.Label
	}
	if dialog.FreeText {
		params.Options = append(params.Options, ACPPermissionOption{OptionID: "submit", Name: "提交填写的回答"})
	}
	params.Options = append(params.Options, ACPPermissionOption{OptionID: "cancel", Name: "取消本组问题", Kind: "reject_once"})
	return params, choices
}
