/**
 * fir_ext — Lightweight Node.js/Bun SDK for fir external process extensions.
 *
 * Speaks JSON-RPC 2.0 over stdin/stdout so extension authors write simple
 * handler functions instead of protocol code.
 *
 * Quick start:
 *
 *     const fir = require("fir_ext");
 *
 *     fir.tool({
 *       name: "greet",
 *       description: "Greet someone by name",
 *       parameters: {
 *         type: "object",
 *         properties: { name: { type: "string" } },
 *         required: ["name"],
 *       },
 *     }, (params, ctx) => {
 *       ctx.notify(`Greeting ${params.name}!`, "info");
 *       return { message: `Hello, ${params.name}!` };
 *     });
 *
 *     fir.on("session_start", (params, ctx) => {
 *       ctx.setStatus("extension ready");
 *     });
 *
 *     fir.run({ name: "my-ext" });
 *
 * Works with Node.js >= 18 and Bun >= 1.0.
 *
 * @module fir_ext
 */

"use strict";

const readline = require("readline");

// ---------------------------------------------------------------------------
// Global registries
// ---------------------------------------------------------------------------

/** @type {Array<Object>} */
const _tools = [];
/** @type {Map<string, Function>} */
const _toolHandlers = new Map();
/** @type {Map<string, Function>} */
const _hookHandlers = new Map();
/** @type {Map<string, Function>} */
const _eventHandlers = new Map();
/** @type {Array<Object>} */
const _commands = [];
/** @type {Map<string, Function>} */
const _commandHandlers = new Map();

// Auth provider registries
/** @type {Array<Object>} */
const _authProviders = [];
/** @type {Map<string, Function>} */
const _authLoginHandlers = new Map();
/** @type {Map<string, Function>} */
const _authRefreshHandlers = new Map();
/** @type {Map<string, Function>} */
const _authApiKeyHandlers = new Map();
/** @type {Map<string, Function>} */
const _authListModelsHandlers = new Map();
/** @type {Map<string, Function>} */
const _authModifyModelsHandlers = new Map();

// Hosted-provider registries — populated by registerProvider() and the
// provider* handler registrations, reported at the init handshake and
// dispatched via provider/* RPCs.
/** @type {Array<Object>} wire-form provider specs */
const _providers = [];
/** @type {Map<string, Function>} */
const _providerStreamHandlers = new Map();
/** @type {Map<string, Function>} */
const _providerListModelsHandlers = new Map();
/** @type {Map<string, Function>} */
const _providerResolveCustomIdHandlers = new Map();
/** @type {Map<string, {cancelled: boolean}>} per-stream cancel flags */
const _streamCancels = new Map();
// Set true once the init handshake response has been sent; provider
// registration after this point can no longer reach fir (providers are
// declared at handshake), so registerProvider() warns instead of silently
// dropping.
let _initialized = false;

// ---------------------------------------------------------------------------
// JSON-RPC I/O
// ---------------------------------------------------------------------------

let _nextId = 1000;
/** @type {Map<number, {resolve: Function, reject: Function, timer?: NodeJS.Timeout}>} */
const _pending = new Map();

/**
 * @param {Object} msg
 * @param {NodeJS.WritableStream} [out]
 */
function writeMessage(msg, out) {
  const stream = out || process.stdout;
  stream.write(JSON.stringify(msg) + "\n");
}

function makeResponse(id, result) {
  return { jsonrpc: "2.0", id, result };
}

function makeError(id, code, message) {
  return { jsonrpc: "2.0", id, error: { code, message } };
}

function makeRequest(id, method, params) {
  const msg = { jsonrpc: "2.0", id, method };
  if (params !== undefined) msg.params = params;
  return msg;
}

// ---------------------------------------------------------------------------
// Context — outbound calls from extension → fir
// ---------------------------------------------------------------------------

class Context {
  /** @param {NodeJS.WritableStream} [out] */
  constructor(out) {
    this._out = out;
  }

  /**
   * Send a JSON-RPC request to fir and wait for the response.
   * @param {string} method
   * @param {any} [params]
   * @param {number} [timeout=10000] ms
   * @returns {Promise<any>}
   */
  _call(method, params, timeout = 10000) {
    return new Promise((resolve, reject) => {
      const id = ++_nextId;
      const timer = setTimeout(() => {
        _pending.delete(id);
        reject(new Error(`Timed out waiting for response to ${method}`));
      }, timeout);
      _pending.set(id, { resolve, reject, timer });
      writeMessage(makeRequest(id, method, params), this._out);
    });
  }

