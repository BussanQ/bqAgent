package tui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

type imageFakeBackend struct {
	fakeBackend
	mu     sync.Mutex
	sent   string
	images []Image
	files  []string
	called chan struct{}
}

func (backend *imageFakeBackend) RunTurnWithAttachments(_ context.Context, _ string, message string, attachments TurnAttachments, _ TurnEvents) (TurnResult, error) {
	backend.mu.Lock()
	backend.sent, backend.images, backend.files = message, attachments.Images, attachments.Files
	backend.mu.Unlock()
	if backend.called != nil {
		close(backend.called)
	}
	return TurnResult{}, nil
}

func testPNG(t *testing.T, width, height int, noisy bool) []byte {
	t.Helper()
	canvas := image.NewNRGBA(image.Rect(0, 0, width, height))
	random := rand.New(rand.NewSource(1))
	for index := 0; index < len(canvas.Pix); index += 4 {
		if noisy {
			canvas.Pix[index], canvas.Pix[index+1], canvas.Pix[index+2] = uint8(random.Intn(256)), uint8(random.Intn(256)), uint8(random.Intn(256))
		} else {
			canvas.Pix[index], canvas.Pix[index+1], canvas.Pix[index+2] = 30, 120, 200
		}
		canvas.Pix[index+3] = 255
	}
	var encoded bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&encoded, canvas); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func testImage(label string, size int) Image {
	return Image{Label: label, MIMEType: "image/png", Data: make([]byte, size)}
}

func TestPrepareImageKeepsSmallImagesAndCompressesOversizedOnes(t *testing.T) {
	small := testPNG(t, 8, 8, false)
	mimeType, data, err := prepareImage(small)
	if err != nil || mimeType != "image/png" || !bytes.Equal(data, small) {
		t.Fatalf("small image = %q %d bytes err=%v, want unchanged PNG", mimeType, len(data), err)
	}

	heavy := testPNG(t, 1200, 1200, true)
	if len(heavy) <= maxPromptImageBytes {
		t.Fatalf("test fixture is only %d bytes", len(heavy))
	}
	mimeType, data, err = prepareImage(heavy)
	if err != nil || mimeType != "image/jpeg" || len(data) > maxPromptImageBytes {
		t.Fatalf("heavy image = %q %d bytes err=%v, want JPEG within 3 MiB", mimeType, len(data), err)
	}

	large := testPNG(t, 4600, 4400, false)
	mimeType, data, err = prepareImage(large)
	if err != nil || mimeType != "image/jpeg" {
		t.Fatalf("large image = %q err=%v", mimeType, err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width*config.Height > maxPromptImagePixels || config.Width != 2300 {
		t.Fatalf("large image resized to %dx%d err=%v", config.Width, config.Height, err)
	}

	for name, data := range map[string][]byte{"text": []byte("not an image"), "empty": nil, "truncated": small[:20]} {
		if _, _, err := prepareImage(data); err == nil {
			t.Fatalf("%s should be rejected", name)
		}
	}
}

func TestHalveImageAveragesPixelBlocks(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 2, 2))
	source.Set(0, 0, color.RGBA{0, 0, 0, 255})
	source.Set(1, 0, color.RGBA{200, 0, 0, 255})
	source.Set(0, 1, color.RGBA{0, 200, 0, 255})
	source.Set(1, 1, color.RGBA{0, 0, 200, 255})
	got := halveImage(source).RGBAAt(0, 0)
	if got != (color.RGBA{50, 50, 50, 255}) {
		t.Fatalf("halved pixel = %#v", got)
	}
}

