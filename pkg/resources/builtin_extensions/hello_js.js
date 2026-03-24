#!/usr/bin/env node
// ---
// demo: true
// events: session_start, agent_end
// commands: hello-js: Say hello from JavaScript
// ---
/**
 * hello.js — Simple JS extension mirroring hello.py.
 * Demonstrates the Node.js/Bun fir extension SDK.
 */

const fir = require("fir_ext");

fir.on("session_start", (params, ctx) => {
  process.stderr.write("hello.js: session_start fired\n");
  ctx.setStatus("hello-js ready");
});

fir.on("agent_end", (params, ctx) => {
  process.stderr.write("hello.js: agent_end fired\n");
});

fir.command("hello-js", "Say hello from JavaScript", (args, ctx) => {
  const name = args.length ? args.join(" ") : "world";
  return { message: `Hello, ${name}! (from JS)` };
});

fir.tool({
  name: "js_word_count",
  description: "Count words in a string (JS implementation)",
  parameters: {
    type: "object",
    properties: { text: { type: "string", description: "Text to count" } },
    required: ["text"],
  },
  displayHint: {
    title_args: [{ name: "text", style: "accent" }],
  },
}, async (params, ctx) => {
  const words = (params.text || "").split(/\s+/).filter(Boolean);
  await ctx.notify(`js_word_count: ${words.length} words`);
  return { count: words.length, words };
});

fir.run({ name: "hello-js" });