  /** Show a notification in fir. level: "info" | "warning" | "error" */
  async notify(message, level = "info") {
    await this._call("notify", { message, level });
  }

  /**
   * Run a command via fir.
   * @returns {Promise<{stdout: string, stderr: string, exit_code: number}>}
   */
  async exec(command, args = [], timeout = 10000) {
    return this._call("exec", { command, args }, timeout);
  }

  /**
   * Inject a custom message into the session.
   * @param {string} customType
   * @param {any} content
   * @param {Object} [opts]
   * @param {boolean} [opts.display]
   * @param {string} [opts.deliverAs] "steer" | "followUp"
   * @param {boolean} [opts.triggerTurn]
   */
  async sendMessage(customType, content, opts = {}) {
    const params = {
      custom_type: customType,
      content,
      display: opts.display || false,
    };
    if (opts.deliverAs) params.deliver_as = opts.deliverAs;
    if (opts.triggerTurn) params.trigger_turn = opts.triggerTurn;
    await this._call("send_message", params);
  }

  /**
   * Inject a user-role message.
   * @param {string} content
   * @param {Object} [opts]
   * @param {string} [opts.deliverAs] "steer" | "followUp"
   */
  async sendUserMessage(content, opts = {}) {
    const params = { content };
    if (opts.deliverAs) params.deliver_as = opts.deliverAs;
    await this._call("send_user_message", params);
  }

  /** Set persistent status text in the footer. Empty string clears. */
  async setStatus(text) {
    await this._call("set_status", { status: text });
  }

  /** Set the display name for the session. */
  async setSessionName(name) {
    await this._call("set_session_name", { name });
  }

  /** Get the display name for the session. @returns {Promise<string>} */
  async getSessionName() {
    const result = await this._call("get_session_name");
    return result && result.name ? result.name : "";
  }

  /** Store a key/value pair persisted across /reexec. */
  async setSessionData(key, value) {
    await this._call("set_session_data", { key, value });
  }

  /** Retrieve a previously stored value. Returns null if absent. */
  async getSessionData(key) {
    const result = await this._call("get_session_data", { key });
    return result && result.ok ? result.value : null;
  }

  /** Set a label on a session entry. */
  async setLabel(entryId, label) {
    await this._call("set_label", { entry_id: entryId, label });
  }

  /** Clear a label from a session entry. */
  async clearLabel(entryId) {
    await this._call("clear_label", { entry_id: entryId });
  }

  /** @returns {Promise<string[]>} */
  async getActiveTools() {
    return this._call("get_active_tools");
  }

  /** @param {string[]} names */
  async setActiveTools(names) {
    await this._call("set_active_tools", { names });
  }

  /**
   * Change the current model.
   * @returns {Promise<boolean>}
   */
  async setModel(provider, modelId) {
    const result = await this._call("set_model", { provider, id: modelId });
    return !!(result && result.ok);
  }

  /** Trigger the agent to continue without injecting any message. */
  async continueSession() {
    await this._call("continue_session", undefined, 60000);
  }

  /**
   * Ask a side question using the current session context.
   * One-shot LLM call, no history.
   * @returns {Promise<string>}
   */
  async sideQuery(question, timeout = 120000) {
    const result = await this._call("side_query", { question }, timeout);
    return result && result.text ? result.text : "";
  }

  /**
   * Call a registered tool by name.
   * @returns {Promise<{content: Array, is_error: boolean}>}
   */
  async callTool(name, params = {}, timeout = 60000) {
    const result = await this._call("call_tool", { name, params }, timeout);
    if (result && typeof result === "object") return result;
    return { content: [{ text: String(result) }], is_error: false };
  }

  /**
   * Return info about all registered tools.
   * @returns {Promise<Array<{name: string, description?: string, parameters?: Object}>>}
   */
  async listTools(timeout = 10000) {
    const result = await this._call("list_tools", {}, timeout);
    return Array.isArray(result) ? result : [];
  }

  /**
   * Add a [SYS_EXT] block to the system prompt.
   * @param {string} content
   */
  async prepend(content) {
    await this._call("prepend_context", { content });
  }
}

// ---------------------------------------------------------------------------
// AuthContext — extended context for auth provider handlers
// ---------------------------------------------------------------------------

