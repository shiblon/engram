# ENGRAM - A Memory Aid for Claude Code

This was inspired by https://github.com/dezgit2025/auto-memory and mostly occupies the same solution space: keep track of commands run, files listed, information obtained, and then gather context in very few tokens at session start.

## Setup Instructions for Humans

- install using `go install github.com/shiblon/engram/cmd/engram@latest`
- Add hooks to `.claude/settings.json`, preferably in your repository root.
- Ensure that `.claude` is in your `.gitignore` or similar file, or at a minimum, `.claude/engram.db`.

The hooks will look like this:

```json
    "PostToolUse": [
      {
        "matcher": "Read|Edit|Write|Bash",
        "hooks": [
          {
            "type": "command",
            "command": "<PATH TO>/engram record"
          }
        ]
      }
    ],
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "<PATH TO>/engram inject"
          }
        ]
      }
    ]
```

## Setup Instructions for Your Agent

Install engram, a per-project memory tool for Claude Code.

1. Run: go install github.com/shiblon/engram/cmd/engram@latest

2. Add these hooks to .claude/settings.json in your project root (create it if
   it doesn't exist), merging with any existing hooks:
   - PostToolUse, matcher "Read|Edit|Write|Bash", command: engram record
   - SessionStart, command: engram inject

Engram records file accesses and searches to a per-project SQLite DB
(.claude/engram.db) and injects recently active context at session start.
Note: add .claude/engram.db to your .gitignore.