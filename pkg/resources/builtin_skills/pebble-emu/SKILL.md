---
name: pebble-emu
description: Build, run, and observe Pebble watchapps/watchfaces in the Rebble pebble-tool emulator. Captures emulator output as PNGs (and optionally VNC/noVNC) so an AI agent can see what the watch is showing and iterate on apps without a physical Pebble.
---

# Pebble emulator — AI-friendly recipe

The Pebble platform is alive again via **Rebble / Core Devices**. The modern
`pebble-tool` (v5.x) bundles QEMU, builds C/JS apps, runs them in an emulator,
and can stream PNG screenshots or expose a VNC server — perfect for agent loops
where you build → install → look → fix.

## When to use

- User asks to write, debug, or iterate on a Pebble app or watchface.
- User mentions Pebble, Rebble, Core Devices, `.pbw`, aplite/basalt/chalk/diorite/emery/flint.
- You need to "see" what the watch screen looks like.

## Prerequisites (one-time)

The host should be macOS or Linux. Check first; only install what is missing.

```bash
command -v pebble && pebble --version    # already installed?
command -v uv     || brew install uv     # macOS; on Linux: curl -LsSf https://astral.sh/uv/install.sh | sh
command -v node   || brew install node   # required for JS/PebbleKit-JS builds
```

Install / upgrade `pebble-tool` (publishes its own bundled QEMU per SDK):

```bash
uv tool install pebble-tool        # or: uv tool upgrade pebble-tool
pebble sdk install latest          # downloads SDK + QEMU binaries for your OS
pebble sdk list                    # confirm an active SDK
```

That gives you `pebble` with subcommands: `build`, `install`, `logs`,
`screenshot`, `kill`, `wipe`, `emu-control`, `emu-tap`, `emu-accel`,
`emu-battery`, `emu-bt-connection`, `emu-compass`, `emu-app-config`,
`gdb`, `repl`, `new-project`, `analyze-size`.

## Platforms (watch hardware variants)

Pass to every emulator command via `--emulator <name>`:

| Name      | Watch                  | Display        | Color |
|-----------|------------------------|----------------|-------|
| `aplite`  | Original Pebble / Steel| 144×168        | B&W   |
| `basalt`  | Pebble Time            | 144×168        | Color |
| `chalk`   | Pebble Time Round      | 180×180 round  | Color |
| `diorite` | Pebble 2               | 144×168        | B&W   |
| `emery`   | Pebble Time 2          | 200×228        | Color |
| `flint`   | Core Devices / Time 2  | 200×228        | Color |

When in doubt, default to **`basalt`** (most common color target).

## Core loop — build, run, observe

### 1. Create or enter a project

```bash
pebble new-project my-face          # scaffolds C + package.json
cd my-face
```

A project is a directory with `package.json` (`pebble.targetPlatforms`,
`pebble.watchapp`) and `src/c/main.c`. Older `appinfo.json` projects can be
upgraded with `pebble convert-project`.

### 2. Build

```bash
pebble build
# produces build/<name>.pbw  (a zip of platform-specific binaries + resources)
```

Build errors land on stderr with `waf` formatting — read them, fix the C, rebuild.

### 3. Install + launch emulator (with logs)

```bash
pebble install --emulator basalt --logs
```

This will:
- spawn a QEMU process for the platform if none is running,
- send the freshly-built `.pbw` over the QEMU pebble-protocol socket,
- launch the app on the emulated watch,
- stream `APP_LOG(...)` output to your terminal until you Ctrl-C.

**Background it for agent use** so you can keep working:

```bash
pebble install --emulator basalt >/tmp/pebble.log 2>&1 &
# logs separately, non-blocking — open a tail when needed:
pebble logs --emulator basalt >/tmp/pebble-logs.log 2>&1 &
```

### 4. Observe — capture the screen as a PNG

This is the agent's eye. The screenshot is pulled live from the running QEMU
over the pebble protocol — no display server needed.

```bash
pebble screenshot --emulator basalt --no-open /tmp/pebble.png
# then read it back as an attachment
```

