# Pi-mono Extension API: Real-World Usage Report

_Generated 2025-03-24 from analysis of ~30+ community extensions_

## Purpose

This report examines what API surface popular pi-mono extensions **actually use** in practice, compared against what fir's existing Python SDK (`fir_ext.py`) already supports. The goal is to inform the design of a Bun/Node.js SDK by focusing on the APIs that matter most.

## Methodology

Surveyed extensions from:
- pi-mono official examples (`packages/coding-agent/examples/extensions/`)
- Community repos: shitty-extensions, tmustier/pi-extensions, jayshah5696/pi-agent-extensions, rytswd/pi-agent-extensions, hjanuschka/shitty-extensions, can1357/oh-my-pi, beycom/onetool-pi
- Awesome list: qualisero/awesome-pi-agent
- npm packages and GitHub Topics (`pi-coding-agent`, `pi-agent`)

---

## Tier 1 — Core API (used by >80% of extensions)

These are the bread-and-butter APIs. Nearly every extension uses some combination of these.

| Pi-mono API | Fir equivalent | Supported? |
|---|---|---|
| `pi.on("session_start", ...)` | `@on("session_start")` | ✅ |
| `pi.on("tool_call", ...)` — block/allow | `@on("hook/tool_call")` | ✅ |
| `pi.on("agent_end", ...)` | `@on("agent_end")` | ✅ |
| `pi.registerTool({ name, description, handler })` | `@tool()` decorator | ✅ |
| `pi.registerCommand("/name", { handler })` | `@command()` decorator | ✅ |
| `ctx.ui.notify(msg, level)` | `ctx.notify()` | ✅ |
| `ctx.ui.setStatus(id, text)` | `ctx.set_status()` | ✅ |
| `pi.sendUserMessage(text, { deliverAs })` | `ctx.send_user_message()` | ✅ |
| `pi.getActiveTools()` / `pi.setActiveTools()` | `ctx.get_active_tools()` / `ctx.set_active_tools()` | ✅ |
| `pi.getAllTools()` | `ctx.list_tools()` | ✅ |

**Assessment:** Fir has full coverage of Tier 1. The core extension authoring loop — register tools, listen to events, gate tool calls, send messages — works today.

---

## Tier 2 — Popular Enhancement API (used by ~30-60% of extensions)

These APIs enable the most popular "enhancement" extensions: status widgets, interactive confirmations, session state persistence.

| Pi-mono API | Fir equivalent | Supported? |
|---|---|---|
| `ctx.ui.setWidget(id, lines)` | — | ❌ **Missing** |
| `ctx.ui.confirm(title, msg)` | — | ❌ **Missing** |
| `ctx.ui.select(title, options)` | — | ❌ **Missing** |
| `ctx.ui.custom(factory)` | — | ❌ **Missing** |
| `ctx.sessionManager.getBranch()` | — | ❌ **Missing** |
| `pi.appendEntry(type, data)` | — | ❌ **Missing** |
| `pi.on("session_shutdown", ...)` | `@on("session_shutdown")` | ✅ |
| `pi.sendMessage({ customType })` | `ctx.send_message()` | ✅ |
| `pi.on("before_agent_start", ...)` | `ctx.prepend()` (different pattern) | ⚠️ Partial |
| `pi.setModel(model)` | `ctx.set_model()` | ✅ |

### What these gaps block

| Missing API | Extensions that need it |
|---|---|
| `setWidget` | status-widget, usage-bar, ultrathink, resistance, loop, files-widget, todos, tab-status, cost-tracker |
| `confirm` | security gates, plan-mode, handoff, safe-git |
| `select` | sessions picker, oracle, model selectors |
| `custom` | tools.ts, snake, files-widget, arcade games, speedreading, todos |
| `getBranch` / `appendEntry` | todos, cost-tracker, handoff, any state-that-survives-compaction pattern |

---

## Tier 3 — Specialized API (used by ~10-30% of extensions)

