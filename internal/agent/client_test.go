package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"bqagent/internal/tools"
)

func TestTokenUsageTracksPromptCache(t *testing.T) {
	var chatUsage chatCompletionUsage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"prompt_tokens_details":{"cached_tokens":75}}`), &chatUsage); err != nil {
		t.Fatal(err)
	}
	chat := chatUsage.tokenUsage()
	if !chat.CacheUsageAvailable || chat.PromptTokens != 100 || chat.CachedPromptTokens != 75 {
		t.Fatalf("chat usage = %#v", chat)
	}

	var responseUsage openAIResponseUsage
	if err := json.Unmarshal([]byte(`{"input_tokens":80,"output_tokens":10,"total_tokens":90,"input_tokens_details":{"cached_tokens":60}}`), &responseUsage); err != nil {
		t.Fatal(err)
	}
	response := tokenUsageFromOpenAIResponse(responseUsage)
	if !response.CacheUsageAvailable || response.PromptTokens != 80 || response.CachedPromptTokens != 60 {
		t.Fatalf("responses usage = %#v", response)
	}

	var claudeUsage anthropicUsage
	if err := json.Unmarshal([]byte(`{"input_tokens":10,"cache_creation_input_tokens":20,"cache_read_input_tokens":70,"output_tokens":5}`), &claudeUsage); err != nil {
		t.Fatal(err)
	}
	claude := claudeUsage.tokenUsage()
	if !claude.CacheUsageAvailable || claude.PromptTokens != 100 || claude.CachedPromptTokens != 70 || claude.TotalTokens != 105 {
		t.Fatalf("anthropic usage = %#v", claude)
	}
}

func TestClientCreateChatCompletionUsesOpenAICompatibleRequest(t *testing.T) {
	t.Helper()

	var seenPath string
	var seenAuth string
	var seenRequest struct {
		Model    string             `json:"model"`
		Messages []map[string]any   `json:"messages"`
		Tools    []tools.Definition `json:"tools"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seenPath = request.URL.Path
		seenAuth = request.Header.Get("Authorization")
		if err := json.NewDecoder(request.Body).Decode(&seenRequest); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}],"usage":{"prompt_tokens":5,"completion_tokens":4,"total_tokens":9,"completion_tokens_details":{"reasoning_tokens":2}}}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, server.Client())
	message, err := client.CreateChatCompletion(
		context.Background(),
		testModel,
		[]map[string]any{{"role": "user", "content": "hello"}},
		tools.Definitions(),
	)
	if err != nil {
		t.Fatalf("CreateChatCompletion returned error: %v", err)
	}
	if seenPath != "/chat/completions" {
		t.Fatalf("request path = %q, want %q", seenPath, "/chat/completions")
	}
	if seenAuth != "Bearer test-key" {
		t.Fatalf("authorization header = %q, want bearer token", seenAuth)
	}
	if seenRequest.Model != testModel {
		t.Fatalf("model = %q, want %q", seenRequest.Model, testModel)
	}
	if len(seenRequest.Messages) != 1 {
		t.Fatalf("messages length = %d, want 1", len(seenRequest.Messages))
	}
	if len(seenRequest.Tools) != 12 {
		t.Fatalf("tools length = %d, want 12", len(seenRequest.Tools))
	}
	if message.FinalContent() != "done" {
		t.Fatalf("final content = %q, want %q", message.FinalContent(), "done")
	}
	if message.Usage.CompletionTokens != 4 || message.Usage.ReasoningTokens != 2 {
		t.Fatalf("usage = %#v", message.Usage)
	}
}

