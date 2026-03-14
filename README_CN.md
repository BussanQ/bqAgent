# nanoAgent

[English](./README.md) | 中文

> *"问题不在于你看到了什么，而在于你看见了什么。"* — 梭罗

用最简单的方式构建一个能与系统交互的智能体。

这是一个使用 Go 编写、基于 OpenAI 兼容 chat completions 接口的最小化 AI 智能体实现。智能体可以执行 bash 命令、读取文件和写入文件。

如果你想了解更多，比如学习什么是 MCP、如何更现代地获取工具，推荐看：https://github.com/sanbuphy/nanoMCP

## 安装

安装 Go 1.22+，然后构建 CLI：

```bash
go build -o nanoagent ./cmd/agent
```

设置环境变量：

**macOS/Linux:**
```bash
export OPENAI_API_KEY='your-key-here'
export OPENAI_BASE_URL='https://api.openai.com/v1'  # 可选
export OPENAI_MODEL='gpt-4o-mini'  # 可选
```

**Windows (PowerShell):**
```powershell
$env:OPENAI_API_KEY='your-key-here'
$env:OPENAI_BASE_URL='https://api.openai.com/v1'  # 可选
$env:OPENAI_MODEL='gpt-4o-mini'  # 可选
```

**Windows (CMD):**
```cmd
set OPENAI_API_KEY=your-key-here
set OPENAI_BASE_URL=https://api.openai.com/v1
set OPENAI_MODEL=gpt-4o-mini
```

如果没有设置 `OPENAI_MODEL`，nanoAgent 默认使用 `MiniMax-M2.5`。

## 快速开始

```bash
go run ./cmd/agent "列出当前目录下所有 python 文件"
go run ./cmd/agent "创建一个名为 hello.txt 的文件，内容是 'Hello World'"
go run ./cmd/agent "读取 README.md 的内容"
```

## 工作原理

智能体使用 OpenAI 兼容的 tool calling 来：
1. 接收用户任务
2. 决定使用哪些工具（`execute_bash`、`read_file`、`write_file`）
3. 在本地执行工具
4. 将工具结果返回给模型
5. 重复直到任务完成

就这样。几个很小的 Go 文件。

核心仍然只是一个循环：调用模型 → 执行工具 → 重复。

当前行为刻意与字面版 `agent.py` 保持一致：

- 未知工具会作为 `Error: Unknown tool '...'` 返回给模型
- 非法 JSON 工具参数会直接让当前运行失败
- 文件读写失败也会直接让当前运行失败

## 能力

- `execute_bash`: 运行 bash 命令
- `read_file`: 读取文件内容
- `write_file`: 写入内容到文件

## 示例

```bash
# 系统操作
go run ./cmd/agent "当前目录是什么，里面有哪些文件？"

# 文件操作
go run ./cmd/agent "创建一个打印 hello world 的 python 脚本"

# 组合任务
go run ./cmd/agent "找到所有 .py 文件并统计总代码行数"
```

---

## 许可证

MIT

────────────────────────────────────────

⏺ *如同一粒种子长成森林，几个小小的 Go 文件也能化作无限可能。*