Used by niche or power-user extensions.

| Pi-mono API | Fir equivalent | Supported? |
|---|---|---|
| `ctx.ui.setEditorText()` / `getEditorText()` | — | ❌ |
| `ctx.ui.setEditorComponent(factory)` | — | ❌ |
| `ctx.ui.input(title, placeholder)` | — | ❌ |
| `ctx.reload()` | — | ❌ |
| `ctx.shutdown()` | — | ❌ |
| `ctx.compact(opts)` | — | ❌ |
| `pi.registerShortcut(key, opts)` | — | ❌ |
| `pi.registerFlag(name, opts)` | — | ❌ |
| `pi.on("context", ...)` — message filtering | — | ❌ |
| `pi.on("input", ...)` — input interception | — | ❌ |
| `pi.on("tool_result", ...)` — modify results | — | ❌ |
| `pi.getThinkingLevel()` / `setThinkingLevel()` | — | ❌ |
| `pi.events` — inter-extension event bus | — | ❌ |
| `renderCall` / `renderResult` custom renderers | — | ❌ |
| `pi.registerMessageRenderer()` | — | ❌ |

---

## Critical Gaps Summary

The gaps cluster into **4 areas**, ordered by impact:

### 1. Widgets (`ctx.ui.setWidget`)
The single most popular enhancement pattern. Extensions show persistent UI lines (usage bars, cost counters, loop state, file lists). Fir has `setStatus` (single line) but no multi-line widget support.

### 2. Interactive Dialogs (`confirm` / `select` / `input`)
Used by security-gating extensions (confirm before dangerous tool calls), session pickers, and model selectors. Without these, permission-flow extensions cannot be ported.

### 3. Session State (`getBranch` / `appendEntry`)
The canonical pattern for stateful extensions that survive compaction and restart. Used by todos, cost trackers, handoff protocols. Without this, extensions lose state on compaction.

### 4. Custom TUI (`ctx.ui.custom`)
Full custom React-like component rendering. Used by flashier extensions (games, file browsers) but also practical ones (todos list, settings panels). Lower priority for an initial SDK.

---

## Recommendation for Bun/Node.js SDK

### Phase 1 — Ship with existing fir capabilities
Cover Tier 1 completely. This enables:
- Tool-registration extensions (MCP bridges, clipboard, memory, search)
- Event-driven extensions (auto-namer, cost logging, notification)
- Tool-gating extensions (security, approval workflows — basic block/allow only)
- Slash command extensions (shortcuts, workflows)

### Phase 2 — Add interactive dialogs
Add `confirm()`, `select()`, `input()` to the bridge API. This unlocks:
- Full security-gate extensions
- Session management extensions
- Model/config picker extensions

### Phase 3 — Add widgets and session state
Add `setWidget()` and session state persistence. This unlocks:
- Status dashboard extensions
- Stateful extensions (todos, cost tracking)
- Loop/progress monitoring

### Phase 4 — Custom TUI and advanced hooks
Add `custom()` rendering and the remaining Tier 3 hooks. This brings full pi-mono parity.

---

## Portability Assessment

| Extension category | Can port to fir today? | Blocking gap |
|---|---|---|
| Tool providers (MCP bridge, clipboard, search) | ✅ Yes | — |
| Event listeners (auto-namer, logging, notify) | ✅ Yes | — |
| Tool gating (basic block/allow) | ✅ Yes | — |
| Slash commands | ✅ Yes | — |
| Security gates with confirmation | ❌ No | `confirm()` |
| Status dashboards / usage bars | ❌ No | `setWidget()` |
| Session pickers / model selectors | ❌ No | `select()` |
| Stateful extensions (todos, cost) | ❌ No | `getBranch()` / `appendEntry()` |
| Custom TUI extensions | ❌ No | `custom()` |
| Input interceptors / message filters | ❌ No | `on("input")` / `on("context")` |
