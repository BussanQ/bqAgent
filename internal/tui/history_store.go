package tui

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	historyMaxEntries = 500
	historyMaxBytes   = 1 << 20
)

type historyStore struct {
	path string
}

func newHistoryStore(agentDir, workspace string) *historyStore {
	cleaned := filepath.Clean(workspace)
	if runtime.GOOS == "windows" {
		cleaned = strings.ToLower(cleaned)
	}
	sum := sha256.Sum256([]byte(cleaned))
	name := hex.EncodeToString(sum[:]) + ".jsonl"
	return &historyStore{path: filepath.Join(agentDir, "tui", "history", name)}
}

func (store *historyStore) load() ([]string, error) {
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries := make([]string, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), historyMaxBytes)
	for scanner.Scan() {
		var entry string
		if json.Unmarshal(scanner.Bytes(), &entry) == nil && strings.TrimSpace(entry) != "" {
			entries = append(entries, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(entries) > historyMaxEntries {
		entries = entries[len(entries)-historyMaxEntries:]
	}
	return entries, nil
}

func (store *historyStore) append(entries []string, value string) ([]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return entries, nil
	}
	encodedValue, err := json.Marshal(value)
	if err != nil || len(encodedValue)+1 > historyMaxBytes {
		return entries, err
	}
	entries = append(entries, value)
	if len(entries) > historyMaxEntries {
		entries = entries[len(entries)-historyMaxEntries:]
	}
	lines := make([][]byte, 0, len(entries))
	total := 0
	for _, entry := range entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		encoded = append(encoded, '\n')
		lines = append(lines, encoded)
		total += len(encoded)
	}
	for total > historyMaxBytes && len(lines) > 1 {
		total -= len(lines[0])
		lines = lines[1:]
		entries = entries[1:]
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return entries, err
	}
	if err := os.Chmod(filepath.Dir(store.path), 0o700); err != nil {
		return entries, err
	}
	file, err := os.OpenFile(store.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return entries, err
	}
	for _, line := range lines {
		if _, err = file.Write(line); err != nil {
			break
		}
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		_ = os.Chmod(store.path, 0o600)
	}
	return entries, err
}
