package extagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func newClaudeUUID() string {
	var id [16]byte
	rand.Read(id[:])
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
}

func validClaudeUUID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(id[:8] + id[9:13] + id[14:18] + id[19:23] + id[24:])
	return err == nil
}

func acceptClaudeSession(state *SessionState, id string) error {
	expected := state.ClaudeNewID
	if state.ForkBinding {
		expected = firstNonEmpty(expected, state.ExternalSessionID)
	}
	if expected != "" && id != expected {
		return errors.New("Claude 未确认原生会话 ID")
	}
	state.ExternalSessionID, state.ClaudeNewID = firstNonEmpty(id, state.ExternalSessionID), ""
	return nil
}

func claudeResumeArgs(ctx context.Context, args []string, state SessionState, cwd string) ([]string, error) {
	if state.ExternalSessionID != "" {
		return append(args, "-r", state.ExternalSessionID), nil
	}
	id := state.ClaudeNewID
	if id == "" {
		return args, nil
	}
	if !validClaudeUUID(id) {
		return nil, errors.New("Claude 分支 ID 无效")
	}
	if hasArg(args, "--no-session-persistence") {
		return nil, errors.New("Claude 分支需要启用会话持久化")
	}
	// The first prompt in an empty fork may have reached Claude before BQ
	// persisted its acknowledgement. Resume the reserved ID if it now exists.
	dir, err := ClaudeProjectsPath(cwd)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err == nil {
		defer root.Close()
		_, err = findClaudeProject(ctx, root, id)
	}
	if errors.Is(err, os.ErrNotExist) {
		return append(args, "--session-id", id), nil
	}
	if err != nil {
		return nil, err
	}
	return append(args, "-r", id), nil
}

func ClaudeProjectsPath(cwd string) (string, error) {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".claude")
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	return filepath.Abs(filepath.Join(dir, "projects"))
}

func findClaudeProject(ctx context.Context, root *os.Root, id string) (string, error) {
	if !validClaudeUUID(id) {
		return "", errors.New("Claude 会话 UUID 无效")
	}
	dirs, err := root.Open(".")
	if err != nil {
		return "", err
	}
	defer dirs.Close()
	entries, err := dirs.ReadDir(-1)
	if err != nil {
		return "", err
	}
	var found string
	for _, dir := range entries {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !dir.IsDir() {
			continue
		}
		info, err := root.Lstat(filepath.Join(dir.Name(), id+".jsonl"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || found != "" {
			return "", errors.New("无法唯一确定 Claude 主会话记录")
		}
		found = dir.Name()
	}
	if found == "" {
		return "", os.ErrNotExist
	}
	return found, nil
}
