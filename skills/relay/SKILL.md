---
name: relay
description: Cross-agent communication, direct headless peer review (`relay ask`), live progress inspection (`relay peek`), and mid-turn steering (`relay steer`) across Qoder CLI, Claude Code, Codex CLI, Pi Coding Agent, Google Antigravity, and Zed. Use whenever the user asks to have another agent review code, ask Qoder/Claude/Codex/Pi for a second opinion, inspect in-flight worktrees/sessions, steer a running worker, or bridge sessions via links (e.g., "让 qoder 审一下当前代码", "用 claude/codex 复核", "relay ask", "relay peek", "relay steer", "把 agy 会话发给 qoder", "relay send", "relay pull"). Defaults to linkless one-command `relay ask` (works from any agent or shell); keeps `relay link`/`send`/`pull` for external terminal bridging or async push into Antigravity chat.
---

# Relay: Cross-Agent Communication & Peer Review

This skill orchestrates cross-agent peer review, live supervision, and session bridging using the `relay` CLI (`~/.local/bin/relay`).

- **Default mode (`relay ask`)**: Linkless, one-command synchronous invocation of headless targets (`qoder`, `claude`, `codex`, `pi`). Works from **any** calling agent that can run bash (Antigravity, Claude Code, Codex, Zed, Pi, Mopheus tasks, or a human terminal) without needing a source session adapter or pre-created link.
- **Optional bidirectional channel mode (`relay link` / `send` / `pull`)**: Retained for humans/scripts bridging two live chat sessions from an external terminal, or when you explicitly want Qoder's reply pushed asynchronously into an Antigravity chat window.

---

## 1. Quick CLI Reference

Ensure `~/.local/bin` is in `PATH`:
```bash
export PATH=$PATH:$HOME/.local/bin
```

| Operation | Command |
| :--- | :--- |
| **Ask / Peer Review (Default)** | `relay ask <qoder\|claude\|codex\|pi>[:id] -w <worktree> [-t review\|followup] -m "<prompt>" [--new] [--json]` |
| **Peek Live Progress** | `relay peek <worktree\|agent:id\|link> [--diff] [-f <file>] [--json]` |
| **Steer (Hook Agents)** | `relay steer <worktree\|agent:id> -m "<correction>" [--blocking]` |
| **Steer Queue & Hook** | `relay steer list [--all]` / `relay steer hook-install --agent all\|claude\|qoder\|pi [--status\|--uninstall]` / `relay steer drain --format hook-json --read-hook-stdin` |
| **List Sessions** | `relay session list [--agent qoder\|claude\|codex\|pi\|agy\|zed] [-w <workspace>] [-g]` |
| **View Audit Log** | `relay log [ask\|<link-name>] [-n <limit>]` |
| **Bidirectional Link (Optional)** | `relay link create <name> <src> <tgt>` / `relay send <name> --to <agent>` / `relay pull <name> --from <agent>` |

---

## 2. Default Peer-Review Workflow (`relay ask` — No Link Required)

Whenever the user asks to **"让 qoder 审一下当前代码"** (or asks Claude Code / Codex / Pi to review or double-check work), use **`relay ask`** directly:

### Step 1: Dispatch Round 1 Review (1 Command)
```bash
relay ask qoder -w <worktree_path> -t review -m "工单: <工单标题/ID>\nWorktree: <worktree_path>\n分支: <branch_name>\n【重点关注】<关注模块或改动说明>"
```
- **Zero setup**: When given a bare agent name (`qoder`, `claude`, `codex`, `pi`), `relay ask` automatically reuses the most recently updated session in `<worktree_path>` — or creates a fresh headless session on the fly in the same single turn (pass `--new` to force a fresh session).
- **Clean output & provenance**: The reviewer's full reply is printed directly to `stdout` (immediately readable by the calling agent), while `[relay ask] session=<agent>:<short_id> tx=<tx_id>` is printed to `stderr` and audited in `$RELAY_HOME/history.jsonl` (`link_name: "ask"`).
- **Templates**: `-t review` drives `/verification-before-completion` (Phase A `ponytail-review` + Phase B architectural guardrails). Omit `-t` for freeform ad-hoc questions (`relay ask claude -w . -m "Check edge cases in pkg/steer"`).
- **Target choice**: Works identically across headless receivers: `qoder`, `claude`, `codex`, `pi`.

### Step 2: Multi-Round Convergence Loop (If Changes Requested)
If Round 1 returns `Verdict: CHANGES REQUESTED`:
1. Implement the required fixes in the worktree and run local verification (`go test`, `vitest`, `pnpm typecheck`, etc.).
2. Send Round 2+ fix evidence to the **exact reviewer session ID** printed on `stderr` in Step 1 using `-t followup`:
   ```bash
   relay ask qoder:<session_id> -t followup -m "已针对上一轮评审意见完成整改：1. ...; 2. ...。验证已全部通过，请核验阻断项闭环并给出终审裁决。"
   ```
3. Repeat until the reviewer outputs `Verdict: APPROVED`.

### Template Management
- List built-in and custom templates: `relay template list`
- Preview a template: `relay template show review` / `followup` / `supervise`
- Custom templates live in `$RELAY_HOME/templates/*.tmpl` (default `~/.config/relay/templates/*.tmpl`).

---

## 3. Live Progress Inspection & Mid-Turn Steering (`peek` + `steer`)

For observing a worker agent while it is still executing (without waiting for its turn to finish) and injecting high-signal corrections into hook-capable agents (Claude Code, Qoder CLI, Pi):

