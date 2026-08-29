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