func TestClientCreateChatCompletionParsesInlineToolCallContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"<think>search</think>\n<tool_call>\n{\"name\":\"web_search\",\"parameters\":{\"query\":\"今天天气\"}}\n</tool_call>"}}]}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, server.Client())
	message, err := client.CreateChatCompletion(
		context.Background(),
		testModel,
		[]map[string]any{{"role": "user", "content": "hello"}},
		tools.Definitions(),
	)
	if err != nil {
		t.Fatalf("CreateChatCompletion returned error: %v", err)
	}
	if len(message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(message.ToolCalls))
	}
	if message.ToolCalls[0].Function.Name != "web_search" {
		t.Fatalf("tool name = %q, want %q", message.ToolCalls[0].Function.Name, "web_search")
	}
	if message.ToolCalls[0].Function.Arguments != `{"query":"今天天气"}` {
		t.Fatalf("tool arguments = %q, want JSON parameters", message.ToolCalls[0].Function.Arguments)
	}
}

func TestAssistantMessageRequestMessageStripsInlineToolCallMarkup(t *testing.T) {
	message := AssistantMessage{
		Role:    "assistant",
		Content: "<think>search</think>\n<tool_call>\n{\"name\":\"web_search\",\"parameters\":{\"query\":\"today\"}}\n</tool_call>",
		ToolCalls: []ToolCall{
			{
				ID:   "inline-tool-1",
				Type: "function",
				Function: FunctionCall{
					Name:      "web_search",
					Arguments: `{"query":"today"}`,
				},
			},
		},
	}

	request := message.RequestMessage()
	if content := request["content"]; content != nil {
		t.Fatalf("content = %#v, want nil for assistant tool call message", content)
	}
	if _, ok := request["tool_calls"]; !ok {
		t.Fatal("request missing tool_calls")
	}
}

func TestClientCreateChatCompletionParsesShorthandInlineToolCallContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"<tool_call>web_search search=\"IT科技新闻 今日 最新\"</tool_call>"}}]}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, server.Client())
	message, err := client.CreateChatCompletion(
		context.Background(),
		testModel,
		[]map[string]any{{"role": "user", "content": "hello"}},
		tools.Definitions(),
	)
	if err != nil {
		t.Fatalf("CreateChatCompletion returned error: %v", err)
	}
	if len(message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(message.ToolCalls))
	}
	if message.ToolCalls[0].Function.Name != "web_search" {
		t.Fatalf("tool name = %q, want %q", message.ToolCalls[0].Function.Name, "web_search")
	}
	if message.ToolCalls[0].Function.Arguments != `{"search":"IT科技新闻 今日 最新"}` {
		t.Fatalf("tool arguments = %q, want shorthand args json", message.ToolCalls[0].Function.Arguments)
	}
}

type failingStreamClient struct{}

func (f *failingStreamClient) CreateChatCompletion(_ context.Context, _ string, _ []map[string]any, _ []tools.Definition) (AssistantMessage, error) {
	return AssistantMessage{}, nil
}

func (f *failingStreamClient) CreateChatCompletionStream(_ context.Context, _ string, _ []map[string]any, _ []tools.Definition, _ func(string)) (AssistantMessage, error) {
	return AssistantMessage{}, errors.New("stream failed")
}

func TestInstrumentedClientLogsChatCompletionTiming(t *testing.T) {
	client := &stubClient{
		responses: []AssistantMessage{{Content: "done"}},
	}
	var logs bytes.Buffer

	wrapped := instrumentChatCompletionClient(client, &logs, nil)
	_, err := wrapped.CreateChatCompletion(context.Background(), testModel, []map[string]any{{"role": "user", "content": "hello"}}, nil)
	if err != nil {
		t.Fatalf("CreateChatCompletion returned error: %v", err)
	}

	content := logs.String()
	if !strings.Contains(content, "[Model] request=chat") {
		t.Fatalf("logs = %q, want model request log", content)
	}
	if !strings.Contains(content, "model="+testModel) {
		t.Fatalf("logs = %q, want model name", content)
	}
	if !strings.Contains(content, "stream=false") {
		t.Fatalf("logs = %q, want non-stream log", content)
	}
	if !strings.Contains(content, "duration=") {
		t.Fatalf("logs = %q, want duration", content)
	}
	if !strings.Contains(content, "status=success") {
		t.Fatalf("logs = %q, want success status", content)
	}
}

