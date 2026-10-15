package tui

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

// Limits match the WebUI so every channel sends the same image payloads.
const (
	maxPromptImages        = 4
	maxPromptImageBytes    = 3 << 20
	maxPromptImagesBytes   = 8 << 20
	maxPromptImagePixels   = 20_000_000
	maxSourceImageBytes    = 64 << 20
	maxSourceImagePixels   = 200_000_000
	compressedImageQuality = 85
)

var promptImageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true}

// prepareImage validates raw image bytes. Oversized PNG/JPEG images, such as
// Retina screenshots, are flattened onto white and re-encoded as JPEG, halving
// their dimensions until they fit the per-image limits.
func prepareImage(data []byte) (string, []byte, error) {
	if len(data) == 0 {
		return "", nil, errors.New("图片为空")
	}
	if len(data) > maxSourceImageBytes {
		return "", nil, fmt.Errorf("图片超过 %d MiB", maxSourceImageBytes>>20)
	}
	mimeType := http.DetectContentType(data)
	if !promptImageTypes[mimeType] {
		return "", nil, errors.New("仅支持 PNG、JPEG 和 GIF 图片")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", nil, fmt.Errorf("无法识别图片：%w", err)
	}
	pixels := int64(config.Width) * int64(config.Height)
	if config.Width <= 0 || config.Height <= 0 {
		return "", nil, errors.New("图片尺寸无效")
	}
	if len(data) <= maxPromptImageBytes && pixels <= maxPromptImagePixels {
		return mimeType, data, nil
	}
	if mimeType == "image/gif" {
		return "", nil, errors.New("GIF 图片不能超过 3 MiB 或 2000 万像素")
	}
	if pixels > maxSourceImagePixels {
		return "", nil, errors.New("图片尺寸过大")
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", nil, fmt.Errorf("无法解码图片：%w", err)
	}
	return compressImage(decoded)
}

func compressImage(source image.Image) (string, []byte, error) {
	bounds := source.Bounds()
	canvas := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(canvas, canvas.Bounds(), source, bounds.Min, draw.Over)
	for int64(canvas.Bounds().Dx())*int64(canvas.Bounds().Dy()) > maxPromptImagePixels {
		canvas = halveImage(canvas)
	}
	for {
		var encoded bytes.Buffer
		if err := jpeg.Encode(&encoded, canvas, &jpeg.Options{Quality: compressedImageQuality}); err != nil {
			return "", nil, fmt.Errorf("压缩图片失败：%w", err)
		}
		if encoded.Len() <= maxPromptImageBytes {
			return "image/jpeg", encoded.Bytes(), nil
		}
		if canvas.Bounds().Dx() < 64 || canvas.Bounds().Dy() < 64 {
			return "", nil, errors.New("图片压缩后仍超过 3 MiB")
		}
		canvas = halveImage(canvas)
	}
}

// halveImage averages each 2x2 block, which keeps text in screenshots legible
// better than dropping pixels.
func halveImage(source *image.RGBA) *image.RGBA {
	width, height := max(1, source.Bounds().Dx()/2), max(1, source.Bounds().Dy()/2)
	target := image.NewRGBA(image.Rect(0, 0, width, height))
	maxX, maxY := source.Bounds().Dx()-1, source.Bounds().Dy()-1
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			var sum [4]int
			for _, offset := range [4][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
				index := source.PixOffset(min(maxX, 2*x+offset[0]), min(maxY, 2*y+offset[1]))
				for channel := range sum {
					sum[channel] += int(source.Pix[index+channel])
				}
			}
			index := target.PixOffset(x, y)
			for channel, value := range sum {
				target.Pix[index+channel] = uint8(value / 4)
			}
		}
	}
	return target
}

// validateImageSet enforces the per-message limits on prepared images.
func validateImageSet(images []Image) error {
	if len(images) > maxPromptImages {
		return fmt.Errorf("每条消息最多附带 %d 张图片", maxPromptImages)
	}
	total := 0
	for _, image := range images {
		total += len(image.Data)
	}
	if total > maxPromptImagesBytes {
		return fmt.Errorf("图片总大小不能超过 %d MiB", maxPromptImagesBytes>>20)
	}
	return nil
}

// stripImageLabels removes image placeholders from text kept in prompt
// history, where recalled labels would no longer have images behind them.
func stripImageLabels(value string, images []Image) string {
	for _, image := range images {
		value = strings.ReplaceAll(value, image.Label, "")
	}
	return strings.TrimSpace(value)
}

func loadImageFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("无法读取图片：%w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("不是图片文件")
	}
	if info.Size() > maxSourceImageBytes {
		return nil, fmt.Errorf("图片超过 %d MiB", maxSourceImageBytes>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("无法读取图片：%w", err)
	}
	return data, nil
}

// pastedImagePaths recognises pasted or dragged image files: one or more
// shell-quoted paths or file:// URLs, all naming existing PNG/JPEG/GIF files.
// Anything else, including prose that mentions a path, stays a text paste.
func pastedImagePaths(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 16<<10 {
		return nil
	}
	words, ok := splitPastedWords(text, runtime.GOOS != "windows")
	if !ok || len(words) == 0 {
		return nil
	}
	paths := make([]string, 0, len(words))
	for _, word := range words {
		path := clipboardPath(word)
		if !isImagePath(path) {
			return nil
		}
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return nil
		}
		paths = append(paths, path)
	}
	return paths
}

func isImagePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif":
		return true
	}
	return false
}

// clipboardPath converts a file:// URL or ~/ path into a local file path.
func clipboardPath(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "file://") {
		if parsed, err := url.Parse(value); err == nil {
			path := parsed.Path
			if runtime.GOOS == "windows" {
				path = strings.TrimPrefix(path, "/")
			}
			return filepath.FromSlash(path)
		}
	}
	if strings.HasPrefix(value, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, value[2:])
		}
	}
	return value
}

// splitPastedWords splits like a shell: quotes group words and, outside
// Windows, a backslash escapes the next character as terminals do for drags.
func splitPastedWords(text string, escapes bool) ([]string, bool) {
	var words []string
	var word strings.Builder
	inWord, quote, escaped := false, rune(0), false
	for _, char := range text {
		switch {
		case escaped:
			word.WriteRune(char)
			escaped = false
		case escapes && char == '\\' && quote != '\'':
			escaped, inWord = true, true
		case quote != 0:
			if char == quote {
				quote = 0
			} else {
				word.WriteRune(char)
			}
		case char == '\'' || char == '"':
			quote, inWord = char, true
		case unicode.IsSpace(char):
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteRune(char)
			inWord = true
		}
	}
	if quote != 0 || escaped {
		return nil, false
	}
	if inWord {
		words = append(words, word.String())
	}
	return words, true
}

func formatImageBytes(size int) string {
	if size < 1<<20 {
		return fmt.Sprintf("%.0f KiB", max(1, float64(size)/1024))
	}
	return fmt.Sprintf("%.1f MiB", float64(size)/(1<<20))
}
