# Dispatch notes

A design plan, not a description of shipped code. Dispatch would let the agent
hand a decomposed task to one or more provider CLIs running as child processes,
possibly on different providers and models, and collect the results. This file
records the shape we settled on and, more importantly, why the alternatives were
set aside, so the plan can be adjusted without re-deriving the reasoning.

The command name is provisional. `dispatch` is used throughout because it reads
correctly for the 1-to-N case, where `run` implies a single thing. Ergonomics
and flag spelling are deliberately unsettled; the ideas below are what matter.

See `docs/design-notes.md` for the memory-model principles this leans on, and
`docs/memory-notes.md` for the "tool for the agent, not an autonomous actor"
split that constrains what dispatch may do implicitly.

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

## The mailbox is state, not memory

Run communication lives in its own table, not in a sixth memory tier. The test
that decides this: would a row belong in `engram save`? Would it belong in the
priority ladder? Would anyone curate it? Three times no, and a tier asserts
otherwise.

Making it a tier would force an "except the mailbox" carve-out in `mem list`,
`mem search`, the FTS index, `curation_events`, `mem dump`, and the save archive,
where session-local IPC rows would otherwise be shipped between machines
forever. Six carve-outs is the signature of a wrong abstraction. The schema
already has the right pattern: `events`, `curation_events`, `projects`, and
`automation_catalog_entries` all sit outside `memories` precisely because they
are state. A run mailbox is the next of those.

A row holds the run identity, status, the process group id, the outbox (results),
the inbox (steering), and a path to the captured transcript. It gets its own thin
CLI verb rather than borrowing `mem write`, so the prompt template that teaches a
child to report reads honestly instead of describing a mailbox as memory.

## The child reports through engram, so there is no stdout parser

Provider CLIs are rigid and inconsistent about what they emit: plain prose here,
JSON lines there, progress interleaved with content elsewhere, and all of it free
to change between releases. Parsing that per provider is the worst job in the
design, so the child is instructed to write its result to its outbox key using
engram, which is already installed for it.

The payoff is that the per-provider adapter shrinks to the only question that
stays stable across releases: how do I launch this CLI headless and
non-interactive? Output shape stops being engram's problem.

The cost is that compliance is cooperative. A child can do the work and never
write the key. So the transcript is always captured as a fallback, and a missing
outbox key on an otherwise clean exit is a legitimate terminal state to record
rather than a crash to hide.

That still leaves a per-provider adapter, which would sit against the
platform-classes principle in `docs/memory-notes.md` if it were compiled into
engram. It is not. See the next section.

## Provider invocation is learned and probed, not compiled in

Hardcoding each provider's flags means a `codex` version bump can only be fixed
by a new engram release, which is the wrong coupling for a tool whose whole pitch
is that it works alongside whatever you already use. Instead, an agent reads the
provider's `--help`, works out how to invoke it headless, and the result is
cached. Recovering from upstream flag churn becomes re-learning, not re-coding.

This is the same mechanism `automation_catalog_entries` already uses: an agent
reads the available documentation, forms a judgment, and the judgment is
persisted with a digest so drift is detected and re-confirmed rather than
silently trusted. It also lands on the right side of the division of labor in
`docs/memory-notes.md`, since deciding that the model flag is `-m` is judgment,
and judgment belongs to the agent rather than to engram's Go.

### The learned artifact is a declarative invocation spec

The recipe is an argv array with placeholders, not a shell command string and not
a script. Docker's exec form and Kubernetes `command`/`args` are the prior art,
and they chose the array for the reason that applies here.

**There is no shell, so there is nothing to quote.** `exec.Command` with a slice
calls `execve` directly, which means a prompt containing semicolons, backticks,
`$(...)`, newlines, or quotes is simply bytes inside one argv element. Shell
injection is a hazard of command *strings*; introducing a wrapper script would
create the exposure and then require a rule to defend against it. An array has no
such exposure to begin with.

- OS-neutral. An argv array works identically on Windows, where a POSIX script
  would not, which matters for the startup-file-only platform class.
- Structured, so engram can validate a spec before spending tokens on it. A
  script can only be run and observed.
