package main

import "bqagent/internal/herdr"

func newHerdrReporter(getenv func(string) string, options cliOptions) *herdr.Reporter {
	// Only foreground terminal agents own a pane, never inherited workers or
	// multiplexed HTTP/bot services running under the same environment.
	if options.background || options.sessionRun || options.server || options.serverRun || options.subagentRun != "" || options.doctor || options.ilinkLogin || options.ilinkStatus {
		return nil
	}
	return herdr.New(getenv, []string{"bqagent", "--chat"})
}
