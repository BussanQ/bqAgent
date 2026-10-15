package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
)

const clipboardTimeout = 5 * time.Second

// imageAttachMsg carries prepared images back to the input. text is the plain
// paste to insert instead when nothing could be attached.
type imageAttachMsg struct {
	images []Image
	text   string
	err    error
}

// clipboardImage is what the system clipboard offered: copied image files or
// raw image bytes such as a screenshot.
type clipboardImage struct {
	paths []string
	data  []byte
}

type clipboardReader struct {
	goos     string
	getenv   func(string) string
	lookPath func(string) (string, error)
	run      func(context.Context, string, ...string) ([]byte, error)
	readText func() (string, error)
}

func systemClipboardReader() clipboardReader {
	return clipboardReader{
		goos:     runtime.GOOS,
		getenv:   os.Getenv,
		lookPath: exec.LookPath,
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
		readText: clipboard.ReadAll,
	}
}

// pasteClipboard prefers a clipboard image and otherwise falls back to the
// clipboard text, so Ctrl+V keeps working as a normal paste.
func pasteClipboard(reader clipboardReader) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), clipboardTimeout)
		defer cancel()
		content, err := reader.readImage(ctx)
		if err == nil {
			if message := prepareClipboardImages(content); len(message.images) > 0 || message.err != nil {
				return message
			}
		}
		// A failed image probe must not break the ordinary text paste.
		if reader.readText == nil {
			return imageAttachMsg{err: err}
		}
		text, textErr := reader.readText()
		if textErr != nil || text == "" {
			return imageAttachMsg{err: err}
		}
		if paths := pastedImagePaths(text); len(paths) > 0 {
			message := prepareClipboardImages(clipboardImage{paths: paths})
			message.text = text
			return message
		}
		return imageAttachMsg{text: text}
	}
}

// loadPastedImages turns a bracketed paste of image paths, such as a file
// dragged onto the terminal, into attachments while keeping the original text
// as the fallback.
func loadPastedImages(paths []string, text string) tea.Cmd {
	return func() tea.Msg {
		message := prepareClipboardImages(clipboardImage{paths: paths})
		message.text = text
		return message
	}
}

func prepareClipboardImages(content clipboardImage) imageAttachMsg {
	var message imageAttachMsg
	add := func(data []byte, err error) {
		if err == nil {
			var mimeType string
			mimeType, data, err = prepareImage(data)
			if err == nil {
				message.images = append(message.images, Image{MIMEType: mimeType, Data: data})
				return
			}
		}
		if message.err == nil {
			message.err = err
		}
	}
	for _, path := range content.paths {
		if !isImagePath(path) {
			continue
		}
		add(loadImageFile(path))
	}
	if len(content.data) > 0 {
		add(content.data, nil)
	}
	return message
}

func (reader clipboardReader) readImage(ctx context.Context) (clipboardImage, error) {
	switch reader.goos {
	case "darwin":
		return reader.readFromScript(ctx, func(out string) (string, []string) {
			return "osascript", append(appleScriptLines(), out)
		})
	case "windows":
		return reader.readFromScript(ctx, func(out string) (string, []string) {
			return "powershell", []string{"-NoProfile", "-NonInteractive", "-STA", "-Command", powerShellClipboardScript(out)}
		})
	case "linux", "freebsd", "openbsd", "netbsd":
		return reader.readUnix(ctx)
	}
	return clipboardImage{}, nil
}

// readFromScript runs a platform script that prints "file:<path>" lines for
// copied files, "png" after saving image data to out, or "none".
func (reader clipboardReader) readFromScript(ctx context.Context, command func(string) (string, []string)) (clipboardImage, error) {
	file, err := os.CreateTemp("", "bqagent-clipboard-*.png")
	if err != nil {
		return clipboardImage{}, err
	}
	out := file.Name()
	file.Close()
	defer os.Remove(out)
	name, args := command(out)
	if _, err := reader.lookPath(name); err != nil {
		return clipboardImage{}, nil
	}
	output, err := reader.run(ctx, name, args...)
	if err != nil {
		return clipboardImage{}, fmt.Errorf("读取剪贴板失败：%w", err)
	}
	var content clipboardImage
	for _, line := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "file:"):
			content.paths = append(content.paths, strings.TrimPrefix(line, "file:"))
		case line == "png":
			data, err := loadImageFile(out)
			if err != nil {
				return clipboardImage{}, err
			}
			content.data = data
		}
	}
	return content, nil
}

