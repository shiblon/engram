# Topic notes

`engram topic` is an experimental, project-local pub/sub surface for agents
working on related problems. It shares findings across sessions without stable
agent identities, subscriber cursors, or automatic injection of message bodies.

## Experimental claim

A short injected topic index, purpose-driven retrieval, and aggressively
compacted subtopics can reduce duplicated investigation while keeping context
bounded.

Promote the experiment when concurrent sessions demonstrably reuse one another's
findings and lossy compaction remains useful. Remove it if agents rarely consult
or contribute, shared streams create more distraction than saved work, or useful
catch-up requires durable per-agent state. Commands, storage, compaction policy,
and monitor events remain unstable during the experiment.

## Model

A topic has a name, short purpose, and `retire when` statement. Topics are not
memory. They are temporary collaboration state; durable findings are deliberately
copied into long-term memory before a topic retires.

Each topic contains named subtopics. Message bodies live only in subtopics, and
each message receives a strictly increasing timestamp assigned by SQLite inside
the write transaction. The database chooses the later of its clock and the
previous subtopic timestamp plus the smallest stored unit. Agent clocks are never
consulted, and two messages in one subtopic never receive the same timestamp.

Each subtopic contains a bounded lossy checkpoint and a short tail:

```text
checkpoint through: 2026-09-21T10:14:03.120Z
tail: messages created after that timestamp
```

Compaction replaces the old checkpoint plus a prefix of the tail, then deletes
those source messages. Timestamps never change. A read after a timestamp older
than the checkpoint returns the checkpoint plus the current tail and reports that
the intervening history was compacted.

Topic lifetime and fixed limits bound the experiment: at most eight active
topics, sixteen subtopics per topic, and a bounded checkpoint and tail per
subtopic. These thresholds are deliberately small and remain subject to tuning.

## Agent contract

`engram inject` includes only one line per active topic: its name, purpose, and
retirement condition. It never includes subtopics or message bodies.

At task start, an agent compares this index with its current goal. For a possibly
relevant topic it runs `engram topic list <topic>`, asks the user about ambiguous
subtopic names, and reads only the relevant ones. With no goal, it expands
nothing.

During work, an agent offers to publish only information another participant is
unlikely to have: a test result, surprise, contradiction, or useful lead outside
its own goal. Its last-read timestamp may remain in session context. Losing it
merely causes the bounded checkpoint and tail to be reread.

## Command surface

```text
engram topic list
engram topic list <topic>
engram topic read <topic>/<subtopic> [--after TIMESTAMP]
engram topic post <topic>/<subtopic> [--after TIMESTAMP]
engram topic compact begin <topic>/<subtopic>
engram topic compact apply <stage-token> <checkpoint>
engram topic compact cancel <stage-token>
engram topic monitor <topic>[/<subtopic>] [--after TIMESTAMP] [--once]
engram topic create <topic> --purpose TEXT --retire-when TEXT
engram topic retire <topic>
```

`list` without an argument is the human form of the injected topic index.
`list <topic>` returns active subtopic names but no bodies. `read` returns the
current checkpoint and tail, restricted to newer content when `--after` can be
honored without crossing the compaction boundary.

`post --after T` closes the read-then-write race. In one transaction it observes
the current head timestamp `H`, returns messages newer than `T` through `H`,
appends the new message with a timestamp later than `H`, and reports that
timestamp. Later commits remain for the next read.

Compaction is an explicit two-phase operation, never an `inject` side effect.
`begin` atomically leases one subtopic and snapshots its current prefix. Posts may
continue beyond that cutoff. `apply` accepts the replacement checkpoint only when
its stage token and base generation still match; `cancel` releases the lease
early, and abandoned leases expire. Thus asynchronous compactors cannot silently
overwrite one another. A soft tail limit requests compaction, while a hard limit
backpressures publication. The exact isolated model invocation remains open.

`retire` removes the topic from injection and disposes of its ephemeral contents
after durable findings have been promoted separately.

## Monitoring

`monitor` is an attached process, not a daemon or stored subscription. It keeps
its latest timestamp only in process memory and emits JSON Lines on matching
changes:

```json
{"event":"message","topic":"graph-memory","subtopic":"allocator-retention","at":"2026-09-21T10:14:04.531Z"}
```

`--once` exits after the first edge, allowing an agent to wait at a deliberate
checkpoint without managing a background job. A harness that surfaces background
output can notify an active agent; otherwise the event reaches only the human or
an explicit waiter. Engram cannot universally interrupt or wake a model session,
so monitoring is an optional optimization over `list` and `read`, not part of
their correctness.

## Open decisions

- exact checkpoint format, threshold tuning, and isolated compactor invocation;
- whether individual subtopics need close semantics in addition to the hard cap;
- monitor event vocabulary and polling strategy;
- whether one posting approval may cover a topic for a whole session.
