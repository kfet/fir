---
name: self-handoff
description: Write a handoff document and restart with a clean LLM context in the same tmux pane. Use when context is getting large, switching tasks, or explicitly asked to hand off. The new session picks up from the handoff doc. This is a self-handoff — same process, clean slate.
---

Write a handoff MD doc capturing everything the next session needs. Choose a temporary location.

Write what the next agent actually needs to continue effectively.

After writing the handoff doc, run ONE last bash command:

```bash
tmux send-keys -t "$TMUX_PANE" "/new" Enter && tmux send-keys -t "$TMUX_PANE" "Read and follow the self-handoff document at /path/to/handoff.md — continue where the previous session left off." Enter
```
