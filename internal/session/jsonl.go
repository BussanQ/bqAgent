package session

import (
	"bufio"
	"encoding/json"
	"io"
	"os"

	"bqagent/internal/atomicfile"
)

func appendJSONL(path string, entries ...any) error {
	if len(entries) == 0 {
		return nil
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	for _, entry := range entries {
		if err := encoder.Encode(entry); err != nil {
			return err
		}
	}
	return nil
}

func writeMessagesJSONL(path string, entries []map[string]any) error {
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
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 10*1024*1024)

	messages := make([]map[string]any, 0)
	for scanner.Scan() {
		var message map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}
