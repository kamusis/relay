#!/usr/bin/env bash
# PostToolUse hook wrapper for hook-capable AI coding agents (Claude Code, Qoder CLI, Pi).
# Drains any pending mid-turn steering directives queued via `relay steer` and emits
# PostToolUse hook JSON on stdout. Exits 0 silently when the queue is empty or relay is absent.
set -u

RELAY_BIN="${RELAY_BIN:-}"
if [ -z "$RELAY_BIN" ]; then
  if command -v relay >/dev/null 2>&1; then
    RELAY_BIN="$(command -v relay)"
  elif [ -x "${HOME}/.local/bin/relay" ]; then
    RELAY_BIN="${HOME}/.local/bin/relay"
  else
    exit 0
  fi
fi

exec "$RELAY_BIN" steer drain --format hook-json --read-hook-stdin