class AuthContext extends Context {
  /** @returns {Promise<{verifier: string, challenge: string}>} */
  async generatePkce(timeout = 10000) {
    return this._call("auth/generate_pkce", {}, timeout);
  }

  /**
   * Start a local HTTP server for OAuth callback.
   * @returns {Promise<{addr: string, redirect_uri: string}>}
   */
  async startCallbackServer(addr = "127.0.0.1:0", path = "/callback", timeout = 10000) {
    return this._call("auth/start_callback_server", { addr, path }, timeout);
  }

  /** @returns {Promise<{code: string, state: string}>} */
  async awaitCallback(timeout = 300000) {
    return this._call("auth/await_callback", {}, timeout);
  }

  async stopCallbackServer(timeout = 10000) {
    await this._call("auth/stop_callback_server", {}, timeout);
  }

  async openUrl(url, instructions = "") {
    await this._call("auth/open_url", { url, instructions });
  }

  async progress(message) {
    await this._call("auth/progress", { message });
  }

  async prompt(message, placeholder = "", allowEmpty = false, timeout = 300000) {
    const result = await this._call(
      "auth/prompt",
      { message, placeholder, allow_empty: allowEmpty },
      timeout
    );
    return result && result.value ? result.value : "";
  }
}

// ---------------------------------------------------------------------------
// ToolError
// ---------------------------------------------------------------------------

class ToolError extends Error {
  /**
   * @param {string} message
   * @param {number} [code=-32000]
   */
  constructor(message, code = -32000) {
    super(message);
    this.name = "ToolError";
    this.code = code;
  }
}

// ---------------------------------------------------------------------------
// Registration API
// ---------------------------------------------------------------------------

/**
 * Register a tool.
 *
 * @param {Object} spec
 * @param {string} spec.name
 * @param {string} spec.description
 * @param {Object} [spec.parameters]
 * @param {Object} [spec.displayHint]
 * @param {Function} handler - (params, ctx) => any | Promise<any>
 */
function tool(spec, handler) {
  const def = {
    name: spec.name,
    description: spec.description,
    parameters: spec.parameters || { type: "object", properties: {} },
  };
  if (spec.displayHint) def.display_hint = spec.displayHint;
  _tools.push(def);
  _toolHandlers.set(spec.name, handler);
}

/**
 * Register a slash command.
 *
 * @param {string} name
 * @param {string} description
 * @param {Function} handler - (args: string[], ctx) => any | Promise<any>
 */
function command(name, description, handler) {
  _commands.push({ name, description: description || "" });
  _commandHandlers.set(name, handler);
}

/**
 * Register an event or hook handler.
 *
 * Hook names start with "hook/" (e.g. "hook/tool_call").
 * Event names are bare (e.g. "session_start").
 *
 * @param {string} eventName
 * @param {Function} handler - (params, ctx) => any | Promise<any>
 */
function on(eventName, handler) {
  if (eventName.startsWith("hook/")) {
    _hookHandlers.set(eventName, handler);
  } else {
    _eventHandlers.set(eventName, handler);
  }
}

/**
 * Register an OAuth auth provider.
 *
 * @param {Object} spec
 * @param {string} spec.id
 * @param {string} spec.name
 * @param {boolean} [spec.usesCallbackServer=true]
 * @param {Function} handler - login handler (params, authCtx) => Promise<{access, refresh, expires}>
 */
function authProvider(spec, handler) {
  _authProviders.push({
    id: spec.id,
    name: spec.name,
    uses_callback_server: spec.usesCallbackServer !== false,
  });
  _authLoginHandlers.set(spec.id, handler);
}

/**
 * Register a token refresh handler for an auth provider.
 * @param {string} providerId
 * @param {Function} handler
 */
function authRefresh(providerId, handler) {
  _authRefreshHandlers.set(providerId, handler);
}

/**
 * Register a custom API key extractor for an auth provider.
 * @param {string} providerId
 * @param {Function} handler
 */
function authApiKey(providerId, handler) {
  _authApiKeyHandlers.set(providerId, handler);
}

/**
 * Register a model lister for an auth provider.
 * @param {string} providerId
 * @param {Function} handler
 */
function authListModels(providerId, handler) {
  _authListModelsHandlers.set(providerId, handler);
}

/**
 * Register a model modifier for an auth provider.
 * @param {string} providerId
 * @param {Function} handler
 */
