package main

import "testing"

func TestShouldUseTUI(t *testing.T) {
	for _, test := range []struct {
		inputTTY  bool
		outputTTY bool
		term      string
		want      bool
	}{
		{true, true, "xterm-256color", true},
		{false, true, "xterm-256color", false},
		{true, false, "xterm-256color", false},
		{true, true, "dumb", false},
	} {
		if got := shouldUseTUI(test.inputTTY, test.outputTTY, test.term); got != test.want {
			t.Fatalf("shouldUseTUI(%v,%v,%q) = %v, want %v", test.inputTTY, test.outputTTY, test.term, got, test.want)
		}
	}
	options, _, err := parseCLI([]string{"--chat", "--stream"})
	if err != nil || !options.chat || !options.stream {
		t.Fatalf("--stream compatibility: options=%#v err=%v", options, err)
	}
}
