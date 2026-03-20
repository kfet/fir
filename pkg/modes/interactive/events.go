// events.go — agent event subscription, chat message management, footer data,
// and plan update handling.
package interactive

import (
	"fmt"
	"os"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/modes/interactive/components"
	itheme "github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
	"github.com/kfet/fir/pkg/tui"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// ============================================================================
// Chat message management
// ============================================================================

// AddUserMessage adds a user message to the display preemptively (before the
// agent loop fires EventMessageStart), so the UI updates instantly on submit.
// It increments pendingPreemptiveUserMsgs so onMessageStart can skip the
// corresponding event and avoid a duplicate render.
func (m *InteractiveMode) AddUserMessage(text string) {
	m.pendingPreemptiveUserMsgs.Add(1)
	m.addUserMessageToChat(text)
	m.ui.RequestRender(false)
}

// addUserMessageToChat adds a user message, rendering skill blocks specially.
func (m *InteractiveMode) addUserMessageToChat(text string) {
	skillBlock := session.ParseSkillBlock(text)
	if skillBlock != nil {
		m.messageContainer.AddChild(tuicomp.NewSpacer(1))
		skillComp := components.NewSkillInvocationMessageComponent(skillBlock, &m.markdownTheme)
		skillComp.SetExpanded(m.toolOutputExpanded)
		m.messageContainer.AddChild(skillComp)
		if skillBlock.UserMessage != "" {
			m.messageContainer.AddChild(components.NewUserMessageComponent(skillBlock.UserMessage, nil))
		}
	} else {
		m.messageContainer.AddChild(components.NewUserMessageComponent(text, nil))
	}
}

// userMsgText extracts the plain-text portion of a user message's Content,
// which may be a bare string or a []any slice of content blocks (text+images).
// Returns "" if no text block is present.
func userMsgText(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	if blocks, ok := content.([]any); ok {
		for _, b := range blocks {
			if bm, ok := b.(map[string]any); ok && bm["type"] == "text" {
				if s, ok := bm["text"].(string); ok {
					return s
				}
			}
		}
	}
	return ""
}

// AddAssistantMessage adds an assistant message to the display.
func (m *InteractiveMode) AddAssistantMessage(msg *ai.AssistantMessage) {
	comp := components.NewAssistantMessageComponent(msg, m.hideThinking, nil)
	m.messageContainer.AddChild(comp)
	m.ui.RequestRender(false)
}

func (m *InteractiveMode) rebuildChatFromMessages() {
	m.messageContainer.Clear()
	m.activityContainer.Clear()
	m.commandStatusContainer.Clear()

	state := m.session.State()
	for _, agentMsg := range state.Messages {
		if u := agentMsg.Message.AsUser(); u != nil {
			if txt := userMsgText(u.Content); txt != "" {
				m.addUserMessageToChat(txt)
			}
		} else if a := agentMsg.Message.AsAssistant(); a != nil {
			comp := components.NewAssistantMessageComponent(a, m.hideThinking, nil)
			m.messageContainer.AddChild(comp)
		}
	}
	m.ui.RequestRender(true)
}

// ============================================================================
// Agent event subscription
// ============================================================================

func (m *InteractiveMode) subscribeToAgent() {
	m.pendingTools = make(map[string]*components.ToolExecutionComponent)
	m.unsubscribe = m.session.Subscribe(func(event session.AgentSessionEvent) {
		m.handleEvent(event)
	})
}

func (m *InteractiveMode) handleEvent(event session.AgentSessionEvent) {
	// Invalidate footer on every event (token counts, etc. may have changed)
	if m.footerComponent != nil {
		m.footerComponent.Invalidate()
	}

	if event.AgentEvent == nil {
		// Handle session-level events
		switch event.Type {
		case "auto_compaction_start":
			t := itheme.GetTheme()
			initialLabel := m.compactionLoaderLabel(event.CompactionInfo, "(auto)")
			loader := tuicomp.NewLoader(
				m.ui.AsRenderRequester(),
				func(spinner string) string { return t.Fg("accent", spinner) },
				func(text string) string { return t.Fg("muted", text) },
				initialLabel,
			)
			m.activityContainer.Clear()
			m.activityContainer.AddChild(loader)
			m.loadingAnimation = loader
			m.ui.RequestRender(false)

			// Provide a progress callback so the loader text updates during streaming.
			var writtenChars int
			m.session.SetAutoCompactionProgress(func(phase, delta string) {
				writtenChars += len(delta)
				tokensWritten := writtenChars / 4
				label := m.compactionLoaderLabel(event.CompactionInfo,
					fmt.Sprintf("%s... %d tokens written (auto)", phase, tokensWritten))
				loader.SetMessage(label)
			})

		case "auto_compaction_end":
			if m.loadingAnimation != nil {
				m.loadingAnimation.Stop()
				m.loadingAnimation = nil
			}
			if event.ErrorMessage != "" {
				m.showWarning("Compaction failed: " + event.ErrorMessage)
			}
			m.rebuildChatFromMessages()
			if event.ErrorMessage == "" && event.CompactionResult != nil && !m.session.HasPendingWork() {
				m.showStatus(fmt.Sprintf("Auto-compacted: %d tokens", event.CompactionResult.TokensBefore))
			}
			m.ui.RequestRender(false)

		case "plan_update":
			m.onPlanUpdate(event.PlanTitle, event.PlanEntries, event.PlanMetadata)
		}
		return
	}

	ae := event.AgentEvent
	switch ae.Type {
	case agent.EventAgentStart:
		m.onAgentStart()
	case agent.EventMessageStart:
		m.onMessageStart(ae)
	case agent.EventMessageUpdate:
		m.onMessageUpdate(ae)
	case agent.EventMessageEnd:
		m.onMessageEnd(ae)
	case agent.EventToolExecutionStart:
		m.onToolExecStart(ae)
	case agent.EventToolExecutionUpdate:
		m.onToolExecUpdate(ae)
	case agent.EventToolExecutionEnd:
		m.onToolExecEnd(ae)
	case agent.EventAgentEnd:
		m.onAgentEnd()
	}
}

func (m *InteractiveMode) onAgentStart() {
	if m.loadingAnimation != nil {
		m.loadingAnimation.Stop()
	}
	m.activityContainer.Clear()
	t := itheme.GetTheme()
	loader := tuicomp.NewLoader(
		m.ui.AsRenderRequester(),
		func(spinner string) string { return t.Fg("accent", spinner) },
		func(text string) string { return t.Fg("muted", text) },
		"Working...",
	)
	m.loadingAnimation = loader
	m.activityContainer.AddChild(loader)
	m.streamingComponent = nil
	m.ui.RequestRender(false)
}

func (m *InteractiveMode) onMessageStart(ae *agent.AgentEvent) {
	if ae.Message == nil {
		return
	}

	if u := ae.Message.AsUser(); u != nil {
		// If this message was already shown preemptively (via AddUserMessage),
		// skip it to avoid a duplicate — regardless of whether the text was
		// template-expanded by session.Prompt before reaching the agent.
		if m.pendingPreemptiveUserMsgs.Add(-1) >= 0 {
			return
		}
		// Counter went negative: no preemptive message was pending (e.g. the
		// message came from an extension). Restore balance and render it.
		m.pendingPreemptiveUserMsgs.Add(1)
		if txt := userMsgText(u.Content); txt != "" {
			m.addUserMessageToChat(txt)
		}
		m.ui.RequestRender(false)
		return
	}

	if msg := ae.Message.AsAssistant(); msg != nil {
		m.streamingComponent = components.NewAssistantMessageComponent(msg, m.hideThinking, nil)
		m.messageContainer.AddChild(m.streamingComponent)
		m.ui.RequestRender(false)
	}
}

func (m *InteractiveMode) onMessageUpdate(ae *agent.AgentEvent) {
	if m.streamingComponent == nil || ae.Message == nil {
		return
	}
	if msg := ae.Message.AsAssistant(); msg != nil {
		m.streamingComponent.UpdateContent(msg)
		m.ui.RequestRender(false)
	}
}

func (m *InteractiveMode) onMessageEnd(ae *agent.AgentEvent) {
	if m.streamingComponent == nil || ae.Message == nil {
		return
	}
	if msg := ae.Message.AsAssistant(); msg != nil {
		m.streamingComponent.UpdateContent(msg)
		m.streamingComponent = nil
		m.ui.RequestRender(false)
	}
}

func (m *InteractiveMode) onToolExecStart(ae *agent.AgentEvent) {
	args := make(map[string]any)
	if ae.Args != nil {
		if argMap, ok := ae.Args.(map[string]any); ok {
			args = argMap
		}
	}
	comp := components.NewToolExecutionComponent(ae.ToolName, args, nil, ae.DisplayHint)
	if m.toolOutputExpanded {
		comp.SetExpanded(true)
	}
	m.pendingTools[ae.ToolCallID] = comp
	m.messageContainer.AddChild(comp)
	m.ui.RequestRender(false)
}

func (m *InteractiveMode) onToolExecUpdate(ae *agent.AgentEvent) {
	comp, ok := m.pendingTools[ae.ToolCallID]
	if !ok {
		return
	}
	if ae.Args != nil {
		if args, ok := ae.Args.(map[string]any); ok {
			comp.UpdateArgs(args)
		}
	}
	m.ui.RequestRender(false)
}

func toolResultDataFromAgent(result *agent.AgentToolResult, isError bool) *components.ToolResultData {
	rd := &components.ToolResultData{
		IsError: isError,
	}
	if result.Details != nil {
		if detailsMap, ok := result.Details.(map[string]any); ok {
			rd.Details = detailsMap
		}
	}
	if rd.Details == nil {
		rd.Details = make(map[string]any)
	}
	for _, c := range result.Content {
		rd.Content = append(rd.Content, components.ToolContentBlock{
			Type:     c.Type,
			Text:     c.Text,
			Data:     c.Data,
			MimeType: c.MimeType,
		})
	}
	return rd
}

func (m *InteractiveMode) onToolExecEnd(ae *agent.AgentEvent) {
	comp, ok := m.pendingTools[ae.ToolCallID]
	if !ok {
		return
	}
	delete(m.pendingTools, ae.ToolCallID)

	if ae.Result != nil {
		var resultData *components.ToolResultData
		switch result := ae.Result.(type) {
		case *agent.AgentToolResult:
			resultData = toolResultDataFromAgent(result, ae.IsError)
		case agent.AgentToolResult:
			resultData = toolResultDataFromAgent(&result, ae.IsError)
		}
		if resultData != nil {
			comp.UpdateResult(resultData, m.toolOutputExpanded)
		}
	}
	m.ui.RequestRender(false)
}

func (m *InteractiveMode) onAgentEnd() {
	if m.loadingAnimation != nil {
		m.loadingAnimation.Stop()
		m.loadingAnimation = nil
	}
	m.activityContainer.Clear()
	m.streamingComponent = nil
	m.pendingTools = make(map[string]*components.ToolExecutionComponent)
	m.ui.RequestRender(false)
}

// ============================================================================
// Plan update handler
// ============================================================================

func (m *InteractiveMode) onPlanUpdate(title string, entries []agent.PlanEntry, metadata map[string]string) {
	if len(entries) == 0 {
		m.planComponent = nil
		if m.planInContainer {
			m.planContainer.Clear()
			m.planInContainer = false
		}
	} else {
		if m.planComponent != nil {
			m.planComponent.SetEntries(title, entries, metadata)
		} else {
			m.planComponent = components.NewPlanComponent(title, entries, metadata)
			if !m.planHidden {
				m.planContainer.AddChild(m.planComponent)
				m.planInContainer = true
			}
		}
	}
	m.ui.RequestRender(false)
}

// ============================================================================
// Footer data
// ============================================================================

func (m *InteractiveMode) getFooterData() components.FooterData {
	pwd, _ := os.Getwd()

	data := components.FooterData{
		Pwd:             pwd,
		AutoCompactMode: m.autoCompactMode,
	}

	if m.session == nil {
		return data
	}

	if model := m.session.Model(); model != nil {
		data.ModelID = model.ID
		data.ModelProvider = model.Provider
		data.ModelReasoning = model.Reasoning
		data.ContextWindow = model.ContextWindow
	}

	data.ThinkingLevel = m.session.ThinkingLevel()

	stats := m.session.GetSessionStats()
	data.TotalInput = stats.Tokens.Input
	data.TotalOutput = stats.Tokens.Output
	data.TotalCacheRead = stats.Tokens.CacheRead
	data.TotalCacheWrite = stats.Tokens.CacheWrite
	data.TotalCost = stats.Cost

	if cu := m.session.GetContextUsage(); cu != nil {
		data.ContextPercent = cu.Percent
		data.ContextTokens = cu.Tokens
	}

	if m.footerDataProvider != nil {
		data.GitBranch = m.footerDataProvider.GetGitBranch()
		data.ExtensionStatuses = m.footerDataProvider.GetExtensionStatuses()
		data.MultipleProviders = m.footerDataProvider.GetAvailableProviderCount() > 1
	}

	data.SessionName = m.session.SessionManager.GetSessionName()
	data.QueuedMessages = m.session.Agent.FollowUpQueueLen()

	if entries := m.session.PlanEntries(); len(entries) > 0 {
		data.PlanTotal = len(entries)
		data.PlanTitle = m.session.PlanTitle()
		for _, e := range entries {
			switch e.Status {
			case agent.PlanEntryStatusCompleted:
				data.PlanCompleted++
			case agent.PlanEntryStatusInProgress:
				if data.PlanCurrentStep == "" {
					data.PlanCurrentStep = e.Content
				}
			}
		}
		if keys := m.keybindings.GetKeys(tui.ActionTogglePlan); len(keys) > 0 {
			data.PlanKeyHint = keys[0]
		}
	}

	return data
}

// ============================================================================
// Reexec sidecar restore
// ============================================================================

// preloadReexecSidecar reads and deletes the reexec sidecar file during
// Init() — before EmitSessionStart fires — and caches its contents on the
// mode.  This lets the caller pass extension data to EmitSessionStart so
// extensions receive their saved state in the session_start event params.
// The actual application of queued messages / editor text happens later in
// restoreReexecSidecar (called from Run).
func (m *InteractiveMode) preloadReexecSidecar() {
	if os.Getenv("FIR_REEXEC_CONTINUE") != "1" {
		return
	}

	sessionFile := m.session.SessionManager.GetSessionFile()
	if sessionFile == "" {
		return
	}

	sidecar, err := store.ReadReexecSidecar(sessionFile)
	if err != nil {
		firlog.Debug("preloadReexecSidecar: read error", "error", err)
		return
	}
	if sidecar == nil {
		return
	}

	// Cache the whole sidecar; restoreReexecSidecar will apply the rest.
	m.reexecSidecar = sidecar
}

// ReexecExtData returns per-extension session data that was saved before the
// last /reexec.  Non-nil only when this process was started by /reexec and
// the sidecar contained extension data.  Used by app.go to pass the data to
// EmitSessionStart before Run() is called.
func (m *InteractiveMode) ReexecExtData() map[string]map[string]string {
	if m.reexecSidecar == nil {
		return nil
	}
	return m.reexecSidecar.ExtensionData
}

// restoreReexecSidecar applies the queued messages and pending editor text
// from a sidecar cached by preloadReexecSidecar.
func (m *InteractiveMode) restoreReexecSidecar() {
	if os.Getenv("FIR_REEXEC_CONTINUE") != "1" {
		return
	}
	os.Unsetenv("FIR_REEXEC_CONTINUE")

	sidecar := m.reexecSidecar
	if sidecar == nil {
		return
	}
	m.reexecSidecar = nil // release

	restored := 0
	for _, msg := range sidecar.QueueMessages {
		m.session.Agent.FollowUp(agent.NewAgentMessage(ai.NewUserMsg(msg, time.Now().UnixMilli())))
		restored++
	}

	if sidecar.PendingInput != "" {
		m.editor.SetText(sidecar.PendingInput)
	}

	if restored > 0 {
		m.showStatus(fmt.Sprintf("Reexec: restored %d queued message(s)", restored))
	}
}
