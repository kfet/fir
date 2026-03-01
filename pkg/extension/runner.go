// Ported from: packages/coding-agent/src/core/extensions/runner.ts
// Upstream hash: 5c0ec26c
package extension

import (
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/core"
	firlog "github.com/kfet/fir/pkg/log"
)

// ============================================================================
// Extension — a loaded extension instance
// ============================================================================

// Extension holds the registrations from a single extension factory.
type Extension struct {
	Name     string
	handlers map[string][]Handler
	tools    map[string]*ToolDefinition
	commands map[string]*Command
	flags    map[string]*Flag
	shortcuts map[string]*ShortcutHandler
}

// ============================================================================
// Runner — manages all loaded extensions
// ============================================================================

// Runner manages loaded extensions and dispatches events.
type Runner struct {
	mu         sync.RWMutex
	extensions []*Extension
	eventBus   core.EventBus

	// Merged registrations across all extensions
	allTools     map[string]*ToolDefinition
	allCommands  map[string]*Command
	allFlags     map[string]*Flag
	allShortcuts map[string]*ShortcutHandler

	// Merged event handlers (event type -> handlers in registration order)
	allHandlers map[string][]Handler

	// Flag values (set from CLI or defaults)
	flagValues map[string]any

	// Action bindings — set by the mode that creates the runner
	actions *Actions

	// UI context — set by the mode
	uiContext UIContext

	// Status/widget keys set by extensions, tracked for cleanup on reload.
	statusKeys map[string]bool
	widgetKeys map[string]bool

	// Error listener
	onError func(ext string, event string, err error)
}

// Actions are callbacks provided by the mode to implement API methods
// that depend on session/agent state.
type Actions struct {
	GetModel           func() *ai.Model
	IsIdle             func() bool
	Abort              func()
	HasPendingMessages func() bool
	Shutdown           func()
	GetContextUsage    func() *core.ContextUsage
	GetSystemPrompt    func() string
	Cwd                func() string
	SessionManager     func() *core.SessionManager
	ModelRegistry      func() *core.ModelRegistry
	AgentSession       func() *core.AgentSession

	// Command context actions (may be nil in non-interactive modes)
	WaitForIdle func()
	NewSession  func() (bool, error)
	Fork        func(entryID string) (string, bool, error)
	Reload      func() error
}

// NewRunner creates a Runner from registered extension factories.
func NewRunner(eventBus core.EventBus) *Runner {
	r := &Runner{
		eventBus:     eventBus,
		allTools:     make(map[string]*ToolDefinition),
		allCommands:  make(map[string]*Command),
		allFlags:     make(map[string]*Flag),
		allShortcuts: make(map[string]*ShortcutHandler),
		allHandlers:  make(map[string][]Handler),
		flagValues:   make(map[string]any),
		statusKeys:   make(map[string]bool),
		widgetKeys:   make(map[string]bool),
	}
	return r
}

// SharedAPI returns an API that registers tools and handlers directly into
// the runner's merged maps. This is used by the extproc adapter to register
// tools from external process extensions without going through the normal
// extension load cycle.
func (r *Runner) SharedAPI() API {
	// Create a synthetic extension so extensionAPI methods work,
	// but override registration to go directly to merged maps.
	ext := &Extension{
		Name:      "_extproc",
		handlers:  make(map[string][]Handler),
		tools:     make(map[string]*ToolDefinition),
		commands:  make(map[string]*Command),
		flags:     make(map[string]*Flag),
		shortcuts: make(map[string]*ShortcutHandler),
	}
	return &sharedAPI{extensionAPI: extensionAPI{runner: r, extension: ext}}
}

// sharedAPI wraps extensionAPI but registers tools and handlers directly
// into the runner's merged maps so they take effect immediately.
type sharedAPI struct {
	extensionAPI
}

func (a *sharedAPI) On(event string, handler Handler) {
	a.extensionAPI.On(event, handler)
	r := a.runner
	r.mu.Lock()
	r.allHandlers[event] = append(r.allHandlers[event], handler)
	r.mu.Unlock()
}

