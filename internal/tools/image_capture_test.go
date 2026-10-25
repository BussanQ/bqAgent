package tools

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestPNG(t *testing.T, path string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestReadFileAttachesImageToCapture(t *testing.T) {
	dir := t.TempDir()
	data := writeTestPNG(t, filepath.Join(dir, "shot.png"))
	read := ReadFileFromRoot(dir)

	ctx, capture := WithImageCapture(context.Background())
	result, err := read(ctx, map[string]any{"path": "shot.png"})
	if err != nil {
		t.Fatalf("read image: %v", err)
	}
	if !strings.Contains(result, "Image loaded: shot.png (image/png") {
		t.Fatalf("result = %q", result)
	}
	images := capture.Images()
	if len(images) != 1 || images[0].MIMEType != "image/png" || images[0].Path != "shot.png" || !bytes.Equal(images[0].Data, data) {
		t.Fatalf("images = %#v", images)
	}

	if _, err := read(context.Background(), map[string]any{"path": "shot.png"}); err == nil || !strings.Contains(err.Error(), "is an image") {
		t.Fatalf("read without capture err = %v, want image error", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, capture = WithImageCapture(context.Background())
	text, err := read(ctx, map[string]any{"path": "notes.txt"})
	if err != nil || text != "plain text" || len(capture.Images()) != 0 {
		t.Fatalf("text read = %q, err = %v, images = %d", text, err, len(capture.Images()))
	}
}

func TestReadFileRejectsOversizedImage(t *testing.T) {
	dir := t.TempDir()
	data := writeTestPNG(t, filepath.Join(dir, "big.png"))
	data = append(data, make([]byte, MaxToolImageBytes)...)
	if err := os.WriteFile(filepath.Join(dir, "big.png"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, capture := WithImageCapture(context.Background())
	if _, err := ReadFileFromRoot(dir)(ctx, map[string]any{"path": "big.png"}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized err = %v", err)
	}
	if len(capture.Images()) != 0 {
		t.Fatal("oversized image was captured")
	}
}
