package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

const skillManagementGuidance = `## Skills: retrieval and first-use capture

A skill is a named, task-triggered procedure. Its trigger says WHEN to retrieve
it, its tldr says WHAT outcome it provides, and its content is the full prompt or
instructions. A tool is callable machinery (script, command, MCP operation); a
skill supplies task-level judgment and may coordinate zero, one, or many tools.

RETRIEVE: The injected Skills index is the retrieval mechanism, not a pointer to
one. At the start of a task, scan that index and match the task against each
trigger; when one matches, read its full instructions with the read command the
index prints for it. The skill is already in your context, so recognition, not
search, is the primary move.

Fall to search only when the index cannot answer: a budget note on the Skills
section says it was truncated, a long or compacted session may have scrolled it
away, or you suspect the match is on wording buried in the skill body (the index
shows only trigger and tldr). Explicit recurrence language ("again", "as usual",
"same process", "another ...") means a skill very likely exists -- find it in the
index first, and search only if it is not visible there:
  engram skill search "<task words>"
  engram skill search -g "<task words>"
Search is OR-matched and forgiving, so a distinctive word or two outperforms a
long inferred phrase. A "no results" is never proof a skill is absent when the
index is in front of you -- trust the index over the search.

CAPTURE ON FIRST USE: After completing and verifying a task, save it as a
project skill without waiting for repetition when BOTH are true:
1. The user gave the activity a recognizable name or concept describing a
   category of work, not merely this instance. Mechanical test: after removing
   dates, IDs, paths, and other instance values, the name still describes a task
   someone could request.
2. Completion required at least two ordered operations, OR exposed a reusable
   correction, constraint, or gotcha.

Derive the key and trigger from the user's language. The trigger describes task
intent ("when the user asks to prepare or post a standup"), the tldr names the
outcome, and content records the verified procedure plus an explicit TOOLS NEEDED
section. Tell the user what was captured. Ask before writing it globally; project
scope is the default. If a matching skill exists, update it instead of creating a
duplicate. If the procedure already exists as ordinary long-term memory, preserve
its body and classify it in place with:
  engram skill adopt <key> --trigger "<task condition>" [--tldr "<outcome>"]

DO NOT CAPTURE: a single direct command with no meaningful procedure; a fact,
decision, or preference; unfinished or unverified work; an instance-specific
fix; or a generic activity named only by the agent ("repository work").

AUTOMATION CATALOG: When inject reports new, changed, or removed automation,
run 'engram skill discover'. Inspect candidate documentation, headers, and call
sites without executing candidates merely to understand them. Persist the actual
judgment with 'engram skill classify': direct-tool for one callable operation,
skill-member for part of a judgment-bearing workflow, internal for plumbing,
review for an intentionally deferred decision, or ignore for deliberately
uncataloged material. Give skill members a shared --skill key when they belong
together. Preserve a changed entry's prior verdict when it still holds; resolve a
missing entry explicitly with --as removed. Classification replaces the old
aggregate acknowledgment workflow.`

const memoryWriteSafetyGuidance = `## Safe memory updates

To change only the tldr of an existing memory, preserve its body structurally:
  engram mem ... tldr <key> "<one-line summary>"
Do not feed ` + "`engram mem read`" + ` output back into ` + "`engram mem write`" + `. Read output is
display-formatted, not a round-trip format; after a failed write it also contains
the old body, so a read-back retry can silently discard the intended edit.`

const memoryTierGuidance = `Engram's CLI tier tokens are fixed: ` + "`invariant`" + `, ` +
	"`preference`" + `, ` + "`long`" + `, ` + "`short`" + `, and ` + "`cold`" + `. Human-facing prose
often says "long-term" and "short-term"; the corresponding canonical flags are
` + "`--tier long`" + ` and ` + "`--tier short`" + `.`

