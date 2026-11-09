package extagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"bqagent/internal/media"
)

// Pi and Codex CLI accept image paths. Keep these private copies for the
// child's lifetime, outside the workspace, and remove them on every exit path.
func cliImageFiles(images []media.Image) ([]string, func(), error) {
	if len(images) == 0 {
		return nil, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "bqagent-images-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	var paths []string
	for i, img := range images {
		ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}[img.ContentType()]
		if ext == "" || len(img.Data) == 0 {
			cleanup()
			return nil, nil, fmt.Errorf("无效的图片附件 %q", img.ContentType())
		}
		path := filepath.Join(dir, fmt.Sprintf("image-%d%s", i+1, ext))
		if err := os.WriteFile(path, img.Data, 0600); err != nil {
			cleanup()
			return nil, nil, err
		}
		paths = append(paths, path)
	}
	return paths, cleanup, nil
}

func claudeStreamInput(prompt string, images []media.Image) (io.Reader, error) {
	// Plain commands use Claude's string form, just as an SDK user message
	// containing /compact would. Image prompts need typed content blocks.
	var content any = prompt
	if len(images) > 0 {
		blocks := make([]map[string]any, 0, len(images)+1)
		if prompt != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": prompt})
		}
		for _, img := range images {
			blocks = append(blocks, map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": img.ContentType(), "data": img.Base64()}})
		}
		content = blocks
	}
	var input bytes.Buffer
	row := map[string]any{"type": "user", "parent_tool_use_id": nil, "message": map[string]any{"role": "user", "content": content}}
	err := json.NewEncoder(&input).Encode(row)
	return &input, err
}