func (a *sharedAPI) RegisterTool(def ToolDefinition) {
	a.extensionAPI.RegisterTool(def)
	r := a.runner
	r.mu.Lock()
	r.allTools[def.Name] = &def
	r.mu.Unlock()
}

// Reset clears all loaded extensions and merged registrations.
// It preserves the event bus, actions, UI context, error listener, and flag values
// so that the runner can be reloaded with a new set of extensions.
func (r *Runner) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Clear status and widget entries set by old extensions.
	if r.uiContext != nil {
		for key := range r.statusKeys {
			r.uiContext.SetStatus(key, "")
		}
		for key := range r.widgetKeys {
			r.uiContext.ClearWidget(key)
		}
	}
	r.statusKeys = make(map[string]bool)
	r.widgetKeys = make(map[string]bool)

	r.extensions = nil
	r.allTools = make(map[string]*ToolDefinition)
	r.allCommands = make(map[string]*Command)
	r.allFlags = make(map[string]*Flag)
	r.allShortcuts = make(map[string]*ShortcutHandler)
	r.allHandlers = make(map[string][]Handler)
	// Keep: eventBus, actions, uiContext, onError, flagValues
}

// LoadAll initializes all registered extension factories.
func (r *Runner) LoadAll() error {
	return r.loadFactories(RegisteredFactories())
}

// LoadEnabled initializes only the extension factories whose names appear in
// the enabled list. Names are matched case-sensitively. Unknown names are
// logged as warnings (the factory may not be compiled in).
func (r *Runner) LoadEnabled(names []string) error {
	if len(names) == 0 {
		return nil
	}
	firlog.Debug("loading extensions", "names", names)
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}
	all := RegisteredFactories()
	filtered := make([]RegisteredFactory, 0, len(names))
	for _, rf := range all {
		if nameSet[rf.Name] {
			filtered = append(filtered, rf)
			delete(nameSet, rf.Name)
		}
	}
	for name := range nameSet {
		log.Printf("extension %q requested but not registered (not compiled in?)", name)
		firlog.Warn("extension not registered", "name", name)
	}
	return r.loadFactories(filtered)
}

// loadFactories initializes the given extension factories.
func (r *Runner) loadFactories(registered []RegisteredFactory) error {
	for _, rf := range registered {
		firlog.Debug("loading extension", "name", rf.Name)
		ext := &Extension{
			Name:      rf.Name,
			handlers:  make(map[string][]Handler),
			tools:     make(map[string]*ToolDefinition),
			commands:  make(map[string]*Command),
			flags:     make(map[string]*Flag),
			shortcuts: make(map[string]*ShortcutHandler),
		}

		// Create per-extension API
		api := &extensionAPI{
			runner:    r,
			extension: ext,
		}

		// Call factory (may panic)
		func() {
			defer func() {
				if rv := recover(); rv != nil {
					log.Printf("extension %q panicked during init: %v", rf.Name, rv)
					firlog.Error("extension panicked during init", "name", rf.Name, "panic", rv)
				}
			}()
			rf.Factory(api)
		}()

		r.mu.Lock()
		r.extensions = append(r.extensions, ext)

		// Merge handlers
		for event, handlers := range ext.handlers {
			r.allHandlers[event] = append(r.allHandlers[event], handlers...)
		}

		// Merge tools (first registration wins)
		for name, tool := range ext.tools {
			if _, exists := r.allTools[name]; !exists {
				r.allTools[name] = tool
			}
		}

		// Merge commands (first registration wins)
		for name, cmd := range ext.commands {
			if _, exists := r.allCommands[name]; !exists {
				r.allCommands[name] = cmd
			} else {
				log.Printf("Warning: extension command %q from %q conflicts with existing command. Skipping.", name, ext.Name)
			}
		}

		// Merge flags (first registration wins)
		for name, flag := range ext.flags {
			if _, exists := r.allFlags[name]; !exists {
				r.allFlags[name] = flag
			}
			// Set default value only if not already set by CLI/user or previous extension
			if flag.Default != nil {
				if _, hasValue := r.flagValues[name]; !hasValue {
					r.flagValues[name] = flag.Default
				}
			}
		}

		// Merge shortcuts
		for key, handler := range ext.shortcuts {
			r.allShortcuts[key] = handler
		}
		r.mu.Unlock()
	}
	firlog.Info("extensions loaded", "count", len(registered), "tools", len(r.allTools), "commands", len(r.allCommands))
	return nil
}

