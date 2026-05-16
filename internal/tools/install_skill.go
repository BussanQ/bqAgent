package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

var repeatedDashPattern = regexp.MustCompile(`-+`)

func InstallSkill(ctx context.Context, args map[string]any) (string, error) {
	return InstallSkillToRoot("")(ctx, args)
}

func InstallSkillToRoot(root string) Function {
	return installSkillToRootWithClient(root, nil, false)
}

func InstallSkillToRootWithClient(root string, client *http.Client, allowPrivateHosts bool) Function {
	return installSkillToRootWithClient(root, client, allowPrivateHosts)
}

func installSkillToRootWithClient(root string, client *http.Client, allowPrivateHosts bool) Function {
	return func(ctx context.Context, args map[string]any) (string, error) {
		rawURL, err := requireString(args, "url")
		if err != nil {
			return "", err
		}
		name, err := optionalString(args, "name")
		if err != nil {
			return "", err
		}
		overwrite, err := optionalBoolString(args, "overwrite")
		if err != nil {
			return "", err
		}

		result, err := fetchReadableContent(ctx, client, allowPrivateHosts, rawURL, extractModeMarkdown, 0)
		if err != nil {
			return "", err
		}

		if strings.TrimSpace(name) == "" {
			name, err = deriveSkillName(result.FinalURL)
			if err != nil {
				return "", err
			}
		}
		skillName, err := normalizeSkillName(name)
		if err != nil {
			return "", err
		}

		content := normalizeSkillMarkdown(result.Content, result.Title, skillName)
		if content == "" {
			return "", fmt.Errorf("fetched skill content is empty")
		}

		workspaceRoot := strings.TrimSpace(root)
		if workspaceRoot == "" {
			var cwdErr error
			workspaceRoot, cwdErr = os.Getwd()
			if cwdErr != nil {
				return "", cwdErr
			}
		}

		skillDir := filepath.Join(workspaceRoot, ".agent", "skills", skillName)
		skillPath := filepath.Join(skillDir, "SKILL.md")
		if !overwrite {
			if _, statErr := os.Stat(skillPath); statErr == nil {
				return "", fmt.Errorf("skill %q already exists; pass overwrite=true to replace it", skillName)
			} else if !os.IsNotExist(statErr) {
				return "", statErr
			}
		}
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			return "", fmt.Errorf("failed to create skill directory: %w", err)
		}
		if err := os.WriteFile(skillPath, []byte(content), 0o644); err != nil {
			return "", fmt.Errorf("failed to write skill %q: %w", skillName, err)
		}

		return fmt.Sprintf("Installed skill %q to %s", skillName, skillPath), nil
	}
}

func optionalString(args map[string]any, key string) (string, error) {
	value, ok := args[key]
	if !ok {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", key)
	}
	return strings.TrimSpace(text), nil
}

func optionalBoolString(args map[string]any, key string) (bool, error) {
	value, ok := args[key]
	if !ok {
		return false, nil
	}
	text, ok := value.(string)
	if !ok {
		return false, fmt.Errorf("argument %q must be a string", key)
	}
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "", "false", "no", "0":
		return false, nil
	case "true", "yes", "1":
		return true, nil
	default:
		return false, fmt.Errorf("argument %q must be true or false", key)
	}
}

func deriveSkillName(rawURL string) (string, error) {
	parsedURL, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	segments := strings.Split(strings.Trim(path.Clean(parsedURL.EscapedPath()), "/"), "/")
	for index := len(segments) - 1; index >= 0; index-- {
		segment, err := url.PathUnescape(strings.TrimSpace(segments[index]))
		if err != nil {
			continue
		}
		segment = strings.TrimSuffix(segment, path.Ext(segment))
		if strings.TrimSpace(segment) != "" && segment != "." {
			return segment, nil
		}
	}
	if parsedURL.Hostname() != "" {
		return parsedURL.Hostname(), nil
	}
	return "", fmt.Errorf("could not derive skill name from url")
}

func normalizeSkillName(name string) (string, error) {
	var builder strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		allowed := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-'
		if allowed {
			builder.WriteRune(r)
			lastDash = r == '-'
			continue
		}
		if !lastDash {
			builder.WriteRune('-')
			lastDash = true
		}
	}
	result := strings.Trim(repeatedDashPattern.ReplaceAllString(builder.String(), "-"), ".-_")
	if result == "" || result == "." || result == ".." {
		return "", fmt.Errorf("skill name %q is not valid", name)
	}
	return result, nil
}

func normalizeSkillMarkdown(content, title, skillName string) string {
	text := strings.TrimSpace(content)
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "---") || strings.HasPrefix(text, "#") {
		return text + "\n"
	}
	heading := strings.TrimSpace(title)
	if heading == "" {
		heading = skillName
	}
	return "# " + heading + "\n\n" + text + "\n"
}
