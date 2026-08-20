package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"bqagent/internal/workspace"
)

type webUIWorkspacesResponse struct {
	Default WorkspaceInfo       `json:"default"`
	Roots   []WorkspaceRootInfo `json:"roots"`
}

type webUIWorkspaceDirectoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type webUIWorkspaceDirectoryResponse struct {
	Root         WorkspaceRootInfo              `json:"root"`
	Path         string                         `json:"path"`
	AbsolutePath string                         `json:"absolute_path"`
	Directories  []webUIWorkspaceDirectoryEntry `json:"directories"`
	NextOffset   *int                           `json:"next_offset,omitempty"`
}

type webUIWorkspaceOpenRequest struct {
	RootID string `json:"root_id"`
	Path   string `json:"path"`
}

type webUIWorkspaceConfigResponse struct {
	Path    string   `json:"path"`
	Created bool     `json:"created"`
	Files   []string `json:"files"`
}

func (channel *WebUIChannel) handleWorkspaceConfigCreate(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	service, _, err := channel.resolveService(request.URL.Query().Get("workspace_id"))
	if err != nil {
		writeError(writer, http.StatusNotFound, chatResponse{Error: "workspace not found"})
		return
	}
	ws := &workspace.Workspace{Root: service.workspaceRoot}
	created := !ws.HasLocalAgentConfig()
	if err := ws.EnsureLocalConfig(); err != nil {
		writeWebUIWorkspaceError(writer, webUIWorkspaceFileError("create workspace agent config", err))
		return
	}
	writeJSON(writer, http.StatusOK, webUIWorkspaceConfigResponse{
		Path:    ws.LocalAgentDir(),
		Created: created,
		Files:   []string{"memory/", "mcp.json", "AGENT.md", "SOUL.md", "USER.md"},
	})
}

func (channel *WebUIChannel) handleWorkspaces(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	if channel.workspaces == nil {
		writeError(writer, http.StatusServiceUnavailable, chatResponse{Error: "workspace switching is unavailable"})
		return
	}
	writeJSON(writer, http.StatusOK, webUIWorkspacesResponse{
		Default: channel.workspaces.DefaultInfo(),
		Roots:   channel.workspaces.Roots(),
	})
}

func (channel *WebUIChannel) handleWorkspaceDirectories(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	if channel.workspaces == nil {
		writeError(writer, http.StatusServiceUnavailable, chatResponse{Error: "workspace switching is unavailable"})
		return
	}
	root, ok := channel.workspaces.Root(request.URL.Query().Get("root_id"))
	if !ok {
		writeError(writer, http.StatusNotFound, chatResponse{Error: "workspace root not found"})
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
	allowedRoot, err := os.OpenRoot(root.Path)
	if err != nil {
		writeWebUIWorkspaceError(writer, webUIWorkspaceFileError("open directory root", err))
		return
	}
	symlinkErr := rejectWebUIWorkspaceSymlinks(allowedRoot, relative)
	closeRootErr := allowedRoot.Close()
	if symlinkErr != nil {
		writeWebUIWorkspaceError(writer, symlinkErr)
		return
	}
	if closeRootErr != nil {
		writeWebUIWorkspaceError(writer, &webUIWorkspaceError{status: http.StatusInternalServerError, err: closeRootErr})
		return
	}
	canonical, err := canonicalWorkspaceDirectory(filepath.Join(root.Path, filepath.FromSlash(relative)))
	if err != nil || !pathWithinRoot(root.Path, canonical) {
		if err == nil {
			err = fmt.Errorf("directory path escapes allowed root")
		}
		writeWebUIWorkspaceError(writer, webUIWorkspaceFileError("open directory", err))
		return
	}
	directory, err := os.Open(canonical)
	if err != nil {
		writeWebUIWorkspaceError(writer, webUIWorkspaceFileError("open directory", err))
		return
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		writeWebUIWorkspaceError(writer, webUIWorkspaceFileError("read directory", readErr))
		return
	}
	if closeErr != nil {
		writeWebUIWorkspaceError(writer, &webUIWorkspaceError{status: http.StatusInternalServerError, err: closeErr})
		return
	}
	result := make([]webUIWorkspaceDirectoryEntry, 0, len(entries))
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), ".git") || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.IsDir() {
			continue
		}
		result = append(result, webUIWorkspaceDirectoryEntry{
			Name: entry.Name(),
			Path: webUIWorkspaceChildPath(relative, entry.Name()),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := strings.ToLower(result[i].Name), strings.ToLower(result[j].Name)
		if left != right {
			return left < right
		}
		return result[i].Name < result[j].Name
	})
	if offset > len(result) {
		offset = len(result)
	}
	end := offset + webUIWorkspacePageSize
	if end > len(result) {
		end = len(result)
	}
	var nextOffset *int
	if end < len(result) {
		next := end
		nextOffset = &next
	}
	writeJSON(writer, http.StatusOK, webUIWorkspaceDirectoryResponse{
		Root: root, Path: relative, AbsolutePath: canonical, Directories: result[offset:end], NextOffset: nextOffset,
	})
}

func (channel *WebUIChannel) handleWorkspaceOpen(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
		return
	}
	if channel.workspaces == nil {
		writeError(writer, http.StatusServiceUnavailable, chatResponse{Error: "workspace switching is unavailable"})
		return
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]))
	if contentType != "application/json" {
		writeError(writer, http.StatusUnsupportedMediaType, chatResponse{Error: "content-type must be application/json"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 64<<10)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var payload webUIWorkspaceOpenRequest
	if err := decoder.Decode(&payload); err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: "invalid JSON request: " + err.Error()})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: "request must contain exactly one JSON object"})
		return
	}
	info, err := channel.workspaces.Open(request.Context(), strings.TrimSpace(payload.RootID), payload.Path)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeError(writer, status, chatResponse{Error: err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, info)
}
