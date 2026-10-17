package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

func printCLIHelp(output io.Writer, flags *flag.FlagSet) {
	fmt.Fprintln(output, `bqAgent — 命令行参数帮助

用法:
  bqagent [选项] [任务描述]

前台无任务描述时进入 TUI，需要标准输入和标准输出为 TTY，且 TERM 不为 dumb。
提供任务描述时单次执行，完成后退出；--resume 同样按有无任务选择模式。
选项应放在任务描述之前；用 -- 结束参数解析。

选项:
  -h, --help
      显示此帮助并退出`)
	flags.VisitAll(func(option *flag.Flag) {
		// Worker flags are implementation details, not user-facing commands.
		if strings.HasPrefix(option.Usage, "internal ") {
			return
		}
		prefix := "--"
		if len(option.Name) == 1 {
			prefix = "-"
		}
		valueName, usage := flag.UnquoteUsage(option)
		fmt.Fprintf(output, "  %s%s", prefix, option.Name)
		if valueName != "" {
			fmt.Fprintf(output, " %s", valueName)
		}
		fmt.Fprintf(output, "\n      %s", usage)
		if option.DefValue != "" && option.DefValue != "false" {
			fmt.Fprintf(output, " (默认 %s)", option.DefValue)
		}
		fmt.Fprintln(output)
	})
	fmt.Fprintln(output, `
示例:
  bqagent
  bqagent "分析当前项目"
  bqagent --resume <sessionID>
  bqagent --resume <sessionID> "继续完成任务"
  bqagent --background "执行任务"
  bqagent --server --listen 127.0.0.1:8080
  bqagent --doctor

TUI 内输入 /help 查看快捷键和交互命令，输入 / 浏览命令候选。`)
}