function authModifyModels(providerId, handler) {
  _authModifyModelsHandlers.set(providerId, handler);
}

// ---------------------------------------------------------------------------
// Hosted-provider registration
// ---------------------------------------------------------------------------

/**
 * Convert a friendly (camelCase) provider spec to its on-the-wire dict form.
 * Mirrors the Python SDK's `_provider_to_wire`. Only non-empty fields are
 * emitted so the wire payload matches fir's `ProviderSpec` expectations.
 * @param {Object} p
 * @returns {Object}
 */
function _providerToWire(p) {
  const out = { id: p.id };
  if (p.api) out.api = p.api;
  if (p.displayName) out.display_name = p.displayName;
  if (p.shortName) out.short_name = p.shortName;
  if (p.priority) out.priority = p.priority;
  if (p.defaultModelId) out.default_model_id = p.defaultModelId;
  if (p.keyLink) out.key_link = p.keyLink;

  const ek = {};
  const envKeys = p.envKeys || {};
  if (envKeys.primary) ek.primary = envKeys.primary;
  if (envKeys.fallbacks && envKeys.fallbacks.length) ek.fallbacks = [...envKeys.fallbacks];
  if (envKeys.authenticated) ek.authenticated = true;
  if (Object.keys(ek).length) out.env_keys = ek;

  if (p.oauthProviderId) out.oauth_provider_id = p.oauthProviderId;
  if (p.claimsModelIdGlobs && p.claimsModelIdGlobs.length) {
    out.claims_model_id_globs = [...p.claimsModelIdGlobs];
  }
  if (p.refuseFuzzyMatch) out.refuse_fuzzy_match = true;
  if (p.supportsLiveList) out.supports_live_list = true;
  if (p.supportsCustomId) out.supports_custom_id = true;

  if (p.models && p.models.length) {
    out.models = p.models.map((m) => {
      const mw = { id: m.id };
      if (m.name) mw.name = m.name;
      if (m.baseUrl) mw.base_url = m.baseUrl;
      if (m.reasoning) mw.reasoning = true;
      if (m.input && m.input.length) mw.input = [...m.input];
      if (m.contextWindow) mw.context_window = m.contextWindow;
      if (m.maxTokens) mw.max_tokens = m.maxTokens;
      const cost = m.cost || {};
      if (cost.input) mw.cost_input = cost.input;
      if (cost.output) mw.cost_output = cost.output;
      if (cost.cacheRead) mw.cost_cache_read = cost.cacheRead;
      if (cost.cacheWrite) mw.cost_cache_write = cost.cacheWrite;
      if (m.serverTools && m.serverTools.length) mw.server_tools = [...m.serverTools];
      if (m.compaction) mw.compaction = true;
      if (m.reasoningEffortValues && m.reasoningEffortValues.length) {
        mw.reasoning_effort_values = [...m.reasoningEffortValues];
      }
      if (m.sweScore) mw.swe_score = m.sweScore;
      if (m.sweInferred) mw.swe_inferred = true;
      return mw;
    });
  }
  return out;
}

/**
 * Declare a hosted AI provider this extension contributes.
 *
 * Adds the provider to the init-handshake payload so fir registers it (a
 * synthetic `ext:<id>` API when no `api` is given, or a built-in wire
 * protocol when `api` is set) and routes streaming/listing back to this
 * extension. Pair with {@link providerStream} (when no `api`) and/or
 * {@link providerListModels} (when `supportsLiveList` is set).
 *
 * Idempotent on `spec.id` — a later call overwrites the earlier spec.
 *
 * Must be called before {@link run} completes the init handshake (e.g. at
 * module load or inside an awaited extension factory). Calls after init are
 * warned about and ignored, since providers are fixed at the handshake.
 *
 * @param {Object} spec — provider spec (camelCase; see _providerToWire)
 */
function registerProvider(spec) {
  if (!spec || !spec.id) throw new Error("registerProvider requires spec.id");
  if (_initialized) {
    process.stderr.write(
      `fir_ext: registerProvider(${spec.id}) ignored — called after init ` +
        `handshake; providers must be declared before run().\n`
    );
    return;
  }
  const wire = _providerToWire(spec);
  const i = _providers.findIndex((p) => p.id === spec.id);
  if (i >= 0) _providers[i] = wire;
  else _providers.push(wire);
}