- Still fully inspectable and hand-editable, which was the entire argument for a
  script. When a provider breaks at 11pm, editing one JSON field beats waiting for
  a release *or* re-running a learner.

The one rule that must hold: **substitution happens into already-split argv
elements, never into a string that is split afterward.** Substituting first would
let a prompt containing spaces become several arguments, which is the same class
of bug as shell injection arriving by a different door.

The spec carries the argv template, the transport for the prompt, an environment
map, and any provider-specific spelling of model names. Environment variables are
declarative data here for the same reason k8s puts `env:` beside `command:`, since
some CLIs are configured that way rather than by flag.

Prompt transport is a learned per-provider and per-payload field rather than a
fixed rule, because argv has two real limits that have nothing to do with quoting:

- Linux caps a single argv element near 128KB regardless of total `ARG_MAX`, and a
  large diff slice can exceed it. The failure arrives at exec time, not gracefully.
- argv is visible in `ps` to the user's other processes, so prompt content sits in
  the process table for the run's duration. stdin does not.

So: argv inline by default, stdin when the CLI reads it in headless mode, and a
mode-0600 temp file that is cleaned up afterward when the payload is large and
stdin is unavailable. Which of the three a provider supports is exactly the kind
of thing the learner discovers and the probe confirms.

If some provider ever needs real branching logic, the spec can name an
executable. That is the escape hatch, not the default.

The spec is the truth, stored as a file under the engram home so it can be opened
and edited; the cached row holds metadata about it (path, the version and help
digest it was learned against, the probe result, and which fields were verified
rather than inferred). Storage is a table in the global database rather than a
memory tier, by the test in `docs/design-notes.md`: a recipe describes the CLI
versions installed on *this* machine, so carrying it in a save archive to a
machine with different versions would be actively wrong.

### What the spec must cover

"Bounded" is only reassuring if it can be enumerated:

- the executable, and how it is located;
- the headless or print subcommand or flag;
- the prompt transport: inline argv, stdin, or a temp file path;
- the model-selection flag, and any provider-specific spelling of model names;
- the approval-suppression flag, if headless operation requires one;
- working-directory semantics, whether inherited or passed explicitly;
- a structured-output flag, where one exists;
- exit-code semantics, to whatever extent they are discoverable.

### Learning must probe, not believe

Reading `--help` and trusting the inference has an expensive silent failure: a
misread model flag means every child in a fan-out quietly runs the default model,
and the output looks entirely plausible. So learning has two phases, and the
second is not optional.

- **Smoke probe.** Run the script with a trivial prompt ("reply with exactly OK")
  on the cheapest model, headless, short timeout. Confirm output and a clean
  exit. Seconds and a rounding error in tokens, and it converts "I think this is
  right" into "I ran it."
- **Flag-liveness probe.** Pass a deliberately invalid model name and confirm the
  CLI *errors*. This verifies the model flag is parsed and honored without
  costing a single token, because it fails before inference. If the CLI instead
  falls back silently, that is itself the finding: record the model field as
  inferred and untrusted.

Do not verify the model by asking the child what model it is. A model reporting
its own identity is either reading something its harness injected or guessing,
and the two are indistinguishable from outside. Trustworthy model identity comes
from the CLI's own output metadata, which is client code, not from the model's
prose.

Record per-field whether a value is verified or inferred, the same way the
automation catalog stores rationale beside a verdict.

### Drift is detected at dispatch, and fails toward a fix

Staleness checks belong at dispatch time, not inject time: spawning every
provider CLI at session start to compare versions is not the light bookkeeping
inject is limited to, and inject has to stay fast. Dispatch already spawns
processes, so one `--version` is free there.

A version mismatch does not refuse the run, because most bumps do not touch
flags. It annotates, and then the first actual failure carries the repair
instruction: learned against v1.2.3, installed is v1.3.0, re-learn with the
explicit verb. Drift becomes an instruction rather than a mystery.

Learning is always an explicit verb and never a side effect of `inject` or
`record`, per box-of-verbs. The first learn for a provider wants human review,
because engram would be constructing an invocation from text a binary printed.
The risk is low, since you installed the binary, but "reviewed once" is the
right amount of friction for something that will then run unattended.

