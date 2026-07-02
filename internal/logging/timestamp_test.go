package logging

import (
	"bytes"
	"testing"
	"time"
)

func TestTimestampWriterPrefixesEachLine(t *testing.T) {
	var output bytes.Buffer
	writer := NewTimestampWriter(&output).(*TimestampWriter)
	writer.now = func() time.Time { return time.Date(2026, 7, 3, 1, 2, 3, 0, time.UTC) }

	if _, err := writer.Write([]byte("first\nsecond")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if _, err := writer.Write([]byte(" continued\n")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if _, err := writer.Write([]byte("\n")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	want := "" +
		"2026-07-03 01:02:03 first\n" +
		"2026-07-03 01:02:03 second continued\n" +
		"2026-07-03 01:02:03 \n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}