/**
 * Register the streaming handler for a hosted provider (synthetic-API mode).
 * The handler `(params, ctx)` must return an (async) iterable of
 * AssistantMessageEvent dicts, ending with a terminal `done`/`error` event.
 * @param {string} providerId
 * @param {Function} handler
 */
function providerStream(providerId, handler) {
  _providerStreamHandlers.set(providerId, handler);
}

/**
 * Register the live model lister for a hosted provider. Invoked only when the
 * provider was declared with `supportsLiveList: true`. Handler `(params, ctx)`
 * returns an array of model-id strings (or `{ model_ids: [...] }`).
 * @param {string} providerId
 * @param {Function} handler
 */
function providerListModels(providerId, handler) {
  _providerListModelsHandlers.set(providerId, handler);
}

/**
 * Register a custom-ID resolver for a hosted provider. Handler `(params, ctx)`
 * returns a wire-form model dict or null.
 * @param {string} providerId
 * @param {Function} handler
 */
function providerResolveCustomId(providerId, handler) {
  _providerResolveCustomIdHandlers.set(providerId, handler);
}

/**
 * Return true if fir has asked us to cancel this provider stream. Long-running
 * {@link providerStream} generators should poll this between yields.
 * @param {string} streamId
 * @returns {boolean}
 */
function isCancelled(streamId) {
  const c = _streamCancels.get(streamId);
  return !!(c && c.cancelled);
}

// ---------------------------------------------------------------------------
// Main loop
// ---------------------------------------------------------------------------

/**
 * Start the extension event loop.
 *
 * @param {Object} [opts]
 * @param {string} [opts.name="node-ext"]
 * @param {NodeJS.ReadableStream} [opts.input]
 * @param {NodeJS.WritableStream} [opts.output]
 */
function run(opts = {}) {
  const name = opts.name || "node-ext";
  const input = opts.input || process.stdin;
  const output = opts.output || process.stdout;

  const ctx = new Context(output);
  const authCtx = new AuthContext(output);

  const subscribedEvents = [
    ...Array.from(_eventHandlers.keys()),
    ...Array.from(_hookHandlers.keys()),
  ];

  const rl = readline.createInterface({ input, crlfDelay: Infinity });

  rl.on("line", (line) => {
    let msg;
    try {
      msg = JSON.parse(line);
    } catch {
      return;
    }
    dispatch(msg, name, subscribedEvents, ctx, authCtx, output);
  });

  rl.on("close", () => {
    // Unblock all pending outbound calls
    for (const [id, p] of _pending) {
      clearTimeout(p.timer);
      p.reject(new Error("shutdown"));
      _pending.delete(id);
    }
  });
}

function dispatch(msg, name, subscribedEvents, ctx, authCtx, out) {
  const method = msg.method || "";
  const id = msg.id;
  const params = msg.params || {};

  // --- Response to an outbound request we made ---
  if (id != null && !method) {
    const p = _pending.get(id);
    if (p) {
      _pending.delete(id);
      clearTimeout(p.timer);
      if (msg.error) {
        p.reject(new Error(msg.error.message || "unknown error"));
      } else {
        p.resolve(msg.result);
      }
    }
    return;
  }

  // --- init handshake ---
  if (method === "init") {
    const initResult = {
      name,
      tools: [..._tools],
      commands: [..._commands],
      events: subscribedEvents,
    };
    if (_authProviders.length > 0) {
      initResult.auth_providers = [..._authProviders];
    }
    if (_providers.length > 0) {
      initResult.providers = [..._providers];
    }
    _initialized = true;
    writeMessage(makeResponse(id, initResult), out);
    return;
  }

  // --- tool_call ---
  if (method === "tool_call") {
    handleToolCall(id, params, ctx, out);
    return;
  }

  // --- hooks ---
  if (method.startsWith("hook/")) {
    handleHook(method, id, params, ctx, out);
    return;
  }

  // --- auth/* ---
  if (method.startsWith("auth/")) {
    handleAuth(method, id, params, authCtx, out);
    return;
  }

  // --- provider/* ---
  if (method.startsWith("provider/")) {
    handleProvider(method, id, params, ctx, out);
    return;
  }

  // --- events (notifications, or requests when the host wants an ack) ---
  if (method.startsWith("event/")) {
    const eventName = method.slice(6);
    const handler = _eventHandlers.get(eventName);
    // Ack once the handler has settled — that is what an awaiting host is
    // waiting for. Sent even when the handler rejects, so a broken handler
    // costs the host nothing but a lost result. A missing handler acks at once
    // rather than leaving the host hanging.
    const ack = () => {
      if (id != null) {
        writeMessage(makeResponse(id, { ok: true }), out);
      }
    };
    if (!handler) {
      ack();
      return;
    }
    Promise.resolve()
      .then(() => handler(params, ctx))
      .catch((err) => {
        if (!String(err).includes("shutdown")) {
          process.stderr.write(`Event handler error (${eventName}): ${err}\n`);
        }
      })
      .finally(ack);
    return;
  }

  // Unknown method with id → error
  if (id != null) {
    writeMessage(makeError(id, -32601, `Method not found: ${method}`), out);
  }
}