Seed scripts still ship with engram so a fresh install works without a learning
round trip. They are seeds, not truth: learning overrides them, and the probe
catches a stale one. The gain over compiled-in adapters is not that defaults
disappear, it is that a stale default becomes recoverable without a release.

Filed and deliberately not acted on: this is the second consumer of "read
something, persist the judgment with a digest, re-derive on drift." Unifying it
with the automation catalog is the noted refactor for when it earns its keep.

## Config is a JSON document; status is a JSON Lines stream

N tasks with per-task prompt, provider, model, and deadline is genuinely
structured data, past the point where flags should carry it. Config arrives as a
JSON document through `--config <file>`, with `-` for stdin. A file is the
default because it is re-runnable, diffable, and reviewable before it spends
tokens. The global concurrency cap and the batch wall-clock deadline live in that
document too, so a batch is reproducible from one artifact.

Status goes to stdout as JSON Lines: one object per line, flushed per line. The
line-delimited part is not a style choice. A single JSON document cannot be read
until it is closed, which defeats watching a batch in flight. stderr carries
human and debug output only, and the two are never interleaved, which also means
no spinners or progress bars in a stream something will parse.

- Every line carries `v` and `type`, so a parser fails loudly on schema drift
  instead of quietly misreading.
- Minimal type set: `batch_start`, `task_start`, `status`, `task_done`,
  `batch_done`.
- Emit on state change *and* on a heartbeat interval. Change-only makes a slow
  task indistinguishable from a hang; heartbeat-only reports too late.
- The heartbeat is the only liveness signal a watching agent gets, so it is
  load-bearing rather than decorative.
- `batch_done` is authoritative and self-contained, so a caller that read nothing
  until exit still receives the whole answer. It carries the assembled result or
  pointers to it, which is why the batch does not wait for the slowest child
  before reporting anything.
- The stream is append-only. Never rewrite or retract a line, because harness
  capture of a backgrounded process is append-only text.
- Capture provider-reported token usage in `task_done` where it is available,
  since a fan-out's cost is otherwise invisible until the bill arrives.

One schema, two transports: the `status` roll-up has the same shape as the
mailbox status column, so reading the live stream and querying the table from
another pane answer identically. The supervisor emits the stream; children write
outbox keys; the two are never conflated.

Per-task progress is unavailable by construction, because the provider CLI will
not report it. Status therefore means running, finished, or failed, not percent
complete. Finer granularity is possible but strictly cooperative, with the child
writing checkpoints the same way it writes results.

## Inbox steers, process groups abort

Sending a prompt into a run mid-flight is worth having, but it cannot work the
way it does inside a harness. A harness subagent is a conversation with a message
queue, so the harness can append a message between turns. A one-shot CLI has no
between-turns for an outsider to reach into.

Writing to the child's stdin depends on whether a given CLI keeps reading it in
print mode, which varies by provider and is exactly the behavior that breaks on
upgrade. So steering is cooperative instead: the prompt template names
checkpoints at which the child reads its inbox key, and the parent writes there.
That works identically across providers because it is only an engram call.

The honest limitation is that "interstitial" means "at checkpoints, if the child
looks." A run four minutes into a wrong path will not notice until its next
checkpoint, or never. These are therefore two distinct tools and must not be
described as one: the inbox steers, and killing the process group aborts.

## Process-group hygiene

Every provider CLI spawns children of its own (git, ripgrep, node, python), so
the unit of control is the process group, never the pid.

- Spawn with `SysProcAttr{Setpgid: true}` and record the pgid on the run row.
- Cancel with `kill(-pgid, SIGTERM)`, then `SIGKILL` after a grace period.
- Override `cmd.Cancel`. `exec.CommandContext` kills only the direct child, so a
  timeout otherwise leaves the grandchildren running, and it fails silently as
  leaked processes rather than as an error.
- Every child needs a wall-clock deadline. A permission or approval prompt in a
  process with no controlling terminal can block forever.
- Exit code is not success. These CLIs commonly exit zero after refusing a task
  or exhausting a token budget, so a terminal state must be derived from the
  outbox and the transcript, not from the status code alone.

## Children return, the parent curates

