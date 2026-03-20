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

## Memory
- **mem_get**: Read a specific file from memory/ directory.
- **mem_save**: Save to daily log (target="daily") or long-term (target="longterm").
- **mem_search**: Keyword search across all memory files.

## Best Practices
- Read before editing — always understand current content first
- Use glob to discover files before grepping
- Prefer edit over write for existing files
- Use mem_search when prior context might be relevant
- Keep shell commands reversible when possible
