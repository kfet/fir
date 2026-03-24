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

  // --- events ---
  if (method.startsWith("event/")) {
    const eventName = method.slice(6);
    const handler = _eventHandlers.get(eventName);
    if (handler) {
      Promise.resolve()
        .then(() => handler(params, ctx))
        .catch((err) => {
          if (!String(err).includes("shutdown")) {
            process.stderr.write(`Event handler error (${eventName}): ${err}\n`);
          }
        });
    }
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
  run,
  ToolError,
  Context,
  AuthContext,
};
