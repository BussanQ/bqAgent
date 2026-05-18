package qq

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenClientGetAccessToken(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s", request.Method)
		}
		if request.URL.Path != "/app/getAppAccessToken" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("Content-Type = %q", request.Header.Get("Content-Type"))
		}
		var body tokenRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.AppID != "app-1" || body.ClientSecret != "secret-1" {
			t.Fatalf("body = %+v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"token-1","expires_in":"7200"}`))
	}))
	defer server.Close()

	client := NewTokenClient("app-1", "secret-1", server.URL, server.Client())
	token, err := client.GetAccessToken(context.Background())
	if err != nil {
		t.Fatalf("GetAccessToken() error = %v", err)
	}
	if token.Token != "token-1" {
		t.Fatalf("Token = %q", token.Token)
	}
	if time.Until(token.ExpiresAt) < time.Hour {
		t.Fatalf("ExpiresAt = %v, want at least an hour in future", token.ExpiresAt)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestCachedTokenSourceReusesToken(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"token-1","expires_in":7200}`))
	}))
	defer server.Close()

	source := NewCachedTokenSource(NewTokenClient("app-1", "secret-1", server.URL, server.Client()))
	first, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	second, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() second error = %v", err)
	}
	if first != "token-1" || second != "token-1" {
		t.Fatalf("tokens = %q, %q", first, second)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestTokenClientReturnsErrorForBadResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "bad", http.StatusUnauthorized)
	}))
	defer server.Close()

	client := NewTokenClient("app-1", "secret-1", server.URL, server.Client())
	if _, err := client.GetAccessToken(context.Background()); err == nil {
		t.Fatalf("GetAccessToken() error = nil, want error")
	}
}

func TestTokenClientReturnsErrorForMissingToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"expires_in":"7200"}`))
	}))
	defer server.Close()

	client := NewTokenClient("app-1", "secret-1", server.URL, server.Client())
	if _, err := client.GetAccessToken(context.Background()); err == nil {
		t.Fatalf("GetAccessToken() error = nil, want error")
	}
}
