package tools

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestWebFetchReturnsMarkdownByDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write([]byte(`<!doctype html><html><head><title>Hello Page</title></head><body><h1>Hello</h1><p>World</p><ul><li>One</li></ul><a href="https://example.com/docs">Docs</a><script>ignored()</script></body></html>`))
	}))
	defer server.Close()

	result, err := WebFetchWithClient(server.Client(), true)(context.Background(), map[string]any{"url": server.URL})
	if err != nil {
		t.Fatalf("WebFetch returned error: %v", err)
	}
	if !strings.Contains(result, "Final-URL: "+server.URL) {
		t.Fatalf("WebFetch result = %q, want final URL", result)
	}
	if !strings.Contains(result, "Title: Hello Page") {
		t.Fatalf("WebFetch result = %q, want title", result)
	}
	if !strings.Contains(result, "# Hello") || !strings.Contains(result, "- One") {
		t.Fatalf("WebFetch result = %q, want markdown output", result)
	}
	if !strings.Contains(result, "[Docs](https://example.com/docs)") {
		t.Fatalf("WebFetch result = %q, want markdown link", result)
	}
	if strings.Contains(result, "ignored()") {
		t.Fatalf("WebFetch result = %q, should strip script content", result)
	}
}

func TestWebFetchReturnsTextWhenRequested(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write([]byte(`<!doctype html><html><body><h1>Hello</h1><p>World</p></body></html>`))
	}))
	defer server.Close()

	result, err := WebFetchWithClient(server.Client(), true)(context.Background(), map[string]any{"url": server.URL, "extract_mode": "text"})
	if err != nil {
		t.Fatalf("WebFetch returned error: %v", err)
	}
	if strings.Contains(result, "# Hello") {
		t.Fatalf("WebFetch result = %q, should not contain markdown heading", result)
	}
	if !strings.Contains(result, "Hello") || !strings.Contains(result, "World") {
		t.Fatalf("WebFetch result = %q, want text output", result)
	}
}

func TestWebFetchReturnsPlainText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write([]byte("hello from text"))
	}))
	defer server.Close()

	result, err := WebFetchWithClient(server.Client(), true)(context.Background(), map[string]any{"url": server.URL})
	if err != nil {
		t.Fatalf("WebFetch returned error: %v", err)
	}
	if !strings.Contains(result, "hello from text") {
		t.Fatalf("WebFetch result = %q, want plain text body", result)
	}
}

func TestWebFetchReturnsRedirectFinalURL(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = writer.Write([]byte(`<html><body><h1>Target</h1></body></html>`))
	}))
	defer final.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, final.URL, http.StatusFound)
	}))
	defer redirect.Close()

	result, err := WebFetchWithClient(redirect.Client(), true)(context.Background(), map[string]any{"url": redirect.URL})
	if err != nil {
		t.Fatalf("WebFetch returned error: %v", err)
	}
	if !strings.Contains(result, "URL: "+redirect.URL) {
		t.Fatalf("WebFetch result = %q, want original URL", result)
	}
	if !strings.Contains(result, "Final-URL: "+final.URL) {
		t.Fatalf("WebFetch result = %q, want redirected final URL", result)
	}
}

func TestWebFetchTruncatesContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write([]byte("abcdefghijklmnopqrstuvwxyz"))
	}))
	defer server.Close()

	result, err := WebFetchWithClient(server.Client(), true)(context.Background(), map[string]any{"url": server.URL, "max_chars": 10})
	if err != nil {
		t.Fatalf("WebFetch returned error: %v", err)
	}
	if !strings.Contains(result, "Truncated: true") {
		t.Fatalf("WebFetch result = %q, want truncation marker", result)
	}
	if !strings.Contains(result, "abcdefghij") {
		t.Fatalf("WebFetch result = %q, want truncated content", result)
	}
}

