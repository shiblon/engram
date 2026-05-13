#!/bin/bash

# Read the tool execution payload from stdin
PAYLOAD=$(cat)

# Define the log file location
LOG_FILE="/Users/chris/chris/code/src/github.com/shiblon/engram/after_hook_test.log"

# Append the payload to the log file
echo "=== AfterTool Hook Fired at $(date) ===" >> "$LOG_FILE"
echo "$PAYLOAD" | jq . >> "$LOG_FILE" 2>/dev/null || echo "$PAYLOAD" >> "$LOG_FILE"
echo "" >> "$LOG_FILE"

# Output valid JSON to stdout to satisfy the hook contract
echo '{"systemMessage": "AfterTool executed and logged successfully."}'
