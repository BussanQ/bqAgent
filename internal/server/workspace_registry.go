package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type WorkspaceFactory func(context.Context, string) (*Service, io.Closer, error)

type WorkspaceRootInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type WorkspaceInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	RootID       string `json:"root_id"`
	RelativePath string `json:"relative_path"`
}

type WorkspaceRegistryOptions struct {
	DefaultRoot    string
	DefaultService *Service
	DefaultCloser  io.Closer
	AllowedRoots   []string
	Factory        WorkspaceFactory
}

type workspaceRegistryEntry struct {
	info    WorkspaceInfo
	service *Service
	closer  io.Closer
}

type workspaceRegistryPending struct {
	done  chan struct{}
	entry *workspaceRegistryEntry
	err   error
}

type WorkspaceRegistry struct {
	mu        sync.Mutex
	defaultID string
	roots     map[string]WorkspaceRootInfo
	entries   map[string]*workspaceRegistryEntry
	pending   map[string]*workspaceRegistryPending
	factory   WorkspaceFactory
	closed    bool
}

func NewWorkspaceRegistry(options WorkspaceRegistryOptions) (*WorkspaceRegistry, error) {
	if options.DefaultService == nil {
		return nil, fmt.Errorf("default workspace service is required")
	}
	defaultRoot, err := canonicalWorkspaceDirectory(options.DefaultRoot)
	if err != nil {
		return nil, fmt.Errorf("default workspace: %w", err)
	}

	allowed := append([]string{}, options.AllowedRoots...)
	allowed = append(allowed, defaultRoot)
	roots := make(map[string]WorkspaceRootInfo)
	for _, candidate := range allowed {
		root, rootErr := canonicalWorkspaceDirectory(candidate)
		if rootErr != nil {
			continue
		}
		id := workspacePathID("root", root)
		roots[id] = WorkspaceRootInfo{ID: id, Name: workspaceDisplayName(root), Path: root}
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("no valid workspace roots")
	}

	defaultInfo, err := workspaceInfoForPath(defaultRoot, roots)
	if err != nil {
		return nil, err
	}
	registry := &WorkspaceRegistry{
		defaultID: defaultInfo.ID,
		roots:     roots,
		entries:   make(map[string]*workspaceRegistryEntry),
		pending:   make(map[string]*workspaceRegistryPending),
		factory:   options.Factory,
	}
	registry.entries[defaultInfo.ID] = &workspaceRegistryEntry{
		info: defaultInfo, service: options.DefaultService, closer: options.DefaultCloser,
	}
	return registry, nil
}

func DefaultWorkspaceAllowedRoots(defaultRoot string, configured []string) []string {
	values := make([]string, 0, len(configured)+2)
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		values = append(values, home)
	}
	values = append(values, defaultRoot)
	values = append(values, configured...)
	return values
}

