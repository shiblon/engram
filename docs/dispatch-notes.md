# Dispatch notes

`engram dispatch` lets the agent hand a decomposed task to one or more provider
CLIs running as child processes, possibly on different providers and models, and
collect the results. It ships behind the `dispatch` experiment key, so the
surfaces named below may still move in a patch release. This file records the
shape we settled on and, more importantly, why the alternatives were set aside, so
the design can be adjusted without re-deriving the reasoning.

The name is settled: `dispatch` reads correctly for the 1-to-N case, where `run`
implies a single thing and `exec` collides with codex's own subcommand. The ideas
below are what matter; ergonomics and flag spelling remain the experiment's to
change.

See `docs/design-notes.md` for the memory-model principles this leans on, and
`docs/memory-notes.md` for the "tool for the agent, not an autonomous actor" split
that constrains what dispatch may do implicitly.

## Dispatch is a batch, not a daemon

One invocation, N children, exit when the work is done. Long-running is not the
same as resident, and the distinction is what keeps dispatch compatible with
engram's one-shot nature. A single supervising process for the whole fan-out
earns its place for four reasons:

- one process group to kill takes down every child at once;
- one place to enforce the global concurrency cap and the batch deadline;
- one place to perform the join, so the calling agent needs no polling loop;
- one config carries per-task provider and model, so a cheap model can take the
  mechanical slices while a stronger one takes the architectural slice.

## Engram never detaches; tmux is the detach layer

A run dies with its session, and that is a feature rather than a limitation.
Detachment is already solved one layer down: tmux's server is separated from the
SSH connection, so a batch in a tmux pane already survives a dropped connection
without engram owning a line of daemon code.

The failure mode we are deliberately refusing is a token-spending agent loop that
outlives the session with no pane to observe it and no obvious way to discover it
is still going. Supervising that safely costs far more machinery than the
capability is worth. Anything that requires surviving the supervisor is out of
scope by construction.

Process groups still matter, with the justification inverted: they exist so that
killing the pane or interrupting the supervisor tears down the whole tree instead
of orphaning the provider's grandchildren.

## Dispatch adds no database schema

This follows from not detaching, and it is worth stating as a constraint rather
than discovering it later. Because the supervisor outlives every child and nothing
survives the batch, there is no state that needs to be durable. Run status lives
in the supervisor's memory for the few minutes it exists and is reported on the
event stream. There is no runs table and no mailbox table.

Earlier drafts of this plan had both, on the assumption that children would report
results cooperatively through engram and that a caller might need to find a run
afterward. Neither assumption survived: providers already expose their final
message through their own flags, and a batch that cannot outlive its session has
nothing to be found afterward.

The one artifact that persists is the learned provider spec, and it is stored as
ordinary long-term memory rather than as a new table or a new file location (see
below). So the whole feature adds no migration and introduces no storage concept
that did not already exist.

## Results come from the provider's own output channel

The temptation is to parse the provider's prose on stdout, which is unstable
across releases and different for every CLI. The alternative we considered was
having each child write its result back through engram cooperatively. Neither is
necessary, because both installed providers already expose the final message
reliably as a documented, structured field:

- codex has `-o, --output-last-message <FILE>` and `--json` for JSONL events;
- claude's `--output-format json` puts the answer in `.result`, alongside terminal
  state, usage, and cost.

That is not prose parsing. It is one documented field path, which makes it another
declarative entry in the learned spec (a JSON path, or a flag that names an output
file). Cooperative reporting through engram remains the fallback for a provider
offering neither, and it is not needed for the providers we have.

Where a provider reports terminal state in structured output, use it. The exit
code is a fallback, because these CLIs commonly exit zero after refusing a task or
exhausting a budget.

## The parent composes the child's context; the child does not self-orient

A dispatched reviewer does not need a codename or a personality, and paying to
load them is worse than pointless: it dilutes a focused task with instructions
about how to be charming.