async function handleToolCall(id, params, ctx, out) {
  const toolName = params.name || "";
  const handler = _toolHandlers.get(toolName);
  if (!handler) {
    writeMessage(makeError(id, -32601, `Unknown tool: ${toolName}`), out);
    return;
  }
  try {
    let result = await handler(params.params || {}, ctx);
    // Wrap plain string results
    if (typeof result === "string") {
      result = { content: [{ text: result }], is_error: false };
    }
    writeMessage(makeResponse(id, result), out);
  } catch (err) {
    if (err instanceof ToolError) {
      writeMessage(makeError(id, err.code, err.message), out);
    } else {
      writeMessage(makeError(id, -32000, String(err.message || err)), out);
    }
  }
}

async function handleHook(method, id, params, ctx, out) {
  if (method === "hook/command") {
    const cmdName = params.name || "";
    const handler = _commandHandlers.get(cmdName);
    if (!handler) {
      writeMessage(makeResponse(id, null), out);
      return;
    }
    try {
      const result = await handler(params.args || [], ctx);
      writeMessage(makeResponse(id, result), out);
    } catch (err) {
      writeMessage(makeError(id, -32000, String(err.message || err)), out);
    }
    return;
  }

  const handler = _hookHandlers.get(method);
  if (!handler) {
    writeMessage(makeResponse(id, null), out);
    return;
  }
  try {
    const result = await handler(params, ctx);
    writeMessage(makeResponse(id, result), out);
  } catch (err) {
    writeMessage(makeError(id, -32000, String(err.message || err)), out);
  }
}

async function handleAuth(method, id, params, authCtx, out) {
  const providerId = params.provider_id || "";
  try {
    if (method === "auth/login") {
      const handler = _authLoginHandlers.get(providerId);
      if (!handler) {
        writeMessage(makeError(id, -32601, `No login handler for provider: ${providerId}`), out);
        return;
      }
      let result = await handler(params, authCtx);
      if (result && result.access && !result.credentials) {
        result = { credentials: result };
      }
      writeMessage(makeResponse(id, result), out);
    } else if (method === "auth/refresh") {
      const handler = _authRefreshHandlers.get(providerId);
      if (!handler) {
        writeMessage(makeError(id, -32601, `No refresh handler for provider: ${providerId}`), out);
        return;
      }
      let result = await handler(params, authCtx);
      if (result && result.access && !result.credentials) {
        result = { credentials: result };
      }
      writeMessage(makeResponse(id, result), out);
    } else if (method === "auth/api_key") {
      const handler = _authApiKeyHandlers.get(providerId);
      if (!handler) {
        const creds = params.credentials || {};
        writeMessage(makeResponse(id, { api_key: creds.access || "" }), out);
        return;
      }
      let result = await handler(params, authCtx);
      if (typeof result === "string") result = { api_key: result };
      writeMessage(makeResponse(id, result), out);
    } else if (method === "auth/list_models") {
      const handler = _authListModelsHandlers.get(providerId);
      if (!handler) {
        writeMessage(makeResponse(id, { models: null }), out);
        return;
      }
      let result = await handler(params, authCtx);
      if (Array.isArray(result)) result = { models: result };
      writeMessage(makeResponse(id, result), out);
    } else if (method === "auth/modify_models") {
      const handler = _authModifyModelsHandlers.get(providerId);
      if (!handler) {
        writeMessage(makeResponse(id, { models: null }), out);
        return;
      }
      let result = await handler(params, authCtx);
      if (Array.isArray(result)) result = { models: result };
      writeMessage(makeResponse(id, result), out);
    } else {
      writeMessage(makeError(id, -32601, `Unknown auth method: ${method}`), out);
    }
  } catch (err) {
    writeMessage(makeError(id, -32000, String(err.message || err)), out);
  }
}