func TestInstrumentedClientLogsStreamErrors(t *testing.T) {
	var logs bytes.Buffer

	wrapped := instrumentChatCompletionClient(&failingStreamClient{}, &logs, nil)
	_, err := wrapped.CreateChatCompletionStream(context.Background(), testModel, []map[string]any{{"role": "user", "content": "hello"}}, nil, nil)
	if err == nil {
		t.Fatal("CreateChatCompletionStream returned nil error, want stream failure")
	}

	content := logs.String()
	if !strings.Contains(content, "[Model] request=chat") {
		t.Fatalf("logs = %q, want model request log", content)
	}
	if !strings.Contains(content, "stream=true") {
		t.Fatalf("logs = %q, want stream log", content)
	}
	if !strings.Contains(content, "status=error") {
		t.Fatalf("logs = %q, want error status", content)
	}
	if !strings.Contains(content, `error="stream failed"`) {
		t.Fatalf("logs = %q, want stream failure detail", content)
	}
}

type manualModelProgressTicker struct {
	ch chan time.Time
}

func (t *manualModelProgressTicker) C() <-chan time.Time {
	return t.ch
}

func (t *manualModelProgressTicker) Stop() {}

type fakeModelProgressClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeModelProgressClock() *fakeModelProgressClock {
	return &fakeModelProgressClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *fakeModelProgressClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeModelProgressClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func installModelProgressTestHooks(t *testing.T) (*manualModelProgressTicker, *fakeModelProgressClock) {
	t.Helper()
	originalInterval := modelProgressInterval
	originalMessages := modelProgressMessages
	originalTicker := newModelProgressTicker
	originalNow := modelProgressNow
	originalSelector := selectModelProgressMessage

	ticker := &manualModelProgressTicker{ch: make(chan time.Time)}
	clock := newFakeModelProgressClock()
	modelProgressInterval = time.Second
	modelProgressMessages = []string{"Still working... test status."}
	newModelProgressTicker = func(time.Duration) modelProgressTicker { return ticker }
	modelProgressNow = clock.Now
	selectModelProgressMessage = func([]string) string { return "Still working... test status." }

	t.Cleanup(func() {
		modelProgressInterval = originalInterval
		modelProgressMessages = originalMessages
		newModelProgressTicker = originalTicker
		modelProgressNow = originalNow
		selectModelProgressMessage = originalSelector
	})
	return ticker, clock
}

func fireModelProgressTick(t *testing.T, ticker *manualModelProgressTicker, clock *fakeModelProgressClock) {
	t.Helper()
	select {
	case ticker.ch <- clock.Now():
	case <-time.After(time.Second):
		t.Fatal("timed out sending model progress tick")
	}
}

func waitForLogContains(t *testing.T, logs *bytes.Buffer, expected string) {
	t.Helper()
	deadline := time.After(time.Second)
	check := time.NewTicker(time.Millisecond)
	defer check.Stop()
	for {
		if strings.Contains(logs.String(), expected) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("logs = %q, want %q", logs.String(), expected)
		case <-check.C:
		}
	}
}

type blockingModelClient struct {
	started       chan struct{}
	release       chan struct{}
	startedOnce   sync.Once
	response      AssistantMessage
	err           error
	streamChunks  []string
	optionsCalled bool
}

func newBlockingModelClient(response AssistantMessage, err error) *blockingModelClient {
	return &blockingModelClient{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		response: response,
		err:      err,
	}
}

func (c *blockingModelClient) CreateChatCompletion(ctx context.Context, _ string, _ []map[string]any, _ []tools.Definition) (AssistantMessage, error) {
	return c.wait(ctx)
}

func (c *blockingModelClient) CreateChatCompletionWithOptions(ctx context.Context, _ string, _ []map[string]any, _ []tools.Definition, _ ChatCompletionOptions) (AssistantMessage, error) {
	c.optionsCalled = true
	return c.wait(ctx)
}