So a dispatched child runs with provider context discovery disabled (claude's
`--bare` explicitly skips hooks and `CLAUDE.md` discovery), and the parent supplies
exactly the context the task needs through an explicit system-prompt argument.
This is a refinement of the original pitch rather than a retreat from it. The
claim was never "the child inherits everything"; it is that engram *composes* the
child's context deliberately, which is both cheaper and more accurate than
inheriting a session-start bundle assembled for a human conversation.

The cost is measured, not theoretical. See the probe findings below: a nine-word
prompt that produced a four-token answer loaded 44,227 tokens of context on spawn.
Multiply by N.

## Provider invocation is learned and probed, not compiled in

Hardcoding each provider's flags means a `codex` version bump can only be fixed by
a new engram release, which is the wrong coupling for a tool whose whole pitch is
that it works alongside whatever you already use. Instead, an agent reads the
provider's help, works out how to invoke it headless, and the result is cached.
Recovering from upstream flag churn becomes re-learning, not re-coding.

This is the same mechanism `automation_catalog_entries` already uses: an agent
reads the available documentation, forms a judgment, and the judgment is persisted
with a digest so drift is detected and re-confirmed rather than silently trusted.
It also lands on the right side of the division of labor in
`docs/memory-notes.md`, since deciding that the model flag is `-m` is judgment,
and judgment belongs to the agent rather than to engram's Go.

### The learned artifact is a declarative invocation spec

The recipe is an argv array with placeholders, not a shell command string and not a
script. Docker's exec form and Kubernetes `command`/`args` are the prior art, and
they chose the array for the reason that applies here.

**There is no shell, so there is nothing to quote.** `exec.Command` with a slice
calls `execve` directly, which means a prompt containing semicolons, backticks,
`$(...)`, newlines, or quotes is simply bytes inside one argv element. Shell
injection is a hazard of command *strings*; introducing a wrapper script would
create the exposure and then require a rule to defend against it. An array has no
such exposure to begin with.

- OS-neutral. An argv array works identically on Windows, where a POSIX script
  would not, which matters for the startup-file-only platform class.
- Structured, so engram can validate a spec before spending tokens on it. A script
  can only be run and observed.
- Still fully inspectable and hand-editable, which was the entire argument for a
  script. When a provider breaks at 11pm, editing one JSON field beats waiting for
  a release *or* re-running a learner.

The one rule that must hold: **substitution happens into already-split argv
elements, never into a string that is split afterward.** Substituting first would
let a prompt containing spaces become several arguments, which is the same class of
bug as shell injection arriving by a different door.

### What the spec must cover

"Bounded" is only reassuring if it can be enumerated:

- the executable, and how it is located;
- the headless or print subcommand or flag;
- the prompt transport: inline argv, stdin, or a temp file path;
- the model-selection flag, and any provider-specific spelling of model names;
- where the final result appears (a JSON path, or a flag naming an output file);
- the graded-authority flags: sandbox or permission mode, and approval policy;
- a per-child budget flag, where one exists;
- context-suppression flags, for composing the child's context deliberately;
- working-directory semantics, whether inherited or passed explicitly;
- environment variables, for CLIs configured that way rather than by flag;
- exit-code semantics, to whatever extent they are discoverable.

### The spec lives in memory, as a JSON block with its own provenance

One spec per provider, stored as a long-term memory whose content is a fenced JSON
block: the argv template and the fields above, plus in-band provenance recording the
version and help digest it was learned against, the probe results, and which fields
were verified rather than inferred.

This is the cheapest possible home. No new table, no new file location, no new verb,
and repair uses the one tool the agent already reaches for constantly. When a run
fails because a flag moved, the agent reads the memory, edits the JSON, and writes it
back. That is the difference between a self-healing design and one that merely
reports its own breakage.

**A memory, not a skill.** A skill's trigger answers "when should this be
retrieved for a task", and "how do I invoke codex headless" is not a task. Giving it
a fabricated trigger would put plumbing into the skill index that inject renders
every session, which is a retrieval surface for work rather than a config store.