func TestWebFetchRejectsInvalidExtractMode(t *testing.T) {
	_, err := WebFetch(context.Background(), map[string]any{"url": "https://example.com", "extract_mode": "html"})
	if err == nil || !strings.Contains(err.Error(), "unsupported extract_mode") {
		t.Fatalf("WebFetch error = %v, want unsupported extract_mode", err)
	}
}

func TestWebFetchRejectsInvalidURL(t *testing.T) {
	_, err := WebFetch(context.Background(), map[string]any{"url": "://bad"})
	if err == nil || !strings.Contains(err.Error(), "invalid url") {
		t.Fatalf("WebFetch error = %v, want invalid url", err)
	}
}

func TestWebFetchRejectsUnsupportedScheme(t *testing.T) {
	_, err := WebFetch(context.Background(), map[string]any{"url": "file:///tmp/test.txt"})
	if err == nil || !strings.Contains(err.Error(), "unsupported url scheme") {
		t.Fatalf("WebFetch error = %v, want unsupported scheme", err)
	}
}

func TestIsBlockedIPAllowsOnlyPublicGlobalUnicast(t *testing.T) {
	for _, test := range []struct {
		address string
		blocked bool
	}{
		{"8.8.8.8", false},
		{"2606:4700:4700::1111", false},
		{"127.0.0.1", true},
		{"10.0.0.1", true},
		{"224.0.0.1", true},
		{"ff02::1", true},
		{"255.255.255.255", true},
		{"240.0.0.1", true},
		{"192.0.2.1", true},
		{"2001:db8::1", true},
	} {
		if got := isBlockedIP(netip.MustParseAddr(test.address)); got != test.blocked {
			t.Errorf("isBlockedIP(%s) = %v, want %v", test.address, got, test.blocked)
		}
	}
}

func TestWebFetchRejectsLocalhost(t *testing.T) {
	_, err := WebFetch(context.Background(), map[string]any{"url": "http://127.0.0.1/test"})
	if err == nil || !strings.Contains(err.Error(), "refusing to fetch private or local address") {
		t.Fatalf("WebFetch error = %v, want private address rejection", err)
	}
}

func TestWebFetchRejectsNonSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.Error(writer, "<html><body><h1>Bad Gateway</h1><p>nope</p></body></html>", http.StatusBadGateway)
	}))
	defer server.Close()

	_, err := WebFetchWithClient(server.Client(), true)(context.Background(), map[string]any{"url": server.URL})
	if err == nil || !strings.Contains(err.Error(), "502 Bad Gateway") || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("WebFetch error = %v, want status failure details", err)
	}
}

func TestWebFetchRejectsUnsupportedContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/octet-stream")
		_, _ = writer.Write([]byte("\x00\x01"))
	}))
	defer server.Close()

	_, err := WebFetchWithClient(server.Client(), true)(context.Background(), map[string]any{"url": server.URL})
	if err == nil || !strings.Contains(err.Error(), "unsupported content type") {
		t.Fatalf("WebFetch error = %v, want unsupported content type", err)
	}
}

func TestWebFetchRejectsOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write([]byte(strings.Repeat("a", maxFetchBodyBytes+1)))
	}))
	defer server.Close()

	_, err := WebFetchWithClient(server.Client(), true)(context.Background(), map[string]any{"url": server.URL})
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("exceeded %d bytes", maxFetchBodyBytes)) {
		t.Fatalf("WebFetch error = %v, want oversized body rejection", err)
	}
}

type testWebFetchResolver func(context.Context, string) ([]net.IPAddr, error)

func (resolver testWebFetchResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return resolver(ctx, host)
}

