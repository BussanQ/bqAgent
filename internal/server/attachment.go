package server

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"bqagent/internal/atomicfile"
	"bqagent/internal/safepath"
	"bqagent/internal/session"
)

const maxAttachmentInlineBytes = 64 << 10

type resolvedFileAttachment struct {
	Name      string
	Path      string
	Data      []byte
	Text      bool
	Truncated bool
}

func (service *Service) materializeFiles(sessionID string, files []FileAttachment) ([]resolvedFileAttachment, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if len(files) > maxWebUIFiles {
		return nil, fmt.Errorf("at most %d files are allowed", maxWebUIFiles)
	}
	canonicalID, err := session.CanonicalID(sessionID)
	if err != nil {
		return nil, err
	}
	rootPath, err := filepath.Abs(service.workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open workspace root: %w", err)
	}
	defer root.Close()

	resolved := make([]resolvedFileAttachment, 0, len(files))
	totalBytes := 0
	for index, file := range files {
		var attachment resolvedFileAttachment
		switch {
		case strings.TrimSpace(file.Path) != "" && file.Name == "" && file.Data == nil:
			attachment, err = service.readWorkspaceAttachment(root, file.Path)
		case strings.TrimSpace(file.Path) == "" && strings.TrimSpace(file.Name) != "":
			if len(file.Data) > maxWebUIFileBytes {
				err = fmt.Errorf("uploaded file exceeds %d bytes", maxWebUIFileBytes)
				break
			}
			attachment, err = writeUploadedAttachment(root, canonicalID, file.Name, file.Data)
		default:
			err = fmt.Errorf("provide either uploaded file data or a workspace path")
		}
		if err != nil {
			return nil, fmt.Errorf("file %d: %w", index+1, err)
		}
		totalBytes += len(attachment.Data)
		if totalBytes > maxWebUITotalFileBytes {
			return nil, fmt.Errorf("file data exceeds %d bytes", maxWebUITotalFileBytes)
		}
		attachment.Text = utf8.Valid(attachment.Data) && !bytes.ContainsRune(attachment.Data, '\x00')
		if attachment.Text && len(attachment.Data) > maxAttachmentInlineBytes {
			attachment.Data = truncateUTF8(attachment.Data, maxAttachmentInlineBytes)
			attachment.Truncated = true
		}
		resolved = append(resolved, attachment)
	}
	return resolved, nil
}

func writeUploadedAttachment(root *os.Root, sessionID, name string, data []byte) (resolvedFileAttachment, error) {
	name, err := sanitizeUploadName(name)
	if err != nil {
		return resolvedFileAttachment{}, err
	}
	dir := filepath.Join(".agent", "uploads", sessionID)
	if err := root.MkdirAll(dir, 0o755); err != nil {
		return resolvedFileAttachment{}, fmt.Errorf("create upload directory: %w", err)
	}
	availableName, err := availableUploadName(root, dir, name)
	if err != nil {
		return resolvedFileAttachment{}, err
	}
	relative := filepath.Join(dir, availableName)
	if err := atomicfile.WriteRoot(root, relative, data, 0o644); err != nil {
		return resolvedFileAttachment{}, fmt.Errorf("write uploaded file: %w", err)
	}
	return resolvedFileAttachment{Name: availableName, Path: filepath.ToSlash(relative), Data: data}, nil
}

func sanitizeUploadName(name string) (string, error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, `\`, "/"))
	name = path.Base(name)
	if strings.ContainsRune(name, '\x00') || len(name) > 240 {
		return "", fmt.Errorf("invalid uploaded file name")
	}
	if err := safepath.ValidateComponent(name); err != nil {
		return "", fmt.Errorf("invalid uploaded file name: %w", err)
	}
	return name, nil
}

func availableUploadName(root *os.Root, dir, name string) (string, error) {
	extension := filepath.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	for suffix := 1; ; suffix++ {
		candidate := name
		if suffix > 1 {
			candidate = fmt.Sprintf("%s-%d%s", stem, suffix, extension)
		}
		_, err := root.Stat(filepath.Join(dir, candidate))
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("inspect upload destination: %w", err)
		}
	}
}

func (service *Service) readWorkspaceAttachment(root *os.Root, input string) (resolvedFileAttachment, error) {
	_, relative, err := safepath.Relative(service.workspaceRoot, strings.TrimSpace(input))
	if err != nil {
		return resolvedFileAttachment{}, fmt.Errorf("path must be inside workspace: %w", err)
	}
	pathInfo, err := root.Stat(relative)
	if err != nil {
		return resolvedFileAttachment{}, fmt.Errorf("stat workspace file: %w", err)
	}
	if !pathInfo.Mode().IsRegular() {
		return resolvedFileAttachment{}, fmt.Errorf("path must refer to a regular file")
	}
	file, err := root.Open(relative)
	if err != nil {
		return resolvedFileAttachment{}, fmt.Errorf("open workspace file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return resolvedFileAttachment{}, fmt.Errorf("stat workspace file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return resolvedFileAttachment{}, fmt.Errorf("path must refer to a regular file")
	}
	if info.Size() > maxWebUIFileBytes {
		return resolvedFileAttachment{}, fmt.Errorf("file exceeds %d bytes", maxWebUIFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxWebUIFileBytes+1))
	if err != nil {
		return resolvedFileAttachment{}, fmt.Errorf("read workspace file: %w", err)
	}
	if len(data) > maxWebUIFileBytes {
		return resolvedFileAttachment{}, fmt.Errorf("file exceeds %d bytes", maxWebUIFileBytes)
	}
	return resolvedFileAttachment{Name: filepath.Base(relative), Path: filepath.ToSlash(relative), Data: data}, nil
}

func formatAttachmentContext(files []resolvedFileAttachment) string {
	if len(files) == 0 {
		return ""
	}
	var context strings.Builder
	for _, file := range files {
		fmt.Fprintf(&context, "\n\n<attachment name=\"%s\" path=\"%s\">\n", html.EscapeString(file.Name), html.EscapeString(file.Path))
		switch {
		case !file.Text:
			context.WriteString("[二进制或非 UTF-8 文件，内容未内联；可通过上述 workspace 路径使用工具读取。]")
		default:
			context.Write(file.Data)
			if file.Truncated {
				context.WriteString("\n[内容已截断；可通过上述 workspace 路径使用 read_file 读取完整内容。]")
			}
		}
		context.WriteString("\n</attachment>")
	}
	return context.String()
}

func truncateUTF8(data []byte, limit int) []byte {
	if len(data) <= limit {
		return data
	}
	end := limit
	for end > 0 && !utf8.RuneStart(data[end]) {
		end--
	}
	return data[:end]
}