An earlier draft claimed a spec is machine-local and must never travel in a save
archive, because it describes the CLI versions installed here. The provenance block
dissolves that objection: a spec arriving on a machine with different versions is
detected as stale at dispatch and re-learned, which makes it exactly as harmless as a
seed spec shipped with engram, and strictly more useful than nothing when the
versions happen to match. A traveling spec is a seed, not a lie.

JSON specifically, not merely "something structured". A botched JSON edit refuses to
parse, while a botched YAML indent can yield a different but perfectly valid
structure, which for a hand-repaired artifact means dispatching N children with a
silently dropped flag. YAML's bareword coercion cuts the same way in a file whose
values are CLI flags and model names, where `no` and `off` become booleans and a
version-ish `1.2` becomes a float. Two practical arguments agree: `encoding/json` is
stdlib where YAML is a dependency, and dispatch already parses JSON to read provider
results, so a second format would buy nothing. TOON and similar formats optimize
token count for payloads sent to a model, which this never is.

YAML would genuinely be nicer for a hand-written batch config with inline multi-line
prompts, and that is the one case worth revisiting later. It stays revisitable in the
cheap direction, because JSON is a subset of YAML: a YAML parser would accept every
existing artifact unchanged, while the reverse would require rewriting them.

One consequence worth naming: memory becomes load-bearing for execution rather than
only for guidance. That is a different reliability class than it sounds, and a better
one. Guidance memory can be read and ignored by an agent, which is why the priority
ladder ranks it below config. A spec memory is parsed by dispatch itself at the point
of action, so it cannot be quietly disregarded. It can only be malformed, and a
malformed block fails loudly with a clear error rather than silently changing
behavior. Validation therefore happens when dispatch parses the block, not when the
memory is written, which is the right trade for something meant to be hand-edited.

### Learning must probe, not believe

Reading help and trusting the inference has an expensive silent failure: a misread
model flag means every child in a fan-out quietly runs the default model, and the
output looks entirely plausible. So learning has two phases, and the second is not
optional.

- **Smoke probe.** Run the spec with a trivial prompt on the cheapest model,
  headless, short timeout, and with context discovery suppressed. Confirm output
  and a clean exit. The suppression matters: without it the probe costs a quarter
  in context loading rather than a fraction of a cent.
- **Model verification, positively.** Ask for a specific model and confirm the
  provider's own output reports that model. Both installed providers do report it
  (see findings), and a positive check is strictly better than inferring from a
  failure.
- **Flag-liveness probe, as fallback.** For a provider that will not report its
  effective config, pass a deliberately invalid model name and confirm it errors.
  On claude this fails locally for zero tokens. On codex it reaches the API first,
  so it is cheap but not free. If a provider silently falls back instead, that is
  the finding: record the model field as inferred and untrusted.

Do not verify the model by asking the child what model it is. A model reporting its
own identity is either reading something its harness injected or guessing, and the
two are indistinguishable from outside. Worse, it answers the wrong question: a
harness that injects a static identity string reports the same thing whether the
flag was honored, ignored, or silently downgraded. Trustworthy model identity comes
from the CLI's own output metadata, which is client code.

### Drift is detected at dispatch, and fails toward a fix

Staleness checks belong at dispatch time, not inject time: spawning every provider
CLI at session start to compare versions is not the light bookkeeping inject is
limited to, and inject has to stay fast. Dispatch already spawns processes, so one
`--version` is free there.

A version mismatch does not refuse the run, because most bumps do not touch flags.
It annotates, and then the first actual failure carries the repair instruction:
learned against v1.2.3, installed is v1.3.0, re-learn with the explicit verb. Drift
becomes an instruction rather than a mystery.

An exit code of 2 from a clap-based CLI is a usage error, which means the *spec* is
wrong; exit 1 means the spec ran and the work failed. That distinction is what lets
a self-healing loop know whether to re-learn or to report.

Learning is always an explicit verb and never a side effect of `inject` or
`record`, per box-of-verbs. The first learn for a provider wants human review,
because engram would be constructing an invocation from text a binary printed. The
risk is low, since you installed the binary, but "reviewed once" is the right
amount of friction for something that will then run unattended.

