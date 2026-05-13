# Long

## context-memory
Project memory in version control: context/long.md is the canonical committed location for long-term project memories. engram inject auto-loads it when the file is newer than the DB (or DB has no long-term entries). At natural commit points, offer to run: engram mem dump --tier long -- user reviews and includes in commit. This covers fresh clones, new machines, and teammate sharing. Short-term, events, and cold are never committed.

## cold-tier
Cold tier: semantic convention for low-priority long-term storage. Injected at session start as a one-line catalog (first line of content only) -- full content never auto-loaded. Use for project ideas, reference material, or anything bulky and rarely needed. Write with first line as summary, details on subsequent lines. Do not read cold entries unprompted; fetch on demand with: engram mem --tier cold read <key>

## platform-strategy
Platform strategy (settled as of v0.2.0):

Claude Code: full hook support -- SessionStart (inject) + PostToolUse (record). bootstrap claude [-g] writes to project or global settings.json.

All other platforms (Gemini CLI, AntiGravity, Copilot, etc.): system-prompt-file only. No hooks, no settings.json manipulation. Bootstrap writes a platform-specific file (GEMINI.md, copilot-instructions.md, KI metadata.json) with an instruction to run 'engram inject --text' on first interaction. No record support on these platforms.

Exception: if a platform later gains reliable hook support, we add it. Gemini CLI SessionStart hook exists in docs but not confirmed working in 0.37.1 -- keep the GEMINI.md approach as primary.

## db-design
Per-project DB at .engram/mem.db, paths relative to project root. Global DB at ~/.engram/mem.db. Single events table + FTS5, session-count pruning (keep 100). Prune runs at session start alongside inject. Legacy path .claude/engram.db supported via read fallback; migrate --cleanup to move.

## global-db
Global memories (invariants, preferences) in ~/.engram/mem.db. Project memories (long, short) + events in .engram/mem.db (relative to project root). Legacy paths (.claude/engram.db) supported via read fallback. Inject reads both global and project DBs.

## status-line-questions
Status line behavior is hit-or-miss across platforms (doesn't show in VS Code, works in CLI terminal only). Codename removed as separate invariant -- now embedded in personality text. Status command still exists but codename source is gone. Open questions: should status read from personality text? Should it show something else? Revisit when platform story is clearer.

## mcp-resource-architecture
MCP v2 architecture: (1) engram://inject and engram://agentinfo BOTH as context URIs -- auto-included at session start by the client. inject provides dynamic data (personality, memories, recent files); agentinfo provides static instructions. Both read once per session, not on demand. (2) engram://mem as single tool with subcommand parameter (write/read/list/search/delete) -- replaces 5 separate tools. dump/load/promote/pop are CLI-only. For platforms supporting MCP context URIs: no CLAUDE.md or hooks needed, fully self-contained. Hook path remains for non-MCP platforms.

## mcp-design
MCP server for engram must be stateless per-call: every tool takes cwd, resolves project root dynamically via FindProjectRoot, never caches project state at server level. The multi-repo problem is not an MCP limitation -- it is a bad implementation pattern. A2A rejected for engram: its statefulness is for long-running agent task lifecycle, not for knowing which project you are in. MCP + stateless design is correct. Cursor is the first target: .cursorrules handles injection (per-project by definition), MCP server handles recording and mem operations.

## bash-events
Bash events: file_path = normalized command string (rtk prefix stripped), snippet = stdout head-N. Only grep and find recorded. Failed commands filtered.

