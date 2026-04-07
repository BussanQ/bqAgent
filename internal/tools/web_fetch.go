package tools

import (
	"context"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultFetchTimeout = 20 * time.Second
	maxFetchBodyBytes   = 1 << 20

	extractModeMarkdown = "markdown"
	extractModeText     = "text"
)

var (
	htmlCommentPattern = regexp.MustCompile(`(?is)<!--.*?-->`)
	scriptPattern      = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	stylePattern       = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)
	tagPattern         = regexp.MustCompile(`(?s)<[^>]+>`)
	titlePattern       = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title>`)
	headingPattern     = regexp.MustCompile(`(?is)<h([1-6])\b[^>]*>(.*?)</h[1-6]>`)
	linkPattern        = regexp.MustCompile(`(?is)<a\b[^>]*href\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))[^>]*>(.*?)</a>`)
	listItemPattern    = regexp.MustCompile(`(?is)<li\b[^>]*>(.*?)</li>`)
)

type fetchedContent struct {
	Title   string
	Content string
}

type fetchResult struct {
	OriginalURL string
	FinalURL    string
	ContentType string
	Title       string
	Content     string
	Truncated   bool
}

func WebFetch(ctx context.Context, args map[string]any) (string, error) {
	return WebFetchWithClient(nil, false)(ctx, args)
}

func WebFetchWithClient(client *http.Client, allowPrivateHosts bool) Function {
	if client == nil {
		client = &http.Client{Timeout: defaultFetchTimeout}
	}

	baseTransport := client.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	transport, ok := baseTransport.(*http.Transport)
	if !ok {
		transport = http.DefaultTransport.(*http.Transport).Clone()
	} else {
		transport = transport.Clone()
	}
	if !allowPrivateHosts {
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			if err := validateHost(host, allowPrivateHosts); err != nil {
				return nil, err
			}
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, net.JoinHostPort(host, port))
		}
	}

	fetchClient := *client
	fetchClient.Transport = transport
	fetchClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return validateRequestURL(req.URL, allowPrivateHosts)
	}

	return func(ctx context.Context, args map[string]any) (string, error) {
		rawURL, err := requireString(args, "url")
		if err != nil {
			return "", err
		}
		extractMode, err := parseExtractMode(args)
		if err != nil {
			return "", err
		}
		maxChars, err := parseOptionalMaxChars(args)
		if err != nil {
			return "", err
		}

		parsedURL, err := url.Parse(strings.TrimSpace(rawURL))
		if err != nil {
			return "", fmt.Errorf("invalid url: %w", err)
		}
		if err := validateRequestURL(parsedURL, allowPrivateHosts); err != nil {
			return "", err
		}

		request, err := http.NewRequest(http.MethodGet, parsedURL.String(), nil)
		if err != nil {
			return "", fmt.Errorf("failed to create request: %w", err)
		}
		request.Header.Set("User-Agent", "bqagent-web-fetch/1.0")

		response, err := fetchClient.Do(request)
		if err != nil {
			return "", fmt.Errorf("web fetch request failed: %w", err)
		}
		defer response.Body.Close()

		mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if err != nil {
			mediaType = ""
		}

		if response.StatusCode < 200 || response.StatusCode >= 300 {
			payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			details := strings.TrimSpace(renderErrorPayload(mediaType, payload))
			if details == "" {
				return "", fmt.Errorf("web fetch failed: %s", response.Status)
			}
			return "", fmt.Errorf("web fetch failed: %s: %s", response.Status, details)
		}

		body, err := io.ReadAll(io.LimitReader(response.Body, maxFetchBodyBytes+1))
		if err != nil {
			return "", fmt.Errorf("failed to read response body: %w", err)
		}
		if len(body) > maxFetchBodyBytes {
			return "", fmt.Errorf("web fetch response exceeded %d bytes", maxFetchBodyBytes)
		}

		content, err := normalizeFetchedContent(mediaType, body, extractMode)
		if err != nil {
			return "", err
		}
		if content.Content == "" {
			return "", fmt.Errorf("web fetch returned no readable content")
		}

		result := fetchResult{
			OriginalURL: parsedURL.String(),
			FinalURL:    response.Request.URL.String(),
			ContentType: fallbackContentType(mediaType),
			Title:       content.Title,
			Content:     content.Content,
		}
		if maxChars > 0 {
			result.Content, result.Truncated = truncateText(result.Content, maxChars)
		}
		if result.Content == "" {
			return "", fmt.Errorf("web fetch returned no readable content")
		}

		return formatFetchResult(result), nil
	}
}

func validateRequestURL(parsedURL *url.URL, allowPrivateHosts bool) error {
	if parsedURL == nil {
		return fmt.Errorf("invalid url")
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("unsupported url scheme %q", parsedURL.Scheme)
	}
	if parsedURL.Hostname() == "" {
		return fmt.Errorf("url must include a host")
	}
	return validateHost(parsedURL.Hostname(), allowPrivateHosts)
}

func validateHost(host string, allowPrivateHosts bool) error {
	if allowPrivateHosts {
		return nil
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("refusing to fetch localhost addresses")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("refusing to fetch private or local address %q", host)
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("failed to resolve host %q: %w", host, err)
	}
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip.IP)
		if !ok {
			continue
		}
		if isBlockedIP(addr.Unmap()) {
			return fmt.Errorf("refusing to fetch private or local address %q", host)
		}
	}
	return nil
}

func isBlockedIP(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsUnspecified()
}

func parseExtractMode(args map[string]any) (string, error) {
	value, ok := args["extract_mode"]
	if !ok {
		return extractModeMarkdown, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", "extract_mode")
	}
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return extractModeMarkdown, nil
	}
	if text != extractModeMarkdown && text != extractModeText {
		return "", fmt.Errorf("unsupported extract_mode %q", text)
	}
	return text, nil
}

func parseOptionalMaxChars(args map[string]any) (int, error) {
	value, ok := args["max_chars"]
	if !ok {
		return 0, nil
	}
	switch typed := value.(type) {
	case int:
		if typed <= 0 {
			return 0, fmt.Errorf("argument %q must be greater than 0", "max_chars")
		}
		return typed, nil
	case int64:
		if typed <= 0 {
			return 0, fmt.Errorf("argument %q must be greater than 0", "max_chars")
		}
		return int(typed), nil
	case float64:
		if typed <= 0 || typed != float64(int(typed)) {
			return 0, fmt.Errorf("argument %q must be a positive integer", "max_chars")
		}
		return int(typed), nil
	case string:
		number, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil || number <= 0 {
			return 0, fmt.Errorf("argument %q must be a positive integer", "max_chars")
		}
		return number, nil
	default:
		return 0, fmt.Errorf("argument %q must be a positive integer", "max_chars")
	}
}

func normalizeFetchedContent(mediaType string, body []byte, extractMode string) (fetchedContent, error) {
	raw := strings.TrimSpace(string(body))
	switch mediaType {
	case "", "text/plain", "application/json", "application/xml", "text/xml":
		return fetchedContent{Content: raw}, nil
	case "text/html":
		return extractHTMLContent(raw, extractMode), nil
	default:
		if strings.HasPrefix(mediaType, "text/") {
			return fetchedContent{Content: raw}, nil
		}
		return fetchedContent{}, fmt.Errorf("unsupported content type %q", mediaType)
	}
}

func extractHTMLContent(source string, extractMode string) fetchedContent {
	title := extractHTMLTitle(source)
	if extractMode == extractModeText {
		return fetchedContent{Title: title, Content: extractHTMLText(source)}
	}
	markdown := extractHTMLMarkdown(source)
	if markdown != "" {
		return fetchedContent{Title: title, Content: markdown}
	}
	return fetchedContent{Title: title, Content: extractHTMLText(source)}
}

func renderErrorPayload(mediaType string, body []byte) string {
	content, err := normalizeFetchedContent(mediaType, body, extractModeText)
	if err == nil && content.Content != "" {
		return content.Content
	}
	return strings.TrimSpace(string(body))
}

func extractHTMLTitle(source string) string {
	match := titlePattern.FindStringSubmatch(source)
	if len(match) < 2 {
		return ""
	}
	return collapseInlineWhitespace(stripTags(match[1]))
}

func extractHTMLMarkdown(source string) string {
	source = sanitizeHTML(source)
	source = replaceAllStringSubmatchFunc(source, headingPattern, func(groups []string) string {
		level, err := strconv.Atoi(groups[1])
		if err != nil || level < 1 {
			level = 1
		}
		text := collapseInlineWhitespace(stripTags(groups[2]))
		if text == "" {
			return "\n\n"
		}
		return "\n\n" + strings.Repeat("#", level) + " " + text + "\n\n"
	})
	source = replaceAllStringSubmatchFunc(source, linkPattern, func(groups []string) string {
		href := firstNonEmpty(groups[1], groups[2], groups[3])
		href = strings.TrimSpace(html.UnescapeString(href))
		text := collapseInlineWhitespace(stripTags(groups[4]))
		if text == "" {
			return ""
		}
		if href == "" {
			return text
		}
		return "[" + text + "](" + href + ")"
	})
	source = replaceAllStringSubmatchFunc(source, listItemPattern, func(groups []string) string {
		text := collapseInlineWhitespace(stripTags(groups[1]))
		if text == "" {
			return "\n"
		}
		return "\n- " + text + "\n"
	})
	source = strings.ReplaceAll(source, "<br>", "\n")
	source = strings.ReplaceAll(source, "<br/>", "\n")
	source = strings.ReplaceAll(source, "<br />", "\n")
	for _, tag := range []string{"</p>", "</div>", "</section>", "</article>", "</main>", "</aside>", "</ul>", "</ol>", "</tr>"} {
		source = strings.ReplaceAll(source, tag, "\n\n")
	}
	source = tagPattern.ReplaceAllString(source, " ")
	source = html.UnescapeString(source)
	return normalizeMarkdown(source)
}

func extractHTMLText(source string) string {
	source = sanitizeHTML(source)
	source = strings.ReplaceAll(source, "<br>", "\n")
	source = strings.ReplaceAll(source, "<br/>", "\n")
	source = strings.ReplaceAll(source, "<br />", "\n")
	source = strings.ReplaceAll(source, "</p>", "\n\n")
	source = strings.ReplaceAll(source, "</div>", "\n")
	source = strings.ReplaceAll(source, "</section>", "\n")
	source = strings.ReplaceAll(source, "</article>", "\n")
	source = strings.ReplaceAll(source, "</li>", "\n")
	source = strings.ReplaceAll(source, "</tr>", "\n")
	source = strings.ReplaceAll(source, "</h1>", "\n\n")
	source = strings.ReplaceAll(source, "</h2>", "\n\n")
	source = strings.ReplaceAll(source, "</h3>", "\n\n")
	source = tagPattern.ReplaceAllString(source, " ")
	source = html.UnescapeString(source)
	return collapseWhitespace(source)
}

func sanitizeHTML(source string) string {
	source = htmlCommentPattern.ReplaceAllString(source, " ")
	source = scriptPattern.ReplaceAllString(source, " ")
	source = stylePattern.ReplaceAllString(source, " ")
	return source
}

func stripTags(source string) string {
	return html.UnescapeString(tagPattern.ReplaceAllString(source, " "))
}

func collapseWhitespace(source string) string {
	lines := strings.Split(source, "\n")
	collapsed := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = collapseInlineWhitespace(line)
		if line == "" {
			if !blank {
				collapsed = append(collapsed, "")
			}
			blank = true
			continue
		}
		blank = false
		collapsed = append(collapsed, line)
	}
	return strings.TrimSpace(strings.Join(collapsed, "\n"))
}

func collapseInlineWhitespace(source string) string {
	return strings.Join(strings.Fields(source), " ")
}

func normalizeMarkdown(source string) string {
	lines := strings.Split(source, "\n")
	normalized := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if !blank {
				normalized = append(normalized, "")
			}
			blank = true
			continue
		}
		blank = false
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "#") {
			normalized = append(normalized, trimmed)
			continue
		}
		normalized = append(normalized, collapseInlineWhitespace(trimmed))
	}
	return strings.TrimSpace(strings.Join(normalized, "\n"))
}

func truncateText(source string, maxChars int) (string, bool) {
	runes := []rune(source)
	if len(runes) <= maxChars {
		return source, false
	}
	return strings.TrimSpace(string(runes[:maxChars])), true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func formatFetchResult(result fetchResult) string {
	lines := []string{
		"URL: " + result.OriginalURL,
		"Final-URL: " + result.FinalURL,
	}
	if result.Title != "" {
		lines = append(lines, "Title: "+result.Title)
	}
	lines = append(lines, "Content-Type: "+result.ContentType)
	if result.Truncated {
		lines = append(lines, "Truncated: true")
	}
	lines = append(lines, "", result.Content)
	return strings.Join(lines, "\n")
}

func replaceAllStringSubmatchFunc(source string, pattern *regexp.Regexp, replacer func(groups []string) string) string {
	matches := pattern.FindAllStringSubmatchIndex(source, -1)
	if len(matches) == 0 {
		return source
	}
	var builder strings.Builder
	last := 0
	for _, match := range matches {
		builder.WriteString(source[last:match[0]])
		groups := make([]string, 0, len(match)/2)
		for index := 0; index < len(match); index += 2 {
			start, end := match[index], match[index+1]
			if start == -1 || end == -1 {
				groups = append(groups, "")
				continue
			}
			groups = append(groups, source[start:end])
		}
		builder.WriteString(replacer(groups))
		last = match[1]
	}
	builder.WriteString(source[last:])
	return builder.String()
}

func fallbackContentType(mediaType string) string {
	if mediaType == "" {
		return "unknown"
	}
	return mediaType
}
