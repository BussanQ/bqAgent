# TOOLS.md - Local Tool Guidance

This file documents the available tools and their recommended usage patterns.
It is guidance only — it does not change what tools are available.

## File Operations
- **read**: Read file with line numbers. Use offset/limit for large files.
- **write**: Write content to file, creates parent dirs. Use for new files.
- **edit**: Replace exactly one occurrence of a string. Use for targeted changes.

## Search
- **glob**: Find files by pattern (supports **). Results sorted by modification time.
- **grep**: Search file contents with regex. Specify path to narrow scope.

## Shell
- **shell**: Run any shell command. Timeout: 60s. Use for builds, tests, installs.

## Web Search
- **web_search**: Search the web for up-to-date information. Requires `SEARCH_API_KEY` (Tavily). Optional `SEARCH_BASE_URL`.

## Memory (implemented)
- **mem_save**: Save knowledge to memory. Parameters: `target` ("daily" or "longterm"), `content` (text to save). Daily saves go to `.agent/memory/YYYY-MM-DD.md`, longterm saves go to `.agent/memory/MEMORY.md`. Use longterm for durable facts, preferences, and patterns. Use daily for session notes.
- **mem_get**: Read memory contents. Parameter: `target` ("daily", "longterm", or "yesterday"). Use to recall saved knowledge and context.
- **mem_search**: Keyword search across all memory files. (not yet implemented)

## Best Practices
- Read before editing — always understand current content first
- Use glob to discover files before grepping
- Prefer edit over write for existing files
- Use mem_search when prior context might be relevant
- Keep shell commands reversible when possible