// ---------------------------------------------------------------------------
// Hosted-provider dispatch
// ---------------------------------------------------------------------------

/** Emit a provider.stream.event notification carrying one event. */
function sendProviderEvent(streamId, event, out) {
  writeMessage(
    { jsonrpc: "2.0", method: "provider.stream.event", params: { stream_id: streamId, event } },
    out
  );
}

/** Build a terminal error event with the given reason/message. */
function terminalError(reason, message) {
  return {
    type: "error",
    reason,
    error: { role: "assistant", stopReason: reason, errorMessage: message },
  };
}

/**
 * Drive a providerStream handler and forward its events as notifications.
 * The handler may return a sync/async iterable of event dicts. A terminal
 * `done`/`error` event is synthesised if the iterable ends without one.
 */
async function runProviderStream(providerId, streamId, params, ctx, out) {
  const cancel = _streamCancels.get(streamId);
  const handler = _providerStreamHandlers.get(providerId);
  if (!handler) {
    sendProviderEvent(streamId, terminalError("error", `no providerStream handler for '${providerId}'`), out);
    _streamCancels.delete(streamId);
    return;
  }
  let terminalSeen = false;
  try {
    const iter = await handler(params, ctx);
    if (iter != null) {
      for await (const event of iter) {
        if (!event || typeof event !== "object") continue;
        const t = event.type || "";
        if (t === "done" || t === "error") terminalSeen = true;
        sendProviderEvent(streamId, event, out);
        if (cancel && cancel.cancelled && !terminalSeen) break;
      }
    }
  } catch (err) {
    sendProviderEvent(streamId, terminalError("error", String(err.message || err)), out);
    terminalSeen = true;
  } finally {
    if (!terminalSeen) {
      const aborted = cancel && cancel.cancelled;
      sendProviderEvent(
        streamId,
        terminalError(
          aborted ? "aborted" : "error",
          aborted ? "stream cancelled" : "provider generator exited without terminal event"
        ),
        out
      );
    }
    _streamCancels.delete(streamId);
  }
}

async function handleProvider(method, id, params, ctx, out) {
  try {
    if (method === "provider/stream/start") {
      const streamId = params.stream_id || "";
      if (!streamId) {
        writeMessage(makeError(id, -32602, "missing stream_id"), out);
        return;
      }
      // Pre-register cancel flag so a fast cancel can't be dropped, then ack
      // immediately — events flow asynchronously via notifications.
      _streamCancels.set(streamId, { cancelled: false });
      writeMessage(makeResponse(id, {}), out);
      runProviderStream(params.provider_id || "", streamId, params, ctx, out).catch((err) => {
        process.stderr.write(`provider stream error: ${err}\n`);
      });
      return;
    }

    if (method === "provider/stream/cancel") {
      const c = _streamCancels.get(params.stream_id || "");
      if (c) c.cancelled = true;
      writeMessage(makeResponse(id, { ok: true }), out);
      return;
    }

    if (method === "provider/listModels") {
      const providerId = params.provider_id || "";
      const handler = _providerListModelsHandlers.get(providerId);
      if (!handler) {
        writeMessage(makeError(id, -32601, `no providerListModels handler for '${providerId}'`), out);
        return;
      }
      let result = await handler(params, ctx);
      if (Array.isArray(result)) result = { model_ids: result };
      else if (!result || typeof result !== "object") result = { model_ids: [] };
      writeMessage(makeResponse(id, result), out);
      return;
    }

    if (method === "provider/resolveCustomId") {
      const handler = _providerResolveCustomIdHandlers.get(params.provider_id || "");
      const result = handler ? await handler(params, ctx) : null;
      writeMessage(makeResponse(id, result ?? null), out);
      return;
    }

    writeMessage(makeError(id, -32601, `Unknown provider method: ${method}`), out);
  } catch (err) {
    writeMessage(makeError(id, -32000, String(err.message || err)), out);
  }
}

// ---------------------------------------------------------------------------
// Exports
// ---------------------------------------------------------------------------

module.exports = {
  tool,
  command,
  on,
  authProvider,
  authRefresh,
  authApiKey,
  authListModels,
  authModifyModels,
  registerProvider,
  providerStream,
  providerListModels,
  providerResolveCustomId,
  isCancelled,
  run,
  ToolError,
  Context,
  AuthContext,
};