Children never write memory. If N reviewers each wrote short-term entries, they
would flood the very signal `curation_events` exists to capture, and the
session-start index would fill with machine chatter. Children write to the
mailbox; the parent, or the human, promotes what is worth keeping into real
memory afterward.

This is the same division of labor `docs/memory-notes.md` already describes:
engram reports deterministically, and judgment stays with the agent.

## Reaping rides inject's existing bookkeeping

Mailbox rows need garbage collection, and cleanup at inject time is sufficient
given how often engram runs. This stays inside the "read-out plus light
bookkeeping" limit in `docs/memory-notes.md`, with direct precedent: inject
already prunes old events.

The rule requires no forecast, which satisfies the first principle in
`docs/design-notes.md`. Because a run never outlives its session, **any
non-terminal row from a prior session is dead by construction** and can be
retired without interpretation. Terminal rows age out on the same pass.

Inject collapses pending runs to a single line, for example "3 pending, 7
complete", so the mailbox can never crowd out curated short-term memory in the
session-start budget.

## Why not a queue, and what would change the answer

A real queue was considered and set aside, including an in-process,
non-journaled one with inbox and outbox queues and a worker per side.

Every property a queue sells is either surrendered or already available here.
Durability is explicitly given up by an in-memory backend. Cross-boundary
decoupling is moot when the producers and consumers are goroutines in one
process. Claim timeouts and dead-worker reclaim guard against a worker dying
independently of the queue, which cannot happen when both die with the process.
Pooled fan-out is `errgroup` plus a semaphore, and backpressure is a buffered
channel.

Two details settle it. First, the stop condition becomes "all input docs are
deleted", which is distributed termination detection standing in for
`errgroup.Wait()`, trading a language primitive for a state that must be
inferred. Second, the design needs a "fail if the doc is gone" check, defending
against a state that is unrepresentable with a slice. When a structure requires
guarding impossible conditions, the indirection is carrying only itself.

There is also a scope cost: it would make engram depend on a queue engine and
turn "works with whatever harness you love" into "brings its own runtime."

This is a judgment about the current requirements, not a rule. A queue earns
reconsideration when any of these becomes true:

- runs must survive the supervisor, which means reversing the no-detach decision;
- workers move to other machines;
- agent work accumulates as a backlog across sessions rather than being
  enumerated at spawn time.

## Consistency with existing principles

- **Box of verbs.** Dispatch is invoked deliberately by the agent and takes no
  action as a side effect of `inject` or `record`.
- **Allowlist.** Dispatch spends tokens and spawns processes, so it stays out of
  the bootstrap allowlist alongside `save` and `restore`. A permission prompt on
  a consequential, infrequent operation is appropriate friction.
- **Experiment framing.** If dispatch ships behind an experiment key, the
  registry must name its hypothesis, the surfaces that may change (config schema,
  event types, mailbox verbs), the event that promotes it, and the event that
  removes it, per `docs/design-notes.md`.

## First use case

Fanned-out code review: slice a diff into N parts, review each in its own child,
and assemble the findings. It is the right first target because it is genuinely
decomposable, it benefits from per-slice model choice, and it exercises the
capability nothing else offers, which is a second opinion from a different
provider carrying your own preferences and invariants.

## Open questions

- The exact fixed interface engram calls the wrapper script with, which is the one
  contract that must stay stable while everything behind it churns.
- Whether `--help` alone is sufficient to learn a recipe, or whether the learner
  must walk subcommand help too, since top-level help frequently lies by omission.
- Whether an invalid model name reliably produces an error across providers, or
  whether some fall back silently and defeat the flag-liveness probe.
- Whether any provider reports the model it actually used in structured output,
  which is the only trustworthy channel for verifying model selection.
- How the run identity reaches the child: environment variable, prompt template,
  or both.
- Whether the workspace authority a child runs with (approvals bypassed or not)
  must be declared explicitly in the task config. Provider credentials are not at
  issue: reading them from `$HOME` through the provider's own login is ordinary
  process-level authentication. The open question is the separate axis of what a
  child may do to the working tree.
- Whether `batch_done` should inline assembled results or always point at mailbox
  rows, which depends on how large review output turns out to be in practice.