func TestPastedImagePathsRecognisesQuotedEscapedAndFileURLs(t *testing.T) {
	dir := t.TempDir()
	spaced := filepath.Join(dir, "my shot.png")
	plain := filepath.Join(dir, "b.JPG")
	notes := filepath.Join(dir, "notes.txt")
	for _, path := range []string{spaced, plain, notes} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string][]string{
		"'" + spaced + "'": {spaced},
		strings.ReplaceAll(spaced, " ", `\ `) + " " + plain: {spaced, plain},
		"file://" + strings.ReplaceAll(spaced, " ", "%20"):  {spaced},
		"  " + plain + "\n":               {plain},
		"look at " + plain:                nil,
		notes:                             nil,
		filepath.Join(dir, "missing.png"): nil,
		dir:                               nil,
		"'" + plain:                       nil,
	}
	for input, want := range cases {
		got := pastedImagePaths(input)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("pastedImagePaths(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestClipboardReaderParsesPlatformOutput(t *testing.T) {
	pngData := testPNG(t, 4, 4, false)
	found := func(string) (string, error) { return "/bin/tool", nil }
	missing := func(string) (string, error) { return "", errors.New("missing") }

	darwin := clipboardReader{goos: "darwin", lookPath: found, run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "osascript" {
			t.Fatalf("darwin command = %s", name)
		}
		return []byte("png\n"), os.WriteFile(args[len(args)-1], pngData, 0o600)
	}}
	content, err := darwin.readImage(context.Background())
	if err != nil || !bytes.Equal(content.data, pngData) {
		t.Fatalf("darwin image = %d bytes err=%v", len(content.data), err)
	}

	windows := clipboardReader{goos: "windows", lookPath: found, run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "powershell" || !strings.Contains(args[len(args)-1], "GetFileDropList") {
			t.Fatalf("windows command = %s %q", name, args)
		}
		return []byte("file:C:\\shots\\a.png\r\nfile:C:\\shots\\b.gif\r\n"), nil
	}}
	content, err = windows.readImage(context.Background())
	if err != nil || strings.Join(content.paths, "|") != `C:\shots\a.png|C:\shots\b.gif` {
		t.Fatalf("windows paths = %q err=%v", content.paths, err)
	}

	var calls []string
	x11 := clipboardReader{goos: "linux", getenv: func(string) string { return "" }, lookPath: found, run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if strings.Contains(strings.Join(args, " "), "TARGETS") {
			return []byte("TARGETS\nimage/png\ntext/plain\n"), nil
		}
		return pngData, nil
	}}
	content, err = x11.readImage(context.Background())
	if err != nil || !bytes.Equal(content.data, pngData) || calls[1] != "xclip -selection clipboard -t image/png -o" {
		t.Fatalf("x11 image = %d bytes calls=%q err=%v", len(content.data), calls, err)
	}

	wayland := clipboardReader{goos: "linux", getenv: func(string) string { return "wayland-0" }, lookPath: found, run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "wl-paste" {
			t.Fatalf("wayland command = %s", name)
		}
		if args[0] == "--list-types" {
			return []byte("text/uri-list\nimage/png\n"), nil
		}
		return []byte("# copied\r\nfile:///home/me/My%20Shot.png\r\n"), nil
	}}
	content, err = wayland.readImage(context.Background())
	if err != nil || len(content.paths) != 1 || content.paths[0] != filepath.FromSlash("/home/me/My Shot.png") {
		t.Fatalf("wayland paths = %q err=%v", content.paths, err)
	}

	none := clipboardReader{goos: "linux", getenv: func(string) string { return "" }, lookPath: missing}
	if content, err := none.readImage(context.Background()); err != nil || len(content.paths)+len(content.data) != 0 {
		t.Fatalf("missing tools = %#v err=%v", content, err)
	}
}

func TestPasteClipboardPrefersImageAndFallsBackToText(t *testing.T) {
	pngData := testPNG(t, 4, 4, false)
	withImage := clipboardReader{goos: "linux", getenv: func(string) string { return "" }, lookPath: func(string) (string, error) { return "xclip", nil },
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if strings.Contains(strings.Join(args, " "), "TARGETS") {
				return []byte("image/png\n"), nil
			}
			return pngData, nil
		},
		readText: func() (string, error) { t.Fatal("text must not be read when an image is present"); return "", nil },
	}
	message := pasteClipboard(withImage)().(imageAttachMsg)
	if len(message.images) != 1 || message.images[0].MIMEType != "image/png" || message.text != "" || message.err != nil {
		t.Fatalf("image paste = %#v", message)
	}

	textOnly := clipboardReader{goos: "plan9", readText: func() (string, error) { return "plain text", nil }}
	message = pasteClipboard(textOnly)().(imageAttachMsg)
	if len(message.images) != 0 || message.text != "plain text" || message.err != nil {
		t.Fatalf("text paste = %#v", message)
	}

	path := filepath.Join(t.TempDir(), "copied.png")
	if err := os.WriteFile(path, pngData, 0o600); err != nil {
		t.Fatal(err)
	}
	pathText := clipboardReader{goos: "plan9", readText: func() (string, error) { return path, nil }}
	message = pasteClipboard(pathText)().(imageAttachMsg)
	if len(message.images) != 1 || message.text != path {
		t.Fatalf("copied path paste = %#v", message)
	}
}

