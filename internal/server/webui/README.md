# WebUI architecture / WebUI 架构

`internal/server/webui` is a dependency-free-at-runtime, vanilla TypeScript + Vite application. The production path is:

```text
index.html + src/*.ts + src/styles.css + public/favicon.*
                    │ npm run build
                    ▼
          dist/index.html + dist/assets/<hash>.*
                    │ go:embed webui/dist
                    ▼
              one Go executable
```

`src/main.ts` wires the preserved DOM and feature modules. Transport/SSE parsing, Markdown safety, workspace session handling, Provider and conversation state, attachments, theme, and particles remain browser-side code. The backend API and SSE event formats are unchanged.

Production serves `/` with `Cache-Control: no-store`, hashed `/assets/*` with a one-year immutable cache, and favicons with a one-day cache. Unknown files, path traversal, and methods other than GET/HEAD are rejected. The executable reads only the embedded filesystem at runtime.

Commands:

```bash
npm ci
npm run dev
npm run build
npx vitest run src/stream.test.ts src/markdown.test.ts src/workspace.test.ts src/model-selection.test.ts src/attachments.test.ts src/tool-groups.test.ts
```

The development server proxies `/api` to `http://127.0.0.1:8080`. `dist` and `node_modules` are generated locally and ignored by Git.

---

`internal/server/webui` 是原生 TypeScript + Vite 工程，运行时没有前端依赖。生产链路为“源码 → Vite 哈希产物 → `go:embed webui/dist` → 单一 Go 可执行文件”。

`src/main.ts` 负责连接保留的 DOM 与各功能模块；传输/SSE、Markdown 安全、工作区会话、Provider、历史会话、附件、主题和粒子效果仍在浏览器端运行，后端 API 与 SSE 协议保持不变。

生产环境中，根页面使用 `no-store`，哈希资源使用一年 immutable 缓存，favicon 使用一天缓存；未知路径、路径穿越以及非 GET/HEAD 请求均会被拒绝。运行时只读取二进制内的嵌入文件系统，不需要 Node.js、Vite、CDN 或磁盘上的 `dist`。


### Slash commands / 斜杠命令

Type `/` in the composer to discover commands; use ↑/↓ to select, Enter/Tab or a click to complete, and Escape to dismiss. Completion does not send a message. Skills include descriptions and aliases, refreshed when the menu opens using the selected workspace. Commands use the existing chat endpoint and shared backend execution.

输入 `/` 展示命令，↑/↓ 选择，Enter/Tab 或点击补全，Esc 关闭；补全后可继续填写任务并发送。`/skill` 展示当前工作区与全局 Skill 的名称、描述及别名，每次重新打开候选时刷新。`/fast`、`/memory`、`/agent` 和外部 Agent 切换沿用已有聊天执行逻辑。模型、努力程度、Run/Ask、新会话、Provider 设置、停止和反馈已有界面入口，不重复展示；终端专用帮助与退出命令也不展示。群聊另外隐藏 `/agent`、外部 Agent 切换及 `/default`，通过群聊成员和 @ 交互。

The authenticated `GET /api/v1/webui/commands?workspace_id=…&conversation_type=default|group` endpoint supplies discovery metadata from the command catalog shared with TUI. Skill files are read through the existing workspace loader. Regression checks: `npx vitest run src/slash-commands.test.ts` and `go test ./internal/server -run '^TestWebUICommandsFiltersButtonsAndRefreshesWorkspaceSkills$'` from the repository root for the Go test.
