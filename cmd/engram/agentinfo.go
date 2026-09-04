package main

import (
	"fmt"
	"strings"

	"github.com/shiblon/engram/pkg/engram"
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

const memoryConsolidationGuidance = `## Memory consolidation

MEMORY IS CONSOLIDATED AS IT IS ACCUMULATED. Memory improves over time by a
learning process: you notice contradictions between memories you are writing and
memories already in your session context, and surface those contradictions or
points of confusion to the user. Your own confusion is a valid signal. Say so
rather than guessing.

Three things qualify: an existing entry answers the same question differently; an
existing entry states the same intent, and a second copy will drift; or you are
about to write "this outranks X" instead of a principle covering both.

When one fires, do not append. Iterate with the user on a replacement memory that
covers the confusing cases and harmonizes them. Sometimes existing memories must
be conditionally scoped; in the best case a single new principle replaces them
all. Write the result and retire what it replaced.

You detect, the user decides. Whether two memories conflict is a property of the
corpus. Which harmonization is USEFUL is the user's call, because usefulness is
defined by the person the memory serves. They are not a fallback for the hard
cases.

Silence is the default. Raise it only when you can name both memories and the
specific disagreement. Adjacent topics are not conflicts. When two rules share no
vocabulary, test them by running one concrete case through both and comparing the
verdicts. On request, run the same check deliberately across everything stored
rather than only across what you are writing; reporting nothing is a valid result.

FEEDBACK ON THE MEMORY MANAGEMENT PROCESS ITSELF refines this section: how to
decide what to consolidate, how to rephrase consolidation candidates, when to
raise a conflict, how much to say. Users will not phrase such feedback as
rule-refinement, so recognize it by what it is about. Unless it obviously belongs
there, do not put it there; ask if unsure. Write such refinements to
engram:/long/__memory_consolidation__ , never here -- this file is regenerated by
engram bootstrap and that entry is not. When it exists it supersedes this
section, and its summary loads every session so you will see it. A standard for
how memory CONTENT should be worded is a separate preference, not a refinement of
this section.`

// experimentalGuidance describes features whose user-facing contract is not stable
// yet. It is deliberately one section rather than one per trial: an agent needs to
// know that the class exists and how to look it up, and each experiment then gets a
// short paragraph carrying only the judgment its help text cannot.
const experimentalGuidance = `## Experimental features

Some engram commands are experimental: their flags, schemas, and output may change
in PATCH releases, which normal semver would forbid. They are labeled
"[experimental: <key>]" in help. Run ` + "`engram experiments`" + ` to see every active trial
with its hypothesis, the surfaces that may move, and the events that promote or
remove it. Prefer these for real work when they fit, and expect to re-learn a
detail after an upgrade rather than assuming last week's invocation still parses.

### engram dispatch (experimental: dispatch)

Hands a decomposed task to one or more provider CLIs (claude, codex, ...) as child
processes -- possibly different providers and models per slice -- and collects the
results. Read ` + "`engram dispatch --help`" + ` before using it. Two things belong here rather
than in help text, because they are judgment and not usage:

WHEN TO FAN OUT. Each child pays its context load before doing any work, and that
is tens of thousands of tokens, so decomposition pays only when per-slice work is
large relative to per-slice overhead AND the slices are genuinely independent. For a
three-line diff, eight children is worse than one call in both money and quality.
Two failure modes are non-obvious and both produce confident garbage:

  - Slicing destroys the seams. Architectural and cross-file problems live exactly
    where the slicing cut, so N deep-but-narrow reviewers can each be correct and
    collectively miss the only bug that mattered. Always keep one child looking at
    the whole change at a higher altitude alongside the deep slices.
  - Fan-out amplifies false positives. A child asked to review something is
    motivated to find something, so N children reliably produce N times the noise.
    The per-slice prompt must explicitly license silence.

The child does not self-orient. A dispatched reviewer needs no codename and no
personality; paying to load them dilutes a focused task with instructions about how
to be charming. So run children with context discovery suppressed and compose
exactly the context the task needs in the system prompt.

READ-ONLY BY DEFAULT, AND SUGGESTING OTHERWISE IS A BIG DEAL. A task that names no
authority gets read-only; anything else must be asked for by name, and dispatch warns
on the stream when a child is write-capable. Almost every good use of dispatch is
read-only -- review, analysis, second opinions, research -- and you should propose a
write-capable fan-out only when you can say why the parent cannot apply the changes
itself. Four reasons, and the first is the one people miss:

  - A blocked approval looks exactly like a hang. An approval prompt in a process
    with no controlling terminal does not degrade to "ask"; it waits until the
    deadline. So a guardrail doing its job is indistinguishable from a broken child,
    and the obvious fix is to reach for bypassPermissions or
    --dangerously-bypass-approvals-and-sandbox. The pressure runs toward removing
    safety precisely BECAUSE the safety worked. Do not follow it; fix the task.
  - Nobody can interrupt. A one-shot CLI has no between-turns for an outsider to
    reach into, so there is no "wait, stop, that is the wrong file." A human sees
    the run when it is over.
  - N writing children share one tree with no coordination. Read-only children are
    trivially parallel-safe; writing ones are not, and dispatch has no locking
    because read-only work never needed any. Two children editing one file is a
    lost update no amount of per-child correctness prevents.
  - You observe the side effects last. Per-task progress does not exist, so for a
    read-only child the result IS the deliverable, while for a writing child the
    thing most needing supervision is the thing seen latest.

The cheap alternative is almost always available: have the child produce a PATCH or a
findings list, and let the parent apply it. The parent has a human attached. That puts
judgment where a human can exercise it and costs a diff.

GET CONSENT FOR THE COST, at least once per kind of fan-out: propose the
decomposition with its shape, its N, and a rough cost, and let the user answer. That
answer is worth capturing in the skill, along with whatever they say about how they
want the work done. Do not ask them to specify it in advance by questionnaire.

FAN-OUT IS A SKILL DECISION, not a separate mechanism. Any skill can carry a
dispatch plan: a fan-out predicate, a decomposition rule ("one child per changed
file"), a per-slice prompt template, a provider and model policy per slice class,
and an assembly rule for merging N results. Name providers by ROLE ("the
architectural slice wants a strong model, the mechanical slices want a cheap one")
so the skill stays portable, and say in the skill's own TRIGGER that it fans out --
the trigger is in context every session, whereas this paragraph is not.

REPAIRING A PROVIDER. Invocation is learned, not compiled in. Each provider's argv
recipe is a long-term memory (key ` + "`dispatch-spec-<provider>`" + `) holding one fenced JSON
block, so when a flag moves upstream you read the memory, edit the JSON, and write it
back. A run that fails with state ` + "`spec_error`" + ` means the spec is wrong rather than the
work; it carries a repair instruction. Re-learn with ` + "`engram dispatch survey <exe>`" + `,
then ` + "`engram dispatch spec put`" + `, then ` + "`engram dispatch probe <provider> --model <M>`" + `.
Always probe with an explicit model: a misread model flag is silent, and every child
in a fan-out then quietly runs the default.`

const memoryTierGuidance = `Engram's CLI tier tokens are fixed: ` + "`invariant`" + `, ` +
	"`preference`" + `, ` + "`long`" + `, ` + "`short`" + `, and ` + "`cold`" + `. Human-facing prose
often says "long-term" and "short-term"; the corresponding canonical flags are
` + "`--tier long`" + ` and ` + "`--tier short`" + `.`

const agentReferenceText = `## Session startup

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

Linked Git worktrees share the main checkout's Engram database. Read-only
commands normally work without write access to that checkout. If Engram reports
that SQLite needs WAL coordination files, request narrowly scoped filesystem
access to the exact directory in the error and retry once. Do not create a
separate .engram directory in the linked worktree.

` + memoryConsolidationGuidance + `

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

` + experimentalGuidance + `

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

// guidanceTopic is the single policy-and-reference unit from which engram renders
// the installed policy kernel, the agentinfo index, topic reads, and the full
// manual. The kernel fields answer only what an agent must recognize and do
// without first deciding to consult documentation; BodyHeading points at the
// corresponding operational reference in agentReferenceText.
type guidanceTopic struct {
	Name        string
	Title       string
	Summary     string
	When        string
	Do          string
	Read        string
	Boundary    string
	BodyHeading string
}

const (
	guidanceStartMarker = "<!-- engram:guidance:start -->"
	guidanceEndMarker   = "<!-- engram:guidance:end -->"
)

func guidanceTopics() []guidanceTopic {
	return []guidanceTopic{
		{
			Name:        "session-context",
			Title:       "Session context",
			Summary:     "Load identity and memory at every context boundary.",
			When:        "At the first interaction in a new, resumed, cleared, or compacted context.",
			Do:          "Treat sections such as `## Orientation`, `## Identity`, `## Preferences`, or `## Long-term memory` as injected Engram context and use them without injecting again. If they are absent, run the provider-specific inject command below before other work and treat its output as session context. On the first reply, briefly name the codename, say whether context arrived or was loaded by hand, and summarize what memory loaded before answering.",
			Boundary:    "A lifecycle hook is delivery machinery for this same rule, not a separate semantic mode.",
			BodyHeading: "Session startup",
		},
		{
			Name:        "memory-workflow",
			Title:       "Memory workflow",
			Summary:     "Scope, classify, write, and revisit memories safely.",
			When:        "When the user asks to remember something, a digression must be checkpointed, or a task finishes.",
			Do:          "Keep writes in the current project by default; ask before global writes. Prefer enforceable configuration over memory, give every memory a tldr, save live work before digressing, and revisit short-term memory when work finishes.",
			Read:        "Before choosing scope, tier, layer, or a memory command, read `engram agentinfo memory-workflow` and `engram agentinfo memory-tiers`.",
			Boundary:    "General cross-agent identity and preferences belong in the primary global layer; agent-specific compensation belongs in that agent's layer.",
			BodyHeading: "Memory workflow",
		},
		{
			Name:        "memory-consolidation",
			Title:       "Memory consolidation",
			Summary:     "Resolve overlapping or contradictory memories instead of appending drift.",
			When:        "Before a memory write, when an existing entry answers the same question differently, expresses the same intent, or would need an outranking rule.",
			Do:          "Name the two memories and their specific disagreement, then ask the user to choose a harmonized replacement. Do not append another rule while the conflict is unresolved.",
			Read:        "Read `engram agentinfo memory-consolidation` before resolving the conflict or storing feedback about consolidation itself.",
			Boundary:    "Adjacent topics are not conflicts; silence is the default unless a concrete case produces different verdicts.",
			BodyHeading: "Memory consolidation",
		},
		{
			Name:        "memory-tiers",
			Title:       "Memory tiers and summaries",
			Summary:     "Choose canonical tiers, databases, agent layers, and summaries.",
			When:        "When reading, writing, moving, or retiring memory.",
			Do:          "Use only the canonical tier tokens `invariant`, `preference`, `long`, `short`, and `cold`. At each session, read every short-term retirement condition and delete entries whose condition is met.",
			Read:        "Read `engram agentinfo memory-tiers`; for summary edits also read `engram agentinfo memory-summaries`.",
			BodyHeading: "Memory tiers",
		},
		{
			Name:        "memory-summaries",
			Title:       "Memory summaries",
			Summary:     "Write concise summaries that make injected memory retrievable.",
			When:        "When writing a memory or reviewing an injected summary.",
			Do:          "Write a one-line tldr with every memory and read the full entry when its injected summary is relevant.",
			Read:        "Read `engram agentinfo memory-summaries` for summary behavior and limits, and `engram agentinfo safe-memory-updates` before changing only a tldr.",
			BodyHeading: "Memory summaries (tldr)",
		},
		{
			Name:        "safe-memory-updates",
			Title:       "Safe memory updates",
			Summary:     "Change summaries without corrupting memory bodies.",
			When:        "When changing only an existing memory's tldr.",
			Do:          "Use `engram mem ... tldr`; never feed display-formatted `engram mem read` output back into `engram mem write`.",
			Read:        "Read `engram agentinfo safe-memory-updates` before retrying a failed memory edit.",
			BodyHeading: "Safe memory updates",
		},
		{
			Name:        "sharing",
			Title:       "Project memory and sharing",
			Summary:     "Export durable project knowledge without reviving legacy context files.",
			When:        "When sharing project memory with teammates or encountering legacy context files.",
			Do:          "Read the sharing topic before exporting, migrating, or removing those files.",
			Read:        "Read `engram agentinfo sharing`.",
			Boundary:    "Do not dump memory or recreate a legacy `context/` directory automatically.",
			BodyHeading: "Project memory and sharing",
		},
		{
			Name:        "agent-tools",
			Title:       "Agent tools",
			Summary:     "Use and place reusable scripts deliberately.",
			When:        "When inject lists an agent tool or a nontrivial command sequence is likely to recur.",
			Do:          "Read the agent-tools topic before invoking, creating, or placing a reusable tool.",
			Read:        "Read `engram agentinfo agent-tools`.",
			Boundary:    "A tool's presence does not trigger its use; an instruction or skill must point the agent to it.",
			BodyHeading: "Agent tools",
		},
		{
			Name:        "skills",
			Title:       "Skills",
			Summary:     "Retrieve matching procedures and capture verified first-use workflows.",
			When:        "At task start, and again after completing and verifying a user-named category of work.",
			Do:          "Scan the injected Skills index and read any matching skill before acting. After verified first use, apply the capture criteria in the skills topic.",
			Read:        "Read `engram agentinfo skills` before searching for a missing skill, capturing one, or classifying automation.",
			Boundary:    "A no-results search does not override a visible injected index, and unfinished or instance-specific work is not a skill.",
			BodyHeading: "Skills: retrieval and first-use capture",
		},
		{
			Name:        "experiments",
			Title:       "Experimental features",
			Summary:     "Use unstable features with their current contracts and safeguards.",
			When:        "When a command or help entry is labeled experimental, including `engram dispatch`.",
			Do:          "Read the experiments topic and current command help before proceeding.",
			Read:        "Read `engram agentinfo experiments`, then run `engram experiments` and the relevant command's `--help`.",
			Boundary:    "Do not assume a PATCH release preserves an experimental invocation.",
			BodyHeading: "Experimental features",
		},
		{
			Name:        "staged-restores",
			Title:       "Staged restores",
			Summary:     "Inspect and place restored snapshots without guessing or overwriting.",
			When:        "When inject reports a `## Staged restores` section.",
			Do:          "Read the staged-restores topic, summarize relevant candidates, and get explicit user consent before applying or discarding any snapshot.",
			Read:        "Read `engram agentinfo staged-restores`.",
			Boundary:    "Unrelated entries are left alone; Engram never auto-applies a restore.",
			BodyHeading: "Staged restores",
		},
	}
}

