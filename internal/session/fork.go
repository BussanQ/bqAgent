package session

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"bqagent/internal/privatefile"
)

type ForkOrigin struct {
	Session   string `json:"session"`
	Epoch     int    `json:"epoch"`
	Target    string `json:"target"`
	Operation string `json:"operation"`
}

const forkOperationsDir = ".fork-operations"

func (f ForkOrigin) childID() string {
	return fmt.Sprintf("fork-%x", sha256.Sum256([]byte(f.Session+"\x00"+f.Operation)))[:37]
}

var ErrForkConflict = errors.New("分支操作 ID 已用于其他回复")
var ErrForkDeleted = errors.New("这个分支聊天已被删除")

func (s *Store) ExistingFork(f ForkOrigin) (*Session, error) {
	child, err := s.Open(f.childID())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if child.meta.ForkedFrom == nil || *child.meta.ForkedFrom != f {
		return nil, ErrForkConflict
	}
	return child, nil
}

// Prepare history, native binding and task state privately. A ready marker
// plus a same-filesystem rename publishes one independently reopenable chat.
// The operation directory survives process restarts and child deletion.
func (s *Store) CreateFork(f ForkOrigin, meta Meta, prepare func(*Session, string) error) (*Session, error) {
	if _, err := CanonicalID(f.Session); err != nil || f.Epoch < 0 || f.Target == "" || f.Operation == "" {
		return nil, errors.New("无效的来源回复")
	}
	id := f.childID()
	dest, err := s.sessionDir(id)
	if err != nil {
		return nil, err
	}
	opDir := filepath.Join(filepath.Dir(dest), forkOperationsDir, id)
	if err := privatefile.MkdirAll(opDir); err != nil {
		return nil, err
	}
	lock, err := privatefile.OpenFile(filepath.Join(opDir, "lock"), os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := TryLockFile(lock, true); err != nil {
		return nil, err
	}
	if child, err := s.ExistingFork(f); child != nil || err != nil {
		return child, err
	}
	record := filepath.Join(opDir, "request.json")
	data, err := os.ReadFile(record)
	if err == nil {
		var prior ForkOrigin
		if json.Unmarshal(data, &prior) != nil || prior != f {
			return nil, ErrForkConflict
		}
	} else if errors.Is(err, os.ErrNotExist) {
		data, err = json.Marshal(f)
		if err != nil {
			return nil, err
		}
		if err := privatefile.WriteFile(record, data); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	stage := filepath.Join(opDir, "chat")
	work := filepath.Join(opDir, "prepare")
	ready := filepath.Join(opDir, "ready")
	if _, err := os.Stat(ready); errors.Is(err, os.ErrNotExist) {
		if err := os.RemoveAll(stage); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		next := &Session{store: s, dir: stage, meta: Meta{ID: id, WorkspaceRoot: s.workspaceRoot, Chat: true, ForkedFrom: &f, Task: meta.Task,
			CurrentMode: meta.CurrentMode, CurrentModel: meta.CurrentModel, FastMode: meta.FastMode, Status: StatusCompleted,
			PromptSchemaVersion: meta.PromptSchemaVersion, PromptStableHash: meta.PromptStableHash, PromptMessageCount: meta.PromptMessageCount,
			CreatedAt: now, UpdatedAt: now}}
		if err := next.persistMeta(); err != nil {
			return nil, err
		}
		if err := privatefile.MkdirAll(work); err != nil {
			return nil, err
		}
		if err := syncForkDirectories(filepath.Dir(dest), filepath.Dir(opDir), opDir); err != nil {
			return nil, err
		}
		if err := prepare(next, work); err != nil {
			_ = os.RemoveAll(stage)
			return nil, err
		}
		if err := syncForkDirectories(stage); err != nil {
			return nil, err
		}
		if err := privatefile.WriteFile(ready, []byte(id)); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if err := os.Rename(stage, dest); errors.Is(err, os.ErrNotExist) {
		return nil, ErrForkDeleted
	} else if err != nil {
		return nil, err
	}
	if err := syncForkDirectories(filepath.Dir(dest), opDir); err != nil {
		return nil, err
	}
	_ = os.RemoveAll(work) // The published chat is now the receipt.
	return s.Open(id)
}

func syncForkDirectories(dirs ...string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	for _, dir := range dirs {
		fd, err := os.Open(dir)
		if err != nil {
			return err
		}
		if err := errors.Join(fd.Sync(), fd.Close()); err != nil {
			return err
		}
	}
	return nil
}
