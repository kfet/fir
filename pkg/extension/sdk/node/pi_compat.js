/**
 * pi_compat — Pi-mono compatibility shim for fir.
 *
 * Lets pi-mono extensions run on fir by mapping the pi-mono ExtensionAPI
 * surface to fir_ext.js under the hood.
 *
 * Usage (in a pi-mono extension, after module resolution is set up):
 *
 *     import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";
 *     export default function (pi: ExtensionAPI) { ... }
 *
 * The run.sh wrapper loads this shim, which:
 *   1. Imports the extension's default export
 *   2. Creates a fake ExtensionAPI object that maps to fir_ext calls
 *   3. Calls the extension factory with the fake API
 *   4. Calls fir.run()
 *
 * @module pi_compat
 */

"use strict";

const fir = require("./fir_ext");

// ---------------------------------------------------------------------------
// Pi-mono ExtensionContext → fir Context adapter
// ---------------------------------------------------------------------------

/**
 * Wraps a fir Context to look like pi-mono's ExtensionContext.
 * Pi-mono ctx has ctx.ui.notify(), ctx.ui.setStatus(), etc.
 * Fir ctx has ctx.notify(), ctx.setStatus(), etc.
 */
function wrapContext(firCtx) {
  const ctx = {
    // ctx.ui.* — pi-mono UI namespace
    ui: {
      notify(message, level) {
        return _ctx.notify(message, level || "info");
      },
      setStatus(id, text) {
        // fir setStatus takes just text, no id — use "id: text" format
        const display = text ? `${id}: ${text}` : "";
        return _ctx.setStatus(display);
      },
      setWidget(_id, _lines) {
        // Not supported in fir — silent no-op
        return Promise.resolve();
      },
      async confirm(_title, _message) {
        // Not supported in fir — default to true (allow)
        return true;
      },
      async select(_title, _options) {
        // Not supported in fir — return undefined (cancelled)
        return undefined;
      },
      async input(_title, _placeholder) {
        // Not supported in fir — return undefined (cancelled)
        return undefined;
      },
      custom(_factory) {
        // Not supported in fir — no-op
        return Promise.resolve();
      },
      setTitle(title) {
        return _ctx.setSessionName(title);
      },
      setEditorText() { return Promise.resolve(); },
      getEditorText() { return Promise.resolve(""); },
    },

    // ctx.cwd
    get cwd() {
      return process.cwd();
    },

    // ctx.hasUI — always true in fir interactive mode
    hasUI: true,

    // ctx.sessionManager — partial stub
    sessionManager: {
      getEntries() { return []; },
      getBranch() { return []; },
      getLeafId() { return undefined; },
      getSessionFile() { return undefined; },
      getLabel(_id) { return undefined; },
    },

    // ctx.modelRegistry — stub
    modelRegistry: {
      find() { return undefined; },
      getAvailable() { return Promise.resolve([]); },
    },

    // ctx.model — undefined
    model: undefined,

    // Control flow
    isIdle() { return true; },
    hasPendingMessages() { return false; },
    abort() { return Promise.resolve(); },

    shutdown() {
      process.exit(0);
    },

    compact(opts) {
      // Not supported — no-op
      return Promise.resolve();
    },

    getContextUsage() {
      return undefined;
    },

    getSystemPrompt() {
      return "";
    },

    // Underlying fir context for direct access
    _firCtx: firCtx,
  };

  return ctx;
}

/**
 * Wraps a fir Context for command handlers (ExtensionCommandContext).
 * Adds waitForIdle, newSession, fork, navigateTree, reload.
 */
function wrapCommandContext(firCtx) {
  const ctx = wrapContext(firCtx);
  ctx.waitForIdle = () => Promise.resolve();
  ctx.newSession = () => Promise.resolve({ cancelled: false });
  ctx.fork = () => Promise.resolve({ cancelled: false });
  ctx.navigateTree = () => Promise.resolve({ cancelled: false });
  ctx.reload = () => Promise.resolve();
  return ctx;
}

// ---------------------------------------------------------------------------
// Pi-mono ExtensionAPI → fir registration adapter
// ---------------------------------------------------------------------------

/**
 * Creates a pi-mono ExtensionAPI object that delegates to fir_ext.
 */