func guidanceTopicByName(name string) (guidanceTopic, bool) {
	for _, topic := range guidanceTopics() {
		if topic.Name == name {
			return topic, true
		}
	}
	return guidanceTopic{}, false
}

func guidanceTopicNames() []string {
	names := make([]string, 0, len(guidanceTopics()))
	for _, topic := range guidanceTopics() {
		names = append(names, topic.Name)
	}
	return names
}

func normalizeGuidanceAgent(agent string) string {
	agent, _ = engram.NormalizeAgent(agent)
	return agent
}

func guidanceInjectCommand(agent string) string {
	cmd := "engram inject --text"
	if agent = normalizeGuidanceAgent(agent); agent != "" {
		cmd += " --agent " + agent
	}
	return cmd
}

func renderKernelTopic(topic guidanceTopic, agent string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n\n- WHEN: %s\n- DO: %s\n", topic.Title, topic.When, topic.Do)
	if topic.Name == "session-context" {
		fmt.Fprintf(&b, "  Provider-specific inject command: `%s`.\n", guidanceInjectCommand(agent))
	}
	if topic.Read != "" {
		fmt.Fprintf(&b, "- READ: %s\n", topic.Read)
	}
	if topic.Boundary != "" {
		fmt.Fprintf(&b, "- BOUNDARY: %s\n", topic.Boundary)
	}
	return b.String()
}

