package runtime

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func LoadDotEnv(workspaceRoot string) map[string]string {
	if strings.TrimSpace(workspaceRoot) == "" {
		return map[string]string{}
	}
	values, err := parseDotEnvFile(filepath.Join(workspaceRoot, ".env"))
	if err != nil {
		return map[string]string{}
	}
	return values
}

func MergeEnv(getenv func(string) string, fileValues map[string]string) func(string) string {
	return func(key string) string {
		if getenv != nil {
			if value := getenv(key); value != "" {
				return value
			}
		}
		return fileValues[key]
	}
}

func parseDotEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, rawValue, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		values[key] = parseDotEnvValue(rawValue)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func parseDotEnvValue(raw string) string {
	value := strings.TrimSpace(raw)
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			if unquoted, err := strconv.Unquote(value); err == nil {
				return unquoted
			}
			return value[1 : len(value)-1]
		}
	}
	return strings.TrimSpace(stripInlineComment(value))
}

func stripInlineComment(value string) string {
	for index := 0; index < len(value); index++ {
		if value[index] != '#' {
			continue
		}
		if index == 0 || value[index-1] == ' ' || value[index-1] == '\t' {
			return strings.TrimSpace(value[:index])
		}
	}
	return value
}