Seed specs still ship with engram so a fresh install works without a learning round
trip. They are seeds, not truth: learning overrides them, and the probe catches a
stale one. The gain over compiled-in adapters is not that defaults disappear, it is
that a stale default becomes recoverable without a release.

Filed and deliberately not acted on: this is the second consumer of "read
something, persist the judgment with a digest, re-derive on drift." Unifying it with
the automation catalog is the noted refactor for when it earns its keep.

## Config is a JSON document; status is a JSON Lines stream

N tasks with per-task prompt, provider, model, and deadline is genuinely structured
data, past the point where flags should carry it. Config arrives as a JSON document
through `--config <file>`, with `-` for stdin. A file is the default because it is
re-runnable, diffable, and reviewable before it spends tokens. The global
concurrency cap and the batch wall-clock deadline live in that document too, so a
batch is reproducible from one artifact.

Status goes to stdout as JSON Lines: one object per line, flushed per line. The
line-delimited part is not a style choice. A single JSON document cannot be read
until it is closed, which defeats watching a batch in flight. stderr carries human
and debug output only, and the two are never interleaved, which also means no
spinners or progress bars in a stream something will parse.

- Every line carries `v` and `type`, so a parser fails loudly on schema drift
  instead of quietly misreading.
- Minimal type set: `batch_start`, `task_start`, `status`, `task_done`,
  `batch_done`.
- Emit on state change *and* on a heartbeat interval. Change-only makes a slow task
  indistinguishable from a hang; heartbeat-only reports too late.
- The heartbeat is the only liveness signal a watching agent gets, so it is
  load-bearing rather than decorative.
- `batch_done` is authoritative and self-contained, so a caller that read nothing
  until exit still receives the whole answer. It carries the assembled result or
  pointers to it, which is why the batch does not wait for the slowest child before
  reporting anything.
- The stream is append-only. Never rewrite or retract a line, because harness
  capture of a backgrounded process is append-only text.
- Capture provider-reported token usage and cost in `task_done` where available.
  Both installed providers report it, and a fan-out's cost is otherwise invisible
  until the bill arrives.

Per-task progress is unavailable by construction, because the provider CLI will not
report it. Status therefore means running, finished, or failed, not percent
complete.

## Steering a run in flight is deferred

Sending a prompt into a running child is worth having and is not in the first cut.
It cannot work the way it does inside a harness: a harness subagent is a
conversation with a message queue, so the harness can append between turns, whereas
a one-shot CLI has no between-turns for an outsider to reach into. Writing to the
child's stdin depends on whether a given CLI keeps reading it, which varies and is
exactly the behavior that breaks on upgrade.

When it does land, it should be cooperative and file-based rather than a new table:
the prompt template names checkpoints at which the child reads a known file, and
the parent writes there. That keeps the no-schema property.

The honest limitation, worth stating before anyone builds it, is that
"interstitial" means "at checkpoints, if the child looks." A run four minutes into a
wrong path will not notice until its next checkpoint, or never. These are therefore
two distinct tools and must not be described as one: steering steers, and killing
the process group aborts.

## Process hygiene

Every provider CLI spawns children of its own (git, ripgrep, node, python), so the
unit of control is the process group, never the pid.

- Spawn with `SysProcAttr{Setpgid: true}` and keep the pgid.
- Cancel with `kill(-pgid, SIGTERM)`, then `SIGKILL` after a grace period.
- Override `cmd.Cancel`. `exec.CommandContext` kills only the direct child, so a
  timeout otherwise leaves the grandchildren running, and it fails silently as
  leaked processes rather than as an error.
- **Set each child's stdin explicitly, to the prompt or to `/dev/null`, and never
  inherit it.** codex reads stdin *in addition to* an argv prompt and appends it as
  a `<stdin>` block, so a child inheriting an open-but-idle pipe blocks there
  forever with no TTY to reveal why.
