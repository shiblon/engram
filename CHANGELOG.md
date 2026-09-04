# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Versions before 0.5.0 predate the project's conventional-commit convention, so
their entries are summarized under "Changed". GitHub release notes are generated
independently from commit messages by goreleaser; this file is the curated,
in-repo companion.

## [Unreleased]

### Added
- bootstrap guidance now covers consolidation, not just storage and retrieval: a
  new "Memory consolidation" section tells an agent to notice when a memory it is
  about to write contradicts or duplicates one already in its context, surface
  that rather than appending, and harmonize with the user into a replacement that
  retires what it replaced. Feedback on the process itself goes to
  `engram:/long/__memory_consolidation__` rather than into the generated file, so
  a user's refinements survive the next bootstrap.

## [0.14.0] - 2026-08-31

### Changed
- memory search is compact by default: `mem search` now prints every ranked match
  as a copyable address and summary instead of emitting complete bodies. Both
  `mem search` and `skill search` accept `--limit` as an explicit bound (`0`, the
  default, means all) and report omissions on stderr. `--full` restores bodies
  for memory results and includes complete instructions for skill results.

## [0.13.2] - 2026-08-06

### Added
- dispatch: a running batch appears in the status line as `⚡ 4/8 1✗ 1m03s`, via a
  transient per-pid progress file rather than a table -- its lifetime is exactly the
  supervisor's, so the no-schema decision holds. A file for a dead pid is reaped on
  the next read, since a SIGKILLed supervisor cannot clean up after itself. The
  segment is absent when nothing is running, and a missing or malformed file fails
  silently: it is decoration, and a broken status line is worse than an absent one.
- dispatch: codex token usage is now accounted for. `codex exec --json` reports token
  counts but suppresses the model preamble, while plain `codex exec` is the reverse,
  so a spec can now declare `probe.base_argv` and let a probe (which must verify the
  model) differ from a run (which wants the counts). `result.usage_jsonl_path` reads
  usage from a JSONL stream independently of wherever the result came from, and the
  reader accepts both providers' spellings for the cache counters. codex reports no
  dollar cost in either mode, which is a provider limit.

### Fixed
- cli: a local build no longer claims to BE the release. Current Go toolchains stamp
  a pseudo-version (`v0.0.0-<timestamp>-<hash>`, plus `+dirty`) instead of the
  `(devel)` this code checked for, so local builds silently passed through as
  releases; separately, Go does not stamp `vcs.*` at all when building from a linked
  git worktree. Both mattered because inject compares the running version against
  the version stamped into the shipped guidance, so two builds laundered into one
  release number compared equal and the drift check stayed silent. A local build now
  reports `v0.13.1+<commit>`, `+<commit>.dirty`, or `+devel` when no revision is
  available. Note this makes the guidance drift check fire after most rebuilds on a
  development tree, which is truthful but noisier than before.
- dispatch: a batch now terminates at its deadline. `Emit` held a mutex across a raw
  `Write` with no deadline and every task goroutine emitted before its `wg.Done()`,
  so one status-stream consumer that stopped draining blocked the join forever -- and
  a deadline cannot interrupt a blocking write. The wait is bounded and the
  supervisor's own emits give up rather than hang. Also: `batch_done` is now provably
  the last line (the heartbeat goroutine is joined, not just signalled), the progress
  counters stay a true partition of the task count, and the deferred SIGKILL
  escalation is cancelled once the child is confirmed gone, so it cannot fire at a
  recycled pid.
- inject: budget truncation counts characters rather than bytes. Every caller passes
  an `Inject*BudgetChars` constant and `MaxTldrLen` measures runes, but the truncation
  counted bytes -- so an index containing em dashes, arrows, or emoji was cut well
  short of the budget it was given.
- restore: a database holding only short-term memories is no longer treated as empty,
  so a restore re-stages under a new slot instead of silently overwriting in-flight
  working state. A failed curated-content check is reported rather than discarded, and
  a failed stage registration removes the slot directory it had just written instead
  of orphaning it.
- prune: `events` and `curation_events` share one retention decision and are now
  deleted in a single transaction, instead of leaving the two tables disagreeing about
  which sessions are recent when the second delete failed.
- curation log: action, source, and scope are checked against their vocabularies where
  they become permanent. All three are set by engram's own code, so an unknown value
  is a developer mistake that would otherwise corrupt an append-only learning signal
  silently; it is now logged loudly and the row still written, since capture must
  never fail the mutation it records.
