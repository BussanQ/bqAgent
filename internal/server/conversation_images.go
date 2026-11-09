package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func storedUserImage(part any) (string, string) {
	block, _ := part.(map[string]any)
	if block["type"] != "image_url" {
		return "", ""
	}
	image, _ := block["image_url"].(map[string]any)
	url, _ := image["url"].(string)
	header, data, ok := strings.Cut(url, ";base64,")
	if !ok || len(data) == 0 || len(data) > base64.StdEncoding.EncodedLen(maxWebUIImageBytes) {
		return "", ""
	}
	switch header {
	case "data:image/png", "data:image/jpeg", "data:image/gif":
		return strings.TrimPrefix(header, "data:"), data
	default:
		return "", ""
	}
}

func conversationImageRefs(message map[string]any, offset int64, contextNumber int) []string {
	var refs []string
	parts, _ := message["content"].([]any)
	for index, part := range parts {
		if _, data := storedUserImage(part); data != "" {
			key := fmt.Sprintf("%d.%d.%x", offset, index, sha256.Sum256([]byte(data)))
			if contextNumber > 0 {
				key = strconv.Itoa(contextNumber) + "." + key
			}
			refs = append(refs, key)
		}
	}
	return refs
}

// Only serve a raster image from a user message in the requested session.
// The digest prevents a stale offset from displaying another image after a rewrite.
func serveConversationImage(w http.ResponseWriter, r *http.Request, svc *Service, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	key := r.URL.Query().Get("image")
	fields := strings.Split(key, ".")
	contextNumber := 0
	if len(fields) == 4 {
		n, err := strconv.Atoi(fields[0])
		if err != nil || n <= 0 {
			http.NotFound(w, r)
			return
		}
		contextNumber = n
		fields = fields[1:]
	}
	if len(fields) != 3 || len(fields[2]) != sha256.Size*2 {
		http.NotFound(w, r)
		return
	}
	offset, err := strconv.ParseInt(fields[0], 10, 64)
	index, idxErr := strconv.Atoi(fields[1])
	if err != nil || idxErr != nil || offset < 0 || index < 0 {
		http.NotFound(w, r)
		return
	}
	saved, err := svc.store.Open(sessionID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	saved, ok := saved.ContextAt(contextNumber)
	if !ok {
		http.NotFound(w, r)
		return
	}
	message, err := saved.ReadMessageAt(offset, 32<<20)
	if err != nil || message["role"] != "user" {
		http.NotFound(w, r)
		return
	}
	parts, _ := message["content"].([]any)
	if index >= len(parts) {
		http.NotFound(w, r)
		return
	}
	mime, data := storedUserImage(parts[index])
	sum := sha256.Sum256([]byte(data))
	if data == "" || hex.EncodeToString(sum[:]) != fields[2] {
		http.NotFound(w, r)
		return
	}
	image, err := decodeWebUIImage(webUIImagePayload{MIMEType: mime, DataBase64: data})
	if err != nil {
		http.NotFound(w, r)
		return
	}
	etag := strconv.Quote(fields[2])
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", image.ContentType())
	w.Header().Set("Content-Length", strconv.Itoa(len(image.Data)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(image.Data)
	}
}