func appleScriptLines() []string {
	// Plain text also coerces to «class furl», so only read a file URL when
	// the clipboard really holds one (a file copied in Finder).
	lines := []string{
		"on run argv",
		"set hasFile to false",
		"repeat with entry in (clipboard info)",
		"if item 1 of entry is «class furl» then set hasFile to true",
		"end repeat",
		"if hasFile then",
		"set copiedFile to the clipboard as «class furl»",
		"return \"file:\" & POSIX path of copiedFile",
		"end if",
		"try",
		"set imageData to the clipboard as «class PNGf»",
		"set imageFile to open for access (POSIX file (item 1 of argv)) with write permission",
		"set eof imageFile to 0",
		"write imageData to imageFile",
		"close access imageFile",
		"return \"png\"",
		"end try",
		"return \"none\"",
		"end run",
	}
	args := make([]string, 0, 2*len(lines))
	for _, line := range lines {
		args = append(args, "-e", line)
	}
	return args
}

func powerShellClipboardScript(out string) string {
	return strings.Join([]string{
		"[Console]::OutputEncoding = [System.Text.Encoding]::UTF8",
		"Add-Type -AssemblyName System.Windows.Forms",
		"Add-Type -AssemblyName System.Drawing",
		"$files = [System.Windows.Forms.Clipboard]::GetFileDropList()",
		"if ($files -and $files.Count -gt 0) { foreach ($file in $files) { 'file:' + $file }; exit }",
		"$image = [System.Windows.Forms.Clipboard]::GetImage()",
		"if ($image) { $image.Save('" + strings.ReplaceAll(out, "'", "''") + "', [System.Drawing.Imaging.ImageFormat]::Png); 'png'; exit }",
		"'none'",
	}, "; ")
}

// readUnix uses wl-paste on Wayland and xclip on X11. Without either tool
// there is no way to read images, so Ctrl+V stays a text paste.
func (reader clipboardReader) readUnix(ctx context.Context) (clipboardImage, error) {
	var list []string
	var get func(string) []string
	var name string
	if reader.getenv("WAYLAND_DISPLAY") != "" {
		if _, err := reader.lookPath("wl-paste"); err == nil {
			name, list = "wl-paste", []string{"--list-types"}
			get = func(mimeType string) []string { return []string{"--no-newline", "--type", mimeType} }
		}
	}
	if name == "" {
		if _, err := reader.lookPath("xclip"); err != nil {
			return clipboardImage{}, nil
		}
		name, list = "xclip", []string{"-selection", "clipboard", "-t", "TARGETS", "-o"}
		get = func(mimeType string) []string { return []string{"-selection", "clipboard", "-t", mimeType, "-o"} }
	}
	output, err := reader.run(ctx, name, list...)
	if err != nil {
		return clipboardImage{}, nil
	}
	types := map[string]bool{}
	for _, line := range strings.Split(string(output), "\n") {
		types[strings.TrimSpace(line)] = true
	}
	if types["text/uri-list"] {
		if data, err := reader.run(ctx, name, get("text/uri-list")...); err == nil {
			var content clipboardImage
			for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") && strings.HasPrefix(strings.ToLower(line), "file://") {
					content.paths = append(content.paths, clipboardPath(line))
				}
			}
			if len(content.paths) > 0 {
				return content, nil
			}
		}
	}
	for _, mimeType := range []string{"image/png", "image/jpeg", "image/gif"} {
		if !types[mimeType] {
			continue
		}
		data, err := reader.run(ctx, name, get(mimeType)...)
		if err != nil {
			return clipboardImage{}, fmt.Errorf("读取剪贴板失败：%w", err)
		}
		if len(data) == 0 {
			return clipboardImage{}, errors.New("剪贴板图片为空")
		}
		return clipboardImage{data: data}, nil
	}
	return clipboardImage{}, nil
}