1. **Inspect Live Progress (`relay peek`)**:
   ```bash
   # Inspect a worktree directly (branch, commits, dirty files, sibling worktrees, active sessions, pending steers)
   relay peek .worktrees/fix-moc-1385 --diff -f server/internal/util/platform_darwin.go

   # Inspect by agent session or link name (works even while the agent turn is in progress)
   relay peek zed:8878e5d3
   relay peek qoder:e09937bb --diff --json
   relay peek claude:652e7f08
   ```
2. **Inject Mid-Turn Correction (`relay steer`) — Hook-Capable Agents**:
   - Queue a correction ONLY for high-confidence defects (hard bugs, broken security/path invariants, scope drift) — never interrupt workers for style nitpicks:
   ```bash
   # Target a feature worktree (automatically matches any hook running inside that worktree or its subdirectories)
   relay steer .worktrees/fix-moc-1385 -m "Use /usr/sbin/ioreg instead of bare ioreg" --blocking

   # Or target a specific hook-capable session
   relay steer qoder:e09937bb -m "Add empty-PATH test assertion"
   ```
   - Inspect or cancel queued directives: `relay steer list [--all]` / `relay steer clear [target] [--id <prefix>]`.
3. **Configure Worker Hook / Extension (One-Time Setup)**:
   - Install idempotently for Claude Code (`~/.claude/settings.json`), Qoder CLI (`~/.qoder/settings.json`), and Pi (`~/.pi/agent/extensions/relay-steer.ts`) — preserves all existing settings/hooks; takes effect on the next session:
     ```bash
     relay steer hook-install --agent all          # or --agent claude / qoder / pi (--scope user|project)
     relay steer hook-install --status             # inspect installation status
     relay steer hook-install --agent all --uninstall
     ```
   - For manual setups (including `.claude/settings.local.json`), print a ready-to-paste config or TypeScript extension via `relay steer hook-snippet --agent claude|qoder|pi`.
   - If `relay steer` is called while no hook/extension is installed for the target (checking user scope, project scope, and `.claude/settings.local.json`), it emits a warning on `stderr` so queued directives never sit silently unconsumed.
   - Claude Code and Qoder CLI hooks run `relay steer drain --format hook-json --read-hook-stdin` (silent exit `0` when empty; emits `PostToolUse` `additionalContext` JSON when directives match). Pi's `relay-steer.ts` extension hooks `tool_result` and injects drained text via `pi.sendUserMessage(text, { deliverAs: "steer" })`.
   - Set `RELAY_HOME=/path/to/dir` to isolate all Relay state (`links.json`, `history.jsonl`, `steer.jsonl`, `templates/`) from `~/.config/relay`.
4. **Supervisor Prompt Template**:
   - Built-in template `supervise` (`relay template show supervise`) encodes the high-signal supervision checklist.

---

## 4. Optional Bidirectional Link Mode (External Terminal / Async AGY Push)

Use `relay link` + `relay send` / `relay pull` only when you explicitly want:
- A human or script in an **external terminal** to shuttle outputs between two live IDE/agent sessions without copy-pasting (e.g., forwarding Antigravity's latest assistant turn to Qoder without `-m`, or asynchronously pushing Qoder's review back into an Antigravity chat window); or
- A named persistent bidirectional channel tracked under `relay log <link-name>`.

```bash
# 1. Discover sessions on both ends
relay session list -w <workspace>

# 2. Bind a named link (supports UUID prefixes)
relay link create review-<repo> agy:<agy_id> qoder:<qoder_id>

# 3. Forward between endpoints (omit -m to auto-extract source's latest assistant turn)
relay send review-<repo> --to qoder -t review -m "<context>"
relay send review-<repo> --to agy                    # Async push into Antigravity chat
relay pull review-<repo> --from qoder                # Synchronous stdout pull (safe for Zed/Pi)
```
*(Note: `relay send <link> --to zed` is intentionally disabled to avoid mutating Zed's live `threads.db`; use `relay ask` or `relay pull` instead.)*

---

## 5. Failure Triage & Guardrails

- **ABSOLUTE RED LINE: Never Bypass Relay by Reading Private Session Files**:
  - **NEVER** use `tail ~/.qoder/projects/*.jsonl | jq`, `cat`, or direct SQLite queries to "peek" or "extract" counterpart outputs behind Relay's back.
  - All inspection and cross-agent exchange must flow through `relay ask` / `relay peek` / `relay send` / `relay pull`.
- **Handling Busy State (`source agent '...' is currently busy generating output`)**:
  - Multi-tool deep reviews typically take 30–120 seconds. Wait 5–10 seconds and re-run the Relay command rather than reading raw files.
- **Exit code 127 (`qodercli` / `claude` / `codex` not found)**:
  Ensure `PATH` includes `~/.local/bin` and `/opt/homebrew/bin` (or set `QODER_CLI_PATH` / `CLAUDE_CLI_PATH` / `CODEX_CLI_PATH` / `PI_CLI_PATH`).
- **Exit code 1 (`Not logged in · Please run /login`)**:
  Run `qodercli login` (or `claude login` / `codex login`) once to authorize the headless worker.
- **`signal: killed` / Execution Timed Out**:
  Default timeout is `600s`. For very deep multi-tool inspections, override per-call via `--timeout 15m` or env:
  ```bash
  RELAY_DELIVERY_TIMEOUT=900s relay ask qoder -w <worktree> -t review -m "<prompt>"
  ```
- **Permission Prompt Blocking**:
  Relay automatically passes non-interactive bypass flags (`--dangerously-skip-permissions` for Qoder/Claude, `--dangerously-bypass-approvals-and-sandbox` for Codex) so automated review tools (`git`, `go test`, `grep`) run without stalling.