// BindActions sets the action callbacks. Must be called before emitting events.
func (r *Runner) BindActions(actions *Actions) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actions = actions
}

// SetUIContext sets the UI context for extensions.
func (r *Runner) SetUIContext(ui UIContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.uiContext = ui
}

// SetOnError sets the error listener.
func (r *Runner) SetOnError(fn func(ext string, event string, err error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onError = fn
}

// SetFlagValue sets a flag value (e.g., from CLI parsing).
func (r *Runner) SetFlagValue(name string, value any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flagValues[name] = value
}

// GetFlagValue returns the value of a flag.
func (r *Runner) GetFlagValue(name string) any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.flagValues[name]
}

// GetFlags returns all registered flags.
func (r *Runner) GetFlags() map[string]*Flag {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]*Flag, len(r.allFlags))
	for k, v := range r.allFlags {
		result[k] = v
	}
	return result
}

// GetTools returns all extension-registered tools.
func (r *Runner) GetTools() map[string]*ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]*ToolDefinition, len(r.allTools))
	for k, v := range r.allTools {
		result[k] = v
	}
	return result
}

// GetCommands returns all extension-registered commands.
func (r *Runner) GetCommands() map[string]*Command {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]*Command, len(r.allCommands))
	for k, v := range r.allCommands {
		result[k] = v
	}
	return result
}

// GetShortcuts returns all extension-registered shortcuts.
func (r *Runner) GetShortcuts() map[string]*ShortcutHandler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]*ShortcutHandler, len(r.allShortcuts))
	for k, v := range r.allShortcuts {
		result[k] = v
	}
	return result
}

// HasHandlers returns whether any handlers are registered for the event type.
func (r *Runner) HasHandlers(eventType string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.allHandlers[eventType]) > 0
}

// GetCommand returns a registered command by name, or nil.
func (r *Runner) GetCommand(name string) *Command {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.allCommands[name]
}

// Extensions returns the loaded extensions.
func (r *Runner) Extensions() []*Extension {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*Extension, len(r.extensions))
	copy(result, r.extensions)
	return result
}

// ============================================================================
// Event emission
// ============================================================================

// createContext creates a Context for event handlers.
func (r *Runner) createContext() Context {
	r.mu.RLock()
	actions := r.actions
	ui := r.uiContext
	r.mu.RUnlock()

	// Wrap UI so status/widget keys are tracked for cleanup on reload.
	var trackedUI UIContext
	if ui != nil {
		trackedUI = &trackingUIContext{inner: ui, runner: r}
	}

	return &runnerContext{
		actions: actions,
		ui:      trackedUI,
	}
}

// createCommandContext creates a CommandContext for command handlers.
func (r *Runner) createCommandContext() CommandContext {
	r.mu.RLock()
	actions := r.actions
	ui := r.uiContext
	r.mu.RUnlock()

	var trackedUI UIContext
	if ui != nil {
		trackedUI = &trackingUIContext{inner: ui, runner: r}
	}

	return &runnerCommandContext{
		runnerContext: runnerContext{
			actions: actions,
			ui:      trackedUI,
		},
	}
}

// emitHandlers runs all handlers for an event type, returning the last non-nil result.
func (r *Runner) emitHandlers(eventType string, event *Event) (any, error) {
	r.mu.RLock()
	handlers := r.allHandlers[eventType]
	snapshot := make([]Handler, len(handlers))
	copy(snapshot, handlers)
	r.mu.RUnlock()

	if len(snapshot) == 0 {
		return nil, nil
	}

	firlog.Debug("extension event", "type", eventType, "handlerCount", len(snapshot))
	ctx := r.createContext()
	var lastResult any

	for _, h := range snapshot {
		result, err := func() (res any, err error) {
			defer func() {
				if rv := recover(); rv != nil {
					err = fmt.Errorf("handler panicked: %v", rv)
					firlog.Error("extension handler panicked", "event", eventType, "panic", rv)
				}
			}()
			return h(event, ctx)
		}()

		if err != nil {
			r.mu.RLock()
			onErr := r.onError
			r.mu.RUnlock()
			if onErr != nil {
				onErr("", eventType, err)
			}
			continue
		}
		if result != nil {
			lastResult = result
		}
	}
	return lastResult, nil
}

