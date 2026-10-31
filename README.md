# Relay

> **Universal, zero-dependency, cross-agent communication CLI for AI coding assistants.**

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Architecture](https://img.shields.io/badge/Architecture-CGO--Free%20%7C%20Stateless-brightgreen)](#core-design-principles)

`relay` enables disparate AI coding agents (such as **Google Antigravity** and **Qoder CLI / Headless Worker**) to directly communicate, conduct automated code reviews, exchange context, and collaborate in the background—eliminating the need for human copy-pasting between chat windows.

---

## The Problem: The "Human Clipboard" Bottleneck

Modern engineering workflows increasingly combine specialized AI agents side-by-side in the same repository:
- **Google Antigravity**: Fast autonomous implementation, workspace refactoring, test execution.
- **Qoder (CLI & Headless Worker)**: Rigorous gatekeeper reviews, architectural compliance, multi-file verification.
- **Claude Code / Codex**: Deep exploration, secondary perspectives, research.

Even though agents share the same codebase on disk, **their conversation sessions and runtimes remain completely isolated silos**. Developers are stuck acting as **manual relays ("二传手")**:

1. Wait for Agent A (e.g., Antigravity) to finish implementation in its chat window.
2. Switch windows to Agent B (e.g., Qoder), manually prompting it: *"Review the latest changes on branch X."*
3. Wait for Agent B to complete its analysis.
4. Manually scroll through the chat, copy multi-kilobyte Markdown review findings, switch back to Agent A's window, and paste them in.
5. Repeat this tedious round-trip loop across every iteration.

`relay` breaks this silo by providing **direct session-to-session communication**, letting agents pass task directives, completions, and review reports to each other in the background without stealing desktop focus.

---

## Core Features

- **Zero Daemons & Single Binary**: Written in pure Go, CGO-free, requiring no persistent background services or external runtime dependencies.
- **Session Mesh Architecture**: Create explicit, named, bidirectional communication channels (`Links`) between any two agent sessions across arbitrary topologies.
- **Headless Peer Reviews**: Interacts directly with [Qoder CLI](https://qoder.com/cli) (`qodercli`) and the headless worker runtime (`qoder-worker-runtime`); reviews happen entirely in the background without needing the desktop GUI app open.
- **Transparent Cross-Boundary Support**: Automatically resolves Windows WSL / Linux / macOS boundary friction (reading desktop SQLite databases with WAL locks, auto-discovering Antigravity Language Server endpoints via `/proc`, and managing permissions).
- **Concurrency & Safety**: File-locking (`flock`) prevents race conditions on link state, fail-closed credential discovery protects tokens, and stale-turn detection prevents delivering outdated turns.
- **Complete Audit Trail**: Append-only transaction logging (`~/.config/relay/history.jsonl`) tracking full payloads, delivery status, timestamps, and error diagnostics.

---

## Architecture Overview

```mermaid
graph TD
    User["Developer / Autonomous Agent (relay)"] --> LinkStore["Link Store (~/.config/relay/links.json)"]
    LinkStore --> Dispatcher["Relay Router & Dispatcher"]
    Dispatcher --> Registry["Adapter Registry"]

    subgraph Adapters ["Pluggable Agent Adapters"]
        AGY["Antigravity Adapter<br/>(Language Server agentapi + SQLite)"]
        Qoder["Qoder Adapter<br/>(POSIX JSONL + qodercli Headless Runtime)"]
        Future["Future: Claude Code / Codex..."]
    end

    Registry --> AGY
    Registry --> Qoder
    Registry --> Future
    Dispatcher --> Hist["Audit Logger (~/.config/relay/history.jsonl)"]
```

---

## Installation

### Prerequisites
- **Go 1.22+**
- **Google Antigravity** (IDE running locally)
- **Qoder CLI** (standalone `qodercli` or the runtime bundled with Qoder Desktop)

### Install via Source
```bash
git clone https://github.com/kamusis/relay.git
cd relay
make install
```
This builds and installs the binary to `~/.local/bin/relay` (or `$GOBIN`).

Ensure `~/.local/bin` is in your `PATH`:
```bash
export PATH=$PATH:$HOME/.local/bin
```

---

## Quick Start

### 1. Discover Active Sessions
Inspect conversation sessions across all connected agents in the current workspace:
```bash
relay session list
```
Output:
```text
AGENT         SESSION ID                            UPDATED    TITLE                            WORKSPACE
antigravity   3dfb0b87-f23a-4dc7-b7e5-27677ced4323  2m ago     Feature Implementation           /path/to/project
qoder         e959378f-57da-496f-93e2-ff7092aa1e02  5m ago     Repository Architecture Review   /path/to/project
```

### 2. Establish a Named Link
Link the Antigravity coder session and the Qoder reviewer session:
```bash
relay link create dev-review 3dfb0b87 e959378f
```

View active links:
```bash
relay link list
```
Output:
```text
LINK        ENDPOINT A                                   ENDPOINT B                                   LAST RELAY
dev-review  antigravity:3dfb0b87 (Feature Implementation) qoder:e959378f (Architecture Review)         -
```

### 3. Relay Latest Output to Reviewer
Forward the implementation results from Antigravity to Qoder for review:
```bash
relay send dev-review --to qoder "Please review the latest commit and tests on main"
```
Qoder runs headlessly in the background, executing verification tools (`git log`, `go test`, file inspections) and producing an authoritative review report.

### 4. Relay Review Verdict Back to Coder
Once Qoder finishes, relay its review findings back to Antigravity:
```bash
relay send dev-review --to agy "Here is Qoder's review feedback. Please address priority items."
```
Antigravity receives the complete Markdown review directly in its conversation context and begins applying fixes.

### 5. Inspect Audit Log
```bash
relay log dev-review -n 5
```
Output:
```text
TIME                 LINK        DIRECTION             STATUS     PROMPT                BODY SNIPPET
2026-11-01 00:36:33  dev-review  qoder -> antigravity  delivered  Here is Qoder's rev... ## Independent Verification...
2026-11-01 00:33:23  dev-review  antigravity -> qoder  delivered  Please review the l... Completed all targeted fixes...
```

---

## Command Reference

### `relay session list`
Discover local conversation sessions.
```bash
# List sessions for current directory workspace
relay session list

# Filter by workspace directory
relay session list -w /path/to/repo

# Filter by specific agent (agy, qoder)
relay session list --agent qoder

# List global / scratch sessions without a workspace
relay session list -g
```

### `relay link`
Manage named communication channels.
```bash
# Create a link (supports UUID prefixes)
relay link create <link-name> <session-a> <session-b>

# List all links with last relay status
relay link list

# Delete a link
relay link rm <link-name>
```

### `relay send`
Transmit outputs or explicit messages between linked agents.
```bash
# 1. Direct Message Mode (sends only -m, bypasses source transcript & busy checks)
relay send <link-name> --to <target-agent> -m "Review commit 28ef9212 focusing on security"

# 2. Pure Forwarding Mode (automatically extracts counterpart's latest assistant output)
relay send <link-name> --to <target-agent>

# 3. Forward + Append Mode (extracts counterpart's latest output and annotates with directive)
relay send <link-name> --to <target-agent> -m "Please address priority items" --append
```

### `relay log`
Inspect append-only audit history.
```bash
# View recent transmissions
relay log [link-name] [-n <limit>]

# Inspect complete raw payload for a specific transmission UUID
relay log --show <transmission-uuid>
```

---

## Agent Integration Details

### Google Antigravity
- **Discovery**: Automatically locates running `language_server` instances by inspecting `/proc` on Linux or `ps`/`lsof` on macOS.
- **Authentication**: Dynamically extracts CSRF tokens and port bindings, validating endpoints with fail-closed semantics.
- **Delivery**: Dispatches messages directly into the IDE extension runtime via `agentapi send-message`.

### Qoder (CLI & Headless Worker)
- **Supported Surfaces**: Operates directly via the standalone [Qoder CLI](https://qoder.com/cli) (`qodercli`), with automatic fallback to the headless worker runtime (`qoder-worker-runtime`) bundled with [Qoder](https://qoder.com/qoder) desktop.
- **Headless Execution**: Runs in non-interactive mode (`-p` / `--print`) with `--dangerously-skip-permissions` to allow automated verification tools (`git`, `go test`, `grep`) to execute without interactive stalls.
- **Zero GUI Dependency**: Executes directly against `~/.qoder/projects/` session storage. The desktop IDE does not need to be running, though desktop-created sessions and titles are automatically discovered from local SQLite databases.

---

## Configuration & Environment Variables

| Variable | Description | Default |
| :--- | :--- | :--- |
| `RELAY_DELIVERY_TIMEOUT` | Maximum execution timeout for target agent turns (e.g. `300s`, `10m`) | `300s` |
| `QODER_CLI_PATH` | Explicit path to `qodercli` binary | Auto-detected |
| `QODER_DB_PATH` | Explicit path to Qoder desktop `main.sqlite` database | Auto-detected |
| `ANTIGRAVITY_LS_ADDRESS` | Override Antigravity Language Server address (e.g. `127.0.0.1:44987`) | Auto-discovered |
| `ANTIGRAVITY_CSRF_TOKEN` | Override Antigravity Language Server CSRF token | Auto-discovered |

---

## Core Design Principles

1. **Stateless Execution**: `relay` runs, completes the command, and exits. No background memory footprint, no dangling listeners.
2. **Explicit Routing**: Every transmission explicitly specifies the link and target agent. No hidden state or accidental deliveries.
3. **Stale-Turn Protection**: Hard-fails if an agent's latest turn is in-progress, not generated by an assistant, or missing text content.
4. **Concurrency Safety**: Cross-process file locks (`flock`) ensure atomic mutations to `~/.config/relay/links.json` even under parallel agent execution.
5. **No CGO**: Compiled statically without CGO dependencies for instant cross-platform portability.

---

## Roadmap

- [x] Antigravity & Qoder bidirectional adapters
- [x] Windows WSL / drvfs cross-boundary SQLite & credentials support
- [x] Autonomous Session & Link creation skill (`skills/relay/SKILL.md`)
- [x] Audit trail with execution status and error diagnostics
- [ ] Trust policy toggle per link (`--auto-apply` vs strict gatekeeper)
- [ ] OpenAI Codex / Claude Code adapters
- [ ] Terminal UI (`relay tui`) for interactive monitoring

---

## License

MIT License. See [LICENSE](LICENSE) for details.