- dispatch tests: the test-quality reviewer found tests that could not fail, so
  they proved nothing about the code they named.
  - The process-group teardown test spawned no descendant, so killing only the
    direct child passed it -- guarding exactly the bug it existed to catch. Its fake
    now spawns a real grandchild and the test asserts that grandchild is gone after
    the deadline. Verified by regressing `terminateProcessGroup` to a direct kill and
    confirming the test fails.
  - `ProbeSpec` had no coverage at all; only pure helpers were tested. The smoke run,
    output extraction, model verification, and the invalid-model fallback are now
    exercised end to end against the fake provider, including the silent-substitution
    and failed-baseline cases.
  - The happy-path assertion checked only that results began with `echo:` and
    differed, which would pass with the prompts swapped between tasks. It now asserts
    each task's exact echoed prompt.
  - The prompt-file transport test asserted a path reached argv but never read it, so
    deleting the `os.WriteFile` would still pass. It now reads the file and compares
    contents.
  - Test setup no longer discards errors. A dropped setup error reappears later as a
    behavioral assertion failing for an unrelated reason, and the package forbids
    silent discards in its own code.
- dispatch: four ways a child's output could harm the parent, found by the security
  reviewer in the same batch.
  - Prompts no longer reach the status stream. `task_start` emits resolved argv, which
    is what makes `--dry-run` useful, but a provider carrying its prompt in argv
    (codex does) published the caller's content into every captured stream --
    including a prompt supplied via `prompt_file` precisely to keep it out of view.
    Argv is now redacted by exact match against the values substituted into it, so
    flags stay readable and content does not.
  - A child's stdout and stderr are bounded as they are captured rather than
    truncated afterwards. Unbounded buffers let a runaway or hostile child write
    until the deadline and exhaust the supervisor, taking the batch with it.
  - The result file is opened with `O_NOFOLLOW`, checked for being a regular file,
    and read under a size cap. A child is told its result path but does not own it:
    replacing it with a symlink made dispatch read any file the user could read and
    publish the contents as that task's result.
  - Task ids that differ but sanitize alike (`a/b` and `a?b` both became `a-b`) no
    longer share a result file. Duplicate rejection compares raw ids, so both passed
    it and then collided, letting two tasks read each other's results.
- dispatch authority: three failures found by reviewing dispatch with dispatch, all
  of which let a child do more than it was told it could.
  - Authority is now a CLOSED set (`read-only`, `edit`, `default`) rejected at config
    parse. Previously an unmapped string passed straight through, so
    `{"authority":"danger-full-access"}` became `--sandbox danger-full-access` on
    codex: one typo in an unreviewed batch file could hand a child the strongest
    authority the CLI offers. Widening a provider now requires editing the spec,
    which is the artifact reviewed once and reused.
  - claude's `read-only` no longer maps to `--permission-mode plan`. Plan mode does
    not withhold writes, it REDIRECTS them: children wrote plan files under
    `~/.claude/plans` and returned planning stubs instead of their work, costing an
    eight-child review batch its output channel. It is now
    `--permission-mode dontAsk --disallowedTools "Edit Write NotebookEdit"`, canaried
    to read successfully, refuse a write with a recorded denial, and produce no plan.
  - A spec whose provenance has not positively verified authority now warns on every
    task. codex echoed `sandbox: read-only` in its own preamble and then created a
    file in the workspace anyway, both before and after bubblewrap was installed. A
    flag the provider ACCEPTS is not a flag the provider ENFORCES, and codex
    authority is recorded as advisory rather than trusted.

- dispatch probe: five ways the probe reported findings it had not earned, all
  found by running it against real provider CLIs.
  - Model verification compared the role name a config asked for against the model
    the provider reported, so a correctly honored `"model": "cheap"` came back
    unverified. A check that cries wolf on correct portable configs trains everyone
    to ignore it, concealing the real substitution it exists to catch. It now
    compares the provider's own spelling after role resolution, and a genuine
    mismatch names both the resolved id and the role.
  - The flag-liveness fallback read "an invalid model name errored" as proof the
    model flag is live, without checking that the valid model had succeeded. With a
    broken login both runs exit nonzero, so a second identical failure was recorded
    as evidence. It is now skipped, and reported inconclusive, unless the baseline
    smoke passed.
  - A failed result read discarded metadata already obtained: the provider's
    preamble named the model it actually ran, and an early return threw that away
    while reporting the model as unknown. The result error is now held and returned
    after every other field has had its chance.
  - A failed probe cleared the spec's `seed` flag, promoting a shipped guess to
    "learned" on the strength of a run that proved nothing. Only a probe whose
    smoke passed may retire it.
  - A credential complaint under context suppression is now recognized and named,
    instead of surfacing as a bare smoke failure that sends someone hunting through
    their argv for a problem that is not there.
