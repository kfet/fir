# Plan: Split statusContainer into Two Containers

## Problem

`statusContainer` in `pkg/modes/interactive/mode.go` serves two unrelated purposes:

1. **Command result display** — transient messages from `/model`, `/theme`, `/queue`, `/compact`, `/resume`, `/new`, `/login`, `/logout`, etc. via `showStatus()` and `showWarning()`.
2. **Agent activity spinners** — "Working...", "Compacting context...", auto-compaction loaders.

These overwrite each other because they share one `*tui.Container`. When an agent starts working, `onAgentStart()` clears the container and shows "Working...", destroying any command status. Conversely, `showStatus`/`showWarning` clears the spinner.

## Current statusContainer Usage (all in `mode.go`)

### Field & Init
| Line | Code | Purpose |
|------|------|---------|
| 52 | `statusContainer *tui.Container` | Field declaration |
| 219-220 | `m.statusContainer = &tui.Container{}; m.ui.AddChild(...)` | Init & add to UI |

### Activity Spinners (agent lifecycle)
| Line | Code | Purpose |
|------|------|---------|
| 2618 | `m.statusContainer.Clear()` | rebuildChatFromMessages — clear all |
| 2664-2665 | `Clear() + AddChild(loader)` | auto_compaction_start — compaction spinner |
| 2732 | `m.statusContainer.Clear()` | onAgentStart — clear before spinner |
| 2741 | `m.statusContainer.AddChild(loader)` | onAgentStart — "Working..." spinner |
| 2797 | (comment) | "Spinner keeps running in statusContainer" |
| 2859 | `m.statusContainer.Clear()` | onAgentEnd — clear spinner |

### Activity Spinners (manual compaction)
| Line | Code | Purpose |
|------|------|---------|
| 995 | `m.statusContainer.Clear()` | handleCompact — clear before compaction |
| 1048 | `m.statusContainer.Clear()` | handleCompact goroutine — clear before spinner |
| 1055 | `m.statusContainer.AddChild(loader)` | handleCompact — compaction spinner |
| 1070 | `m.statusContainer.Clear()` | handleCompact — clear after compaction done |
| 1097 | `m.statusContainer.AddChild(loader)` | handleCompact — "Working..." after compact resumes |

### Command Status (showStatus / showWarning)
| Line | Code | Purpose |
|------|------|---------|
| 2463-2468 | `showStatus()` | Green text in statusContainer |
| 2475-2480 | `showWarning()` | Yellow text in statusContainer |

Called from ~30 places: `/model`, `/thinking`, `/theme`, `/resume`, `/compact` result, `/new`, `/queue`, `/dequeue`, `/unqueue`, `/login`, `/logout`, `/models`, bash errors, extension errors, unknown commands.

### Other
| Line | Code | Purpose |
|------|------|---------|
| 1376-1377 | `if m.statusContainer != nil { Clear() }` | handleNew — clear on new session |

### Test file (`mode_test.go`)
| Line | Code |
|------|------|
| 176-177 | Init statusContainer in test setup |
| 840, 866, 881 | Check statusContainer.Children for warnings |
| 1568 | Assert statusContainer empty |

## Proposed Design

### Two new containers

Replace `statusContainer` with:

```
activityContainer      *tui.Container  // spinners: "Working...", "Compacting..."
commandStatusContainer *tui.Container  // transient messages: showStatus, showWarning
```

### Layout (top to bottom)

```
┌─────────────────────────┐
│ messageContainer        │  (chat messages, scrollable)
│                         │
├─────────────────────────┤
│ activityContainer       │  (spinner when agent is working; empty otherwise)
├─────────────────────────┤
│ commandStatusContainer  │  (last command result; clears on next command or agent start)
├─────────────────────────┤
│ footerComponent         │  (token counts, model, key bindings)
├─────────────────────────┤
│ inputComponent          │  (text input)
└─────────────────────────┘
```

Activity above command status so the spinner is visually closest to the streaming content.

### Behavior Changes

1. **`onAgentStart()`**: clears `activityContainer`, adds "Working..." spinner. Does NOT touch `commandStatusContainer` — last command result remains visible.
2. **`onAgentEnd()`**: clears `activityContainer`. Does NOT touch `commandStatusContainer`.
3. **`showStatus()` / `showWarning()`**: write to `commandStatusContainer` only.
4. **`rebuildChatFromMessages()`**: clears both containers.
5. **`handleNew()`**: clears both containers.
6. **Auto-compaction / manual compaction spinners**: use `activityContainer`.
7. **Compaction result messages** (`showStatus("Compacted: ...")`): use `commandStatusContainer` as before.

### Edge Cases

- **Command status lifetime**: Command status persists until the next `showStatus`/`showWarning` call or `rebuildChatFromMessages`. This matches current behavior (no auto-clear timeout). A timeout could be added later but is out of scope.
- **Compaction flow**: Manual `/compact` first shows spinner in `activityContainer`, then on completion clears activity and calls `showStatus` into `commandStatusContainer`. These no longer conflict.
- **Compaction → resume**: After compaction, "Working..." spinner replaces compaction spinner in `activityContainer`. The compaction result in `commandStatusContainer` stays visible.
- **Multiple rapid commands**: Each `showStatus`/`showWarning` replaces the previous one (same as today).
- **Nil guards**: Both `showStatus` and `showWarning` already nil-check. Apply same pattern to both containers.

## Exact Changes Required

### `pkg/modes/interactive/mode.go`

1. **Line 52**: Replace `statusContainer *tui.Container` with two fields.
2. **Lines 219-220**: Create both containers, add both to `m.ui` (activity first, then commandStatus, before footer).
3. **Lines 995, 1048, 1055, 1097**: Change to `activityContainer`.
4. **Line 1070**: Change to `activityContainer.Clear()`.
5. **Lines 1074, 1081, 1089-1101**: Compaction result messages already use `showStatus`/`showWarning` — no change needed.
6. **Lines 1376-1377**: Clear both containers.
7. **Lines 2463-2468** (`showStatus`): Change to `commandStatusContainer`.
8. **Lines 2475-2480** (`showWarning`): Change to `commandStatusContainer`.
9. **Line 2618**: Clear both containers.
10. **Lines 2664-2665**: Change to `activityContainer`.
11. **Lines 2732, 2741**: Change to `activityContainer`.
12. **Line 2859**: Change to `activityContainer`.

### `pkg/modes/interactive/mode_test.go`

1. **Lines 176-177**: Create both containers instead of one.
2. **Lines 840, 866, 881**: Check `commandStatusContainer.Children` instead of `statusContainer.Children`.
3. **Line 1568**: Assert both containers empty, or just `commandStatusContainer`.

### No other files affected

`statusContainer` is only referenced within `mode.go` and `mode_test.go`.