func (c *blockingModelClient) CreateChatCompletionStream(ctx context.Context, _ string, _ []map[string]any, _ []tools.Definition, onChunk func(string)) (AssistantMessage, error) {
	for _, chunk := range c.streamChunks {
		if onChunk != nil {
			onChunk(chunk)
		}
	}
	return c.wait(ctx)
}

func (c *blockingModelClient) wait(ctx context.Context) (AssistantMessage, error) {
	c.startedOnce.Do(func() { close(c.started) })
	select {
	case <-ctx.Done():
		return AssistantMessage{}, ctx.Err()
	case <-c.release:
		return c.response, c.err
	}
}

func TestInstrumentedClientEmitsProgressWhileChatCompletionInProgress(t *testing.T) {
	ticker, clock := installModelProgressTestHooks(t)
	client := newBlockingModelClient(AssistantMessage{Content: "done"}, nil)
	var logs bytes.Buffer
	var progress bytes.Buffer
	wrapped := instrumentChatCompletionClient(client, &logs, &progress)

	resultCh := make(chan AssistantMessage, 1)
	errCh := make(chan error, 1)
	go func() {
		message, err := wrapped.CreateChatCompletion(context.Background(), testModel, []map[string]any{{"role": "user", "content": "hello"}}, nil)
		resultCh <- message
		errCh <- err
	}()

	<-client.started
	fireModelProgressTick(t, ticker, clock)
	waitForLogContains(t, &progress, "Still working... test status.")
	if strings.Contains(logs.String(), "Still working... test status.") {
		t.Fatalf("logs = %q, want progress outside model logs", logs.String())
	}

	close(client.release)
	message := <-resultCh
	if err := <-errCh; err != nil {
		t.Fatalf("CreateChatCompletion returned error: %v", err)
	}
	if message.FinalContent() != "done" {
		t.Fatalf("final content = %q, want done", message.FinalContent())
	}
	if content := logs.String(); !strings.Contains(content, "status=success") {
		t.Fatalf("logs = %q, want final success log", content)
	}
}

func TestInstrumentedClientStopsProgressReporterAfterChatCompletion(t *testing.T) {
	ticker, clock := installModelProgressTestHooks(t)
	client := newBlockingModelClient(AssistantMessage{Content: "done"}, nil)
	var logs bytes.Buffer
	var progress bytes.Buffer
	wrapped := instrumentChatCompletionClient(client, &logs, &progress)
	done := make(chan struct{})

	go func() {
		_, _ = wrapped.CreateChatCompletion(context.Background(), testModel, []map[string]any{{"role": "user", "content": "hello"}}, nil)
		close(done)
	}()

	<-client.started
	fireModelProgressTick(t, ticker, clock)
	close(client.release)
	<-done
	before := strings.Count(progress.String(), "Still working... test status.")
	select {
	case ticker.ch <- clock.Now():
		t.Fatal("progress ticker still has a receiver after request completed")
	case <-time.After(20 * time.Millisecond):
	}
	after := strings.Count(progress.String(), "Still working... test status.")
	if after != before {
		t.Fatalf("progress count after completion = %d, want %d", after, before)
	}
}

func TestInstrumentedClientPreservesErrorWhileEmittingProgress(t *testing.T) {
	ticker, clock := installModelProgressTestHooks(t)
	expectedErr := errors.New("model failed")
	client := newBlockingModelClient(AssistantMessage{}, expectedErr)
	var logs bytes.Buffer
	var progress bytes.Buffer
	wrapped := instrumentChatCompletionClient(client, &logs, &progress)
	errCh := make(chan error, 1)

	go func() {
		_, err := wrapped.CreateChatCompletion(context.Background(), testModel, []map[string]any{{"role": "user", "content": "hello"}}, nil)
		errCh <- err
	}()

	<-client.started
	fireModelProgressTick(t, ticker, clock)
	close(client.release)
	if err := <-errCh; !errors.Is(err, expectedErr) {
		t.Fatalf("CreateChatCompletion error = %v, want %v", err, expectedErr)
	}
	waitForLogContains(t, &progress, "Still working... test status.")
	if strings.Contains(logs.String(), "Still working... test status.") {
		t.Fatalf("logs = %q, want progress outside model logs", logs.String())
	}
	content := logs.String()
	if !strings.Contains(content, "status=error") {
		t.Fatalf("logs = %q, want final error log", content)
	}
}