func TestWebFetchSecureModeDialsVerifiedIPsAndFollowsRedirects(t *testing.T) {
	var hosts []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hosts = append(hosts, request.Host)
		switch request.Host {
		case "redirect.test":
			http.Redirect(writer, request, "http://target.test/final", http.StatusFound)
		case "target.test":
			writer.Header().Set("Content-Type", "text/plain")
			_, _ = writer.Write([]byte("redirected content"))
		default:
			t.Errorf("unexpected Host header %q", request.Host)
		}
	}))
	defer server.Close()

	var mu sync.Mutex
	resolverCalls := map[string]int{}
	resolver := testWebFetchResolver(func(_ context.Context, host string) ([]net.IPAddr, error) {
		mu.Lock()
		defer mu.Unlock()
		resolverCalls[host]++
		switch host {
		case "redirect.test":
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("2606:4700:4700::1111")}}, nil
		case "target.test":
			return []net.IPAddr{{IP: net.ParseIP("8.8.4.4")}, {IP: net.ParseIP("2606:4700:4700::1001")}}, nil
		default:
			return nil, fmt.Errorf("unexpected DNS lookup for %q", host)
		}
	})
	var dialed []string
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, address)
		attempt := len(dialed)
		mu.Unlock()
		if attempt == 1 || attempt == 3 {
			return nil, errors.New("simulated dial failure")
		}
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, server.Listener.Addr().String())
	}

	result, err := webFetchWithOptions(webFetchOptions{resolver: resolver, dialContext: dial})(context.Background(), map[string]any{"url": "http://redirect.test/start"})
	if err != nil {
		t.Fatalf("WebFetch returned error: %v", err)
	}
	if !strings.Contains(result, "redirected content") || !strings.Contains(result, "Final-URL: http://target.test/final") {
		t.Fatalf("WebFetch result = %q, want redirected content and final URL", result)
	}
	if got, want := resolverCalls["redirect.test"], 1; got != want {
		t.Fatalf("redirect resolver calls = %d, want %d", got, want)
	}
	if got, want := resolverCalls["target.test"], 1; got != want {
		t.Fatalf("target resolver calls = %d, want %d", got, want)
	}
	if got, want := strings.Join(dialed, ","), "8.8.8.8:80,[2606:4700:4700::1111]:80,8.8.4.4:80,[2606:4700:4700::1001]:80"; got != want {
		t.Fatalf("dialed addresses = %q, want %q", got, want)
	}
	if got, want := strings.Join(hosts, ","), "redirect.test,target.test"; got != want {
		t.Fatalf("Host headers = %q, want %q", got, want)
	}
}

func TestWebFetchSecureModeDisablesProxyAndCustomTLSDialer(t *testing.T) {
	var serverName string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write([]byte("secure content"))
	}))
	server.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		serverName = hello.ServerName
		return nil, nil
	}}
	server.StartTLS()
	defer server.Close()

	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true
	proxyUsed := false
	transport.Proxy = func(*http.Request) (*url.URL, error) {
		proxyUsed = true
		return nil, errors.New("custom proxy must not be used")
	}
	tlsDialUsed := false
	transport.DialTLSContext = func(context.Context, string, string) (net.Conn, error) {
		tlsDialUsed = true
		return nil, errors.New("custom TLS dialer must not be used")
	}
	resolver := testWebFetchResolver(func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host != "api.example.test" {
			return nil, fmt.Errorf("unexpected DNS lookup for %q", host)
		}
		return []net.IPAddr{{IP: net.ParseIP("8.8.4.4")}}, nil
	})
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, server.Listener.Addr().String())
	}

	result, err := webFetchWithOptions(webFetchOptions{
		client:      &http.Client{Transport: transport},
		resolver:    resolver,
		dialContext: dial,
	})(context.Background(), map[string]any{"url": "https://api.example.test/data"})
	if err != nil {
		t.Fatalf("WebFetch returned error: %v", err)
	}
	if !strings.Contains(result, "secure content") {
		t.Fatalf("WebFetch result = %q, want secure content", result)
	}
	if proxyUsed || tlsDialUsed {
		t.Fatalf("unsafe transport hooks used: proxy=%v tlsDial=%v", proxyUsed, tlsDialUsed)
	}
	if serverName != "api.example.test" {
		t.Fatalf("TLS SNI = %q, want %q", serverName, "api.example.test")
	}
}
