package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

const agentInfoText = `# Engram - Memory and Personality for AI Agents

Engram manages your identity, preferences, and project memory across sessions.

## Verifying engram is available

Run: engram mem --help

If this fails, tell the user: "engram is not configured -- run engram bootstrap to set it up."

## Session startup

Check whether inject context appears in your system prompt (look for sections like
"## Identity", "## Preferences", "## Long-term memory"). This means inject already ran.

On your first interaction this session, let the user know whether context was loaded
-- inject context present means you arrived oriented, absent means you loaded it manually.
Use your codename if one is set. Keep it brief and in character.

## Memory workflow

When the user asks you to remember something: infer the right tier, write it with
engram mem, and tell the user where it went and why.

When starting a digression: save current context to short-term memory first,
confirm it is saved, then proceed. Re-read short-term when done and resume.

When a task finishes: check short-term for anything worth promoting to long-term
or deleting.

When the user says "come back to this", "revisit later", or similar: write it to
short-term memory immediately without being asked. Confirm it was saved.

## Memory tiers and commands

Run: engram mem --help

for full details. Quick reference:

Tiers:
- invariant/preference (--global): personality, rules -- applies to all projects
- long:                            settled project decisions and facts
- short:                           in-flight context, stack, backlog
- cold:                            low-priority archive -- injected as index only, content not loaded

Cold tier: for things worth keeping long-term but not worth loading every session
(e.g. project ideas without an active repo, reference material, detailed background).
- Injected at session start as a one-line catalog only -- full content is never auto-loaded
- Check when the user asks about a topic that might have a cold entry:
    engram mem --tier cold list
    engram mem --tier cold read <key>
- Write to cold (not long) when content is bulky and rarely needed
- First line of a cold entry is the catalog summary; details go on subsequent lines
- Do not proactively read cold entries unprompted

## Project memory and version control

Long-term memories can be committed to the repo as context/long.md -- this makes
them available to teammates and survives fresh clones or new machines. engram
automatically loads context/long.md into the project DB when the file is newer
than the DB (or the DB has no long-term memories yet).

- At natural commit points (user says "let's commit", work is wrapping up, etc.):
  offer to run: engram mem dump --tier long
  The file lands at context/long.md; the user reviews the diff and includes it in
  the commit if the contents look right.
- Do not dump automatically; always offer and let the user decide.
- context/long.md is a wiki of settled project knowledge -- not scratch notes.
  Only long-tier memories (intentionally written) end up there; short-tier and
  event history are never committed.

Common commands:
  engram mem -t short list             list all short-term entries
  engram mem -t short read <key>       read one short-term entry
  engram mem -t long list              list all long-term entries
  engram mem write <key> <content>     write to short-term (default tier)
  engram mem -t long write <key> ...   write to long-term
  engram mem -t cold write <key> ...   write to cold (archive, injected as index only)
  engram mem dump --tier long          export long-term memory to context/long.md
  engram mem load --tier long          import from context/long.md into DB
  engram mem pop                       read and remove top of short stack
  engram mem search <query>            full-text search across all tiers
`

var agentInfoCmd = &cobra.Command{
	Use:   "agentinfo",
	Short: "Print instructions for AI agents on how to use engram",
	Long:  `Prints the standard instructions meant to be embedded in system prompt files such as CLAUDE.md, .cursorrules, or AGENTS.md. Run 'engram agentinfo >> CLAUDE.md' or pipe to any equivalent file for your platform.`,
	RunE:  runAgentInfo,
}

func runAgentInfo(_ *cobra.Command, _ []string) error {
	fmt.Print(agentInfoText)
	return nil
}

func init() {
	rootCmd.AddCommand(agentInfoCmd)
}