// renderGuidanceKernel renders the durable policy installed in every provider's
// init surface. It contains recognition triggers and immediate obligations, not
// the operational manual. All provider differences reduce to the inject command.
func renderGuidanceKernel(agent string) string {
	agent = normalizeGuidanceAgent(agent)
	var b strings.Builder
	b.WriteString("\n" + guidanceStartMarker + "\n")
	b.WriteString("## Engram Policy Kernel\n\n")
	fmt.Fprintf(&b, "> Guidance version: %s. If session-start inject reports a different version, this guidance predates the installed engram; if the line is absent, it is also stale. Tell the user and offer to run `engram bootstrap` to refresh it.\n\n", engramVersion())
	b.WriteString("This kernel carries obligations that must survive context boundaries. Operational reference is available through `engram agentinfo`; run it with no arguments for the topic index.\n\n")
	for i, topic := range guidanceTopics() {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(renderKernelTopic(topic, agent))
	}
	b.WriteString(guidanceEndMarker)
	return b.String()
}

// referenceSections parses the legacy prose source into independently
// addressable topic bodies. The registry owns their public names and order;
// headings in the source are merely stable anchors while the prose is migrated
// section-by-section out of the former monolith.
func referenceSections() map[string]string {
	text := strings.ReplaceAll(agentReferenceText, "ENGRAM_GUIDANCE_VERSION", engramVersion())
	sections := make(map[string]string)
	var heading string
	var body strings.Builder
	flush := func() {
		if heading != "" {
			sections[heading] = strings.TrimSpace(body.String())
		}
		body.Reset()
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "## ") {
			flush()
			heading = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			continue
		}
		if heading != "" {
			body.WriteString(line)
			body.WriteByte('\n')
		}
	}
	flush()
	return sections
}