// Emit emits a generic event to all handlers.
func (r *Runner) Emit(event *Event) error {
	_, err := r.emitHandlers(event.Type, event)
	return err
}

// EmitSessionStart emits the session_start event.
func (r *Runner) EmitSessionStart() error {
	return r.Emit(&Event{
		Type:         "session_start",
		SessionStart: &SessionStartEvent{},
	})
}

// EmitSessionShutdown emits the session_shutdown event.
func (r *Runner) EmitSessionShutdown() error {
	return r.Emit(&Event{
		Type:            "session_shutdown",
		SessionShutdown: &SessionShutdownEvent{},
	})
}

// EmitAgentStart emits agent_start.
func (r *Runner) EmitAgentStart() error {
	return r.Emit(&Event{
		Type:       "agent_start",
		AgentStart: &AgentStartEvent{},
	})
}

// EmitAgentEnd emits agent_end and returns any result.
func (r *Runner) EmitAgentEnd(messages []agent.AgentMessage) error {
	return r.Emit(&Event{
		Type: "agent_end",
		AgentEnd: &AgentEndEvent{
			Messages: messages,
		},
	})
}

// EmitToolCall emits tool_call and returns a ToolCallResult if any handler blocked it.
func (r *Runner) EmitToolCall(toolCallID, toolName string, input map[string]any) *ToolCallResult {
	event := &Event{
		Type: "tool_call",
		ToolCall: &ToolCallEvent{
			ToolCallID: toolCallID,
			ToolName:   toolName,
			Input:      input,
		},
	}
	result, _ := r.emitHandlers("tool_call", event)
	if result == nil {
		return nil
	}
	if tcr, ok := result.(*ToolCallResult); ok {
		firlog.Debug("tool hook", "tool", toolName, "blocked", tcr.Block)
		return tcr
	}
	return nil
}

// EmitToolResult emits tool_result and returns a modified result if any handler changed it.
func (r *Runner) EmitToolResult(event *ToolResultEvent) *ToolResultResult {
	ev := &Event{
		Type:       "tool_result",
		ToolResult: event,
	}
	result, _ := r.emitHandlers("tool_result", ev)
	if result == nil {
		return nil
	}
	if trr, ok := result.(*ToolResultResult); ok {
		return trr
	}
	return nil
}

// EmitBeforeAgentStart emits before_agent_start.
func (r *Runner) EmitBeforeAgentStart(prompt string, images []ai.ImageContent, systemPrompt string) *BeforeAgentStartResult {
	event := &Event{
		Type: "before_agent_start",
		BeforeAgentStart: &BeforeAgentStartEvent{
			Prompt:       prompt,
			Images:       images,
			SystemPrompt: systemPrompt,
		},
	}
	result, _ := r.emitHandlers("before_agent_start", event)
	if result == nil {
		return nil
	}
	if bar, ok := result.(*BeforeAgentStartResult); ok {
		return bar
	}
	return nil
}

// EmitContext emits the context event and returns modified messages.
func (r *Runner) EmitContext(messages []agent.AgentMessage) []agent.AgentMessage {
	event := &Event{
		Type: "context",
		Context: &ContextEvent{
			Messages: messages,
		},
	}
	result, _ := r.emitHandlers("context", event)
	if result == nil {
		return messages
	}
	if cr, ok := result.(*ContextResult); ok && cr.Messages != nil {
		return cr.Messages
	}
	return messages
}

