---
builtin: true
name: tmux-observer
description: Attach a tmux window to an already-running fir session — observe its transcript and drive its input via tmux send-keys. Use when you need to watch or steer another fir agent.
override: true
---

Attach a tmux window to an **already-running** fir session so you can both observe its transcript and drive its input. The fir session must already exist (started by someone else, or by you in a previous step) — this skill does not start sessions.

The whole skill is three tmux commands plus one fir command. There is no script.

## When to use

- You started or know about another fir session (ACP, interactive, or `-p`) and want to watch what it's doing.
- You want to send prompts, steer mid-turn, or queue follow-ups into that session from your own process.
- You want a persistent observation window the human (or you) can re-check anytime.

## The model

**One tmux window per fir session you want to drive.** The window runs `fir observe <id> --interact`, which both tails the transcript and accepts stdin as messages back into the session. You read state with `tmux capture-pane` and send input with `tmux send-keys`.

## Step 1 — discover the session id

```bash
fir observe        # table of running sessions: ID NAME CWD STATUS AGE
```

Pick the 8-char id prefix from the `ID` column. Or resolve by cwd:

```bash
fir observe --cwd /path/to/project   # errors if 0 or >1 matches
```

You can also read sidecars directly: `~/.local/state/fir/agents/<id>.json` contains `session_id`, `session_name`, `cwd`, `status`, `mode`, etc.

## Step 2 — spawn the observer window

```bash
tmux new-window -n fir-<short> "fir observe <id> --interact"
```

- Formatted transcript with full message text — no truncation. Best for capture-pane reading.
- `--interact` = stdin lines are sent as messages. **Required** if you want to use `tmux send-keys` to drive the session.
- Use `--json` if you need raw JSONL events for programmatic parsing (more tokens, less readable).
- Window name `fir-<short>` is just a convention — pick whatever helps you find it later. Use `tmux list-windows` to see your windows.

If you want the observer in a fresh tmux session instead of the current one, use `tmux new-session -d -s <name> ...` — that's your call. This skill does not prescribe layout, naming, or session strategy.

## Step 3 — read the transcript

```bash
tmux capture-pane -p -t <window>           # currently visible content
tmux capture-pane -p -S - -t <window>      # full scrollback from the start
tmux capture-pane -p -S -200 -t <window>   # last 200 lines
```

`-p` prints to stdout. `-t` accepts window names (`fir-<short>`) or indices.

## Step 4 — send input

Each `tmux send-keys ... Enter` sends one message because `--interact` is line-oriented (Enter sends immediately).

```bash
# Plain prompt — fresh turn (queues if mid-turn).
tmux send-keys -t <window> 'what is your current plan?' Enter

# Steer — interrupt the current turn.
tmux send-keys -t <window> '!stop, read foo.go first' Enter

# FollowUp — queue for after the current turn.
tmux send-keys -t <window> '+also update the changelog' Enter

# Literal leading ! or +  — escape with backslash.
tmux send-keys -t <window> '\!literal bang' Enter
```

Sigil rules (first char of the line):
- `!` → `deliver_as=steer` (interrupt)
- `+` → `deliver_as=followUp` (queue)
- `\!` / `\+` → escape, send literal
- anything else → fresh prompt

Empty / whitespace-only lines are silently skipped.

## Step 5 — close the window

```bash
tmux kill-window -t <window>
```

The fir session itself keeps running; you only close your observer.

## Caveats

- **`--interact` against a `mode: interactive` session races with the human's TTY input.** Check the sidecar's `mode` field before driving an interactive session. ACP and print-mode sessions are the natural targets.
- **Quoting in `tmux send-keys`.** Single-quote your message and the shell takes care of it. For messages containing single quotes, prefer a heredoc piped to `fir send`:
  ```bash
  fir send <id> <<'EOF'
  message with 'quotes' and special chars
  EOF
  ```
  (`fir send` is the alternative input path — same socket, no observer pane needed. Use it when send-keys quoting gets ugly.)
- **Multi-line messages via send-keys.** Each Enter sends a separate message. For multi-line content, pipe via `fir send` instead.
- **Post-mortem.** If the session has ended, the sidecar still points at its transcript; `fir observe <id>` (no `--interact`) tails the file with no live updates. The window will not accept input.
