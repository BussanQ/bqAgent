package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"

	"bqagent/internal/atomicfile"
)

// Recovery and append must not race: a repaired prefix must never replace an
// append which succeeded after the damaged tail was read.
// Striped locks bound bookkeeping and let independent sessions proceed in parallel.
var jsonlLocks [64]sync.Mutex

func jsonlMutex(path string) *sync.Mutex {
	absolute, err := filepath.Abs(path)
	if err == nil {
		path = absolute
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(filepath.Clean(path)))
	return &jsonlLocks[hash.Sum32()%uint32(len(jsonlLocks))]
}

func appendJSONL(path string, entries ...any) error {
	if len(entries) == 0 {
		return nil
	}
	var pending bytes.Buffer
	encoder := json.NewEncoder(&pending)
	for _, entry := range entries {
		if err := encoder.Encode(entry); err != nil {
			return err
		}
	}
	mu := jsonlMutex(path)
	mu.Lock()
	defer mu.Unlock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	if info.Size() > 0 {
		var last [1]byte
		if _, err := file.ReadAt(last[:], info.Size()-1); err != nil {
			file.Close()
			return err
		}
		if last[0] != '\n' {
			file.Close()
			if _, err := readMessagesJSONLUnlocked(path); err != nil {
				return err
			}
			file, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return err
			}
			// A complete final JSON value is valid without a newline. Separate
			// it from the next record; a recovered prefix already ends in one.
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				file.Close()
				return readErr
			}
			if len(content) > 0 && content[len(content)-1] != '\n' {
				if _, err := file.Write([]byte{'\n'}); err != nil {
					file.Close()
					return err
				}
			}
		}
	}
	_, writeErr := file.Write(pending.Bytes())
	return errors.Join(writeErr, file.Close())
}

func writeMessagesJSONL(path string, entries []map[string]any) error {
	mu := jsonlMutex(path)
	mu.Lock()
	defer mu.Unlock()
	return atomicfile.WriteFunc(path, 0o644, func(file io.Writer) error {
		encoder := json.NewEncoder(file)
		for _, entry := range entries {
			if err := encoder.Encode(entry); err != nil {
				return err
			}
		}
		return nil
	})
}

func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	return atomicfile.Write(path, content, mode)
}

func readMessagesJSONL(path string) ([]map[string]any, error) {
	mu := jsonlMutex(path)
	mu.Lock()
	defer mu.Unlock()
	return readMessagesJSONLUnlocked(path)
}

func readMessagesJSONLUnlocked(path string) ([]map[string]any, error) {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	messages := make([]map[string]any, 0)
	for offset, line := 0, 1; offset < len(content); line++ {
		end := bytes.IndexByte(content[offset:], '\n')
		terminated := end >= 0
		if terminated {
			end += offset
		} else {
			end = len(content)
		}
		raw := content[offset:end]
		var message map[string]any
		if err := json.Unmarshal(raw, &message); err != nil {
			var partial any
			decodeErr := json.NewDecoder(bytes.NewReader(raw)).Decode(&partial)
			if !terminated && len(bytes.TrimSpace(raw)) > 0 && errors.Is(decodeErr, io.ErrUnexpectedEOF) {
				if err := preserveIncompleteTail(path, content, offset); err != nil {
					return nil, err
				}
				return messages, nil
			}
			return nil, fmt.Errorf("read %s record %d: %w", path, line, err)
		}
		messages = append(messages, message)
		offset = end + 1
	}
	return messages, nil
}

func preserveIncompleteTail(path string, content []byte, validEnd int) error {
	backup, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".incomplete-*")
	if err != nil {
		return fmt.Errorf("preserve incomplete transcript: %w", err)
	}
	_, writeErr := backup.Write(content)
	err = errors.Join(writeErr, backup.Sync(), backup.Close())
	if err != nil {
		return fmt.Errorf("preserve incomplete transcript %s: %w", backup.Name(), err)
	}
	if err := atomicfile.Write(path, content[:validEnd], 0o644); err != nil {
		return fmt.Errorf("recover transcript (original saved at %s): %w", backup.Name(), err)
	}
	log.Printf("session recovery: ignored incomplete final record in %s; recovered %d bytes, original preserved at %s", path, validEnd, backup.Name())
	return nil
}