### Changed
- dispatch spec: `authority` is now a closed map of role names to complete argv
  fragments, replacing one template with one substituted value per role. That shape
  could not express a multi-flag read-only policy, which is what forced claude onto
  plan mode. The schema version stays at 1 -- dispatch is experimental and has no
  other users, so a bump would buy a migration path nobody needs and cost a re-seed
  that discards local probe results. Refresh stored specs with
  `engram dispatch spec seed --overwrite`.

- dispatch seeds: corrected against the installed CLIs, with the numbers measured
  rather than assumed.
  - claude's context suppression is `--setting-sources local`, not `--bare`.
    `--bare` documents that Anthropic auth is strictly `ANTHROPIC_API_KEY` or
    `apiKeyHelper`, and that OAuth and keychain are never read, so on a
    subscription login it fails outright; it would also move billing to API credits,
    a different payment rail rather than a lower cost. Measured on claude 2.1.222
    with an identical prompt: no flag = 36,888 cache-creation tokens at $0.0751,
    `--setting-sources user` = 14,848 at $0.0325, `--setting-sources local` = 3,693
    at $0.0126. The `user` rung is not isolation: a child asked its codename under
    it answered with the parent operator's codename, because the user-level
    `CLAUDE.md` carries engram identity.
  - Both specs now map role names (`cheap`, `balanced`, `strong`) to full model ids.
    Asking claude for the alias `haiku` silently ran `claude-sonnet-5` with a clean
    exit and a plausible answer, so the maps avoid aliases entirely.

## [0.13.1] - 2026-08-05

### Added
- dispatch (experiment key `dispatch`): `engram dispatch` hands a decomposed task
  to one or more provider CLIs as child processes, possibly on different providers
  and models per slice, and joins the results. A batch is one invocation, N
  children, and exit; it adds no database schema, and a run dies with its session
  on purpose (put a long batch in a tmux pane). Its flags, schemas, and output may
  change in patch releases; see `engram experiments` for the hypothesis and exit
  conditions, and `docs/dispatch-notes.md` for the reasoning.
  - `dispatch run --config <file|->` runs a batch and streams status to stdout as
    JSON Lines (`batch_start`, `task_start`, `status`, `task_done`, `batch_done`),
    every line carrying `v` and `type`. Status is emitted on state change and on a
    heartbeat; `batch_done` is authoritative and self-contained. `--dry-run`
    resolves and prints every invocation without spawning anything.
  - `dispatch spec` stores each provider's invocation recipe as an ordinary
    long-term memory (`dispatch-spec-<provider>`) holding one fenced JSON block, so
    a moved upstream flag is repaired by editing JSON rather than by waiting for a
    release. Recipes are argv arrays with placeholders, substituted into
    already-split elements, so there is no shell and nothing to quote.
  - `dispatch probe <provider> --model <M>` smokes a spec and positively verifies
    the model against the CLI's own output metadata, falling back to an
    invalid-model liveness check; findings are written into the spec's provenance.
    A provider that silently accepts an invalid model is recorded as untrusted.
  - `dispatch survey <exe>` captures top-level and subcommand help with a content
    digest, for an agent to read when learning or re-learning a spec.
  - Seed specs for `claude` and `codex` ship with engram so a fresh install works;
    they are marked as seeds and unprobed until probed here.
  - Children run in their own process group with an explicit stdin and a
    wall-clock deadline; cancellation signals the group so a provider's
    grandchildren are not orphaned. An argv that would exceed the kernel's
    per-argument or total limit is refused with the transport that fixes it,
    rather than surfacing as an opaque `E2BIG`.
  - Authority is read-only unless a task names otherwise, so write access is
    opt-in by name and the batch config is the auditable record of what each
    child was allowed to do. Dispatch warns on the status stream for every
    write-capable task, every spec with no authority flag, and every spec that
    maps the requested level to nothing. A dispatched child has no controlling
    terminal, so an approval prompt blocks until the deadline instead of falling
    back to asking; that makes a working guardrail look like a hang, and the
    tempting fix is to disable the guardrail.
