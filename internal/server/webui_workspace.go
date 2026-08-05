package server

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	webUIWorkspacePageSize      = 250
	maxWebUIPreviewTextBytes    = 512 << 10
	maxWebUIPreviewImageBytes   = 3 << 20
	webUIWorkspaceRootOpenPath  = "."
	webUIWorkspacePreviewBinary = "binary"
)

var (
	errWebUIWorkspaceHidden  = errors.New("workspace path is hidden")
	errWebUIWorkspaceSymlink = errors.New("workspace symbolic links cannot be browsed")
)

type webUIWorkspaceEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Type       string `json:"type"`
	Size       int64  `json:"size"`
	Attachable bool   `json:"attachable"`
}

type webUIWorkspaceListResponse struct {
	Path       string                `json:"path"`
	Entries    []webUIWorkspaceEntry `json:"entries"`
	NextOffset *int                  `json:"next_offset,omitempty"`
}

type webUIWorkspacePreviewResponse struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	MIMEType    string `json:"mime_type"`
	PreviewType string `json:"preview_type"`
	Content     string `json:"content,omitempty"`
	DataBase64  string `json:"data_base64,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Attachable  bool   `json:"attachable"`
}

type webUIWorkspaceError struct {
	status int
	err    error
}

func (err *webUIWorkspaceError) Error() string {
	if err == nil || err.err == nil {
		return "workspace request failed"
	}
	return err.err.Error()
}

func (channel *WebUIChannel) handleWorkspaceList(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	if channel == nil || channel.service == nil {
		writeError(writer, http.StatusServiceUnavailable, chatResponse{Error: "service is unavailable"})
		return
	}

	relative, err := normalizeWebUIWorkspacePath(request.URL.Query().Get("path"))
	if err != nil {
		writeWebUIWorkspaceError(writer, err)
		return
	}
	offset, err := webUIWorkspaceOffset(request.URL.Query().Get("offset"))
	if err != nil {
		writeWebUIWorkspaceError(writer, err)
		return
	}

	root, err := openWebUIWorkspaceRoot(channel.service.workspaceRoot)
	if err != nil {
		writeWebUIWorkspaceError(writer, err)
		return
	}
	defer root.Close()

	rootPath := webUIWorkspaceOSPath(relative)
	if err := rejectWebUIWorkspaceSymlinks(root, relative); err != nil {
		writeWebUIWorkspaceError(writer, err)
		return
	}
	info, err := root.Lstat(rootPath)
	if err != nil {
		writeWebUIWorkspaceError(writer, webUIWorkspaceFileError("inspect workspace directory", err))
		return
	}
	if !info.IsDir() {
		writeWebUIWorkspaceError(writer, &webUIWorkspaceError{status: http.StatusBadRequest, err: fmt.Errorf("workspace path is not a directory")})
		return
	}

	directory, err := root.Open(rootPath)
	if err != nil {
		writeWebUIWorkspaceError(writer, webUIWorkspaceFileError("open workspace directory", err))
		return
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		writeWebUIWorkspaceError(writer, webUIWorkspaceFileError("read workspace directory", readErr))
		return
	}
	if closeErr != nil {
		writeWebUIWorkspaceError(writer, &webUIWorkspaceError{status: http.StatusInternalServerError, err: fmt.Errorf("close workspace directory: %w", closeErr)})
		return
	}

	result := make([]webUIWorkspaceEntry, 0, len(entries))
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), ".git") {
			continue
		}
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			writeWebUIWorkspaceError(writer, webUIWorkspaceFileError("inspect workspace entry", infoErr))
			return
		}
		entryType := webUIWorkspaceEntryType(entryInfo.Mode())
		size := int64(0)
		attachable := false
		if entryType == "file" {
			size = entryInfo.Size()
			attachable = size <= maxWebUIFileBytes
		}
		result = append(result, webUIWorkspaceEntry{
			Name:       entry.Name(),
			Path:       webUIWorkspaceChildPath(relative, entry.Name()),
			Type:       entryType,
			Size:       size,
			Attachable: attachable,
		})
	}

	sort.Slice(result, func(left, right int) bool {
		leftDirectory := result[left].Type == "directory"
		rightDirectory := result[right].Type == "directory"
		if leftDirectory != rightDirectory {
			return leftDirectory
		}
		leftName := strings.ToLower(result[left].Name)
		rightName := strings.ToLower(result[right].Name)
		if leftName != rightName {
			return leftName < rightName
		}
		return result[left].Name < result[right].Name
	})

	if offset > len(result) {
		offset = len(result)
	}
	end := offset + webUIWorkspacePageSize
	if end > len(result) {
		end = len(result)
	}
	page := append([]webUIWorkspaceEntry{}, result[offset:end]...)
	var nextOffset *int
	if end < len(result) {
		next := end
		nextOffset = &next
	}
	writeJSON(writer, http.StatusOK, webUIWorkspaceListResponse{Path: relative, Entries: page, NextOffset: nextOffset})
}

func (channel *WebUIChannel) handleWorkspacePreview(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	if channel == nil || channel.service == nil {
		writeError(writer, http.StatusServiceUnavailable, chatResponse{Error: "service is unavailable"})
		return
	}

	relative, err := normalizeWebUIWorkspacePath(request.URL.Query().Get("path"))
	if err != nil {
		writeWebUIWorkspaceError(writer, err)
		return
	}
	if relative == "" {
		writeWebUIWorkspaceError(writer, &webUIWorkspaceError{status: http.StatusBadRequest, err: fmt.Errorf("workspace file path is required")})
		return
	}

	root, err := openWebUIWorkspaceRoot(channel.service.workspaceRoot)
	if err != nil {
		writeWebUIWorkspaceError(writer, err)
		return
	}
	defer root.Close()
	if err := rejectWebUIWorkspaceSymlinks(root, relative); err != nil {
		writeWebUIWorkspaceError(writer, err)
		return
	}

	osPath := webUIWorkspaceOSPath(relative)
	info, err := root.Lstat(osPath)
	if err != nil {
		writeWebUIWorkspaceError(writer, webUIWorkspaceFileError("inspect workspace file", err))
		return
	}
	if !info.Mode().IsRegular() {
		writeWebUIWorkspaceError(writer, &webUIWorkspaceError{status: http.StatusBadRequest, err: fmt.Errorf("workspace path is not a regular file")})
		return
	}

	file, err := root.Open(osPath)
	if err != nil {
		writeWebUIWorkspaceError(writer, webUIWorkspaceFileError("open workspace file", err))
		return
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		writeWebUIWorkspaceError(writer, webUIWorkspaceFileError("inspect opened workspace file", err))
		return
	}
	if !openedInfo.Mode().IsRegular() {
		writeWebUIWorkspaceError(writer, &webUIWorkspaceError{status: http.StatusBadRequest, err: fmt.Errorf("workspace path is not a regular file")})
		return
	}

	preview, err := buildWebUIWorkspacePreview(file, relative, openedInfo)
	if err != nil {
		writeWebUIWorkspaceError(writer, webUIWorkspaceFileError("read workspace file preview", err))
		return
	}
	writeJSON(writer, http.StatusOK, preview)
}

func buildWebUIWorkspacePreview(file *os.File, relative string, info os.FileInfo) (webUIWorkspacePreviewResponse, error) {
	response := webUIWorkspacePreviewResponse{
		Name:       path.Base(relative),
		Path:       relative,
		Size:       info.Size(),
		Attachable: info.Size() <= maxWebUIFileBytes,
	}

	sniff := make([]byte, 512)
	read, err := io.ReadFull(file, sniff)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return webUIWorkspacePreviewResponse{}, err
	}
	sniff = sniff[:read]
	response.MIMEType = http.DetectContentType(sniff)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return webUIWorkspacePreviewResponse{}, err
	}

	if isWebUIPreviewImage(response.MIMEType) {
		if info.Size() > maxWebUIPreviewImageBytes {
			response.PreviewType = "unavailable"
			response.Reason = "图片超过 3 MiB 预览上限"
			return response, nil
		}
		data, err := io.ReadAll(io.LimitReader(file, maxWebUIPreviewImageBytes+1))
		if err != nil {
			return webUIWorkspacePreviewResponse{}, err
		}
		if len(data) > maxWebUIPreviewImageBytes {
			response.PreviewType = "unavailable"
			response.Reason = "图片超过 3 MiB 预览上限"
			return response, nil
		}
		response.PreviewType = "image"
		response.DataBase64 = base64.StdEncoding.EncodeToString(data)
		return response, nil
	}

	data, err := io.ReadAll(io.LimitReader(file, maxWebUIPreviewTextBytes+utf8.UTFMax))
	if err != nil {
		return webUIWorkspacePreviewResponse{}, err
	}
	previewData := data
	if len(previewData) > maxWebUIPreviewTextBytes {
		previewData = truncateUTF8(previewData, maxWebUIPreviewTextBytes)
		response.Truncated = true
	} else if info.Size() > int64(len(previewData)) {
		response.Truncated = true
	}
	if utf8.Valid(previewData) && !bytes.ContainsRune(previewData, '\x00') && !isKnownWebUIBinaryMIME(response.MIMEType) {
		response.PreviewType = "text"
		response.Content = string(previewData)
		return response, nil
	}

	response.PreviewType = webUIWorkspacePreviewBinary
	response.Truncated = false
	response.Reason = "此文件不是可预览的 UTF-8 文本或受支持图片"
	return response, nil
}

func normalizeWebUIWorkspacePath(input string) (string, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", nil
	}
	if strings.ContainsRune(value, '\x00') {
		return "", &webUIWorkspaceError{status: http.StatusBadRequest, err: fmt.Errorf("workspace path contains NUL")}
	}
	value = strings.ReplaceAll(value, `\`, "/")
	if path.IsAbs(value) || filepath.IsAbs(filepath.FromSlash(value)) || strings.HasPrefix(value, "//") || webUIWorkspaceHasDrivePrefix(value) {
		return "", &webUIWorkspaceError{status: http.StatusBadRequest, err: fmt.Errorf("workspace path must be relative")}
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", &webUIWorkspaceError{status: http.StatusBadRequest, err: fmt.Errorf("workspace path contains an invalid component")}
		}
		if strings.EqualFold(part, ".git") {
			return "", &webUIWorkspaceError{status: http.StatusNotFound, err: errWebUIWorkspaceHidden}
		}
	}
	return strings.Join(parts, "/"), nil
}

func webUIWorkspaceOffset(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(value)
	if err != nil || offset < 0 {
		return 0, &webUIWorkspaceError{status: http.StatusBadRequest, err: fmt.Errorf("offset must be a non-negative integer")}
	}
	return offset, nil
}

func openWebUIWorkspaceRoot(workspaceRoot string) (*os.Root, error) {
	rootPath, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, &webUIWorkspaceError{status: http.StatusInternalServerError, err: fmt.Errorf("resolve workspace root: %w", err)}
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, webUIWorkspaceFileError("open workspace root", err)
	}
	return root, nil
}

func rejectWebUIWorkspaceSymlinks(root *os.Root, relative string) error {
	if relative == "" {
		return nil
	}
	current := ""
	for _, component := range strings.Split(relative, "/") {
		current = filepath.Join(current, filepath.FromSlash(component))
		info, err := root.Lstat(current)
		if err != nil {
			return webUIWorkspaceFileError("inspect workspace path", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return &webUIWorkspaceError{status: http.StatusBadRequest, err: errWebUIWorkspaceSymlink}
		}
	}
	return nil
}

func writeWebUIWorkspaceError(writer http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var workspaceErr *webUIWorkspaceError
	if errors.As(err, &workspaceErr) && workspaceErr.status != 0 {
		status = workspaceErr.status
	}
	writeError(writer, status, chatResponse{Error: err.Error()})
}

func webUIWorkspaceFileError(action string, err error) error {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, os.ErrNotExist):
		status = http.StatusNotFound
	case errors.Is(err, os.ErrPermission):
		status = http.StatusForbidden
	}
	return &webUIWorkspaceError{status: status, err: fmt.Errorf("%s: %w", action, err)}
}

func webUIWorkspaceEntryType(mode os.FileMode) string {
	switch {
	case mode&os.ModeSymlink != 0:
		return "symlink"
	case mode.IsDir():
		return "directory"
	case mode.IsRegular():
		return "file"
	default:
		return "other"
	}
}

func webUIWorkspaceChildPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return path.Join(parent, name)
}

func webUIWorkspaceOSPath(relative string) string {
	if relative == "" {
		return webUIWorkspaceRootOpenPath
	}
	return filepath.FromSlash(relative)
}

func webUIWorkspaceHasDrivePrefix(value string) bool {
	return len(value) >= 2 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':'
}

func isWebUIPreviewImage(mimeType string) bool {
	mimeType = strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func isKnownWebUIBinaryMIME(mimeType string) bool {
	mimeType = strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	switch mimeType {
	case "application/pdf", "application/zip", "application/gzip", "application/x-rar-compressed", "application/vnd.rar":
		return true
	default:
		return strings.HasPrefix(mimeType, "audio/") || strings.HasPrefix(mimeType, "video/")
	}
}
