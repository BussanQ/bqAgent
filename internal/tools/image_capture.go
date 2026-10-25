package tools

import (
	"context"
	"sync"
)

// MaxToolImageBytes bounds one image file attached by read_file. It matches the
// smallest provider limit (Anthropic base64 images) so every API accepts it.
const MaxToolImageBytes = 5 << 20

// ToolImage is an image file read by a tool, to be shown to the model in a
// follow-up multimodal user message.
type ToolImage struct {
	Path     string
	MIMEType string
	Data     []byte
}

// ImageCapture collects images produced by tool calls made with its context.
type ImageCapture struct {
	mu     sync.Mutex
	images []ToolImage
}

type imageCaptureKey struct{}

// WithImageCapture returns a ctx whose tool calls may attach images to the
// returned capture. Without a capture, read_file refuses image files.
func WithImageCapture(ctx context.Context) (context.Context, *ImageCapture) {
	capture := &ImageCapture{}
	return context.WithValue(ctx, imageCaptureKey{}, capture), capture
}

// Images returns a copy of the captured images in call order.
func (capture *ImageCapture) Images() []ToolImage {
	if capture == nil {
		return nil
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return append([]ToolImage(nil), capture.images...)
}

func imageCaptureFromContext(ctx context.Context) *ImageCapture {
	capture, _ := ctx.Value(imageCaptureKey{}).(*ImageCapture)
	return capture
}

func (capture *ImageCapture) add(image ToolImage) {
	capture.mu.Lock()
	capture.images = append(capture.images, image)
	capture.mu.Unlock()
}