- agent guidance: `agentinfo` and the bootstrap protocol block gain a shared
  "Experimental features" section covering how to look up an experiment's exit
  conditions, plus the dispatch judgment that does not belong in help text: when
  fan-out pays for its per-child context cost, that slicing destroys the seams and
  amplifies false positives, that a dispatched child should not self-orient, why
  write-capable dispatch is a materially bigger step than read-only dispatch, and
  how to repair a provider spec.

## [0.13.0] - 2026-08-05

### Added
- mem: `edit <key>` opens an existing memory's tldr and body in `$VISUAL` or
  `$EDITOR`, preserves its tier, scope, layer, trigger, and session metadata,
  treats unchanged saves as no-ops, and retains the temporary file after an
  editor, parse, or database failure so human edits are not lost.
- mem: copyable `engram:` addresses carry scope, tier, agent layer, and escaped
  key through `read`, `write`, `edit`, `delete`, `tldr`, `list`, and `move`.
  Rootless addresses select the current project (`engram:long/key`); a leading
  slash selects global memory (`engram:/long/key`). Authorities, queries, and
  fragments are reserved and rejected for now. Existing bare keys and flags
  remain supported.

### Changed
- mem: `list` now defaults to a compact, human-scannable address/tldr index;
  use `--keys` for one visible key per line or `--full` for the previous
  body-inclusive output. JSON output remains unchanged.
- database access: inspection commands now use true read-only SQLite
  connections and do not create databases or run schema initialization.
  Writable opens preserve WAL coordination files so sandboxed linked worktrees
  can read the main checkout's shared database without write access. When a
  one-time coordination-file setup is still needed, errors report the exact
  shared path instead of SQLite's bare `unable to open database file (14)`.
- status: database-open and query failures are reported instead of being
  silently rendered as zero memory counts.

## [0.12.3] - 2026-07-30

### Fixed
- version reporting: source-tree builds now retain `-v`/`--version` and fall
  back to the checked-in release version instead of hiding the flag or reporting
  `(devel)`; root help begins with the running version for every installation path.

## [0.12.2] - 2026-07-30

### Added
- curation log (experimental `curation-log`): every mutating curation action --
  create, update, delete, move, tldr-set, skill-adopt, skill-classify -- is now
  captured into an append-only `curation_events` table, snapshotting the content
  and tldr at event time (for a delete, the removed values). The `memories` table
  is last-write-wins, so overwrites and deletes previously erased all history;
  the log preserves it as raw signal for a future learning layer. Capture rides
  the data-access primitives (`WriteMemory`/`DeleteMemory`/`SetMemoryTldr` and the
  move/skill-classify paths) so no mutation path is missed, and is best-effort so
  it never fails the mutation. Read it with the experimental `engram curation`
  command. This is capture only: nothing weighs or learns from it yet. `prune`
  rotates the log by the same session model, but never drops session-less rows.

### Fixed
- memory tiers: enforce the five canonical tiers at the write boundary, accept
  human-facing `long-term` and `short-term` as aliases for `long` and `short`,
  reject unknown destination tiers, and normalize existing alias rows during
  schema migration. Compact agent guidance now states the exact CLI tier tokens.
- mem write safety: reject human-readable `mem read` output when it is fed back
  as replacement content, explicitly report that a failed write left the stored
  memory unchanged, and recognize a re-supplied unchanged body plus `--tldr` as
  a metadata-only edit instead of claiming the body was stored. Generated agent
  guidance directs summary-only edits to `mem tldr` and warns that a read-back
  retry after a failed write retrieves the old body and can otherwise discard
  the intended edit.

## [0.12.1] - 2026-07-17

### Fixed
- skill/memory search: free-text queries are now tokenized, quoted, and OR-ed
  before hitting FTS5 instead of passed through as bareword implicit-AND. Extra
  words now widen recall (bm25 rank floats the best match up) rather than
  narrowing it, so an inferred phrase like "standup morning routine" finds a
  skill whose text only says "standup". Quoting also stops FTS operator
  characters in arbitrary queries from provoking a MATCH syntax error.

