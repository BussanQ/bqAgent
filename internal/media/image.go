package media

import (
	"encoding/base64"
	"strings"
)

// Image contains decoded attachment bytes shared by the chat transports.
type Image struct {
	MIMEType string
	Data     []byte
}

func (i Image) ContentType() string {
	if mime := strings.TrimSpace(i.MIMEType); mime != "" {
		return mime
	}
	return "image/jpeg"
}

func (i Image) Base64() string  { return base64.StdEncoding.EncodeToString(i.Data) }
func (i Image) DataURL() string { return "data:" + i.ContentType() + ";base64," + i.Base64() }
