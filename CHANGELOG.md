# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Versions before 0.5.0 predate the project's conventional-commit convention, so
their entries are summarized under "Changed". GitHub release notes are generated
independently from commit messages by goreleaser; this file is the curated,
in-repo companion.

## [Unreleased]

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

[Unreleased]: https://github.com/shiblon/engram/compare/v0.11.3...HEAD
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
