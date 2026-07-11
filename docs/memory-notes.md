# Memory notes

A couple of architecture decisions that lived in engram's own project memory
(`context/long.md`, retired) but are worth keeping on hand for anyone
extending the codebase. See also `docs/design-notes.md` for the broader set of
memory-model principles.

## Engram is a tool for the agent, not an autonomous actor

Engram exposes explicit subcommands the agent invokes deliberately; it does
not take consequential action on its own.

- **Box of verbs.** The agent holds the judgment; engram just provides the
  verbs. Minimize implicit run-as-side-effect magic.
- **Inject is read-out plus light bookkeeping only.** It may prune old events
  or note that a project exists, but it must never apply, place, or overwrite
  project memory or tools.
- **Heavy lifting lives in explicit subcommands.** Anything that
  consequentially mutates state (`save`, applying a `restore`, tool
  promotion, `discard`) is a verb the agent calls on purpose, never a hidden
  side effect of `inject` or `record`.
- **Division of labor.** Engram reports information deterministically; the
  agent exercises judgment (near-miss project matching, whether to promote a
  tool, whether to apply a restore). Those judgment calls are written into the
  agent-facing guidance, not baked into engram's own logic.
- **Allowlist policy follows the same split.** Bootstrap pre-approves only the
  routine, high-frequency curation verbs (`engram mem`, `engram tool`).
  Infrequent, heavy verbs (`save`, `restore`) stay outside the allowlist on
  purpose -- a permission prompt on a rare, consequential operation is
  appropriate friction, not something to remove.

## Design across platform classes, not one agent

Hook-capable CLIs (Claude Code, Codex CLI, Gemini CLI) share a similar hook
shape, but event names, matchers, and trust/config locations still differ
between them; other integrations (AntiGravity, Copilot, Cursor) are
startup-file-only, with no hook support at all. When adding a feature, avoid
building shared mechanics on one platform's hook-only metadata (e.g. Claude's
`SessionStart` source) unless the feature is explicitly scoped to that
platform. Prefer portable signals -- file mtime/age, file presence, explicit
commands, agent-driven judgment surfaced through injected instructions --
that degrade cleanly on startup-file-only platforms rather than requiring a
per-platform adapter for every lifecycle-specific feature.
