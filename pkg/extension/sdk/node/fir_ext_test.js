/**
 * Tests for the Node SDK provider surface and the pi-mono compat mapping.
 *
 * Drives fir_ext.run() with in-memory streams (mirroring the Python SDK's
 * fake-stdio tests) and asserts the init handshake payload plus provider/*
 * RPC dispatch. Run with: node fir_ext_test.js
 */

"use strict";

const assert = require("assert");
const { PassThrough } = require("stream");

const fir = require("./fir_ext");
const piCompat = require("./pi_compat");

// --- Register providers/handlers (must happen before run()/init) ----------

// 1. A pi-mono extension registering a provider through the compat shim.
const piApi = piCompat.createExtensionAPI();
piApi.registerProvider("llama-cpp", {
  name: "llama.cpp",
  api: "openai-completions",
  apiKey: "LLAMA_API_KEY",
  baseUrl: "http://localhost:8080/v1",
  models: [
    {
      id: "qwen3-coder",
      name: "Qwen3 Coder",
      reasoning: false,
      input: ["text", "image"],
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      contextWindow: 262144,
      maxTokens: 8192,
    },
  ],
});

// 2. A live-list provider exercised via provider/listModels.
fir.registerProvider({ id: "live-prov", api: "openai-completions", supportsLiveList: true });
fir.providerListModels("live-prov", (params) => {
  assert.strictEqual(params.provider_id, "live-prov");
  return ["model-a", "model-b"];
});

// 3. A synthetic-API provider exercised via provider/stream/start.
fir.registerProvider({ id: "stream-prov" });
fir.providerStream("stream-prov", function* () {
  yield { type: "text_start", contentIndex: 0 };
  yield { type: "text_delta", contentIndex: 0, delta: "hi" };
  yield { type: "done", reason: "stop" };
});

// --- Drive run() over in-memory streams ------------------------------------

const input = new PassThrough();
const out = new PassThrough();
const lines = [];
out.on("data", (chunk) => {
  for (const l of chunk.toString().split("\n")) {
    if (l.trim()) lines.push(JSON.parse(l));
  }
});

fir.run({ name: "test-ext", input, output: out });

const send = (msg) => input.write(JSON.stringify(msg) + "\n");
const byId = (id) => lines.find((m) => m.id === id);
const notifs = (method) => lines.filter((m) => m.method === method);
const wait = (ms) => new Promise((r) => setTimeout(r, ms));

async function main() {
  // init handshake
  send({ jsonrpc: "2.0", id: 1, method: "init", params: {} });
  await wait(20);
  const init = byId(1);
  assert.ok(init, "init response received");
  const provs = init.result.providers;
  assert.ok(Array.isArray(provs), "providers present in init payload");

  // pi-mono mapping → wire shape
  const llama = provs.find((p) => p.id === "llama-cpp");
  assert.ok(llama, "llama-cpp registered via compat shim");
  assert.strictEqual(llama.api, "openai-completions");
  assert.strictEqual(llama.display_name, "llama.cpp");
  assert.deepStrictEqual(llama.env_keys, { primary: "LLAMA_API_KEY" });
  assert.strictEqual(llama.models.length, 1);
  const m = llama.models[0];
  assert.strictEqual(m.id, "qwen3-coder");
  assert.strictEqual(m.base_url, "http://localhost:8080/v1");
  assert.strictEqual(m.context_window, 262144);
  assert.strictEqual(m.max_tokens, 8192);
  assert.deepStrictEqual(m.input, ["text", "image"]);
  assert.ok(!("reasoning" in m), "falsy reasoning omitted from wire");
  assert.ok(!("cost_input" in m), "zero cost omitted from wire");

  const live = provs.find((p) => p.id === "live-prov");
  assert.strictEqual(live.supports_live_list, true);

  // provider/listModels
  send({ jsonrpc: "2.0", id: 2, method: "provider/listModels", params: { provider_id: "live-prov" } });
  await wait(20);
  assert.deepStrictEqual(byId(2).result, { model_ids: ["model-a", "model-b"] });

  // unknown listModels provider → error
  send({ jsonrpc: "2.0", id: 3, method: "provider/listModels", params: { provider_id: "nope" } });
  await wait(20);
  assert.ok(byId(3).error, "missing lister yields an error");

  // provider/stream/start → ack {} then notifications ending in done
  send({
    jsonrpc: "2.0",
    id: 4,
    method: "provider/stream/start",
    params: { provider_id: "stream-prov", stream_id: "s1" },
  });
  await wait(30);
  assert.deepStrictEqual(byId(4).result, {}, "stream start acked immediately");
  const events = notifs("provider.stream.event").filter((n) => n.params.stream_id === "s1");
  assert.strictEqual(events.length, 3, "three stream events forwarded");
  assert.strictEqual(events[0].params.event.type, "text_start");
  assert.strictEqual(events[2].params.event.type, "done");

  // missing stream_id → error
  send({ jsonrpc: "2.0", id: 5, method: "provider/stream/start", params: { provider_id: "stream-prov" } });
  await wait(20);
  assert.ok(byId(5).error, "missing stream_id yields an error");

  // post-init registerProvider is ignored and warns on stderr
  let warned = "";
  const origWrite = process.stderr.write.bind(process.stderr);
  process.stderr.write = (s) => { warned += s; return true; };
  fir.registerProvider({ id: "too-late" });
  process.stderr.write = origWrite;
  assert.ok(/too-late.*after init/.test(warned), "post-init registration warns");

  input.end();
  console.log("ok - fir_ext provider surface + pi_compat mapping");
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
