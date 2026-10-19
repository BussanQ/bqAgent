package main

import (
	"flag"
	"fmt"
	"strings"

	"bqagent/internal/session"
	"bqagent/internal/workspace"
)

// normalizeResumeArgs supplies an empty value for a bare --resume while
// preserving flag parsing boundaries and values belonging to other options.
func normalizeResumeArgs(flags *flag.FlagSet, args []string) []string {
	result := append([]string(nil), args...)
	for i := 0; i < len(result); i++ {
		arg := result[i]
		if arg == "--" || arg == "-" || !strings.HasPrefix(arg, "-") {
			break
		}
		name, _, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		option := flags.Lookup(name)
		if option == nil {
			break // Let flag.Parse report unknown options and handle help.
		}
		if hasValue {
			continue
		}
		if name == "resume" && (i+1 == len(result) || strings.HasPrefix(result[i+1], "-")) {
			result[i] += "="
			continue
		}
		if boolean, ok := option.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			continue
		}
		i++ // Skip a required value, even when it looks like a flag.
	}
	return result
}

func (options *cliOptions) resolveResume(ws *workspace.Workspace) error {
	if !options.resumeRequested || options.resumeID != "" {
		return nil
	}
	storeOptions := session.DefaultOptions()
	storeOptions.AgentDir = ws.AgentDir()
	metas, err := session.NewStore(ws.Root, storeOptions).List(1)
	if err != nil {
		return fmt.Errorf("查找最近会话失败：%w", err)
	}
	if len(metas) == 0 {
		return fmt.Errorf("当前工作区没有可恢复的历史对话")
	}
	options.resumeID = metas[0].ID
	return nil
}