func renderAgentInfoIndex() string {
	var b strings.Builder
	b.WriteString("# Engram agent reference\n\n")
	b.WriteString("Use `engram agentinfo <topic>` to read a topic, `<topic> --full` to include its policy-kernel entry, or `engram agentinfo --full` for the assembled manual.\n\n")
	for _, topic := range guidanceTopics() {
		fmt.Fprintf(&b, "%-22s %s\n", topic.Name, topic.Summary)
	}
	return b.String()
}

func renderAgentInfoTopic(topic guidanceTopic, full bool, agent string) string {
	var b strings.Builder
	if full {
		b.WriteString(renderKernelTopic(topic, normalizeGuidanceAgent(agent)))
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "## %s\n\n", topic.Title)
	body := referenceSections()[topic.BodyHeading]
	b.WriteString(body)
	b.WriteByte('\n')
	return b.String()
}

// renderAgentInfo returns the assembled manual for compatibility with internal
// callers and tests. The CLI's no-argument form deliberately renders only the
// compact index; --full selects this view.
func renderAgentInfo() string {
	return renderAgentInfoForAgent("")
}

func renderAgentInfoForAgent(agent string) string {
	var b strings.Builder
	b.WriteString("# Engram - Memory and Personality for AI Agents\n\n")
	b.WriteString(renderGuidanceKernel(agent))
	b.WriteString("\n\n# Operational reference\n\n")
	for _, topic := range guidanceTopics() {
		b.WriteString(renderAgentInfoTopic(topic, false, ""))
		b.WriteByte('\n')
	}
	return b.String()
}

