package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"bqagent/internal/atomicfile"
	"bqagent/internal/privatefile"
	"bqagent/internal/session"
)

// In full transcript mode the actual model input may contain a compacted
// summary that differs from the display journal. Share immutable messages
// between rewind points so repeated images and long histories are stored once.
func saveRewindWorking(root *os.Root, id string, rows []map[string]any) error {
	dir := filepath.Join("rewind", "work")
	if err := root.MkdirAll(dir, privatefile.DirMode); err != nil {
		return err
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		data, err := json.Marshal(row)
		if err != nil {
			return err
		}
		key := fmt.Sprintf("%x", sha256.Sum256(data))
		path := filepath.Join(dir, key)
		if _, err := root.Stat(path); errors.Is(err, os.ErrNotExist) {
			if err := atomicfile.WriteRoot(root, path, data, privatefile.FileMode); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		keys = append(keys, key)
	}
	data, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	return atomicfile.WriteRoot(root, filepath.Join("rewind", id+".work.json"), data, privatefile.FileMode)
}

func loadRewindWorking(s *session.Session, id string) ([]map[string]any, error) {
	root, err := os.OpenRoot(s.Dir())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	data, err := root.ReadFile(filepath.Join("rewind", id+".work.json"))
	if err != nil {
		return nil, err
	}
	var keys []string
	if err := json.Unmarshal(data, &keys); err != nil {
		return nil, err
	}
	rows := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		if decoded, err := hex.DecodeString(key); err != nil || len(decoded) != sha256.Size {
			return nil, errors.New("无效的工作上下文引用")
		}
		data, err := root.ReadFile(filepath.Join("rewind", "work", key))
		if err != nil {
			return nil, err
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != key {
			return nil, errors.New("历史工作上下文内容已更改")
		}
		var row map[string]any
		if err := json.Unmarshal(data, &row); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}