### Changed
- skills guidance: the injected Skills index is now framed as the retrieval
  mechanism itself, not a pointer to one. Agents scan it and read the matching
  skill directly; `skill search` is a fallback for a truncated or scrolled-away
  index or a body-only match, and a "no results" is no longer treated as proof a
  skill is absent. `skill search` with no hits now says so.

## [0.12.0] - 2026-07-16

### Added
- skill discovery: classifications are now persisted per candidate with their
  digest, optional rationale, skill grouping, and direct-tool invocation.
  `skill discover` reports only new, changed, or removed entries by default;
  `skill classify` supports individual and batch JSON verdicts. Changed entries
  retain their prior judgment without invalidating unrelated classifications.
- skills: trigger-bearing long-term memories now provide first-class workflow
  CRUD/search, a scoped session-start trigger index, and first-use capture
  guidance that distinguishes skills from callable tools. `skill adopt`
  classifies an existing long-term memory in place without rewriting its body.
- bootstrap: markdown-init platforms now carry a guidance version marker and
  the same skill retrieval/capture protocol as the full Claude guidance.
- experiments: `engram experiments` reports each trial's hypothesis, unstable
  surfaces, and event-based promotion/removal conditions; tests keep registered
  experiments aligned with visibly labeled CLI commands.

### Changed
- skill discovery: promoted to stable after trigger-bearing skills and
  per-candidate catalog classifications proved persistable, injectable, and
  round-trip safe without executing discovered scripts.

## [0.11.3] - 2026-07-15
### Added
- skill (experimental: `skill-discovery`): `discover` inventories conventional repository
  automation without executing it, and inject prompts when the current script
  snapshot has not been cataloged. `--acknowledge` records reviewed snapshots
  as project bookkeeping rather than memory.

## [0.11.2] - 2026-07-14
### Added
- mem: `tldr <key>` with no summary now prints the current tldr (or the first-line
  fallback when none is set), so you can review before overwriting.
- mem: `list --missing-tldr` lists memories that have no tldr (invariants excluded)
  -- a coverage view for finding summaries worth adding.

### Fixed
- mem: `write <key> <content>` without `--tldr` no longer wipes an existing tldr;
  it now preserves the current summary. Pass `--tldr ""` to clear it deliberately.

## [0.11.1] - 2026-07-14
### Added
- register: `--forget <path|identity>` removes a project from the manifest,
  path-first so naming a path evicts exactly one working copy and leaves sibling
  clones alone; `--purge` also deletes the stray `.engram` directory on disk
  (never the global `~/.engram`).
- mem: `tldr <key> <summary>` sets or clears a memory's one-line inject summary
  without rewriting its content, so preferences show a curated summary at session
  start instead of falling back to their first line.
- packaging: releases now include Debian `.deb` packages (linux amd64/arm64) for
  `dpkg -i` install, alongside the existing tarballs and Homebrew tap. No apt
  repository yet -- these are download-and-install artifacts.

### Changed
- packaging: the Homebrew tap is now published as a cask instead of a formula
  (the `brews` config goreleaser deprecated). Installs strip the macOS quarantine
  attribute so an unsigned binary still runs on first launch.

## [0.11.0] - 2026-07-11
### Added
- inject: carry the version-drift check in inject, not just the guidance
- mem: dump to stdout by default, drop the context/ location
- inject: drop context/long.md auto-import
- inject: lead session context with the running engram version
- agentinfo: stamp engram version into the shipped guidance
- inject: name touched files in the rollup and budget the areas section
- inject: bound session-start context and roll up active files

### Changed
- test/docs: pin migration-nudge assertion; note dump/load --global asymmetry
- save: drop the --include-context option
- tools: remove the project agent-tool subsystem, keep global
- mem: drop inert project-root check in resolveMemDir
- cmd: single engramVersion() source; drop dead goreleaser ldflags

## [0.10.1] - 2026-07-11
### Fixed
- bootstrap: report file writes truthfully and count them in the summary

## [0.10.0] - 2026-07-11
### Added
- mem: project preferences, tldr summaries, and cross-db move

## [0.9.3] - 2026-07-01
### Fixed
- inject: surface global long-term and short-term memory

## [0.9.2] - 2026-06-12

_Internal changes only._

## [0.9.1] - 2026-06-11
### Changed
- git worktree support, one database for multiple worktrees

## [0.9.0] - 2026-06-11
### Changed
- Add agent-based layering for preferences, invariants, and personality.

## [0.8.2] - 2026-06-10
### Changed
- codex session start hook is too noisy, add --no-session-hook flag to bootstrap, use AGENTS fallback

