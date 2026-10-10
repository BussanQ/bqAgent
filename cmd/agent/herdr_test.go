package main

import "testing"

func TestHerdrSkipsNonForegroundModes(t *testing.T) {
	for _, options := range []cliOptions{{background: true}, {sessionRun: true}, {server: true}, {serverRun: true}, {subagentRun: "worker"}, {doctor: true}, {ilinkLogin: true}, {ilinkStatus: true}} {
		if reporter := newHerdrReporter(func(string) string { t.Fatal("excluded mode read Herdr environment"); return "" }, options); reporter != nil {
			reporter.Close()
			t.Fatalf("mode claimed pane: %+v", options)
		}
	}
	if reporter := newHerdrReporter(func(string) string { return "" }, cliOptions{chat: true}); reporter != nil {
		reporter.Close()
		t.Fatal("ordinary terminal enabled Herdr")
	}
}
