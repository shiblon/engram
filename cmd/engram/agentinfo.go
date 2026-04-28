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
- invariant/preference (--global): personality, rules -- applies to all projects
- long:                            settled project decisions and facts
- short:                           in-flight context, stack, backlog
`

var agentInfoCmd = &cobra.Command{
	Use:   "agentinfo",
	Short: "Print instructions for AI agents on how to use engram",
	Long:  `Prints the standard instructions meant to be embedded in system prompt files such as CLAUDE.md or .cursorrules. Run 'engram agentinfo >> CLAUDE.md' or pipe to any equivalent file for your platform.`,
	RunE:  runAgentInfo,
}

func runAgentInfo(_ *cobra.Command, _ []string) error {
	fmt.Print(agentInfoText)
	return nil
}

func init() {
	rootCmd.AddCommand(agentInfoCmd)
}