// EmitInput emits input event. Returns the final action result.
func (r *Runner) EmitInput(text string, images []ai.ImageContent, source string) *InputResult {
	event := &Event{
		Type: "input",
		Input: &InputEvent{
			Text:   text,
			Images: images,
			Source: source,
		},
	}

	r.mu.RLock()
	handlers := r.allHandlers["input"]
	snapshot := make([]Handler, len(handlers))
	copy(snapshot, handlers)
	r.mu.RUnlock()

	if len(snapshot) == 0 {
		return nil
	}

	ctx := r.createContext()
	currentText := text
	currentImages := images

	for _, h := range snapshot {
		// Update the event with potentially transformed text
		event.Input.Text = currentText
		event.Input.Images = currentImages

		result, err := func() (res any, err error) {
			defer func() {
				if rv := recover(); rv != nil {
					err = fmt.Errorf("handler panicked: %v", rv)
				}
			}()
			return h(event, ctx)
		}()
		if err != nil {
			r.mu.RLock()
			onErr := r.onError
			r.mu.RUnlock()
			if onErr != nil {
				onErr("", "input", err)
			}
			continue
		}
		if result == nil {
			continue
		}

		if ir, ok := result.(*InputResult); ok {
			switch ir.Action {
			case "handled":
				return ir
			case "transform":
				currentText = ir.Text
				if ir.Images != nil {
					currentImages = ir.Images
				}
			}
		}
	}

	// If text was transformed, return the final state
	if currentText != text {
		return &InputResult{Action: "transform", Text: currentText, Images: currentImages}
	}
	return nil
}

// EmitUserBash emits user_bash and returns the result if handled.
func (r *Runner) EmitUserBash(command string, excludeFromContext bool, cwd string) *UserBashResult {
	event := &Event{
		Type: "user_bash",
		UserBash: &UserBashEvent{
			Command:            command,
			ExcludeFromContext: excludeFromContext,
			Cwd:                cwd,
		},
	}
	result, _ := r.emitHandlers("user_bash", event)
	if result == nil {
		return nil
	}
	if ubr, ok := result.(*UserBashResult); ok {
		return ubr
	}
	return nil
}

// EmitModelSelect emits model_select.
func (r *Runner) EmitModelSelect(model, previousModel *ai.Model, source string) error {
	return r.Emit(&Event{
		Type: "model_select",
		ModelSelect: &ModelSelectEvent{
			Model:         model,
			PreviousModel: previousModel,
			Source:        source,
		},
	})
}

// ExecuteCommand runs an extension command. Returns false if not found.
func (r *Runner) ExecuteCommand(name, args string) (bool, error) {
	r.mu.RLock()
	cmd := r.allCommands[name]
	r.mu.RUnlock()

	if cmd == nil {
		return false, nil
	}

	ctx := r.createCommandContext()
	return true, cmd.Handler(args, ctx)
}

// ============================================================================
// Context implementations
// ============================================================================

type runnerContext struct {
	actions *Actions
	ui      UIContext
}

func (c *runnerContext) UI() UIContext {
	if c.ui != nil {
		return c.ui
	}
	return &noopUIContext{}
}

func (c *runnerContext) HasUI() bool {
	return c.ui != nil
}

func (c *runnerContext) Cwd() string {
	if c.actions != nil && c.actions.Cwd != nil {
		return c.actions.Cwd()
	}
	return "."
}

func (c *runnerContext) SessionManager() *core.SessionManager {
	if c.actions != nil && c.actions.SessionManager != nil {
		return c.actions.SessionManager()
	}
	return nil
}

func (c *runnerContext) ModelRegistry() *core.ModelRegistry {
	if c.actions != nil && c.actions.ModelRegistry != nil {
		return c.actions.ModelRegistry()
	}
	return nil
}

func (c *runnerContext) Model() *ai.Model {
	if c.actions != nil && c.actions.GetModel != nil {
		return c.actions.GetModel()
	}
	return nil
}

func (c *runnerContext) IsIdle() bool {
	if c.actions != nil && c.actions.IsIdle != nil {
		return c.actions.IsIdle()
	}
	return true
}

func (c *runnerContext) Abort() {
	if c.actions != nil && c.actions.Abort != nil {
		c.actions.Abort()
	}
}

func (c *runnerContext) HasPendingMessages() bool {
	if c.actions != nil && c.actions.HasPendingMessages != nil {
		return c.actions.HasPendingMessages()
	}
	return false
}