func TestPromptImageChipsAreAtomicOrderedAndLimited(t *testing.T) {
	input := newPromptInput(true)
	input.setValue("看 ")
	if err := input.attachImage(testImage("", 10)); err != nil {
		t.Fatal(err)
	}
	input.area.InsertString(" 和 ")
	if err := input.attachImage(testImage("", 20)); err != nil {
		t.Fatal(err)
	}
	if input.displayValue() != "看 [图片 #1] 和 [图片 #2]" || input.value() != input.displayValue() {
		t.Fatalf("display=%q value=%q", input.displayValue(), input.value())
	}
	images := input.images()
	if len(images) != 2 || images[0].Label != "[图片 #1]" || len(images[1].Data) != 20 {
		t.Fatalf("images = %#v", images)
	}
	input.update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if input.displayValue() != "看 [图片 #1] 和 " || len(input.images()) != 1 {
		t.Fatalf("backspace did not remove the image atomically: %q", input.displayValue())
	}
	input.reset()
	if err := input.attachImage(testImage("", 1)); err != nil || input.displayValue() != "[图片 #3]" {
		t.Fatalf("labels must stay unique after reset: %q err=%v", input.displayValue(), err)
	}
	for range maxPromptImages - 1 {
		if err := input.attachImage(testImage("", 1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := input.attachImage(testImage("", 1)); err == nil || len(input.images()) != maxPromptImages {
		t.Fatalf("fifth image err=%v images=%d", err, len(input.images()))
	}
	input.reset()
	if err := input.attachImage(testImage("", maxPromptImagesBytes)); err != nil {
		t.Fatal(err)
	}
	if err := input.attachImage(testImage("", 1)); err == nil {
		t.Fatal("total image size limit was not enforced")
	}
	input.restoreDraft("还原 [图片 #9]", []Image{testImage("[图片 #9]", 3), testImage("[图片 #10]", 4)})
	if restored := input.images(); len(restored) != 1 || restored[0].Label != "[图片 #9]" {
		t.Fatalf("restored images = %#v", restored)
	}
}

func TestImageAttachMessageInsertsChipOrFallsBackToText(t *testing.T) {
	model := NewModel(&imageFakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.width, model.height = 80, 24
	updated, _ := model.Update(imageAttachMsg{images: []Image{testImage("", 2048)}})
	model = updated.(Model)
	if model.input.displayValue() != "[图片 #1]" || !strings.Contains(model.renderStatus(80), "已添加 [图片 #1]") {
		t.Fatalf("attached display=%q status=%q", model.input.displayValue(), model.renderStatus(80))
	}
	updated, _ = model.Update(imageAttachMsg{text: " 文本"})
	model = updated.(Model)
	if model.input.displayValue() != "[图片 #1] 文本" {
		t.Fatalf("text fallback display=%q", model.input.displayValue())
	}
	updated, _ = model.Update(imageAttachMsg{err: errors.New("仅支持 PNG、JPEG 和 GIF 图片")})
	model = updated.(Model)
	if !strings.Contains(model.renderStatus(80), "图片未添加：仅支持") {
		t.Fatalf("error status = %q", model.renderStatus(80))
	}
	model.handleKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if strings.Contains(model.renderStatus(80), "图片未添加") {
		t.Fatal("the next key press should clear the image notice")
	}
}

func TestPastedImagePathLoadsImageInsteadOfText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drag.png")
	if err := os.WriteFile(path, testPNG(t, 4, 4, false), 0o600); err != nil {
		t.Fatal(err)
	}
	model := NewModel(&imageFakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	updated, command := model.Update(tea.PasteMsg{Content: "'" + path + "' "})
	model = updated.(Model)
	if model.input.displayValue() != "" || command == nil {
		t.Fatalf("path paste should load asynchronously, display=%q", model.input.displayValue())
	}
	var message imageAttachMsg
	for _, cmd := range flattenCommands(command) {
		if msg, ok := cmd().(imageAttachMsg); ok {
			message = msg
		}
	}
	updated, _ = model.Update(message)
	model = updated.(Model)
	if model.input.displayValue() != "[图片 #1]" || len(model.input.images()) != 1 {
		t.Fatalf("dragged image display=%q", model.input.displayValue())
	}
}

func flattenCommands(command tea.Cmd) []tea.Cmd {
	if command == nil {
		return nil
	}
	message := command()
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		return []tea.Cmd{func() tea.Msg { return message }}
	}
	var commands []tea.Cmd
	for _, nested := range batch {
		commands = append(commands, flattenCommands(nested)...)
	}
	return commands
}

func TestEnterSendsAttachedImagesWithTheTurn(t *testing.T) {
	backend := &imageFakeBackend{called: make(chan struct{})}
	model := NewModel(backend, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.input.setValue("描述 ")
	if err := model.input.attachImage(testImage("", 64)); err != nil {
		t.Fatal(err)
	}
	commands := model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.active == nil || model.input.displayValue() != "" || len(commands) != 1 {
		t.Fatalf("enter did not start a turn: active=%v input=%q", model.active, model.input.displayValue())
	}
	batch := commands[0]().(tea.BatchMsg)
	for _, command := range batch {
		if command != nil {
			go command()
		}
	}
	select {
	case <-backend.called:
	case <-time.After(5 * time.Second):
		t.Fatal("image backend was not called")
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.sent != "描述 [图片 #1]" || len(backend.images) != 1 || backend.images[0].Label != "[图片 #1]" {
		t.Fatalf("sent %q with %#v", backend.sent, backend.images)
	}
	if last := model.history[len(model.history)-1]; last != "描述" {
		t.Fatalf("history entry = %q, want image label stripped", last)
	}
}

func TestImageDraftsAreKeptWhenTheyCannotBeSent(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.input.setValue("看图 ")
	if err := model.input.attachImage(testImage("", 8)); err != nil {
		t.Fatal(err)
	}
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.active != nil || model.input.displayValue() != "看图 [图片 #1]" || len(model.input.images()) != 1 || !strings.Contains(model.renderStatus(80), "当前后端不支持图片输入") {
		t.Fatalf("text-only backend: active=%v input=%q status=%q", model.active, model.input.displayValue(), model.renderStatus(80))
	}

	model = NewModel(&imageFakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.input.setValue("/help ")
	if err := model.input.attachImage(testImage("", 8)); err != nil {
		t.Fatal(err)
	}
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.input.displayValue() != "/help [图片 #1]" || !strings.Contains(model.renderStatus(80), "Slash 命令不能附带图片") {
		t.Fatalf("slash with image: input=%q status=%q", model.input.displayValue(), model.renderStatus(80))
	}
}

func TestQueuedImagesMergeWithinLimitsAndRestoreOnCancel(t *testing.T) {
	model := NewModel(&imageFakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.sequence = 1
	model.active = &activeTurn{sequence: 1, cancel: func() {}}
	model.queue("第一张 [图片 #1]", []Image{testImage("[图片 #1]", 8)})
	model.queue("另三张 [图片 #2][图片 #3][图片 #4]", []Image{testImage("[图片 #2]", 8), testImage("[图片 #3]", 8), testImage("[图片 #4]", 8)})
	if len(model.queued.images) != 4 {
		t.Fatalf("queued images = %d", len(model.queued.images))
	}
	model.queue("第五张 [图片 #5]", []Image{testImage("[图片 #5]", 8)})
	if len(model.queued.images) != 4 || model.input.displayValue() != "第五张 [图片 #5]" || len(model.input.images()) != 1 {
		t.Fatalf("over-limit merge: queued=%d input=%q", len(model.queued.images), model.input.displayValue())
	}

	model.active.cancelled = true
	model.handleTurnEvent(turnEvent{sequence: 1, kind: "done", err: context.Canceled})
	restored := model.input.images()
	if model.queued != nil || len(restored) != 5 || !strings.HasPrefix(model.input.displayValue(), "第一张 [图片 #1]\n另三张 [图片 #2][图片 #3][图片 #4]\n第五张 [图片 #5]") {
		t.Fatalf("cancel restore: input=%q images=%d", model.input.displayValue(), len(restored))
	}
	model.active = nil
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.active != nil || !strings.Contains(model.renderStatus(80), "最多附带 4 张图片") {
		t.Fatalf("restored draft over the limit must not be sent: status=%q", model.renderStatus(80))
	}
}
