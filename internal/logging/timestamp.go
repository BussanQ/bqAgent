package logging

import (
	"bytes"
	"io"
	"sync"
	"time"
)

const timestampFormat = "2006-01-02 15:04:05"

type TimestampWriter struct {
	mu          sync.Mutex
	inner       io.Writer
	now         func() time.Time
	atLineStart bool
}

func NewTimestampWriter(inner io.Writer) io.Writer {
	if inner == nil {
		return nil
	}
	return &TimestampWriter{
		inner:       inner,
		now:         time.Now,
		atLineStart: true,
	}
}

func (writer *TimestampWriter) Write(content []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()

	var buffer bytes.Buffer
	for _, char := range content {
		if writer.atLineStart {
			buffer.WriteString(writer.now().Format(timestampFormat))
			buffer.WriteByte(' ')
			writer.atLineStart = false
		}
		buffer.WriteByte(char)
		if char == '\n' {
			writer.atLineStart = true
		}
	}
	if buffer.Len() == 0 {
		return 0, nil
	}
	if _, err := writer.inner.Write(buffer.Bytes()); err != nil {
		return 0, err
	}
	return len(content), nil
}