func (c *runnerContext) Shutdown() {
	if c.actions != nil && c.actions.Shutdown != nil {
		c.actions.Shutdown()
	}
}

func (c *runnerContext) GetContextUsage() *core.ContextUsage {
	if c.actions != nil && c.actions.GetContextUsage != nil {
		return c.actions.GetContextUsage()
	}
	return nil
}

func (c *runnerContext) GetSystemPrompt() string {
	if c.actions != nil && c.actions.GetSystemPrompt != nil {
		return c.actions.GetSystemPrompt()
	}
	return ""
}

// runnerCommandContext extends runnerContext with command-only methods.
type runnerCommandContext struct {
	runnerContext
}

func (c *runnerCommandContext) WaitForIdle() {
	if c.actions != nil && c.actions.WaitForIdle != nil {
		c.actions.WaitForIdle()
	}
}

func (c *runnerCommandContext) NewSession() (bool, error) {
	if c.actions != nil && c.actions.NewSession != nil {
		return c.actions.NewSession()
	}
	return false, fmt.Errorf("new session not available")
}

func (c *runnerCommandContext) Fork(entryID string) (bool, error) {
	if c.actions != nil && c.actions.Fork != nil {
		_, cancelled, err := c.actions.Fork(entryID)
		return cancelled, err
	}
	return false, fmt.Errorf("fork not available")
}

func (c *runnerCommandContext) Reload() error {
	if c.actions != nil && c.actions.Reload != nil {
		return c.actions.Reload()
	}
	return fmt.Errorf("reload not available")
}

// ============================================================================
// Noop UI context (for non-interactive modes)
// ============================================================================

type noopUIContext struct{}

func (n *noopUIContext) Select(string, []string) (string, error) { return "", nil }
func (n *noopUIContext) Confirm(string, string) (bool, error)    { return false, nil }
func (n *noopUIContext) Input(string, string) (string, error)    { return "", nil }
func (n *noopUIContext) Notify(string, string)                   {}
func (n *noopUIContext) SetStatus(string, string)                {}
func (n *noopUIContext) SetWidget(string, []string)              {}
func (n *noopUIContext) ClearWidget(string)                      {}

// trackingUIContext wraps a UIContext and records which status/widget keys
// are set so the Runner can clear them on Reset/reload.
type trackingUIContext struct {
	inner  UIContext
	runner *Runner
}

func (t *trackingUIContext) Select(title string, opts []string) (string, error) {
	return t.inner.Select(title, opts)
}
func (t *trackingUIContext) Confirm(title, msg string) (bool, error) {
	return t.inner.Confirm(title, msg)
}
func (t *trackingUIContext) Input(title, placeholder string) (string, error) {
	return t.inner.Input(title, placeholder)
}
func (t *trackingUIContext) Notify(msg, level string) { t.inner.Notify(msg, level) }
func (t *trackingUIContext) SetStatus(key, text string) {
	t.runner.mu.Lock()
	if text != "" {
		t.runner.statusKeys[key] = true
	} else {
		delete(t.runner.statusKeys, key)
	}
	t.runner.mu.Unlock()
	t.inner.SetStatus(key, text)
}
func (t *trackingUIContext) SetWidget(key string, lines []string) {
	t.runner.mu.Lock()
	t.runner.widgetKeys[key] = true
	t.runner.mu.Unlock()
	t.inner.SetWidget(key, lines)
}
func (t *trackingUIContext) ClearWidget(key string) {
	t.runner.mu.Lock()
	delete(t.runner.widgetKeys, key)
	t.runner.mu.Unlock()
	t.inner.ClearWidget(key)
}

// ============================================================================
// extensionAPI — the per-extension API implementation
// ============================================================================

type extensionAPI struct {
	runner    *Runner
	extension *Extension
}

func (a *extensionAPI) On(event string, handler Handler) {
	a.extension.handlers[event] = append(a.extension.handlers[event], handler)
}

func (a *extensionAPI) RegisterTool(def ToolDefinition) {
	a.extension.tools[def.Name] = &def
}

