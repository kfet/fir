package agent_test

// This file is the second internal consumer of pkg/agent (see
// docs/design/ai-agent-extraction.md Phase 4). The examples drive the
// public API with a fake StreamFn — no fir-side imports, no provider
// HTTP, no session store. They serve three purposes:
//
//  1. Compile-checked usage documentation (visible on pkg.go.dev once
//     extracted as kfet/agent).
//  2. Validation that the package boundary really is self-sufficient.
//  3. Smoke-test ergonomics on the API before it ossifies at v0.1.0.
//
// If an example becomes painful to write, fix the API, not the example.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai/core"
)

// fakeStreamFn returns a StreamFn that replays the given assistant
// messages, one per call, looping on the last response after the
// canned list is exhausted.
func fakeStreamFn(responses ...*core.AssistantMessage) agent.StreamFn {
	var mu sync.Mutex
	idx := 0
	return func(_ *core.Model, _ core.Context, _ *core.SimpleStreamOptions) *core.AssistantMessageEventStream {
		mu.Lock()
		msg := responses[idx]
		if idx < len(responses)-1 {
			idx++
		}
		mu.Unlock()
		s := core.NewAssistantMessageEventStream()
		go func() {
			s.Push(core.AssistantMessageEvent{Type: core.EventStart, Partial: msg})
			s.Push(core.AssistantMessageEvent{Type: core.EventDone, Reason: msg.StopReason, Message: msg})
			s.End(nil)
		}()
		return s
	}
}

// exampleModel returns a Model wired up for Anthropic Messages so the
// examples compile against a realistic shape. No HTTP is involved —
// the StreamFn is faked.
func exampleModel() *core.Model {
	return &core.Model{
		ID:            "example-model",
		Name:          "Example Model",
		Api:           core.ApiAnthropicMessages,
		Provider:      core.ProviderAnthropic,
		ContextWindow: 200000,
		MaxTokens:     4096,
	}
}

func textResponse(text string) *core.AssistantMessage {
	return &core.AssistantMessage{
		Role:       core.RoleAssistant,
		Content:    []core.AssistantContent{core.NewTextContent(text)},
		Api:        core.ApiAnthropicMessages,
		Provider:   core.ProviderAnthropic,
		Model:      "example-model",
		StopReason: core.StopReasonStop,
		Timestamp:  time.Now().UnixMilli(),
	}
}

// Example demonstrates the headline path: create an Agent, subscribe
// to its events, send a prompt, wait for it to finish.
func Example() {
	a := agent.NewAgent(agent.AgentOptions{
		Model:    exampleModel(),
		StreamFn: fakeStreamFn(textResponse("Hello, world.")),
	})

	var got string
	var mu sync.Mutex
	unsubscribe := a.Subscribe(func(ev agent.AgentEvent) {
		if ev.Type != agent.EventMessageEnd || ev.Message == nil {
			return
		}
		if text := ev.Message.Text(); text != "" {
			mu.Lock()
			got = text
			mu.Unlock()
		}
	})
	defer unsubscribe()

	if err := a.Prompt("hi"); err != nil {
		fmt.Println("prompt error:", err)
		return
	}
	a.WaitForIdle()

	mu.Lock()
	defer mu.Unlock()
	fmt.Println(got)
	// Output: Hello, world.
}

// ExampleAgent_SimplePrompt shows the non-streaming one-shot variant
// that just returns the final assistant text. Useful for batch jobs
// or for embedding the agent inside a larger non-interactive flow.
func ExampleAgent_SimplePrompt() {
	a := agent.NewAgent(agent.AgentOptions{
		Model:    exampleModel(),
		StreamFn: fakeStreamFn(textResponse("42")),
	})

	out, err := a.SimplePrompt(context.Background(), []agent.AgentMessage{
		{Message: core.NewUserMsg("What is the answer?", time.Now().UnixMilli())},
	}, nil)
	if err != nil {
		fmt.Println("simple prompt error:", err)
		return
	}
	fmt.Println(out)
	// Output: 42
}

// ExampleDefaultStreamFn shows the package-level injection hook hosts
// use to wire a provider-backed default. Once installed, callers can
// omit StreamFn in AgentOptions and the agent falls back to the host's
// stream factory.
func ExampleDefaultStreamFn() {
	// Pretend this runs at host init: install a default that closes
	// over a real provider client. Here we just use the fake.
	prev := agent.DefaultStreamFn
	agent.DefaultStreamFn = func(_ context.Context) agent.StreamFn {
		return fakeStreamFn(textResponse("from default"))
	}
	defer func() { agent.DefaultStreamFn = prev }()

	a := agent.NewAgent(agent.AgentOptions{
		Model: exampleModel(),
		// No StreamFn — falls through to DefaultStreamFn.
		// No ConvertToLLM — defaults to DefaultConvertToLLM.
	})

	out, _ := a.SimplePrompt(context.Background(), []agent.AgentMessage{
		{Message: core.NewUserMsg("hi", time.Now().UnixMilli())},
	}, nil)
	fmt.Println(out)
	// Output: from default
}

// ExampleClampThinkingLevel shows how a host clamps a requested
// reasoning level to whatever the underlying model supports. The
// canonical ladder is max → xhigh → high → medium → low → minimal → off.
func ExampleClampThinkingLevel() {
	// A model that supports up to "high".
	available := []agent.ThinkingLevel{
		agent.ThinkingOff, agent.ThinkingLow, agent.ThinkingMedium, agent.ThinkingHigh,
	}
	fmt.Println(agent.ClampThinkingLevel(agent.ThinkingMax, available))
	fmt.Println(agent.ClampThinkingLevel(agent.ThinkingMedium, available))
	fmt.Println(agent.ClampThinkingLevel("", available))
	// Output:
	// high
	// medium
	//
}