function createExtensionAPI() {
  // Event bus for inter-extension communication
  const _eventBus = new Map();

  // Shared context for outbound RPC calls (reused across all API methods)
  const _ctx = new fir.Context();

  // Minimal AbortSignal stub for pi-mono tool execute compatibility
  function makeAbortSignal() {
    const listeners = [];
    return {
      aborted: false,
      addEventListener(event, fn) { if (event === "abort") listeners.push(fn); },
      removeEventListener(event, fn) {
        if (event === "abort") {
          const i = listeners.indexOf(fn);
          if (i >= 0) listeners.splice(i, 1);
        }
      },
    };
  }

  const api = {
    // ── Events ────────────────────────────────────────────────────────
    on(eventName, handler) {
      // Pi-mono event names → fir event/hook names
      const hookEvents = new Set([
        "tool_call",
        "tool_result",
        "session_before_switch",
        "session_before_fork",
        "session_before_compact",
        "session_before_tree",
        "input",
        "context",
        "before_provider_request",
        "user_bash",
      ]);

      if (hookEvents.has(eventName)) {
        // Map to fir hook
        fir.on(`hook/${eventName}`, async (params, firCtx) => {
          const ctx = wrapContext(firCtx);
          const event = mapInboundEvent(eventName, params);
          const result = await handler(event, ctx);
          return mapHookResult(eventName, result);
        });
      } else {
        // Map to fir event
        fir.on(eventName, async (params, firCtx) => {
          const ctx = wrapContext(firCtx);
          const event = mapInboundEvent(eventName, params);
          await handler(event, ctx);
        });
      }
    },

    // ── Tools ─────────────────────────────────────────────────────────
    registerTool(def) {
      fir.tool({
        name: def.name,
        description: def.description || "",
        parameters: def.parameters || { type: "object", properties: {} },
      }, async (params, firCtx) => {
        const ctx = wrapContext(firCtx);
        // Pi-mono execute signature: (toolCallId, params, signal, onUpdate, ctx)
        // We pass stubs for toolCallId, signal, onUpdate
        const toolCallId = `call_${Date.now()}`;
        const signal = makeAbortSignal();
        const onUpdate = () => {}; // streaming updates not supported over stdio
        const result = await def.execute(toolCallId, params, signal, onUpdate, ctx);
        // Normalize result
        if (!result) return { content: [{ type: "text", text: "" }], is_error: false };
        return {
          content: result.content || [{ type: "text", text: String(result) }],
          is_error: result.isError || false,
        };
      });
    },

    // ── Commands ──────────────────────────────────────────────────────
    registerCommand(name, opts) {
      fir.command(name, opts.description || "", async (args, firCtx) => {
        const ctx = wrapCommandContext(firCtx);
        const argsStr = Array.isArray(args) ? args.join(" ") : (args || "");
        await opts.handler(argsStr, ctx);
        return { message: "" };
      });
    },

    // ── Messages ─────────────────────────────────────────────────────
    sendMessage(message, opts) {
      
      const deliverAs = (opts && opts.deliverAs) || "steer";
      const triggerTurn = (opts && opts.triggerTurn) || false;
      return _ctx.sendMessage(
        message.customType || "extension",
        message.content || "",
        { display: message.display || false, deliverAs, triggerTurn }
      );
    },

    sendUserMessage(content, opts) {
      
      const deliverAs = (opts && opts.deliverAs) || undefined;
      return _ctx.sendUserMessage(
        typeof content === "string" ? content : JSON.stringify(content),
        { deliverAs }
      );
    },

    // ── State persistence ────────────────────────────────────────────
    appendEntry(customType, data) {
      // Map to fir session data (best effort — fir uses key/value, not entries)
      
      const key = `pi_entry_${customType}_${Date.now()}`;
      return _ctx.setSessionData(key, JSON.stringify(data));
    },

    // ── Session name ─────────────────────────────────────────────────
    setSessionName(name) {
      
      return _ctx.setSessionName(name);
    },

    getSessionName() {
      
      return _ctx.getSessionName();
    },

    // ── Labels ───────────────────────────────────────────────────────
    setLabel(entryId, label) {
      
      if (label) {
        return _ctx.setLabel(entryId, label);
      }
      return _ctx.clearLabel(entryId);
    },

    // ── Tools management ─────────────────────────────────────────────
    getActiveTools() {
      
      return _ctx.getActiveTools();
    },

    getAllTools() {
      
      return _ctx.listTools();
    },

    setActiveTools(names) {
      
      return _ctx.setActiveTools(names);
    },

    // ── Model ────────────────────────────────────────────────────────
    async setModel(model) {
      
      if (typeof model === "object" && model.provider && model.id) {
        return _ctx.setModel(model.provider, model.id);
      }
      return false;
    },

    // ── Thinking level ───────────────────────────────────────────────
    getThinkingLevel() { return "off"; },
    setThinkingLevel(_level) { /* no-op */ },

    // ── Exec ─────────────────────────────────────────────────────────
    exec(command, args, opts) {
      
      const timeout = (opts && opts.timeout) || 10000;
      return _ctx.exec(command, args || [], timeout);
    },

    // ── Shortcuts / Flags ────────────────────────────────────────────
    registerShortcut(_shortcut, _opts) { /* no-op in fir */ },
    registerFlag(_name, _opts) { /* no-op in fir */ },
    getFlag(_name) { return undefined; },

    // ── Commands query ───────────────────────────────────────────────
    getCommands() { return []; },

    // ── Message renderer ─────────────────────────────────────────────
    registerMessageRenderer(_customType, _renderer) { /* no-op */ },

    // ── Provider registration ────────────────────────────────────────
    registerProvider(name, config) {
      mapAndRegisterProvider(name, config || {});
    },
    unregisterProvider(name) {
      // Providers are fixed at the init handshake, so a live unregister
      // can't reach fir. No-op (warn once so authors aren't surprised).
      process.stderr.write(
        `pi_compat: unregisterProvider(${name}) is not supported — ` +
          `providers are declared at startup and cannot be removed live.\n`
      );
    },

    // ── Event bus ────────────────────────────────────────────────────
    events: {
      on(name, handler) {
        if (!_eventBus.has(name)) _eventBus.set(name, []);
        _eventBus.get(name).push(handler);
      },
      emit(name, data) {
        const handlers = _eventBus.get(name) || [];
        for (const h of handlers) {
          try { h(data); } catch (e) { process.stderr.write(`Event bus error: ${e}\n`); }
        }
      },
    },
  };

  return api;
}

