# Directory Structure

```
pi-go/
├── cmd/pi/
│   ├── main.go                  # Entry point
│   └── app.go                   # Arg parsing, mode dispatch
├── pkg/
│   ├── ai/
│   │   ├── types.go             # Message, Model, Tool, Context, Usage
│   │   ├── eventstream.go       # Channel-based event stream
│   │   ├── stream.go            # StreamSimple() dispatcher
│   │   ├── models.go            # Built-in model definitions + GetModel()
│   │   ├── envkeys.go           # Env var API key resolution
│   │   ├── registry.go          # Provider registry
│   │   ├── jsonparse.go         # Streaming JSON parser
│   │   ├── overflow.go          # Context overflow detection
│   │   ├── providers/
│   │   │   ├── anthropic.go
│   │   │   ├── openai.go        # OpenAI completions + compatible
│   │   │   ├── openai_responses.go
│   │   │   ├── google.go
│   │   │   ├── bedrock.go
│   │   │   ├── options.go       # Shared reasoning/budget logic
│   │   │   └── transform.go     # Message format transforms
│   │   └── oauth/
│   │       ├── types.go         # OAuthCredentials, OAuthProviderInterface
│   │       ├── pkce.go          # PKCE verifier + challenge
│   │       ├── anthropic.go     # Anthropic OAuth flow
│   │       ├── github_copilot.go # GitHub Copilot device flow
│   │       ├── google_antigravity.go # Google Antigravity OAuth
│   │       ├── google_gemini_cli.go  # Gemini CLI gcloud auth
│   │       └── openai_codex.go  # OpenAI Codex OAuth
│   ├── agent/
│   │   ├── types.go             # AgentTool, AgentMessage, AgentState, events
│   │   ├── agent.go             # Agent struct
│   │   └── loop.go              # Core loop
│   ├── core/
│   │   ├── tools/
│   │   │   ├── read.go
│   │   │   ├── bash.go
│   │   │   ├── edit.go
│   │   │   ├── write.go
│   │   │   ├── grep.go
│   │   │   ├── find.go
│   │   │   ├── ls.go
│   │   │   ├── truncate.go
│   │   │   └── pathutils.go
│   │   ├── compaction/
│   │   │   ├── compaction.go
│   │   │   └── utils.go
│   │   ├── session.go           # SessionManager
│   │   ├── messages.go          # Message types, convertToLlm
│   │   ├── settings.go          # SettingsManager
│   │   ├── modelregistry.go     # User models.json + built-in
│   │   ├── modelresolver.go     # Scope/pattern matching
│   │   ├── authstorage.go       # Credential persistence
│   │   ├── systemprompt.go      # System prompt builder
│   │   ├── defaults.go          # Constants
│   │   ├── sdk.go               # CreateAgentSession()
│   │   ├── agentsession.go      # AgentSession (the big one)
│   │   ├── resourceloader.go    # Discover skills, prompts, context files
│   │   ├── keybindings.go       # Configurable keybindings
│   │   ├── slashcmds.go         # Built-in slash commands
│   │   ├── prompttemplates.go
│   │   ├── skills.go
│   │   ├── bashexec.go          # High-level bash with op tracking
│   │   └── eventbus.go
│   ├── tui/
│   │   ├── terminal.go          # Raw terminal I/O
│   │   ├── tui.go               # Differential renderer
│   │   ├── keys.go              # Key parsing (ANSI, Kitty)
│   │   ├── utils.go             # visibleWidth, ANSI slicing
│   │   ├── fuzzy.go
│   │   ├── image.go             # Kitty/iTerm2 image protocol
│   │   └── components/
│   │       ├── text.go
│   │       ├── box.go
│   │       ├── input.go
│   │       ├── editor.go
│   │       ├── markdown.go
│   │       ├── selectlist.go
│   │       ├── loader.go
│   │       └── spacer.go
│   └── modes/
│       ├── interactive/
│       │   ├── mode.go          # InteractiveMode
│       │   ├── theme.go
│       │   └── components/
│       │       ├── assistantmsg.go
│       │       ├── usermsg.go
│       │       ├── toolexec.go
│       │       ├── footer.go
│       │       ├── modelselector.go
│       │       ├── sessionselector.go
│       │       └── ...
│       ├── print.go             # Print mode
│       └── rpc/
│           ├── server.go
│           └── types.go
├── docs/plan/                   # This planning directory
├── sync/
│   ├── UPSTREAM_MAP.md          # TS file → Go file mapping
│   ├── SYNC_LOG.md              # Record of upstream syncs
│   └── sync-check.sh           # Detect upstream changes
├── assets/themes/
│   ├── dark.json
│   └── light.json
├── Makefile
├── go.mod
└── README.md
```