func TestInstrumentedClientSuppressesProgressDuringActiveStreamingChunks(t *testing.T) {
	ticker, clock := installModelProgressTestHooks(t)
	client := newBlockingModelClient(AssistantMessage{Content: "done"}, nil)
	client.streamChunks = []string{"hello"}
	var logs bytes.Buffer
	var progress bytes.Buffer
	var chunks strings.Builder
	wrapped := instrumentChatCompletionClient(client, &logs, &progress)
	done := make(chan struct{})

	go func() {
		_, _ = wrapped.CreateChatCompletionStream(context.Background(), testModel, []map[string]any{{"role": "user", "content": "hello"}}, nil, func(chunk string) {
			chunks.WriteString(chunk)
		})
		close(done)
	}()

	<-client.started
	clock.Advance(500 * time.Millisecond)
	fireModelProgressTick(t, ticker, clock)
	if strings.Contains(progress.String(), "Still working... test status.") {
		t.Fatalf("progress = %q, want no progress while stream chunks are active", progress.String())
	}
	close(client.release)
	<-done
	if chunks.String() != "hello" {
		t.Fatalf("stream chunks = %q, want hello", chunks.String())
	}
}

func TestInstrumentedClientEmitsProgressWhenStreamingStalls(t *testing.T) {
	ticker, clock := installModelProgressTestHooks(t)
	client := newBlockingModelClient(AssistantMessage{Content: "done"}, nil)
	client.streamChunks = []string{"hello"}
	var logs bytes.Buffer
	var progress bytes.Buffer
	wrapped := instrumentChatCompletionClient(client, &logs, &progress)
	done := make(chan struct{})

	go func() {
		_, _ = wrapped.CreateChatCompletionStream(context.Background(), testModel, []map[string]any{{"role": "user", "content": "hello"}}, nil, nil)
		close(done)
	}()

	<-client.started
	clock.Advance(time.Second)
	fireModelProgressTick(t, ticker, clock)
	waitForLogContains(t, &progress, "Still working... test status.")
	if strings.Contains(logs.String(), "Still working... test status.") {
		t.Fatalf("logs = %q, want progress outside model logs", logs.String())
	}
	close(client.release)
	<-done
}

func TestInstrumentedClientEmitsProgressForChatCompletionWithOptions(t *testing.T) {
	ticker, clock := installModelProgressTestHooks(t)
	client := newBlockingModelClient(AssistantMessage{Content: `{"steps":["one"]}`}, nil)
	var logs bytes.Buffer
	var progress bytes.Buffer
	wrapped := instrumentChatCompletionClient(client, &logs, &progress)
	done := make(chan struct{})

	go func() {
		optionsClient, ok := wrapped.(chatCompletionOptionsClient)
		if !ok {
			t.Errorf("instrumented client does not implement chatCompletionOptionsClient")
			close(done)
			return
		}
		_, _ = optionsClient.CreateChatCompletionWithOptions(context.Background(), testModel, []map[string]any{{"role": "user", "content": "hello"}}, nil, ChatCompletionOptions{ResponseFormat: map[string]any{"type": "json_object"}})
		close(done)
	}()

	<-client.started
	fireModelProgressTick(t, ticker, clock)
	waitForLogContains(t, &progress, "Still working... test status.")
	if strings.Contains(logs.String(), "Still working... test status.") {
		t.Fatalf("logs = %q, want progress outside model logs", logs.String())
	}
	close(client.release)
	<-done
	if !client.optionsCalled {
		t.Fatal("CreateChatCompletionWithOptions was not called on inner client")
	}
}