// ---------------------------------------------------------------------------
// Provider mapping
// ---------------------------------------------------------------------------

/**
 * Map a pi-mono ProviderConfig to fir's registerProvider spec and register it.
 *
 * Supported: `name`, `api`, `apiKey`, `baseUrl` (applied to each model), and
 * `models` (id/name/reasoning/input/contextWindow/maxTokens/cost). Because fir
 * declares providers at the init handshake and pi_compat awaits the extension
 * factory before `fir.run()`, factory-time registration — including after async
 * model discovery, as pi-llama does — is captured.
 *
 * `apiKey` is passed as fir's `env_keys.primary`, i.e. the NAME of an
 * environment variable fir reads in its own process. Unlike pi-mono it is not
 * a literal-key fallback: fir's provider wire cannot carry a literal secret,
 * and the extension runs in a separate process so it cannot inject one into
 * fir's environment. Authors should pass an env-var name (the recommended
 * pi-mono pattern); a literal key won't authenticate. Local unauthenticated
 * servers (e.g. llama.cpp) need no key.
 *
 * Not supported (warned, then dropped): `oauth` (use fir's auth-provider API),
 * `streamSimple` (custom JS streaming isn't bridged; use `api` passthrough),
 * and `headers`/`authHeader` (no equivalent in fir's provider wire).
 *
 * @param {string} name
 * @param {Object} config — pi-mono ProviderConfig
 */
function mapAndRegisterProvider(name, config) {
  const unsupported = [];
  if (config.oauth) unsupported.push("oauth");
  if (config.streamSimple) unsupported.push("streamSimple");
  if (config.headers) unsupported.push("headers");
  if (config.authHeader) unsupported.push("authHeader");
  if (unsupported.length) {
    process.stderr.write(
      `pi_compat: provider '${name}' uses unsupported option(s) ` +
        `[${unsupported.join(", ")}] — ignored.\n`
    );
  }

  const models = (config.models || []).map((m) => ({
    id: m.id,
    name: m.name,
    baseUrl: m.baseUrl || config.baseUrl || undefined,
    reasoning: !!m.reasoning,
    input: m.input,
    contextWindow: m.contextWindow,
    maxTokens: m.maxTokens,
    cost: m.cost,
  }));

  if (!models.length && config.baseUrl) {
    // URL-only override of an existing provider isn't expressible via fir's
    // handshake provider wire (which registers models, not URL patches).
    process.stderr.write(
      `pi_compat: provider '${name}' baseUrl-only override is not supported ` +
        `without models — ignored.\n`
    );
    return;
  }

  fir.registerProvider({
    id: name,
    api: config.api,
    displayName: config.name,
    envKeys: config.apiKey ? { primary: config.apiKey } : undefined,
    models,
  });
}