func SplitWorkspaceRoots(raw string) []string {
	parts := filepath.SplitList(strings.TrimSpace(raw))
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func (registry *WorkspaceRegistry) DefaultInfo() WorkspaceInfo {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if entry := registry.entries[registry.defaultID]; entry != nil {
		return entry.info
	}
	return WorkspaceInfo{}
}

func (registry *WorkspaceRegistry) Roots() []WorkspaceRootInfo {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	result := make([]WorkspaceRootInfo, 0, len(registry.roots))
	for _, root := range registry.roots {
		result = append(result, root)
	}
	sort.Slice(result, func(i, j int) bool {
		leftName, rightName := strings.ToLower(result[i].Name), strings.ToLower(result[j].Name)
		if leftName != rightName {
			return leftName < rightName
		}
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return result[i].Path < result[j].Path
	})
	return result
}

func (registry *WorkspaceRegistry) Root(id string) (WorkspaceRootInfo, bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	root, ok := registry.roots[strings.TrimSpace(id)]
	return root, ok
}

func (registry *WorkspaceRegistry) Resolve(id string) (*Service, WorkspaceInfo, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if strings.TrimSpace(id) == "" {
		id = registry.defaultID
	}
	entry := registry.entries[id]
	if entry == nil {
		return nil, WorkspaceInfo{}, os.ErrNotExist
	}
	return entry.service, entry.info, nil
}

func (registry *WorkspaceRegistry) Open(ctx context.Context, rootID, relative string) (WorkspaceInfo, error) {
	root, ok := registry.Root(rootID)
	if !ok {
		return WorkspaceInfo{}, fmt.Errorf("workspace root not found: %w", os.ErrNotExist)
	}
	relative, err := normalizeWebUIWorkspacePath(relative)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	allowedRoot, err := os.OpenRoot(root.Path)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	symlinkErr := rejectWebUIWorkspaceSymlinks(allowedRoot, relative)
	closeErr := allowedRoot.Close()
	if symlinkErr != nil {
		return WorkspaceInfo{}, symlinkErr
	}
	if closeErr != nil {
		return WorkspaceInfo{}, closeErr
	}
	candidate := filepath.Join(root.Path, filepath.FromSlash(relative))
	canonical, err := canonicalWorkspaceDirectory(candidate)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	if !pathWithinRoot(root.Path, canonical) {
		return WorkspaceInfo{}, fmt.Errorf("workspace path escapes allowed root")
	}
	info, err := workspaceInfoForPath(canonical, registry.rootsSnapshot())
	if err != nil {
		return WorkspaceInfo{}, err
	}

	registry.mu.Lock()
	if registry.closed {
		registry.mu.Unlock()
		return WorkspaceInfo{}, errors.New("workspace registry is closed")
	}
	if entry := registry.entries[info.ID]; entry != nil {
		registry.mu.Unlock()
		return entry.info, nil
	}
	if pending := registry.pending[info.ID]; pending != nil {
		registry.mu.Unlock()
		select {
		case <-ctx.Done():
			return WorkspaceInfo{}, ctx.Err()
		case <-pending.done:
			if pending.err != nil {
				return WorkspaceInfo{}, pending.err
			}
			return pending.entry.info, nil
		}
	}
	pending := &workspaceRegistryPending{done: make(chan struct{})}
	registry.pending[info.ID] = pending
	registry.mu.Unlock()

	build := func() (*workspaceRegistryEntry, error) {
		if registry.factory == nil {
			return nil, fmt.Errorf("workspace service factory is unavailable")
		}
		service, closer, err := registry.factory(ctx, canonical)
		if err != nil {
			return nil, err
		}
		return &workspaceRegistryEntry{info: info, service: service, closer: closer}, nil
	}
	entry, buildErr := build()

	registry.mu.Lock()
	delete(registry.pending, info.ID)
	if buildErr == nil && !registry.closed {
		registry.entries[info.ID] = entry
	} else if buildErr == nil {
		buildErr = errors.New("workspace registry is closed")
	}
	pending.entry = entry
	pending.err = buildErr
	close(pending.done)
	registry.mu.Unlock()
	if buildErr != nil {
		if entry != nil && entry.closer != nil {
			_ = entry.closer.Close()
		}
		return WorkspaceInfo{}, buildErr
	}
	return entry.info, nil
}

func (registry *WorkspaceRegistry) Close() error {
	registry.mu.Lock()
	if registry.closed {
		registry.mu.Unlock()
		return nil
	}
	registry.closed = true
	entries := make([]*workspaceRegistryEntry, 0, len(registry.entries))
	for _, entry := range registry.entries {
		entries = append(entries, entry)
	}
	registry.mu.Unlock()

	var result error
	for _, entry := range entries {
		if entry.closer != nil {
			result = errors.Join(result, entry.closer.Close())
		}
	}
	return result
}

func (registry *WorkspaceRegistry) rootsSnapshot() map[string]WorkspaceRootInfo {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	result := make(map[string]WorkspaceRootInfo, len(registry.roots))
	for id, root := range registry.roots {
		result[id] = root
	}
	return result
}

func workspaceInfoForPath(root string, allowed map[string]WorkspaceRootInfo) (WorkspaceInfo, error) {
	var selected WorkspaceRootInfo
	selectedLength := -1
	for _, candidate := range allowed {
		if pathWithinRoot(candidate.Path, root) && len(candidate.Path) > selectedLength {
			selected = candidate
			selectedLength = len(candidate.Path)
		}
	}
	if selectedLength < 0 {
		return WorkspaceInfo{}, fmt.Errorf("workspace path is outside allowed roots")
	}
	relative, err := filepath.Rel(selected.Path, root)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	if relative == "." {
		relative = ""
	}
	return WorkspaceInfo{
		ID:           workspacePathID("workspace", root),
		Name:         workspaceDisplayName(root),
		Path:         root,
		RootID:       selected.ID,
		RelativePath: filepath.ToSlash(relative),
	}, nil
}

func canonicalWorkspaceDirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("workspace path is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace path is not a directory")
	}
	return filepath.Clean(canonical), nil
}

func pathWithinRoot(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func workspacePathID(kind, value string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + filepath.Clean(value)))
	return hex.EncodeToString(sum[:16])
}

func workspaceDisplayName(value string) string {
	name := filepath.Base(filepath.Clean(value))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return value
	}
	return name
}