func (a *extensionAPI) RegisterCommand(name string, cmd Command) {
	a.extension.commands[name] = &cmd
}

func (a *extensionAPI) RegisterFlag(name string, flag Flag) {
	a.extension.flags[name] = &flag
}

func (a *extensionAPI) RegisterShortcut(shortcut string, handler ShortcutHandler) {
	a.extension.shortcuts[shortcut] = &handler
}

func (a *extensionAPI) SendMessage(msg CustomMessageSpec, opts *SendMessageOptions) {
	a.runner.mu.RLock()
	actions := a.runner.actions
	a.runner.mu.RUnlock()
	if actions == nil || actions.AgentSession == nil {
		return
	}
	session := actions.AgentSession()
	if session == nil {
		return
	}
	// Persist custom entry in session.
	raw, err := json.Marshal(msg.Content)
	if err != nil {
		return
	}
	session.SessionManager.AppendCustomEntry(msg.CustomType, raw)

	// Route message to agent based on DeliverAs.
	if opts != nil && opts.DeliverAs != "" {
		cm := &core.CustomMessage{
			Role:       "custom",
			CustomType: msg.CustomType,
			Content:    msg.Content,
			Display:    msg.Display,
			Details:    msg.Details,
			Timestamp:  time.Now().UnixMilli(),
		}
		agentMsg := agent.AgentMessage{Custom: cm}
		switch opts.DeliverAs {
		case "steer":
			session.Agent.Steer(agentMsg)
		case "followUp":
			session.Agent.FollowUp(agentMsg)
		}
	}

	// Trigger a new agent turn if requested.
	if opts != nil && opts.TriggerTurn {
		go func() { _ = session.Agent.Continue() }()
	}
}

func (a *extensionAPI) SendUserMessage(content string, opts *SendUserMessageOptions) {
	a.runner.mu.RLock()
	actions := a.runner.actions
	a.runner.mu.RUnlock()
	if actions == nil || actions.AgentSession == nil {
		return
	}
	session := actions.AgentSession()
	if session == nil {
		return
	}
	// Route based on DeliverAs.
	deliverAs := ""
	if opts != nil {
		deliverAs = opts.DeliverAs
	}
	switch deliverAs {
	case "steer":
		userMsg := agent.AgentMessage{
			Message: ai.NewUserMsg(content, time.Now().UnixMilli()),
		}
		session.Agent.Steer(userMsg)
	case "followUp":
		userMsg := agent.AgentMessage{
			Message: ai.NewUserMsg(content, time.Now().UnixMilli()),
		}
		session.Agent.FollowUp(userMsg)
	default:
		// Run prompt in a goroutine to avoid blocking the event handler.
		go func() { _ = session.Prompt(content) }()
	}
}

func (a *extensionAPI) AppendEntry(customType string, data any) {
	a.runner.mu.RLock()
	actions := a.runner.actions
	a.runner.mu.RUnlock()
	if actions != nil && actions.SessionManager != nil {
		sm := actions.SessionManager()
		if sm != nil {
			raw, err := json.Marshal(data)
			if err == nil {
				sm.AppendCustomEntry(customType, raw)
			}
		}
	}
}

func (a *extensionAPI) SetSessionName(name string) {
	a.runner.mu.RLock()
	actions := a.runner.actions
	a.runner.mu.RUnlock()
	if actions != nil && actions.AgentSession != nil {
		if s := actions.AgentSession(); s != nil {
			s.SetSessionName(name)
		}
	}
}

func (a *extensionAPI) GetSessionName() string {
	a.runner.mu.RLock()
	actions := a.runner.actions
	a.runner.mu.RUnlock()
	if actions != nil && actions.AgentSession != nil {
		if s := actions.AgentSession(); s != nil {
			return s.GetSessionName()
		}
	}
	return ""
}

func (a *extensionAPI) SetLabel(entryID string, label string) {
	a.runner.mu.RLock()
	actions := a.runner.actions
	a.runner.mu.RUnlock()
	if actions != nil && actions.SessionManager != nil {
		sm := actions.SessionManager()
		if sm != nil {
			sm.AppendLabelChange(entryID, label)
		}
	}
}