// ---------------------------------------------------------------------------
// Event mapping helpers
// ---------------------------------------------------------------------------

/**
 * Map fir inbound event params to pi-mono event shape.
 * Pi-mono events have specific field names; fir sends generic params.
 */
function mapInboundEvent(eventName, params) {
  // Most events can pass through directly since field names are similar.
  // Add specific mappings as needed.
  switch (eventName) {
    case "tool_call":
      return {
        toolName: params.tool_name || params.name || "",
        toolCallId: params.tool_call_id || params.id || "",
        input: params.input || params.params || {},
      };
    case "tool_result":
      return {
        toolName: params.tool_name || "",
        toolCallId: params.tool_call_id || "",
        input: params.input || {},
        content: params.content || [],
        details: params.details || {},
        isError: params.is_error || false,
      };
    case "agent_end":
      return {
        messages: params.messages || [],
      };
    case "turn_start":
    case "turn_end":
      return {
        turnIndex: params.turn_index || 0,
        message: params.message,
        toolResults: params.tool_results || [],
      };
    default:
      return params || {};
  }
}

/**
 * Map pi-mono hook handler return values to fir hook results.
 */
function mapHookResult(eventName, result) {
  if (!result) return null;

  switch (eventName) {
    case "tool_call":
      if (result.block) {
        return { allow: false, reason: result.reason || "Blocked by extension" };
      }
      return { allow: true };
    case "tool_result":
      // Pass through content/details/isError modifications
      return result;
    case "input":
      if (result.action === "handled") return { handled: true };
      if (result.action === "transform") return { text: result.text };
      return null;
    case "session_before_switch":
    case "session_before_fork":
    case "session_before_compact":
    case "session_before_tree":
      if (result.cancel) return { cancel: true };
      return result;
    default:
      return result;
  }
}

// ---------------------------------------------------------------------------
// isToolCallEventType helper
// ---------------------------------------------------------------------------

function isToolCallEventType(toolName, event) {
  return event && event.toolName === toolName;
}

// ---------------------------------------------------------------------------
// Entry point — load and run a pi-mono extension
// ---------------------------------------------------------------------------

/**
 * Load a pi-mono extension module and run it on fir.
 *
 * @param {string} extensionPath — path to the extension .ts/.js file
 * @param {Object} [opts]
 * @param {string} [opts.name] — extension name for init handshake
 */
async function loadAndRun(extensionPath, opts) {
  const name = (opts && opts.name) || require("path").basename(extensionPath, require("path").extname(extensionPath));

  // Create the pi-mono API facade
  const piApi = createExtensionAPI();

  // Load the extension module
  let mod;
  try {
    mod = require(extensionPath);
  } catch (e) {
    // Try dynamic import for ESM
    mod = await import(extensionPath);
  }

  // Get the default export (the extension factory function)
  const factory = mod.default || mod;
  if (typeof factory !== "function") {
    process.stderr.write(`Error: ${extensionPath} does not export a function\n`);
    process.exit(1);
  }

  // Call the factory with our fake ExtensionAPI
  await factory(piApi);

  // Start the fir event loop
  fir.run({ name });
}

// ---------------------------------------------------------------------------
// CLI: when run directly, load the extension specified as argv[2]
// ---------------------------------------------------------------------------

if (require.main === module) {
  const extPath = process.argv[2];
  if (!extPath) {
    process.stderr.write("Usage: node pi_compat.js <extension-path>\n");
    process.exit(1);
  }
  const resolved = require("path").resolve(extPath);
  loadAndRun(resolved).catch((err) => {
    process.stderr.write(`Failed to load extension: ${err}\n`);
    process.exit(1);
  });
}

// ---------------------------------------------------------------------------
// Exports (for programmatic use)
// ---------------------------------------------------------------------------

module.exports = {
  createExtensionAPI,
  wrapContext,
  loadAndRun,
  isToolCallEventType,
};
