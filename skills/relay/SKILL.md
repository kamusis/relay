---
name: relay
description: Cross-agent communication and peer-review orchestration between Google Antigravity and Qoder CLI (https://qoder.com/cli). Use whenever the user asks to relay work results, code changes, or review tasks between Antigravity and Qoder sessions (e.g., "把 agy 会话里的工作成果发给 qoder 审核", "让 qoder 审一下当前代码", "把 qoder 评审传回 agy", "建立 agy 和 qoder 的 link", "relay send"). Handles autonomous session discovery, auto-creation of missing Qoder sessions, auto-creation of missing links, and bidirectional headless delivery.
---

# Relay: Cross-Agent Communication & Peer Review

This skill orchestrates background, cross-agent communication between Google Antigravity and Qoder using the `relay` CLI (`~/.local/bin/relay`).

It enables disparate AI agents to conduct asynchronous peer code reviews, pass context, and exchange deliverables without requiring human copy-pasting or opening desktop GUI windows.

---

## 1. Quick CLI Reference

Ensure `~/.local/bin` is in `PATH`:
```bash
export PATH=$PATH:$HOME/.local/bin
```

| Operation | Command |
| :--- | :--- |
| **List Sessions** | `relay session list [-w <workspace>] [-g]` |
| **List Links** | `relay link list` |
| **Create Link** | `relay link create <link-name> <endpoint-a> <endpoint-b>` |
| **Relay Output** | `relay send <link-name> --to <target-agent> "<prompt>"` |
| **View Audit Log** | `relay log <link-name> [-n <limit>]` |

---

## 2. Autonomous Peer-Review Workflow

When the user asks to **"把 agy 某个会话的工作成果发给 qoder 审核"** (or similar requests), follow this standard 5-step pipeline:

```
[Step 1: Resolve AGY Source]
           │
           ▼
[Step 2: Find or Auto-Create Qoder Session]
           │
           ▼
[Step 3: Find or Auto-Create Link]
           │
           ▼
[Step 4: Execute `relay send --to qoder`]
           │
           ▼
[Step 5: Relay Verdict Back to AGY]
```

### Step 1: Resolve the Source Session (Antigravity)
1. Run `relay session list -w <workspace>` to locate available Antigravity sessions in the workspace.
2. If the user provided a session ID or title snippet (e.g. `3dfb0b87`), match it against the listed sessions.
3. If the user referred to "当前会话" (current session), identify the active conversation UUID from the current environment context or match the latest updated session.
4. Record `srcSessionID = <agy-session-id>`.

### Step 2: Resolve or Auto-Create the Target Session (Qoder)
1. Query existing Qoder sessions for the target workspace:
   ```bash
   relay session list -w <workspace>
   ```
2. **If an existing Qoder session is found**:
   - Use that session ID: `tgtSessionID = <qoder-session-id>`.
3. **If NO Qoder session exists for the workspace (Auto-Bootstrapping)**:
   - Initialize a brand new headless Qoder session using `qodercli`:
     ```bash
     qodercli -w <workspace> --dangerously-skip-permissions -p "你是本项目的资深架构与代码评审员，请保持待命进行审查。"
     ```
     *(If `qodercli` is not in PATH, use `~/.local/bin/qodercli` or fallback to `~/.qoder/remote/protocol-v1/installs/*/*/bin/qoder-worker-runtime`).*
   - Re-run `relay session list -w <workspace>` to capture the newly created Qoder session ID: `tgtSessionID = <new-id>`.
   - Inform the user that a dedicated Qoder reviewer session has been auto-created.

### Step 3: Resolve or Auto-Create the Link
1. Inspect existing links:
   ```bash
   relay link list
   ```
2. Check if any link already connects `srcSessionID` and `tgtSessionID`:
   - If found (e.g. `relay-test`), reuse that link: `linkName = <existing-link>`.
3. **If NO link connects them (Auto-Linking)**:
   - Generate a clean, descriptive link name based on the workspace basename:
     - Format: `review-<workspace-name>` (e.g. `review-relay`).
     - If that name is taken, append a short hash: `review-<workspace-name>-<short-id>`.
   - Create the link:
     ```bash
     relay link create <link-name> <srcSessionID> <tgtSessionID>
     ```
   - Inform the user that the communication link has been created.

### Step 4: Execute the Relay to Qoder
1. Send the review request from Antigravity to Qoder using **Direct Message mode (`-m`)**:
   ```bash
   relay send <link-name> --to qoder -m "<structured_review_request>"
   ```
   - **CRITICAL**: Always use `-m` to supply a clean, self-contained, structured review prompt (specifying target branch, commit hash, and verification checklist).
   - Using `-m` delivers the message directly:
     - It **bypasses source transcript extraction** (completely avoiding transcript contamination from intermediate peripheral chat like "已停止重定向").
     - It **bypasses busy state checks** (allowing the active agent to call `relay send` without self-deadlock).
   - *Example*:
     ```bash
     relay send <link-name> --to qoder -m "请对最新提交 commit 28ef9212 进行代码与架构深入复审，重点关注：1. 正确性与边界处理；2. 架构规范对齐；3. 并发安全与回归风险。"
     ```
2. **Execution Characteristics**:
   - Qoder will run headlessly via `qodercli` without needing the desktop GUI app.
   - For multi-tool reasoning tasks, Qoder may run `git log`, inspect files, or run tests. It typically completes in 30–60 seconds.
   - `relay send` will wait for Qoder to complete and print delivery details (Transmission ID, From, To, Relayed Body bytes).
   - If delivery succeeds, the audit log status updates to `delivered`.

### Step 5: Relay the Verdict back to Antigravity
Once Qoder finishes generating its review:
1. Relay Qoder's completed output back into Antigravity (using pure forwarding or forward+append):
   ```bash
   # Pure forwarding of Qoder's latest review:
   relay send <link-name> --to agy

   # Or forwarding annotated with directive:
   relay send <link-name> --to agy -m "查收 Qoder 评审结论与整改意见" --append
   ```
2. The latest assistant turn from Qoder is immediately delivered into the source Antigravity session.
3. Check status:
   ```bash
   relay log <link-name> -n 2
   ```

---

## 3. Failure Triage & Guardrails

- **Exit code 127 (`qodercli: command not found`)**:
  Ensure PATH includes `~/.local/bin`: `export PATH=$PATH:$HOME/.local/bin`.
- **Exit code 1 (`Not logged in · Please run /login`)**:
  Run `qodercli login` to authorize the headless worker once.
- **`signal: killed` / Execution Timed Out**:
  If Qoder performs very deep multi-tool inspections exceeding the default 300s, override via:
  ```bash
  RELAY_DELIVERY_TIMEOUT=600s relay send <link-name> --to qoder "<prompt>"
  ```
- **Permission Prompt Blocking**:
  `pkg/adapter/qoder` automatically passes `--dangerously-skip-permissions` so non-interactive tools (`go test`, `git diff`) run without stalling.
- **Desktop GUI Closed**:
  Qoder Desktop App is completely optional. All operations run directly against `~/.qoder/projects/` and the headless CLI engine.
