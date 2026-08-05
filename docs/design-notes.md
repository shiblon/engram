# Design notes

A running list of the considerations we weigh when changing engram's memory
model. These are not rules so much as the load-bearing ideas the code already
leans on. If a change fights one of these, that is worth a conversation, not a
silent override.

## No memory default should require predicting the future

The tier and lifetime of a memory should be decided by a present-tense test, not
a forecast about how long you will care. Forecasts are answered inconsistently,
so they leak into the user having to name everything by hand.

- **Long vs short** is "can you name, right now, the event that retires this?"
  If yes it is short (record the trigger: `Retire when: ...`); if no it is long.
  Not "how long will I want it?".
- **tldr, not warmth.** Every memory gets a one-line summary, uniformly. We
  rejected a per-memory "warmth" level precisely because it reintroduced a
  forecast ("how relevant will this be at some future session?").

When you add a knob, ask whether it forces a prediction. If it does, prefer a
mechanical rule keyed on something that already exists (the tier, an event).

## Identity is full and redundant; everything else is a summary

Invariants (codename, personality) render in full wherever they appear, and we
are happy to have them in more than one always-loaded place. Personality is the
context canary: the more surfaces it lives on, the sooner drift shows. Every
other tier is surfaced as a tldr at session start, with full text an
`engram mem read <key>` away.

Corollary: never make identity depend on a single channel being present. If a
channel is missing or stale, personality must still be there.

## Inject is the universal channel; rendered `.md` files are an enhancement

`engram inject` is the one channel every platform has, so it must carry enough on
its own. The rendered standing files (`engram-invariants.md`,
`engram-preferences.md`) that ride a platform's always-loaded instruction channel
are a *full-text enhancement*, wired for Claude Code today and absent elsewhere.
So inject stays self-sufficient (identity full, the rest summarized) and the
`.md` layer, where present, adds durable full text on top. We deliberately accept
a little duplication on Claude Code rather than couple inject to whether the
render targets happen to be live.

## Retrieval rides the injected index; search is a lossy fallback

A skill is delivered the same way every other memory is: its trigger and tldr
render in the session-start index, in context before the task begins. So the
first retrieval move is recognition, not search -- the matching skill is already
in front of the agent. Making `engram skill search` the primary step regressed
this: as a bare long-term memory the summary just worked, and calling the same
entry a "skill" bolted a lookup in front of something already present. Search
earns its place only when the index cannot answer (it was budget-truncated,
scrolled out of a long session, or the match is on wording in the body the
one-line index never shows), and a "no results" is never proof of absence when
the index is sitting in context.

Search is a fallback partly because it is lossy, and the loss is easy to
underestimate. FTS5 treats space-separated barewords as an implicit **AND**, so
a free-text query gets *narrower* with every word added, the opposite of what
recall wants. An agent that helpfully expands "standup" into "standup morning
routine" matches fewer entries, not more, and a skill whose text only says
"standup" drops out entirely. So the search layer builds its own MATCH query
(tokenize, quote each term, OR them) and leans on bm25 rank to order the hits:
more words widen the net and the best match floats up. Quoting each token also
keeps stray operator characters in arbitrary text from turning a query into an
FTS syntax error. Whenever search sits on FTS, assume raw user words should be an
OR of ranked terms, not an AND of required ones.

## Identity is global by nature; behavior can be scoped

Who the agent is does not change per repo, so identity lives only in the global
database. Preferences are behavioral and can be global (hold everywhere) or
project-scoped (this repo or team). Inject merges both databases, global first.

## Experiments need exit conditions, not permanent asterisks

An experimental feature may be introduced and refined in patch releases because
its user-facing contract is explicitly not stable yet. The CLI help and changelog
must name its experiment key, and `engram experiments` must report:

- the hypothesis being tested;
- which surfaces may change;
- the event that promotes it to stable; and
- the event that removes it.

Promotion happens in a minor release. It removes the experiment label and registry
entry, making the feature part of the supported surface. An experiment can instead
be deprecated until its removal condition is met. Minor-release preparation must
review every registry entry and choose deliberately among promotion, continued
trial, deprecation, and removal.

The registry and Cobra command annotations are checked by tests. This makes an
experimental label incomplete unless it points to explicit exit conditions, and
makes a stale registry entry fail once its command label disappears. As with
memory lifetime, the conditions name observable events rather than dates.

## Automation catalogs store judgments, not acknowledgments

Repository automation discovery is useful only when its expensive result -- the
agent's classification after reading headers, documentation, and call sites --
survives the scan. The catalog therefore stores one verdict per candidate with
that candidate's content digest, rather than one aggregate "reviewed" hash.

Reconciliation is local: an unchanged candidate stays silent; a changed one
retains its prior verdict and rationale for confirmation; a new one starts
unclassified; and a removed one requires explicit retirement. Changed or removed
code is not surfaced as an active project tool or skill member until reconciled.
This makes discovery a durable review loop instead of proof that somebody once
looked at a now-indivisible pile of files.

## Migrations replay from version 0, so every step must be idempotent

A fresh database applies `schema.sql` and then replays *every* migration from
version 0; an upgraded one replays from its stored version. So a migration must
be safe to run against a schema that already reflects it. SQLite has no
`ADD COLUMN IF NOT EXISTS`, so column changes use the canonical table-rebuild
pattern (copy the kept columns into a new table, swap names, recreate indexes and
triggers) rather than a bare `ALTER`. Keep `schema.sql` current as the truth for
a fresh database.

## Memory writes are scoped to the current project

Do not write memory or files in other repos or projects without explicit user
approval, and ask before writing or updating global memory. Relocating a memory
across the global/project boundary is a deliberate act: `engram mem move <key>
--to <tier> --to-db global|project`.

## Durable state is not automatically memory

New subsystems tend to arrive asking for a tier, because a tier is the most
visible place to put something that has to persist. Most of them want a table
instead. Three questions settle it, and all three are about what the tiers
already promise:

- Would a row belong in `engram save`, carried to another machine?
- Would it take a place in the priority ladder that shapes agent behavior?
- Would a human ever curate it?

Three times no means it is state, and a tier would assert otherwise. The cost of
getting this wrong is visible rather than theoretical: a tier that is not really
memory forces an "except this one" carve-out in `mem list`, `mem search`, the FTS
index, `curation_events`, `mem dump`, and the save archive. Several carve-outs
around one value is the signature of a wrong abstraction, and the schema already
has the right pattern for the alternative, since `events`, `curation_events`,
`projects`, and `automation_catalog_entries` all live outside `memories` for
exactly this reason.

The corollary is that a new table is cheap and a new tier is not. Tiers are
user-facing vocabulary that propagate into help text, agent guidance, the
priority ladder, and every uniform traversal of `memories`. Worked example: the
run mailbox in `docs/dispatch-notes.md`.
