#!/bin/bash
# Read from stdin if available
PAYLOAD=$(cat)
LOG_FILE="/Users/chris/chris/code/src/github.com/shiblon/engram/session_hook_test.log"
echo "=== SessionStart Hook Fired at $(date) ===" >> "$LOG_FILE"
echo "$PAYLOAD" | jq . >> "$LOG_FILE" 2>/dev/null || echo "$PAYLOAD" >> "$LOG_FILE"
echo "" >> "$LOG_FILE"
# Return valid JSON
echo '{"systemMessage": "SessionStart logged."}'