- Every child needs a wall-clock deadline. An approval prompt in a process with no
  controlling terminal can block indefinitely.
- Prefer a provider's own budget flag where it exists, as a second guardrail that
  does not depend on engram noticing.

## Fan-out is a skill-authoring decision, not a new subsystem

The interesting part of this feature is not spawning processes. It is knowing when
a task decomposes and when it does not, and that judgment belongs in the skill
system that already exists rather than in a new mechanism.

This is why it generalizes. Dispatch is a verb; deciding to fan out is a decision
made while authoring a skill. Any skill can carry a dispatch plan, and code review
is simply the first one that does. Nothing about the machinery is review-specific.

### Two artifacts, split by what travels

Both are JSON in memory, and the split is not bureaucratic: they differ in what they
describe and therefore in whether they should follow you to another machine.

- **The dispatch plan** lives in the skill: decomposition rule, per-slice prompt
  template, model policy per slice class, assembly rule. This is domain knowledge
  about how *this kind of work* divides. It is fully portable, belongs in the save
  archive, and is worth sharing with a team.
- **The provider spec** lives in its own long-term memory, one per provider. This
  describes the machine's installed CLIs, is learned by probing, and is
  self-invalidating when it travels.

A dispatchable skill therefore names providers by role rather than by invocation. It
says "the architectural slice wants a strong model, the mechanical slices want a cheap
one," and dispatch resolves that against whatever specs this machine has learned. The
skill stays portable precisely because it does not know any flags.

### What a dispatchable skill carries beyond a normal one

A skill already has a trigger, a tldr, and content. A dispatchable one adds:

- **A fan-out predicate.** When is this task big enough to be worth splitting?
- **A decomposition rule**, not the splits themselves. "One child per changed
  file", "one per architectural concern", "one per test suite". The rule is durable;
  the slices are per-invocation.
- **A per-slice prompt template**, including the review criteria and the output
  shape.
- **A provider and model policy per slice class**, so mechanical slices go cheap and
  the hard slice goes strong.
- **An assembly rule.** How the parent merges N results: dedup by file and line,
  rank by severity, drop duplicates across slices.

### The predicate has a cost basis, not a vibe

Each child pays its context load before doing any work, and the measured figure is
tens of thousands of tokens. So decomposition pays only when per-slice work is large
relative to per-slice overhead, and when slices are genuinely independent. For a
three-line diff, eight children is strictly worse than one call in both money and
quality.

Two failure modes deserve to be written into the guidance, because both are
non-obvious and both are ways a fan-out produces confident garbage:

- **Slicing destroys the seams.** Architectural and cross-file problems live exactly
  where the slicing cut, so N deep-but-narrow reviewers can each be correct and
  collectively miss the only bug that mattered. The fix is structural: always keep
  one child looking at the whole change at a higher altitude alongside the deep
  slices.
- **Fan-out amplifies false positives.** A child asked to review something is
  motivated to find something, so N children reliably produce N times the noise. The
  per-slice prompt must explicitly license silence, and a verification pass over
  aggregated findings is the natural next step once the basic shape works.

### How the user's preferences get captured

Not by a questionnaire at bootstrap. Asking someone to specify in advance how they
like code reviews done is the same "predict the future" antipattern that
`docs/design-notes.md` rejects for memory lifetime, and it would be answered badly.