## [0.8.1] - 2026-06-10
### Changed
- removed duplicate inject instructions for codex and gemini

## [0.8.0] - 2026-06-10
### Added
- agents: file-activity-only record + Codex & Gemini hook parity

## [0.7.0] - 2026-06-10
### Added
- standing: render preferences (P2) alongside invariants (P1)
- invariants: render the invariant tier to the authoritative channel

### Fixed
- surface previously-swallowed errors on the inject path
- inject: count only context memories that actually persisted
- save: never silently drop projects from the archive

### Changed
- clarify misleading names and fix a misattributed doc comment
- engram: dedup DB-open and memory row scanning
- bootstrap,uninstall: collapse duplicated platform sections
- A preference stored only in engram is silently beaten by harness and config-level defaults (Co-Authored-By was suppressed only by includeCoAuthoredBy:false, never by memory). Direct agents to check for a settings.json flag / hook / managed policy first, propose it via the update-config skill, and keep memory as a pointer to where it is enforced. Lives in engram.md — the authoritative always-loaded channel — precisely because the same staying-power problem would bury it if it lived in memory.

## [0.6.5] - 2026-06-09
### Added
- manifest: one row per working copy, keyed by (identity, path)

### Changed
- Explicitly specify inject hooks for session compaction, etc.

## [0.6.4] - 2026-06-05
### Added
- tool: --as flag on promote to rename at destination

## [0.6.3] - 2026-06-05
### Added
- register: --list to show all projects in the manifest

## [0.6.2] - 2026-06-05
### Added
- register: --scan to bulk-register existing projects from a directory tree

## [0.6.1] - 2026-06-05
### Added
- manifest: engram register command + inject self-registration

## [0.6.0] - 2026-06-05
### Added
- dump-restore: agent instructions for staged restore surfacing (phase 5)
- dump-restore: restore + apply (phases 3+4)
- dump-restore: save archive (phase 2)
- dump-restore: project manifest (phase 1)

### Changed
- housekeeping how to use long-term memory, updating allowlist defaults

## [0.5.1] - 2026-06-04
### Changed
- agenttools: move global tools to $HOME/.engram/agenttools

## [0.5.0] - 2026-06-04
### Added
- agenttools: self-describing agent tool catalog with staged-candidate graduation

## [0.4.1] - 2026-06-03
### Changed
- make orientation visible: status line + inject orientation header
- lower Go floor to 1.25.5 for broader package-manager support
- added homebrew instructions

## [0.4.0] - 2026-05-19
### Changed
- update goreleaser with token
- added goreleaser for homebrew tap

## [0.3.8] - 2026-05-19
### Changed
- improved consistency for all text and functionality, reduced duplication

## [0.3.7] - 2026-05-19
### Changed
- updated help text to match implementation to avoid confusing agents

## [0.3.6] - 2026-05-19
### Changed
- add never edit directive to agentinfo.go

## [0.3.5] - 2026-05-19
### Changed
- add generated content header to dumped long-term memory, improve instructions

## [0.3.4] - 2026-05-15
### Changed
- updated instructions for engram not in the path
- database migration capability, trimmed agentinfo

## [0.3.3] - 2026-05-14
### Changed
- updated to guide away from cross-project edits without permission
- cleanup, better Go idioms

## [0.3.2] - 2026-05-14
### Changed
- added codex support and generic init file support

## [0.3.1] - 2026-05-13
### Changed
- added versioning

## [0.3.0] - 2026-05-13
### Changed
- Add long-term memory dump. Add cold storage.
- updated global v project instructions

## [0.2.1] - 2026-04-28
### Changed
- added cursor support
- README now more generic, updated to include go install instructions

## [0.2.0] - 2026-04-28
### Fixed
- fix release to avoid node.js version warnings

### Changed
- simplify approach for non-claude, promote is now move
- use file inclusion in CLAUDE.md
- added context resources for inject and agentinfo, should be read at session start
- gitignore simply a file in .engram, db migration created
- refactoring to reduce duplication in SQL, update agentinfo with flag examples
- MCP implementation, some improvements to agent instructions

## [0.1.1] - 2026-04-25
### Changed
- README references pre-built binaries
- goreleaser