const agentInfoText = `<!-- GENERATED FILE -- do not edit directly. This file is written by
"engram bootstrap"; edits here are overwritten on the next run. To change it, edit
the source in the engram binary (cmd/engram/agentinfo.go, const agentInfoText) and
re-run engram bootstrap. -->

# Engram - Memory and Personality for AI Agents

> Guidance version: ENGRAM_GUIDANCE_VERSION. Session-start inject reports the running
> engram version and flags this file as stale (it predates the installed engram) when
> they differ or this line is absent; refresh with ` + "`engram bootstrap <platform>`" + `.

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

Compact list and search output use copyable memory addresses. Prefer passing one
back to an entry command when it is available: engram:tier/key is in the current
project, engram:/tier/key is global, and engram:/preference/@codex/key selects a
global agent layer. An address supplies scope, tier, and layer without extra flags.
Bare keys plus --tier/--global/--agent remain supported.

## Memory tiers

` + memoryTierGuidance + `

Two axes: TIER (what kind of memory) and DATABASE (-g/--global for ~/.engram, else
the project's .engram). They are orthogonal -- any of the five tiers can live in
either database. Run engram mem --help for the full command reference.

- invariant  (global): identity -- codename, personality. Rarely changes.
- preference (global or project): behavioral rules and standing defaults.
- long:  settled decisions, facts, durable backlog.
- short: in-flight working state, the live stack.
- cold:  archive; injected as an index only. Do not read cold entries unprompted --
         fetch on demand with engram mem read engram:cold/<key>.

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
the whole entry by passing its listed address to engram mem read.

Write a tldr with every memory:
  engram mem write engram:<tier>/<key> <content> --tldr "<one-line summary>"
It has a hard character limit, so compress deliberately -- the tldr is what future
sessions see first. Omit it and inject falls back to the first line of the content.

` + memoryWriteSafetyGuidance + `

## Project memory and sharing

Long-term memory lives in the project's .engram DB and follows you across
machines via engram save / engram restore. Engram does NOT commit memory into
the repo and no longer auto-imports any file.

To share settled knowledge with teammates, export it and put it where your team
actually reads docs: engram mem dump --tier long prints markdown to stdout (offer
to redirect it to a file the user names -- a docs page, a wiki export, wherever
their documentation home is). Do not dump automatically; offer at natural commit
points.

If you find a committed context/long.md (or a context/ directory) in a repo, it
is from an older engram: tell the user engram no longer auto-imports it, and that
its contents are better moved into their real documentation home. Do not recreate
context/.

## Agent tools

Engram surfaces a catalog of your GLOBAL reusable scripts
($HOME/.engram/agenttools/) at session start under "## Agent tools", each shown
as the exact command to run it. Invoke a listed tool with that command; read
the script's header for usage detail. Tools run through a runner (bash
foo.sh), never directly, so the executable bit does not matter.

HOW TO DECIDE when a tool is worth creating: when a nontrivial command
sequence recurs, or a common invocation needs many flags, consider extracting
a readable script instead of re-typing or re-approving the same thing. When
you write one, ask (a) what am I accomplishing at a high level? and (b) will I
need this again? If plausibly yes, it is worth extracting.

WHERE it lives and HOW it gets used: engram no longer stages or promotes
project tools -- that machinery is gone. If there's an obvious home in the
repo (a scripts/ dir, a Makefile target), put it there. If not, PROMPT THE
USER for where it should live and how they want the agent instructed to reach
for it later (a docs note, an AGENTS.md entry, etc.). Be honest that a tool's
mere presence does not trigger its use -- something must tell the agent to
reach for it, or it will sit unused. Personal, cross-repo tools can go in
$HOME/.engram/agenttools/ (with the header below), where engram will catalog
them globally for you.

Tool scripts are self-describing via a header (the single source of truth for
the catalog, so docs never drift). One namespaced line-comment convention, the
same in any language:
  # engram-desc: <one-line description>   (REQUIRED -- without it the file is not a tool)
  # engram-usage: <invocation example>    (optional)
  # engram-run: <runner command>          (optional; else inferred from extension or shebang)

` + skillManagementGuidance + `

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
