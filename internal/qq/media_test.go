package qq

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchImageDownloads(t *testing.T) {
	gifBytes := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00\x3b")
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		_, _ = writer.Write(gifBytes)
	}))
	defer server.Close()

	client := NewClient(nil, "", server.Client())
	data, mimeType, err := client.FetchImage(context.Background(), InboundImage{URL: server.URL + "/pic.gif"})
	if err != nil {
		t.Fatalf("FetchImage returned error: %v", err)
	}
	if gotPath != "/pic.gif" {
		t.Fatalf("download path = %q", gotPath)
	}
	if mimeType != "image/gif" {
		t.Fatalf("mimeType = %q, want image/gif", mimeType)
	}
	if !bytes.Equal(data, gifBytes) {
		t.Fatalf("data mismatch")
	}
}

func TestFetchImageRequiresURL(t *testing.T) {
	client := NewClient(nil, "", nil)
	if _, _, err := client.FetchImage(context.Background(), InboundImage{}); err == nil {
		t.Fatal("FetchImage() error = nil, want error for missing url")
	}
}