func (a *extensionAPI) ClearLabel(entryID string) {
	a.runner.mu.RLock()
	actions := a.runner.actions
	a.runner.mu.RUnlock()
	if actions != nil && actions.SessionManager != nil {
		sm := actions.SessionManager()
		if sm != nil {
			sm.AppendLabelChange(entryID, "")
		}
	}
}

func (a *extensionAPI) GetActiveTools() []string {
	a.runner.mu.RLock()
	actions := a.runner.actions
	a.runner.mu.RUnlock()
	if actions != nil && actions.AgentSession != nil {
		if s := actions.AgentSession(); s != nil {
			state := s.State()
			names := make([]string, len(state.Tools))
			for i, t := range state.Tools {
				names[i] = t.Name
			}
			return names
		}
	}
	return nil
}

func (a *extensionAPI) GetAllTools() []ToolInfo {
	a.runner.mu.RLock()
	actions := a.runner.actions
	a.runner.mu.RUnlock()
	if actions != nil && actions.AgentSession != nil {
		if s := actions.AgentSession(); s != nil {
			state := s.State()
			result := make([]ToolInfo, len(state.Tools))
			for i, t := range state.Tools {
				result[i] = ToolInfo{Name: t.Name, Description: t.Description}
			}
			return result
		}
	}
	return nil
}

func (a *extensionAPI) SetActiveTools(names []string) {
	a.runner.mu.RLock()
	actions := a.runner.actions
	a.runner.mu.RUnlock()
	if actions == nil || actions.AgentSession == nil {
		return
	}
	session := actions.AgentSession()
	if session == nil {
		return
	}
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}
	state := session.State()
	filtered := make([]agent.AgentTool, 0, len(names))
	for _, t := range state.Tools {
		if nameSet[t.Name] {
			filtered = append(filtered, t)
		}
	}
	session.Agent.SetTools(filtered)
}

func (a *extensionAPI) GetCommands() []core.SlashCommandInfo {
	var result []core.SlashCommandInfo
	a.runner.mu.RLock()
	for name, cmd := range a.runner.allCommands {
		result = append(result, core.SlashCommandInfo{
			Name:        name,
			Description: cmd.Description,
			Source:      core.SlashCommandSourceExtension,
		})
	}
	a.runner.mu.RUnlock()
	return result
}

func (a *extensionAPI) SetModel(model *ai.Model) bool {
	a.runner.mu.RLock()
	actions := a.runner.actions
	a.runner.mu.RUnlock()
	if actions != nil && actions.AgentSession != nil {
		if s := actions.AgentSession(); s != nil {
			if actions.ModelRegistry != nil {
				mr := actions.ModelRegistry()
				if mr != nil && mr.GetApiKey(model) == "" {
					return false
				}
			}
			s.SetModel(model)
			return true
		}
	}
	return false
}

func (a *extensionAPI) GetThinkingLevel() string {
	a.runner.mu.RLock()
	actions := a.runner.actions
	a.runner.mu.RUnlock()
	if actions != nil && actions.AgentSession != nil {
		if s := actions.AgentSession(); s != nil {
			return s.ThinkingLevel()
		}
	}
	return "off"
}

func (a *extensionAPI) SetThinkingLevel(level string) {
	a.runner.mu.RLock()
	actions := a.runner.actions
	a.runner.mu.RUnlock()
	if actions != nil && actions.AgentSession != nil {
		if s := actions.AgentSession(); s != nil {
			s.SetThinkingLevel(level)
		}
	}
}

func (a *extensionAPI) GetFlag(name string) any {
	// Strip leading -- for convenience
	name = strings.TrimPrefix(name, "--")
	return a.runner.GetFlagValue(name)
}

func (a *extensionAPI) Events() core.EventBus {
	return a.runner.eventBus
}

func (a *extensionAPI) Exec(command string, args []string) (*ExecResult, error) {
	cmd := exec.Command(command, args...)
	stdout, err := cmd.Output()
	result := &ExecResult{
		Stdout: string(stdout),
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.Stderr = string(exitErr.Stderr)
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}