## [0.1.0] - 2026-04-25
### Changed
- README is tightened up - removed all the installation noise - just bootstrap
- better bootstrap, tightened some things up, a warning about duplicate hooks
- safety valve for global updates when another project has already set things up.
- mod tidy
- add status line command, add status hook to bootstrap
- Update README with additional note on agent compatibility
- Fix formatting of note in README.md
- nicer formatting for installation
- Revise setup instructions in README
- Enhance clarity on Engram's structural impact
- added gitignore stuff to bootstrap, simplified readme now that bootstrap does so much
- bootstrap now adds a personality todo in short-term memory and installation is unified and simpler
- better personality instructions, pointer from global CLAUDE.md to engram tool
- better gopath instructions for the agent
- clickable install section link
- better personality instructions for the agent
- added memory management (very powerful) and updated the README to explain it all
- remove shell scripts, add instructions
- first take
- Initial commit

[Unreleased]: https://github.com/shiblon/engram/compare/v0.14.0...HEAD
[0.14.0]: https://github.com/shiblon/engram/compare/v0.13.2...v0.14.0
[0.13.2]: https://github.com/shiblon/engram/compare/v0.13.1...v0.13.2
[0.13.1]: https://github.com/shiblon/engram/compare/v0.13.0...v0.13.1
[0.13.0]: https://github.com/shiblon/engram/compare/v0.12.3...v0.13.0
[0.12.3]: https://github.com/shiblon/engram/compare/v0.12.2...v0.12.3
[0.12.2]: https://github.com/shiblon/engram/compare/v0.12.1...v0.12.2
[0.12.1]: https://github.com/shiblon/engram/compare/v0.12.0...v0.12.1
[0.12.0]: https://github.com/shiblon/engram/compare/v0.11.3...v0.12.0
[0.11.3]: https://github.com/shiblon/engram/compare/v0.11.2...v0.11.3
[0.11.2]: https://github.com/shiblon/engram/compare/v0.11.1...v0.11.2
[0.11.1]: https://github.com/shiblon/engram/compare/v0.11.0...v0.11.1
[0.11.0]: https://github.com/shiblon/engram/compare/v0.10.1...v0.11.0
[0.10.1]: https://github.com/shiblon/engram/compare/v0.10.0...v0.10.1
[0.10.0]: https://github.com/shiblon/engram/compare/v0.9.3...v0.10.0
[0.9.3]: https://github.com/shiblon/engram/compare/v0.9.2...v0.9.3
[0.9.2]: https://github.com/shiblon/engram/compare/v0.9.1...v0.9.2
[0.9.1]: https://github.com/shiblon/engram/compare/v0.9.0...v0.9.1
[0.9.0]: https://github.com/shiblon/engram/compare/v0.8.2...v0.9.0
[0.8.2]: https://github.com/shiblon/engram/compare/v0.8.1...v0.8.2
[0.8.1]: https://github.com/shiblon/engram/compare/v0.8.0...v0.8.1
[0.8.0]: https://github.com/shiblon/engram/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/shiblon/engram/compare/v0.6.5...v0.7.0
[0.6.5]: https://github.com/shiblon/engram/compare/v0.6.4...v0.6.5
[0.6.4]: https://github.com/shiblon/engram/compare/v0.6.3...v0.6.4
[0.6.3]: https://github.com/shiblon/engram/compare/v0.6.2...v0.6.3
[0.6.2]: https://github.com/shiblon/engram/compare/v0.6.1...v0.6.2
[0.6.1]: https://github.com/shiblon/engram/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/shiblon/engram/compare/v0.5.1...v0.6.0
[0.5.1]: https://github.com/shiblon/engram/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/shiblon/engram/compare/v0.4.1...v0.5.0
[0.4.1]: https://github.com/shiblon/engram/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/shiblon/engram/compare/v0.3.8...v0.4.0
[0.3.8]: https://github.com/shiblon/engram/compare/v0.3.7...v0.3.8
[0.3.7]: https://github.com/shiblon/engram/compare/v0.3.6...v0.3.7
[0.3.6]: https://github.com/shiblon/engram/compare/v0.3.5...v0.3.6
[0.3.5]: https://github.com/shiblon/engram/compare/v0.3.4...v0.3.5
[0.3.4]: https://github.com/shiblon/engram/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/shiblon/engram/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/shiblon/engram/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/shiblon/engram/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/shiblon/engram/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/shiblon/engram/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/shiblon/engram/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/shiblon/engram/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/shiblon/engram/releases/tag/v0.1.0
