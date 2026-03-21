package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const memoryLongtermFile = "MEMORY.md"

var memoryNow = time.Now

func MemSaveInDir(memDir string) Function {
	return func(args map[string]any) (string, error) {
		target, err := requireString(args, "target")
		if err != nil {
			return "", err
		}
		content, err := requireString(args, "content")
		if err != nil {
			return "", err
		}

		var memPath string
		now := memoryNow()
		switch target {
		case "daily":
			memPath = filepath.Join(memDir, now.Format("2006-01-02")+".md")
		case "longterm":
			memPath = filepath.Join(memDir, memoryLongtermFile)
		default:
			return "", fmt.Errorf("target must be \"daily\" or \"longterm\", got %q", target)
		}

		if err := os.MkdirAll(filepath.Dir(memPath), 0o755); err != nil {
			return "", err
		}

		file, err := os.OpenFile(memPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return "", err
		}
		defer file.Close()

		entry := fmt.Sprintf("\n## %s\n%s\n", now.Format("2006-01-02 15:04:05"), content)
		if _, err := file.WriteString(entry); err != nil {
			return "", err
		}

		return fmt.Sprintf("Saved to %s memory.", target), nil
	}
}

func MemGetInDir(memDir string) Function {
	return func(args map[string]any) (string, error) {
		target, err := requireString(args, "target")
		if err != nil {
			return "", err
		}

		var memPath string
		now := memoryNow()
		switch target {
		case "daily":
			memPath = filepath.Join(memDir, now.Format("2006-01-02")+".md")
		case "longterm":
			memPath = filepath.Join(memDir, memoryLongtermFile)
		case "yesterday":
			memPath = filepath.Join(memDir, now.AddDate(0, 0, -1).Format("2006-01-02")+".md")
		default:
			return "", fmt.Errorf("target must be \"daily\", \"longterm\", or \"yesterday\", got %q", target)
		}

		data, err := os.ReadFile(memPath)
		if os.IsNotExist(err) {
			return fmt.Sprintf("No %s memory found.", target), nil
		}
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
}
