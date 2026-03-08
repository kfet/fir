---
name: fleet-update
description: Update fir on remote Tailscale hosts. List boxes, confirm selection, run fir update or deploy, and optionally push a new gh auth token.
---

# Fleet Update

Update fir across Tailscale network hosts.

## Steps

1. **Discover hosts** — run `tailscale status` and filter for online Linux/macOS hosts (exclude the local machine, phones, and offline devices).

2. **Show status** — for each reachable host, SSH in and collect:
   - `uname -s -m` (OS and arch)
   - `~/.local/bin/fir --version` (current version, or "not installed")
   - `gh auth status` (whether gh is authed)
   
   Present a table and ask the user which hosts to update (default: all).

3. **Update** — for each selected host, run:
   ```
   ssh HOST "export PATH=\$HOME/.local/bin:\$PATH && fir update"
   ```
   If `fir update` fails (e.g. no gh auth, no release found), offer to deploy via `make deploy HOST=<host>` instead, but only if running from the fir repository

4. **Push gh token** (optional) — if the user asks, or if a host fails due to missing gh auth:
   - Get the local token: `gh auth token`
   - Install gh on the remote host if missing (use GitHub's apt repo)
   - Push the token: `ssh HOST "echo '<token>' | gh auth login --with-token"`
   - Verify: `ssh HOST "gh auth status"`

## Notes

- fir is installed at `~/.local/bin/fir` on remote hosts.
- `~/.local/bin` must be in PATH — it's typically added to `~/.zshrc` or `~/.bashrc`.
- The local `gh` OAuth token (`gho_*`) can rotate. If updates fail across all hosts with auth errors, push a fresh token.
- Skip Android and iOS devices.
- When SSHing, some hosts may show post-quantum warnings — ignore those.
