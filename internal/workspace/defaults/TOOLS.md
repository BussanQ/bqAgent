# TOOLS.md - Local Tool Guidance

This file documents the available tools and their recommended usage patterns.
It is guidance only — the tools exposed in the current request are the source of truth.

## Command Execution

- **execute_bash**: Execute a Bash command from the workspace root. Provide `command`. Captured stdout/stderr may be truncated at the configured output limit.

## File Operations

- **read_file**: Read a workspace file. Provide `path`; use optional `offset` and `limit` for large files.
- **write_file**: Write an entire workspace file, creating parent directories when needed. Provide `path` and `content`; existing content is overwritten.
- **edit_file**: Replace an exact string in a workspace file. Provide `path`, `old_string`, and `new_string`; use optional `replace_all=true` only when every occurrence should change.

## Search

- **glob**: Find workspace files by relative glob pattern. Supports `**` and brace alternatives such as `**/*.{go,md}`. Provide `pattern`; use optional `path` to narrow the base directory. If nothing matches, change the pattern or use another exploration tool instead of repeating the same call.
- **grep**: Search workspace file contents with a Go regular expression. Provide `pattern`; optional arguments include `path`, `glob`, `ignore_case`, and `max_results`.

## Optional Task Tracking

- **todo_write**: Optional checklist for genuinely long, multi-step work. It is not part of the standard workflow: ordinary tasks should start substantive work directly. Use it only when persistent progress tracking is actually useful, never merely to restate the request, announce a plan, or before routine code analysis. Provide `todos` as a JSON array string containing `content`, `status`, and `activeForm`; keep at most one item `in_progress`. After updating it, immediately use another substantive tool. Do not call `todo_write` again until task content or status changes.
- **plan**: When this tool is exposed, break a complex task into sequential steps. Provide `task`.

## Web

- **web_search**: Search the web for up-to-date information. Provide `query`. Tavily uses `SEARCH_API_KEY`; Firecrawl environment variables are supported as a compatibility fallback.
- **web_fetch**: Fetch content from a URL. Provide `url`; optional arguments are `extract_mode` (`markdown` or `text`) and `max_chars`.

## Skills

- **install_skill**: Install a skill from a URL. Provide `url`; optional arguments are `name`, `overwrite`, and `target` (`global` by default or `workspace`).
- The system prompt lists only each skill's name, frontmatter description, and workspace-relative `SKILL.md` path. When a listed skill is relevant, first use **read_file** to read the complete file, then follow its instructions in the current conversation.
- `/skill <name-or-alias> [args]` explicitly selects a skill but still uses the normal **read_file** workflow. There is no separate `run_skill` tool.

## Memory

- **memory**: Structured persistent memory. Recall only with `action=search` (provide `query`) or `action=list`. Write with `action=add` or `action=replace` and a `kind` of `user_preference`, `project_fact`, `lesson`, or `decision`. Optional `target` is `workspace` (default) or `global`; use `global` only when the user asks for global memory. Other actions: `remove`, `confirm`, `compact`. Do not call this tool at session start or before exploring a repository.
- **mem_save**: Write-only fallback. Provide `target` (`daily` or `longterm`) and `content`. Prefer **memory** `action=add` when that tool is exposed.

## Best Practices

- Read a file with **read_file** before changing it.
- Use **glob** to discover files and **grep** to search their contents.
- Prefer **edit_file** over **write_file** for targeted changes to existing files.
- Recall prior facts only with **memory** `action=search` or `action=list`; an empty result means stop, do not retry.
- Keep **execute_bash** commands safe and reversible when possible.
