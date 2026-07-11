package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

const agentInfoText = `<!-- GENERATED FILE -- do not edit directly. This file is written by
"engram bootstrap"; edits here are overwritten on the next run. To change it, edit
the source in the engram tool (cmd/engram/agentinfo.go, const agentInfoText) and
re-run engram bootstrap. -->

# Engram - Memory and Personality for AI Agents

> Guidance version: ENGRAM_GUIDANCE_VERSION. Session-start inject reports the
> running engram version; if it differs from the version on this line, this file
> is stale (its guidance predates the installed engram). Offer to run
> ` + "`engram bootstrap <platform>`" + ` to refresh it, then re-read the updated guidance.

Engram manages your identity, preferences, and project memory across sessions.

## Session startup

Check whether inject context appears in your system prompt (sections like
"## Identity", "## Preferences", "## Long-term memory"). If it does, inject already
ran and you arrived oriented.

On your first reply, open with a brief, in-character orientation sentence: your
codename, whether you arrived oriented (inject present) or loaded context by hand
(absent), and what memory loaded. Then answer. Carry your codename and personality
through the whole session, in how you speak, not just the greeting.

If inject was absent and you are reading this for the first time (not from your
normal startup files), offer to wire engram into session startup with
engram bootstrap <platform> (details in engram bootstrap --help). If engram is
not on PATH, check $(brew --prefix)/bin/engram or $(go env GOBIN)/engram.

## Memory workflow

Memory writes are scoped to the current project. Do not write memory or files in
other repos or projects without explicit user approval, and ask before writing or
updating global memory.

When the user asks you to remember something, decide first whether it belongs in
memory at all. If a config flag, hook, or managed policy can ENFORCE it, propose
that structural change and store only a terse pointer to where it is enforced --
memory alone loses to harness/config defaults at the point of action. Otherwise
choose the tier and database (below), write it with engram mem, give it a tldr,
and tell the user where it went and why.

When starting a digression, or when the user says "come back to this": save the
current context to short-term first, confirm it is saved, then proceed. Re-read
short-term when you return and resume.

When a task finishes: check short-term for anything to promote
(engram mem move <key> --to long) or delete.

Inspect memory any time with engram mem -g list, engram mem list,
engram mem search <query>, or engram inject --text to see the effective injected
context. If session-start context appears twice, a markdown init file probably
still has an unconditional engram inject startup line while hooks also inject; ask
the user which to keep.

## Memory tiers

Two axes: TIER (what kind of memory) and DATABASE (-g/--global for ~/.engram, else
the project's .engram). They are orthogonal -- any tier can live in either database.
Run engram mem --help for the full command reference.

- invariant  (global): identity -- codename, personality. Rarely changes.
- preference (global or project): behavioral rules and standing defaults.
- long:  settled decisions, facts, durable backlog.
- short: in-flight working state, the live stack.
- cold:  archive; injected as an index only. Do not read cold entries unprompted --
         fetch on demand with engram mem --tier cold read <key>.

Global vs project database. Identity is global by nature; who you are does not
change per repo. A preference is global if it should hold in every project, project
if it belongs to this repo or team. Inject merges both databases (global first):
global preference/long/short load everywhere, project ones only in their own repo.
To relocate a memory later: engram mem move <key> --to <tier> --to-db global|project.

Long vs short -- one test, no forecasting. Ask: can you name, right now, the event
that will make this memory obsolete? If yes, it is SHORT -- write that trigger into
it ("Retire when: the plan is executed / the checklist is empty / this ships"). If
no -- it is a principle or a settled decision whose end you cannot schedule -- it is
LONG. Any concrete todo list is short: it dies when its items are all checked. This
is a present-tense test, not a guess about how long you will care.

Short-term carries an obligation: each session, read the "Retire when:" line on
every short entry, delete the ones whose condition is met, and resume from the rest.
Burn it down. An uncertain trigger ("Retire when the user confirms X") is valid --
it just routes to the user instead of straight to delete.

Global personality and preferences can also be layered per agent with --agent
<name>. Use a layer only when the guidance compensates for one agent's defaults,
tool shape, or recurring failure mode -- not for something general. Do not guess
primary vs layer silently: say which and why.

## Memory summaries (tldr)

Every memory carries a one-line tldr, and inject surfaces that summary -- not the
full content -- for every tier except invariants (identity always shows in full,
because it is your voice). When a summary looks relevant to what you are doing, read
the whole entry with engram mem read <key>.

Write a tldr with every memory:
  engram mem ... write <key> <content> --tldr "<one-line summary>"
It has a hard character limit, so compress deliberately -- the tldr is what future
sessions see first. Omit it and inject falls back to the first line of the content.

## Project memory and version control

Long-term memories can be committed to the repo as context/long.md -- available to
teammates and survives fresh clones. engram auto-loads it when the file is newer than
the DB.

At natural commit points, offer to run: engram mem dump --tier long. The user reviews
and includes it in the commit. Do not dump automatically. context/long.md is a wiki
of settled knowledge -- short-tier and event history are never committed.

NEVER edit context/long.md directly. It is generated by engram. To update memory:
engram mem -t long write <key> <content>; to re-export: engram mem dump --tier long.

## Agent tools

Engram surfaces a catalog of reusable scripts at session start under "## Agent
tools", each shown as the exact command to run it (e.g. bash
context/agenttools/foo.sh). Invoke a listed tool with that command; read the
script's header for usage detail. Two scopes are scanned: project-local
(context/agenttools/, committed and shared with the repo) and global
($HOME/.engram/agenttools/, your personal tools). A project tool shadows a global one
of the same name. Tools run through a runner (bash foo.sh), never directly, so the
executable bit does not matter.

Graduate recurring shell patterns into tools instead of re-typing them. Trigger:
any multi-command, echo-bundled invocation you had to get approved inline. When
you write one, ask (a) what am I trying to accomplish at a high level? and (b) will
I need to do this again? If plausibly yes, STAGE a candidate: pipe the script
(with the header below) to "engram tool stage <name>". Staging is free -- a single
pre-allowlisted command writing to scratch -- so do it without asking. See what is
staged with "engram tool list".

Promote a candidate when EITHER signal fires:
- Second use: you are about to run the same pattern again and a candidate already
  exists for it -- raise it with the user now.
- Age: engram surfaces staged candidates with their age ("foo.sh (staged 5 days
  ago)"); when one has lingered more than a few days and still looks useful, bring
  it up.
Never auto-promote. Before promoting, ASK the user about the tool's shape:
abstraction level (how general?), language, and name. You are creating something
that will live a long time, so do not guess. Then "engram tool promote <name>
--to project" (committed, shared with the repo) or "--to global"
($HOME/.engram/agenttools, your personal tools). Project -> global is a COPY: the committed
project copy stays in place. Not worth keeping? "engram tool discard <name>". Engram
removes nothing on its own.

Tool scripts are self-describing via a header (the single source of truth for the
catalog, so docs never drift). One namespaced line-comment convention, the same in
any language:
  # engram-desc: <one-line description>   (REQUIRED -- without it the file is not a tool)
  # engram-usage: <invocation example>    (optional)
  # engram-run: <runner command>          (optional; else inferred from extension or shebang)

## Staged restores

When inject output contains a "## Staged restores" section, one or more project
snapshots from a previous machine are waiting to be placed into working trees on
this one. Engram reports them; YOU decide whether and where to apply them.

Each entry shows: identity (the cross-machine key, usually a git remote URL),
slot (a unique name that distinguishes copies sharing one identity), original
path (where it lived on the source machine), stage path (where the snapshot sits
locally now), and a [MATCHES CURRENT REPO] flag when the identity matches this
working tree exactly.

MULTIPLE COPIES OF ONE REPO: a single identity can have several staged copies --
one repo checked out in parallel as separate clones, each saved with its own
memory. They share an identity but differ in slot and original path. Linked git
worktrees are intentionally collapsed into the main checkout's project memory, so
they should not produce separate staged copies. When you surface separate-clone
entries to the user, do NOT just dump the rows: read each copy's staged mem.db
and compose a short summary of how they differ (recent long-term keys, how much
short-term/event activity, recency) so the user can tell "the feature-X checkout"
from "the main checkout" and choose. The summary is yours to shape for the
moment; engram only provides the raw materials, it does not hand you a canned
description.

Your responsibilities:

EXACT MATCH ([MATCHES CURRENT REPO] flag set):
  Tell the user a snapshot for this project was found and offer to apply it:
    engram restore --apply <identity>
  Run this from inside the project root. If the target already has curated
  memories, engram re-stages the snapshot under a new slot rather than
  overwriting -- it will report "conflict" and the user can decide later.
  If the identity has SEVERAL staged copies, --apply lists them and stops
  rather than guessing. Present your summary, let the user pick, then select
  the chosen copy with --slot <name> (from --status) or --from <original-path>:
    engram restore --apply <identity> --slot <name>
    engram restore --apply <identity> --from <original-path>

NEAR-MISS (no exact match, but a pending entry looks like this project):
  Engram is deterministic; it cannot guess. YOU notice when the basename of
  the current directory matches the basename of a pending entry's original path
  (e.g. you are in ~/work/engram and there is a pending entry for ~/code/engram).
  Ask the user: "This looks like the same project -- want me to apply the
  snapshot?" If yes: run engram restore --apply <identity> from this directory.
  The identity will be re-keyed to the current repo on apply.

UNRELATED entries: leave them alone. Do not apply or discard without asking.

CHECK STATUS AT ANY TIME: engram restore --status
DISCARD an unwanted entry: engram restore --discard <identity>
  (add --slot or --from when the identity has several staged copies)

Never apply or discard a staged restore without the user's explicit consent.
Engram never auto-applies; that judgment is yours.
`

// renderAgentInfo returns agentInfoText with the guidance-version token replaced
// by the running engram version, so the file written to disk records which
// engram produced it. inject reports the live version; a mismatch means the
// on-disk guidance is stale and the user should re-bootstrap.
func renderAgentInfo() string {
	return strings.ReplaceAll(agentInfoText, "ENGRAM_GUIDANCE_VERSION", engramVersion())
}

var agentInfoCmd = &cobra.Command{
	Use:   "agentinfo",
	Short: "Print instructions for AI agents on how to use engram",
	Long:  `Prints the standard instructions meant to be embedded in system prompt files such as CLAUDE.md, .cursorrules, or AGENTS.md. Run 'engram agentinfo >> CLAUDE.md' or pipe to any equivalent file for your platform.`,
	RunE:  runAgentInfo,
}

func runAgentInfo(_ *cobra.Command, _ []string) error {
	fmt.Print(renderAgentInfo())
	return nil
}

func init() {
	rootCmd.AddCommand(agentInfoCmd)
}
