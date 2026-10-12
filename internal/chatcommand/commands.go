// Package chatcommand defines command discovery metadata shared by chat UIs.
package chatcommand

type Command struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Arguments   []Argument `json:"arguments,omitempty"`
}
type Argument struct {
	Label       string `json:"label,omitempty"`
	Value       string `json:"value"`
	Description string `json:"description"`
}

func Builtins() []Command {
	return []Command{
		{Name: "/help", Description: "显示快捷键和命令帮助"},
		{Name: "/clear", Description: "清理显示并在下一条消息创建新会话"},
		{Name: "/exit", Description: "安全退出"},
		{Name: "/provider", Description: "分步配置模型 Provider"},
		{Name: "/run", Description: "切换到 Run 模式（完整工具能力）"},
		{Name: "/ask", Description: "切换到 Ask 模式（只读问答，不修改文件或执行命令）"},
		{Name: "/model", Description: "查看或切换模型"},
		{Name: "/effort", Description: "查看或设置模型推理努力程度", Arguments: []Argument{{Value: "auto", Description: "自动（模型默认）"}, {Value: "low", Description: "低"}, {Value: "medium", Description: "中"}, {Value: "high", Description: "高"}, {Value: "max", Description: "最高"}}},
		{Name: "/fast", Description: "切换 API Fast 模式", Arguments: []Argument{{Value: "on", Description: "开启"}, {Value: "off", Description: "关闭"}, {Value: "status", Description: "查看状态"}}},
		{Name: "/skill", Description: "使用 Skill 或 alias"},
		{Name: "/memory", Description: "管理记忆", Arguments: []Argument{{Value: "list", Description: "列出记忆"}, {Value: "search", Description: "搜索记忆"}, {Value: "confirm", Description: "确认候选记忆"}, {Value: "compact", Description: "压缩记忆"}}},
		{Name: "/feedback", Description: "提交运行反馈", Arguments: []Argument{{Value: "up", Description: "正向反馈"}, {Value: "down", Description: "负向反馈"}}},
		{Name: "/agent", Description: "管理子代理", Arguments: []Argument{{Value: "spawn", Description: "启动子代理"}, {Value: "list", Description: "列出子代理"}, {Value: "status", Description: "查看状态"}, {Value: "wait", Description: "等待结果"}, {Value: "interrupt", Description: "中断"}, {Value: "cancel", Description: "取消"}, {Value: "resume", Description: "恢复"}, {Value: "result", Description: "查看结果"}, {Value: "collect", Description: "收集结果"}, {Value: "apply", Description: "应用变更"}, {Value: "cleanup", Description: "清理"}}},
		{Name: "/claude", Description: "切换到 Claude 外部 Agent"},
		{Name: "/codex", Description: "切换到 Codex 外部 Agent"},
		{Name: "/cursor", Description: "切换到 Cursor 外部 Agent"},
		{Name: "/opencode", Description: "切换到 OpenCode 外部 Agent"},
		{Name: "/pi", Description: "切换到 Pi 外部 Agent"},
		{Name: "/default", Description: "恢复默认 Agent"},
		{Name: "/stop", Description: "停止当前外部 Agent 进程"},
	}
}
