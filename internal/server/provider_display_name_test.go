package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/globalconfig"
	"bqagent/internal/modelcatalog"
)

func TestParseProviderModelsDisplayNames(t *testing.T) {
	models, err := parseProviderModels([]byte(`{"data":[{"id":" z ","display_name":" Zeta "},{"id":"a"},{"id":" ","display_name":"ignored"},{"id":"b","display_name":"  "}],"models":[{"id":"a","display_name":"Alpha"},{"id":"z","display_name":"duplicate"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 || models[0].ID != "a" || models[0].DisplayName != "Alpha" || models[1].ID != "b" || models[1].DisplayName != "" || models[2].ID != "z" || models[2].DisplayName != "Zeta" {
		t.Fatalf("catalog = %#v", models)
	}
	if _, err := parseProviderModels([]byte(`invalid`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

func TestProviderDisplayNamesPersistAndApply(t *testing.T) {
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), AgentDir: t.TempDir()})
	handler := NewHandler(HandlerOptions{Service: service})
	body := `{"active_provider":"p","providers":[{"id":"p","name":"P","api_type":"openai","models":["m"],"model_display_names":{"m":" Friendly Model ","unused":"Unused"},"default_model":"m"}]}`
	writer := httptest.NewRecorder()
	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodPut, "/api/v1/webui/providers", strings.NewReader(body)))
	if writer.Code != http.StatusOK {
		t.Fatalf("save: %d %s", writer.Code, writer.Body.String())
	}
	saved, err := globalconfig.NewStore(service.agentDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Providers[0].ModelDisplayNames.Name("m") != "Friendly Model" || len(saved.Providers[0].ModelDisplayNames) != 1 {
		t.Fatalf("saved = %#v", saved.Providers)
	}
	writer = httptest.NewRecorder()
	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/api/v1/webui/providers", nil))
	var response providerSettingsResponse
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Providers[0].ModelDisplayNames.Name("m") != "Friendly Model" {
		t.Fatalf("response = %#v", response)
	}
	if info := service.RuntimeLLMInfo(); info.Model != "m" || info.ModelDisplayName != "Friendly Model" {
		t.Fatalf("runtime = %#v", info)
	}
}

func TestModelDisplayNamesKeepRequestIDs(t *testing.T) {
	client := &modelRecordingClient{}
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), AgentDir: t.TempDir(), Client: client, Model: "default-id", Models: []string{"default-id", "selected-id"}, ModelDisplayNames: modelcatalog.Names{"default-id": "Default Name", "selected-id": "Selected Name"}})
	switched, err := service.HandleTurn(context.Background(), TurnRequest{Message: "/model selected-id"})
	if err != nil {
		t.Fatal(err)
	}
	if switched.Reply != "已切换当前会话模型：Selected Name" {
		t.Fatal(switched.Reply)
	}
	info := service.RuntimeLLMInfoForSession(switched.SessionID)
	if info.Model != "selected-id" || info.ModelDisplayName != "Selected Name" {
		t.Fatalf("runtime = %#v", info)
	}
	listing, err := service.HandleTurn(context.Background(), TurnRequest{SessionID: switched.SessionID, Message: "/model"})
	if err != nil || !strings.Contains(listing.Reply, "- Selected Name") || strings.Contains(listing.Reply, "selected-id") {
		t.Fatalf("list = %#v, err=%v", listing, err)
	}
	if _, err := service.HandleTurn(context.Background(), TurnRequest{SessionID: switched.SessionID, Message: "hello"}); err != nil {
		t.Fatal(err)
	}
	if len(client.models) != 1 || client.models[0] != "selected-id" {
		t.Fatalf("request IDs = %#v", client.models)
	}
	options := service.ModelOptions()
	if options[2].ID != "selected-id" || options[2].DisplayName != "Selected Name" {
		t.Fatalf("options = %#v", options)
	}
	// Same model ID on a different provider must not inherit the previous name.
	service.ConfigureLLM("other", agent.APITypeOpenAI, "", "", "selected-id", []string{"selected-id"}, nil)
	if service.RuntimeLLMInfo().ModelDisplayName != "selected-id" {
		t.Fatal("stale display name")
	}
}

type modelCatalogTransport func(*http.Request) (*http.Response, error)

func (transport modelCatalogTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestFetchProviderModelsDisplayNames(t *testing.T) {
	originalClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = originalClient })
	for _, apiType := range []string{"openai", "anthropic"} {
		t.Run(apiType, func(t *testing.T) {
			http.DefaultClient = &http.Client{Transport: modelCatalogTransport(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path != "/v1/models" || request.Method != http.MethodGet {
					t.Fatalf("request = %s %s", request.Method, request.URL)
				}
				if apiType == "anthropic" {
					if request.Header.Get("x-api-key") != "test-key" {
						t.Fatal("missing Anthropic key")
					}
				} else if request.Header.Get("Authorization") != "Bearer test-key" {
					t.Fatal("missing OpenAI key")
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"model-id","display_name":"Friendly Model"},{"id":"legacy"}]}`))}, nil
			})}
			models, err := FetchProviderModels(context.Background(), apiType, "https://provider.invalid/v1", "test-key")
			if err != nil || len(models) != 2 || models[1].ID != "model-id" || models[1].DisplayName != "Friendly Model" || models[0].DisplayName != "" {
				t.Fatalf("models = %#v, err=%v", models, err)
			}
		})
	}
}
