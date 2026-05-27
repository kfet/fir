// Package agent is a model-agnostic coding-agent runtime in Go.
//
// It turns a stream of LLM events into a stream of tool calls, with
// retries, steering, follow-ups, abort, and thinking-budget clamping
// already correct. Callers supply a model, a tool set, a prompt, and a
// streaming function; the agent emits structured events the host can
// render however it wants.
//
// Package agent is intentionally small. It does not depend on fir's
// session store, MCP runtime, TUI, extension host, provider catalog,
// or any HTTP client. The only fir-side dependency is [pkg/ai/core],
// which holds the portable AI primitives (Message, Tool, Model, Usage,
// Context, streaming event types). See
// docs/design/ai-agent-extraction.md for the extraction roadmap.
//
// # Headline API
//
// The simplest path is [Agent]:
//
//	a := agent.NewAgent(agent.AgentOptions{
//	    InitialState: &agent.AgentState{Model: model},
//	    StreamFn:     myStreamFn,        // any provider client
//	    Tools:        agent.ToolSetFrom(tools),
//	    ConvertToLLM: agent.DefaultConvertToLLM,
//	})
//	unsubscribe := a.Subscribe(func(ev agent.AgentEvent) {
//	    // render ev however you like
//	})
//	defer unsubscribe()
//	_ = a.Prompt("Refactor handler.go to use the new config struct.")
//	a.WaitForIdle()
//
// For non-streaming one-shot use cases, [Agent.SimplePrompt] returns
// the assistant's final text without spinning up the full loop.
//
// # StreamFn
//
// The agent does not know how to talk to a specific provider. Callers
// pass a [StreamFn] — any function that, given a model, prompt context,
// and options, returns an [core.AssistantMessageEventStream]. When the
// per-call StreamFn is nil, the agent falls back to [DefaultStreamFn],
// a package-level factory hook that hosts (such as fir) install to
// wire up their own provider registry. Setting neither yields a clear
// "no stream function configured" error.
//
// # Tools
//
// Tools are [AgentTool] values; collect them in a [ToolSet]. Each tool
// is a name, a JSON schema, an executor, and optional display hints.
// The standard coding toolbox lives in pkg/agent/tools.
//
// # Thinking levels
//
// The canonical ladder is max → xhigh → high → medium → low → minimal
// → off. [ClampThinkingLevel] walks a requested level down to whatever
// the model actually supports. Knowledge about which specific model
// IDs support which levels lives outside this package — the host
// computes the available set and passes it in.
//
// # Concurrency
//
// [Agent] is safe for concurrent use by its event subscribers and by
// callers issuing Prompt/Steer/FollowUp/Abort. Internal state is
// guarded by a single mutex; subscribers are dispatched synchronously
// from the agent's goroutine, so subscribers must not block.
package agent
