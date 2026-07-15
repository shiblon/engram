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
