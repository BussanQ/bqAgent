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

- **glob**: Find workspace files by relative glob pattern. Provide `pattern`; use optional `path` to narrow the base directory.
- **grep**: Search workspace file contents with a Go regular expression. Provide `pattern`; optional arguments include `path`, `glob`, `ignore_case`, and `max_results`.

## Task Management

- **todo_write**: Create or update the current task list. Provide `todos` as a JSON array string containing `content`, `status`, and `activeForm`; keep at most one item `in_progress`.
- **plan**: When this tool is exposed, break a complex task into sequential steps. Provide `task`.

## Web

- **web_search**: Search the web for up-to-date information. Provide `query`. Tavily uses `SEARCH_API_KEY`; Firecrawl environment variables are supported as a compatibility fallback.
- **web_fetch**: Fetch content from a URL. Provide `url`; optional arguments are `extract_mode` (`markdown` or `text`) and `max_chars`.

## Skills

- **install_skill**: Install a skill from a URL. Provide `url`; optional arguments are `name`, `overwrite`, and `target` (`global` by default or `workspace`).
- The system prompt lists only each skill's name, frontmatter description, and workspace-relative `SKILL.md` path. When a listed skill is relevant, first use **read_file** to read the complete file, then follow its instructions in the current conversation.
- `/skill <name-or-alias> [args]` explicitly selects a skill but still uses the normal **read_file** workflow. There is no separate `run_skill` tool.

## Memory

- **mem_save**: Save knowledge to memory. Provide `target` (`daily` or `longterm`) and `content`.
- **mem_get**: Read memory contents. Provide `target` (`daily`, `longterm`, or `yesterday`).
- **memory**: When this structured-memory tool is exposed, use its supported actions to add, replace, remove, search, list, confirm, or compact persistent memory.

## Best Practices

- Read a file with **read_file** before changing it.
- Use **glob** to discover files and **grep** to search their contents.
- Prefer **edit_file** over **write_file** for targeted changes to existing files.
- Use **mem_get**, or **memory** when exposed, to recall prior context.
- Keep **execute_bash** commands safe and reversible when possible.