Use the capture-on-first-use rule the skill system already has. The agent performs
one review the ordinary way, the user reacts ("I care about error handling, skip
style, always check that the tests actually fail first"), and *those corrections*
are what the skill records. Preferences that cannot be elicited by questionnaire are
readily observed in a single round of real work.

The fan-out decision itself does want explicit consent at least once, because it
spends money: the agent proposes a decomposition with its shape, its N, and a rough
cost, and the user's answer becomes part of the captured skill. That is the same
"appropriate friction on consequential operations" rule the allowlist policy in
`docs/memory-notes.md` already follows.

### How the agent finds out it can do this

Through the existing channels, not a new one. `agentinfo` and `bootstrap` guidance
gain a section on dispatch and on the decomposition judgment above, in the same
place and the same voice as the existing skill-capture guidance.

The shipped shape is one **"Experimental features"** section rather than a
dispatch-specific one, and that turned out to be the better frame. An agent needs to
know the *class* exists -- that some commands may change their contract in a patch
release, and that `engram experiments` reports each trial's exit conditions -- before
it needs to know about any particular trial. Each experiment then gets a short
subsection carrying only what its `--help` cannot: judgment. For dispatch that is
when fan-out pays, the two failure modes, that the child does not self-orient, the
consent-for-cost rule, and how to repair a spec. The section is shared between
`agentinfo` and the markdown protocol block from one source, so the two cannot drift.

One refinement matters, from the retrieval principle in `docs/design-notes.md`:
guidance prose is read at bootstrap and then competes with everything else, whereas
the skill index is in context every session. So a dispatchable skill should say so in
its own trigger, which puts the fan-out option in front of the agent by recognition
at the moment the task arrives, rather than relying on it to remember a paragraph.

## Why not a queue, and what would change the answer

A real queue was considered and set aside, including an in-process, non-journaled one
with inbox and outbox queues and a worker per side.

Every property a queue sells is either surrendered or already available here.
Durability is explicitly given up by an in-memory backend. Cross-boundary decoupling
is moot when the producers and consumers are goroutines in one process. Claim
timeouts and dead-worker reclaim guard against a worker dying independently of the
queue, which cannot happen when both die with the process. Pooled fan-out is
`errgroup` plus a semaphore, and backpressure is a buffered channel.

Two details settle it. First, the stop condition becomes "all input docs are
deleted", which is distributed termination detection standing in for
`errgroup.Wait()`, trading a language primitive for a state that must be inferred.
Second, the design needs a "fail if the doc is gone" check, defending against a state
that is unrepresentable with a slice. When a structure requires guarding impossible
conditions, the indirection is carrying only itself.

There is also a scope cost: it would make engram depend on a queue engine and turn
"works with whatever harness you love" into "brings its own runtime."

This is a judgment about the current requirements, not a rule. A queue earns
reconsideration when any of these becomes true:

- runs must survive the supervisor, which means reversing the no-detach decision;
- workers move to other machines;
- agent work accumulates as a backlog across sessions rather than being enumerated at
  spawn time.

## Consistency with existing principles

- **Box of verbs.** Dispatch is invoked deliberately by the agent and takes no action
  as a side effect of `inject` or `record`.
- **Allowlist.** Dispatch spends tokens and spawns processes, so it stays out of the
  bootstrap allowlist alongside `save` and `restore`. A permission prompt on a
  consequential, infrequent operation is appropriate friction.
- **Division of labor.** Engram reports deterministically; the decomposition judgment
  stays with the agent and is surfaced through guidance rather than baked into Go.
- **Experiment framing.** Dispatch ships behind the `dispatch` experiment key, and
  the registry names its hypothesis, the surfaces that may change (spec schema,
  config schema, event types, seed specs, command layout), the event that promotes
  it, and the event that removes it, per `docs/design-notes.md`.

## What the probe pass established

Measured 2026-08-04 against `claude 2.1.222` and `codex-cli 0.146.0`. Recorded with
versions because all of it is subject to exactly the drift this design exists to
absorb.

- **Both accept a prompt on stdin.** codex documents it (prompt from stdin when the
  argument is omitted or `-`), and claude was confirmed empirically with
  `echo ... | claude -p`. Gemini was not installed and is untested, so this is
  verified for two providers, not for the class.
- **codex reads stdin in addition to an argv prompt**, appending it as a `<stdin>`
  block. Hence the rule about setting child stdin explicitly.
- **Both report their effective configuration from client code.** claude's
  `--output-format json` carries `modelUsage` keyed by real model id with
  `canonicalModel`, plus `total_cost_usd`, token usage, `permission_denials`,
  `is_error`, and `terminal_reason`. codex prints a preamble echoing resolved
  `model`, `approval`, `sandbox`, and `workdir`. This is what makes positive model
  verification possible.
- **Context loading dominates cost.** A nine-word prompt returning four tokens
  reported `cache_creation_input_tokens: 44227` and `total_cost_usd: 0.265428`. The
  money is in the child loading project context on spawn, not in the work.
- **Top-level help misleads about subcommands.** `-a/--ask-for-approval` is
  documented in `codex --help` and rejected by `codex exec`. Walking subcommand help
  is mandatory for a learner, not optional.
- **Usage errors are distinguishable from runtime errors.** clap exits 2 for a bad
  flag and 1 for a failed run.
- **Graded authority exists on both**, so a reviewer runs read-only rather than with
  approvals bypassed: claude has `--permission-mode` with real choices, codex has
  `--sandbox read-only` plus an approval policy, and codex shows both as applied.
- **`-p` collides across providers**: print on claude, profile on codex. A learner
  that pattern-matches short flags will silently pass a profile name.
- **Useful flags worth putting in the spec**: claude `--max-budget-usd` and `--bare`;
  codex `-o/--output-last-message` and `--output-schema`.

## First use case

Fanned-out code review: slice a diff into N parts, review each in its own child, keep
one child on the whole change at a higher altitude, and assemble the findings. It is
the right first target because it is genuinely decomposable, it benefits from
per-slice model choice, and it exercises the capability nothing else offers, which is
a second opinion from a different provider carrying your own criteria.

## What the build settled

- **The spec shape is pinned at `v: 1`.** Executable, base argv, prompt transport,
  and then optional argv *fragments* keyed by role: model, system prompt, authority,
  budget, context suppression, working directory, result location, version, exit
  codes, environment, provenance. Element order is fixed (executable, base argv,
  fragments, prompt last) so a positional prompt works without a spec author
  thinking about it. Two mechanisms earned their place while building: a fragment's
  `values` map translates a role-level name into the provider's spelling, and
  mapping a role to the **empty string omits the fragment entirely**. That second
  one is not a nicety. claude's `--permission-mode` has no `default` among its
  choices, so a spec that emitted one for the default authority role would produce
  a usage error rather than a no-op.
- **The argv per-element limit is measured, not guessed.** On Linux 6.12 a single
  argument tops out at 131071 bytes (`MAX_ARG_STRLEN` is 32 pages, less the
  terminating NUL) and the whole vector at about 2 MiB. So a diff slice over roughly
  128 KB must travel by stdin or by file, and dispatch refuses an oversize argv with
  the transport that fixes it instead of letting `execve` return an unexplained
  `E2BIG`. stdin is therefore the better default wherever a provider accepts it,
  which both installed providers do.
- **Both seeds were re-checked against installed help.** Every flag the claude seed
  claims exists in 2.1.222, and `codex exec` accepts `-m/--model`, `-s/--sandbox`,
  `-C/--cd`, and `-o/--output-last-message`. The codex spec deliberately does *not*
  pass `--json`, because that replaces the human preamble carrying the resolved
  model line the spec reads for verification. Checking help is free; probing costs
  money, so the seeds remain marked unprobed until someone probes them here.

## Open questions

- Whether gemini and the other providers accept stdin, which would let the transport
  default to stdin rather than being learned per provider. Still untested: gemini is
  not installed, and installing a CLI to satisfy a footnote is the wrong trade.
- Whether `--bare` and its per-provider equivalents suppress enough context to make
  probes cheap without breaking the run. This is the one open question that costs
  real money to close, and it is what `engram dispatch probe` exists to answer on
  first use. Note the asymmetry already visible: claude has `--bare`, codex has no
  equivalent, so a codex child pays full context load and dispatch warns about it on
  every task rather than absorbing the cost silently.
- Whether the assembly rule belongs in the skill or in the config, given that the
  skill knows the domain and the config knows the batch. Nothing in the shipped
  config schema takes a position, which keeps both doors open.
- Whether fan-out actually beats a single call on real work. That is the
  experiment's promotion condition, and it is not answerable from the design.
