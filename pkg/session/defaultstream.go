// Wire fir's provider-registry-backed default StreamFn into pkg/agent.
//
// Before Phase 3.5, pkg/agent shipped a hardcoded fallback closure
// that called ai.StreamSimple(ctx, ai.DefaultRegistry, ...). That
// coupling is gone — pkg/agent now consults a pluggable factory in
// agent.DefaultStreamFn whenever the per-call StreamFn is nil. Fir
// installs the factory here so existing call sites keep working
// without changing their AgentOptions.
//
// See docs/design/ai-agent-extraction.md (Phase 3.5).

package session

import (
	"context"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/core"
)

func init() {
	agent.DefaultStreamFn = func(ctx context.Context) agent.StreamFn {
		return func(model *core.Model, prompt core.Context, opts *core.SimpleStreamOptions) *core.AssistantMessageEventStream {
			return ai.StreamSimple(ctx, ai.DefaultRegistry, model, prompt, opts)
		}
	}
}
