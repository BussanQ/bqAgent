package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServerChanChannelStopAcceptingTurnsRejectsNewRequests(t *testing.T) {
	channel := &ServerChanChannel{}
	channel.StopAcceptingTurns()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/serverchan/chat", nil)
	response := httptest.NewRecorder()
	channel.handleChat(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}