var agentInfoFull bool
var agentInfoAgent string

var agentInfoCmd = &cobra.Command{
	Use:   "agentinfo [topic]",
	Short: "List or read operational guidance for AI agents",
	Long: `With no arguments, list the available guidance topics. With a topic,
print that topic's operational reference. Add --full to include policy: for one
topic it prepends the matching kernel entry; without a topic it prints the
assembled policy kernel and reference manual.`,
	Args:      cobra.MaximumNArgs(1),
	ValidArgs: guidanceTopicNames(),
	RunE:      runAgentInfo,
}

func runAgentInfo(cmd *cobra.Command, args []string) error {
	agent, err := engram.NormalizeAgent(agentInfoAgent)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		if agentInfoFull {
			_, err := fmt.Fprint(cmd.OutOrStdout(), renderAgentInfoForAgent(agent))
			return err
		}
		_, err := fmt.Fprint(cmd.OutOrStdout(), renderAgentInfoIndex())
		return err
	}
	topic, ok := guidanceTopicByName(args[0])
	if !ok {
		return fmt.Errorf("unknown agentinfo topic %q; run 'engram agentinfo' to list topics", args[0])
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), renderAgentInfoTopic(topic, agentInfoFull, agent))
	return err
}

func init() {
	agentInfoCmd.Flags().BoolVar(&agentInfoFull, "full", false, "include policy-kernel guidance")
	agentInfoCmd.Flags().StringVar(&agentInfoAgent, "agent", "", "render agent-specific inject commands in policy guidance")
	rootCmd.AddCommand(agentInfoCmd)
}