Useful flags:
- `--no-open` — don't shell-open the image (essential for headless/agent runs).
- `--no-correction` — raw framebuffer colors (skip Pebble's color correction).
- `--scale 4` — integer upscale; helpful for tiny aplite displays.

In a fir agent loop, `Read` the PNG path — Read sends image files as
attachments so the model can actually see the watch face.

### 5. Drive inputs and sensors

```bash
pebble emu-tap     --emulator basalt --direction +x       # tap on x axis
pebble emu-accel   --emulator basalt tilt-left            # canned motion
pebble emu-battery --emulator basalt --percent 20 --charging
pebble emu-bt-connection --emulator basalt --connected no
pebble emu-compass --emulator basalt --heading 90
pebble emu-app-config --emulator basalt                   # opens config page
pebble emu-control --emulator basalt                      # interactive sensor UI in browser
```

Buttons (back/up/select/down) are sent through `emu-control` or via the QEMU
console; for scripted button presses see "Direct QEMU socket" below.

### 6. Live VNC / noVNC (when a human or VNC client is watching)

Add `--vnc` to *any* emulator-launching command and `pebble-tool` will:
- start QEMU with `-vnc :1` → **TCP port 5901** on localhost (RFB protocol),
- spawn `websockify` on **port 6080** so you can also point a noVNC web client at
  `http://localhost:6080/vnc.html?host=localhost&port=6080`.

```bash
pebble install --emulator basalt --vnc --logs
# then, from a VNC client:
open vnc://localhost:5901            # macOS Screen Sharing
# or for a one-shot capture from the agent:
vncdo -s localhost:5901 capture /tmp/pebble-vnc.png   # pip install vncdotool
```

Notes:
- Only **one** VNC-enabled emulator runs at a time (display `:1` is shared).
  `pebble-tool` will kill any other VNC emulator on launch.
- VNC and `pebble screenshot` work simultaneously — keep VNC for the human,
  use `screenshot` for the agent.
- The VNC framebuffer shows **only the raw watch display**, not the chrome
  around it.

### 7. Kill / reset between runs

```bash
pebble kill                 # stop all running emulators (and websockify)
pebble wipe                 # clear emulator persistent storage for current SDK
```

Always `pebble kill` at the end of a session, or you'll leak QEMU processes.

## Recommended agent loop

For autonomous iteration on a watchface:

```bash
set -e
pebble build
pebble install --emulator basalt >/tmp/pebble-install.log 2>&1
sleep 1                                            # let UI settle
pebble screenshot --emulator basalt --no-open --scale 2 /tmp/watch.png
tail -n 200 /tmp/pebble-install.log               # build/install diagnostics
```

Then `Read /tmp/watch.png` to see the result, compare to the goal, edit
`src/c/main.c`, repeat. After interactive moments (button press, time change,
notification) re-screenshot.

For longer interactions, leave logs streaming to a file and grep:

```bash
pebble logs --emulator basalt >/tmp/pebble.log 2>&1 &
# … exercise the app …
grep -E 'APP_LOG|ERROR|WARN' /tmp/pebble.log
```

## Direct QEMU socket (advanced)

`pebble-tool` exposes the running emulator's QEMU monitor on a unix socket
under `~/Library/Application Support/Pebble SDK/SDKs/<ver>/` (macOS) or
`~/.pebble-sdk/SDKs/<ver>/` (Linux). Useful when you need to inject a button
press without `emu-control`:

```bash
# find the socket
find "$HOME/Library/Application Support/Pebble SDK" -name 'qemu_*.sock' 2>/dev/null
# send a custom QEMU monitor command via socat / nc -U
```

You rarely need this — `emu-*` subcommands cover the common cases.

## Common pitfalls

- **`pebble: command not found` after `uv tool install`** — add `~/.local/bin`
  to PATH (uv prints the line to add).
- **"No active SDK"** — run `pebble sdk install latest` then `pebble sdk activate <ver>`.
- **Screenshot looks wrong color** — the corrected output applies a Pebble-LCD
  gamma curve. Use `--no-correction` to compare to raw rendering.
- **Emulator hangs after a crash** — `pebble kill && pebble wipe` then retry.
- **Two emulators of different platforms** — fine; each platform has its own
  QEMU process. But only one with `--vnc` at a time.
- **Apple Silicon** — if you see Rosetta prompts or QEMU fails to launch, try
  `uv tool upgrade pebble-tool && pebble sdk install latest`.
- **Don't commit `build/`** — it's regenerated. Add to `.gitignore` if missing.

## References

- SDK install: https://developer.repebble.com/sdk/
- pebble-tool source (when an arg surface changes, read the installed module):
  `python3 -c "import pebble_tool, os; print(os.path.dirname(pebble_tool.__file__))"`
- Older but still-useful guides: https://developer.rebble.io/guides/tools-and-resources/pebble-tool/
- QEMU fork: https://github.com/pebble/qemu
