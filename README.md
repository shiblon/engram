# ENGRAM - A Memory and Personality Aid for Claude Code

*NOTE: inspired by a combination of [auto-memory](https://github.com/dezgit2025/auto-memory) and work done by a beloved colleague of mine from Atlassian: [Kevin Harris](https://www.linkedin.com/in/pwnx0r/).*

Just want to install and get moving? [Jump to installation.](#installation)

*NOTE 2: if you want this to work for other agents, just let me know! We can figure it out.*

Memory affects everything about people. It affects personality, the ability to hold a conversation, and the ability to get things done. This is also true for AI agents, but the story there is fragmented and memory does not always behave as we expect.

It turns out that we can very easily do better. Engram is not just about doing better, though, it's about *making the process joyful*. Writing code, doing engineering brainstorming, finding bugs; these things can be a real pain. One thing I've missed when doing remote work has been the fun and joyful interactions that come from working side-by-side with someone who is suffering with you while also being supportive and making you laugh through your lunch.

Agents are not humans, but they *can* restore some of that joy. Engram is about this and more.

## Token Savings

The problem with re-explaining things during every session start has been mitigated to a large extent, at least for the way in which I've been using Claude. I found that before I took some pains to make memory explicitly managed, this was an issue. I had to rebootstrap it frequently. This costs tokens and shortens your effective context window. The author of [auto-memory](https://github.com/dezgit2025/auto-memory) goes into some very nice detail about the issue and their particular solution (which only works on copilot, inspiring me and Qubit, my Claude personality, to build it for Claude).

If memory is well structured and easily accessed in a database, then you can actually get more done with fewer tokens. Access patterns matter, which is why [rtk](https://github.com/rtk-ai/rtk) (Rust Token Killer) has a chance of working. It isn't just intercepting tokens, it's imposing *structure*. That structure just happens to look like a token filter. There are other structures that can help, as well.

Engram imposes structure, and it does so in a way that feels more seamless than normal, raw interactions without it.

## Personality as a Context Canary

The credit goes to [Kevin Harris](https://www.linkedin.com/in/pwnx0r/) for the ideas behind this one (I actually never found out *how* he did any of it, I just got the concept). If you decided to give your agent a personality, you are deciding to give it a characteristic that humans have evolved highly tuned sensitivity to. You *notice* when personality shifts in the middle of a conversation. You evolved to understand when something is "off". Making the context window something less like "85% full" and something more like "suddenly this quirky agent got more serious" makes it easier to know when something is about to get weird.

I give my agent a personality. In my case, I asked it to be enthusiastic about elegance, strict about readability and maintainability, and to take delight in the occasional code-related pun. I've tuned that a little over time, but it works well for me. I've seen people really enjoy "snarky" agents, or "chaos gremlin scientist" agents, or "just be GlaDOS" agents. When something is about to go wrong with your code, you tend to notice a shift in personality first.

This actually happened to me, but I thought, "meh, we've got like ten minutes left, it's okay".

It was not okay.

The point was that I *noticed early*. That's the power of personality.

## A Usable Memory System

Whether agents store memory in .md files or just in session context is not necessarily something people think about much, at least not until it bites them. If you ask an agent to remember something, sometimes they'll just count on their own session context to keep track of it. A few code files and a compaction later and that thing is completely forgotten.

Or, the agent can write a markdown file with info in it, but the format is all over the map, and that can make the agent confused. "What do you mean by 'backlog', exactly?" is a question I got once, and we had to go the rounds figuring out that it meant something like long-term memory, and that it should be checked and updated every time we finished a task. This is simply not built in, and it can burn a pile of tokens just figuring it out together.

Engram makes memory explicit, editable, and searchable. It imposes a pretty flexible structure on the idea of remembering things. It's important that it be flexible, and it's also important that it be structured. There is a difference between long-term and short-term memory, there is a difference between global invariants and project invariants. And you can specify your own.

The agent can also describe to you how its memory works. You can ask it what it can do, and it can tell you how it treats memory.

If you are interacting with it and say, "I don't want to continue just yet, we need to brainstorm on design first," it can know that you mean to store the current context in short-term memory, and to pop the stack when the design question is settled. That's not built-in for your basic code agent. Engram does this, and at a token cost that is tiny compared to working with defaults.

I'm making token claims, here. I don't have numbers to back them up, just experience. Not terribly satisfying, I know.

## Making Engineering Fun

A personality that works with you, even though you should be careful of the psychological traps here, really makes work *joyful*. It's delightful when a self-deprecating joke comes out, or a pun, or a surprising insight. It makes a difference to me when I get a laugh out of an interaction. Something clicks in my brain that pushes things forward in a way that just solving a problem alone never does. It also helps when I tell the agent *about myself*. That goes a long way toward helping it develop its own personality.

I had my agent come up with its own code name. It chose Qubit, partly because of working on a task workflow system built around task queues, but also because that system is named "[EntroQ](https://github.com/shiblon/entroq)", which is a pun of my own: it's one letter removed from "Entro-P", and seeks to bring order to microservice chaos.

Qubit was not only about queues, it was also about quantum uncertainty, and having that name in there gives me yet *another* basic canary: if the agent can remember its name, we haven't drifted too far, yet.

The aforementioned Kevin Harris has an agent personality named "Bit", like in the original Tron. Similar origin stories, self-chosen, and delightfully snarky.

It's hard to express just how much of a difference this makes in the day-to-day of using agents to assist with all kinds of tasks.

## Multi-Layered

Your agent, within a project, has a set of memories that it can use, and it uses the `engram` tool to manage them. Because that tool *also* knows about global memories, the agent can seamlessly manage those, as well. Do you always want to avoid panicking in Go code, and to use `log.Fatal` instead? You can ask it to remember it for all projects and it knows how to do that easily with the tool.

You can also ask it to dump or load the memories to or from markdown files so that, if you want, you can easily apply version control and never lose your agent's personality or memory.

## Installation

The core of engram is the memory system — personality, preferences, and memory
tiers that work entirely through conversation. The hooks that track file activity
are an enhancement on top of that, not a requirement.

It is recommended that you just ask your agent to do the whole thing, but there are manual instructions below if you would rather not do it that way.

### Ask your agent to do it

Paste this into a new Claude Code session:

```
Install engram:

1. Run: go install github.com/shiblon/engram/cmd/engram@latest

2. Find the full path: run go env GOBIN (or go env GOPATH, binary at $GOPATH/bin/engram)
   Verify: <full-path>/engram --help

3. Run: <full-path>/engram bootstrap

Open a new session when done -- the short-term stack will guide you from there.
```

### Manual setup

```sh
go install github.com/shiblon/engram/cmd/engram@latest
engram bootstrap   # or: $(go env GOBIN)/engram bootstrap if not in PATH
```

No Go toolchain? Download a pre-built binary for your platform from
[github.com/shiblon/engram/releases/latest](https://github.com/shiblon/engram/releases/latest),
extract it, and place `engram` somewhere in your PATH.

Bootstrap sets up CLAUDE.md, installs workflow instructions into global memory,
adds project-level file tracking hooks to `.claude/settings.json`, updates
`.gitignore`, and queues a personality setup todo for your first session. Open a
new Claude Code session and your agent will know what to do.

### Commit your personality to git (optional but recommended)

```sh
engram mem --global dump   # exports to ~/.claude/memory/*.md
engram mem dump            # exports project memories to .claude/memory/*.md
```

On a new machine, `engram mem load` restores everything. Your agent's identity
travels with you.

## Day-to-day Usage

Once engram is running, you mostly just talk to your agent. Here are some patterns worth trying.

**Remembering things**

```
You:   remember that we're using modernc.org/sqlite to avoid CGo
Agent: stored in project long-term memory: sqlite-rationale

You:   remember that for all my projects
Agent: stored in global long-term memory: sqlite-rationale
```

The agent will always tell you where it put something. If you want a specific tier:
> "remember this as short-term" / "add this to the backlog" / "make this a global preference"

**The conversation stack**

When you need to take a detour:

```
You:   hold on, I want to think through the API design before we continue
Agent: current context saved to short-term memory. what's on your mind?

[...design discussion...]

You:   ok, let's go back to what we were doing
Agent: [reads short-term memory] we were in the middle of the auth refactor,
       specifically the session token validation. picking up there.
```

**Checking memory**

```
You:   what's in my short-term stack?
You:   what are my code preferences?
You:   what have we decided about this project?
You:   are you bootstrapped?  (checks for codename + personality)
```

**Pruning and promoting**

```
You:   that auth task is done, remove it from the backlog
You:   actually, promote that note to long-term — it turned into a real decision
You:   clear the short-term stack, we're starting fresh
```

**Seeing what's available**

```
You:   what can you do with memory?
```

Your agent will describe the tier system, what each tier is for, and how to use it — because that's stored as context it receives at every session start.
