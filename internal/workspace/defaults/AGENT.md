# AGENT.md - Operating Instructions

Personal defaults for every workspace. Repository instructions take precedence.

## Tools
- The tools exposed in the current request are the source of truth for availability, capabilities, and parameters.
- Locate code with glob and grep, then read the relevant parts with read_file. If a search finds nothing, change the pattern or the tool instead of repeating the call.
- File and command output may be truncated. Narrow the read or command when needed; omitted output is not absent content.
- Keep shell commands safe and reversible, and prefer the file tools over the shell for reading and editing files.

## Progress tracking
- task_update is the single persistent record for a multi-step programming task: goal, acceptance criteria, remaining work and status.
- Do not create a task for ordinary Q&A or a simple single-step change.
- For multi-step implementation, register the goal, acceptance criteria and remaining work before editing.
- For read-only analysis, review or report work set kind=analysis: it completes when each criterion cites concrete reading, search or report evidence and nothing remains; do not run tests just to close it. Tasks that change files keep the default kind=code and need real verification.
- Update it at phase changes, when acceptance evidence is obtained, when blocked, or when scope changes. Do not repeat the plan after every tool call.

## Skills
- The skill list contains discovery metadata, not full instructions. Before using a relevant skill, read its complete SKILL.md with read_file.
- An explicit /skill selection follows the same read_file workflow; it does not execute a separate run_skill tool.

## Verification
- For anything with a runtime (a page, a UI, a service), running it is the proof: serve it, load it and drive the real interaction. "It's in the code", "it loads" and "no syntax error" do not mean it works.
- Grepping for a name, counting braces or citing a byte count proves nothing. Run the real thing, or mark it unverified.
- An install that fails once is unavailable here. Do not hunt for alternatives or retry variations; say so, verify another way and mark what you could not prove as unverified.
- Design, prose, mockups and research have no test to pass: produce them well and describe them briefly.

## Response style
- Break complex explanations into steps, and show code changes as diffs when helpful.
