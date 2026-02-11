# Upstream Map: TS → Go

Master mapping of TypeScript source files to their Go ports.

## AI Layer (`pkg/ai/`)

| TS Source (relative to `pi-mono/packages/`) | Go File | Status |
|---|---|---|
| `ai/src/types.ts` | `pkg/ai/types.go` | ✅ |
| `ai/src/utils/event-stream.ts` | `pkg/ai/eventstream.go` | ✅ |
| `ai/src/models.ts` + `ai/src/models.generated.ts` | `pkg/ai/models.go` + `pkg/ai/models_generated.go` | ✅ |
| `ai/src/env-api-keys.ts` | `pkg/ai/envkeys.go` | ✅ |
| `ai/src/api-registry.ts` | `pkg/ai/registry.go` | ✅ |
| `ai/src/utils/overflow.ts` | `pkg/ai/overflow.go` | ✅ |
| `ai/src/utils/json-parse.ts` | `pkg/ai/jsonparse.go` | ✅ |
| `ai/src/stream.ts` | `pkg/ai/stream.go` | ✅ |

## AI Providers (`pkg/ai/providers/`)

| TS Source | Go File | Status |
|---|---|---|
| `ai/src/providers/simple-options.ts` | `pkg/ai/providers/options.go` | ✅ |
| `ai/src/providers/transform-messages.ts` | `pkg/ai/providers/transform.go` | ✅ |
| `ai/src/providers/anthropic.ts` | `pkg/ai/providers/anthropic.go` | ✅ |
| `ai/src/providers/openai-completions.ts` | `pkg/ai/providers/openai.go` | ✅ |
| `ai/src/providers/openai-responses.ts` | `pkg/ai/providers/openai_responses.go` | ✅ |
| `ai/src/providers/openai-responses-shared.ts` | `pkg/ai/providers/openai_responses_shared.go` | ✅ |
| `ai/src/providers/azure-openai-responses.ts` | `pkg/ai/providers/azure_openai_responses.go` | ✅ |
| `ai/src/providers/google.ts` | `pkg/ai/providers/google.go` | ✅ |
| `ai/src/providers/amazon-bedrock.ts` | `pkg/ai/providers/bedrock.go` | ✅ |
| `ai/src/providers/register-builtins.ts` | `pkg/ai/providers/register_builtins.go` | ✅ |
| (internal SSE client) | `pkg/ai/providers/sse.go` | ✅ |

## OAuth (`pkg/ai/oauth/`)

| TS Source | Go File | Status |
|---|---|---|
| `ai/src/utils/oauth/types.ts` | `pkg/ai/oauth/types.go` | ☐ |
| `ai/src/utils/oauth/pkce.ts` | `pkg/ai/oauth/pkce.go` | ☐ |
| `ai/src/utils/oauth/anthropic.ts` | `pkg/ai/oauth/anthropic.go` | ☐ |
| `ai/src/utils/oauth/github-copilot.ts` | `pkg/ai/oauth/github_copilot.go` | ☐ |
| `ai/src/utils/oauth/google-antigravity.ts` | `pkg/ai/oauth/google_antigravity.go` | ☐ |
| `ai/src/utils/oauth/google-gemini-cli.ts` | `pkg/ai/oauth/google_gemini_cli.go` | ☐ |
| `ai/src/utils/oauth/openai-codex.ts` | `pkg/ai/oauth/openai_codex.go` | ☐ |

## Agent Layer (`pkg/agent/`)

| TS Source | Go File | Status |
|---|---|---|
| `agent/src/types.ts` | `pkg/agent/types.go` | ✅ |
| `agent/src/agent-loop.ts` | `pkg/agent/loop.go` | ✅ |
| `agent/src/agent.ts` | `pkg/agent/agent.go` | ✅ |

## Tools (`pkg/core/tools/`)

| TS Source | Go File | Status |
|---|---|---|
| `coding-agent/src/core/tools/truncate.ts` | `pkg/core/tools/truncate.go` | ✅ |
| `coding-agent/src/core/tools/path-utils.ts` | `pkg/core/tools/pathutils.go` | ✅ |
| `coding-agent/src/core/tools/read.ts` | `pkg/core/tools/read.go` | ✅ |
| `coding-agent/src/core/tools/bash.ts` | `pkg/core/tools/bash.go` | ✅ |
| `coding-agent/src/core/tools/edit.ts` + `edit-diff.ts` | `pkg/core/tools/edit.go` + `pkg/core/tools/editdiff.go` | ✅ |
| `coding-agent/src/core/tools/write.ts` | `pkg/core/tools/write.go` | ✅ |
| `coding-agent/src/core/tools/grep.ts` | `pkg/core/tools/grep.go` | ✅ |
| `coding-agent/src/core/tools/find.ts` | `pkg/core/tools/find.go` | ✅ |
| `coding-agent/src/core/tools/ls.ts` | `pkg/core/tools/ls.go` | ✅ |

## Core Infrastructure (`pkg/core/`)

| TS Source | Go File | Status |
|---|---|---|
| `coding-agent/src/core/defaults.ts` | `pkg/core/defaults.go` | ✅ |
| `coding-agent/src/core/messages.ts` | `pkg/core/messages.go` | ✅ |
| `coding-agent/src/core/auth-storage.ts` | `pkg/core/authstorage.go` | ✅ |
| `coding-agent/src/core/settings-manager.ts` | `pkg/core/settings.go` | ✅ |
| `coding-agent/src/core/system-prompt.ts` | `pkg/core/systemprompt.go` | ✅ |
| `coding-agent/src/core/skills.ts` | `pkg/core/skills.go` | ✅ |
| `coding-agent/src/core/prompt-templates.ts` | `pkg/core/prompttemplates.go` | ✅ |
| `coding-agent/src/core/model-registry.ts` | `pkg/core/modelregistry.go` | ✅ |
| `coding-agent/src/core/model-resolver.ts` | `pkg/core/modelresolver.go` | ✅ |
| `coding-agent/src/core/resource-loader.ts` | `pkg/core/resourceloader.go` | ✅ |
| `coding-agent/src/core/keybindings.ts` | `pkg/core/keybindings.go` | ✅ |
| `coding-agent/src/core/bash-executor.ts` | `pkg/core/bashexec.go` | ✅ |
| `coding-agent/src/core/slash-commands.ts` | `pkg/core/slashcmds.go` | ✅ |
| `coding-agent/src/core/event-bus.ts` | `pkg/core/eventbus.go` | ✅ |
| `coding-agent/src/core/session-manager.ts` | `pkg/core/session.go` | ✅ |
| `coding-agent/src/core/agent-session.ts` | `pkg/core/agentsession.go` | ✅ |
| `coding-agent/src/core/sdk.ts` | `pkg/core/sdk.go` | ✅ |
| `coding-agent/src/core/footer-data-provider.ts` | `pkg/core/footerdataprovider.go` | ✅ |
| `coding-agent/src/core/timings.ts` | `pkg/core/timings.go` | ✅ |
| `coding-agent/src/core/resolve-config-value.ts` | `pkg/core/configvalue.go` | ✅ |
| `coding-agent/src/utils/frontmatter.ts` | `pkg/core/frontmatter.go` | ✅ |
| `coding-agent/src/utils/clipboard.ts` | `pkg/core/clipboard.go` | ✅ |
| `coding-agent/src/utils/clipboard-image.ts` | `pkg/core/clipboardimage.go` | ✅ |
| `coding-agent/src/utils/image-resize.ts` | `pkg/core/tools/imageresize.go` | ✅ |
| `coding-agent/src/utils/changelog.ts` | `pkg/core/changelog.go` | ✅ |
| `coding-agent/src/core/compaction/compaction.ts` | `pkg/core/compaction/compaction.go` | ✅ |
| `coding-agent/src/core/compaction/utils.ts` | `pkg/core/compaction/utils.go` | ✅ |

## TUI (`pkg/tui/`)

| TS Source | Go File | Status |
|---|---|---|
| `tui/src/terminal.ts` | `pkg/tui/terminal.go` | ✅ |
| `tui/src/keys.ts` | `pkg/tui/keys.go` | ✅ |
| `tui/src/utils.ts` | `pkg/tui/utils.go` | ✅ |
| `tui/src/tui.ts` | `pkg/tui/tui.go` | ✅ |
| `tui/src/fuzzy.ts` | `pkg/tui/fuzzy.go` | ✅ |
| `tui/src/terminal-image.ts` | `pkg/tui/image.go` | ✅ |

## TUI Components (`pkg/tui/components/`)

| TS Source | Go File | Status |
|---|---|---|
| `tui/src/components/text.ts` | `pkg/tui/components/text.go` | ✅ |
| `tui/src/components/box.ts` | `pkg/tui/components/box.go` | ✅ |
| `tui/src/components/input.ts` | `pkg/tui/components/input.go` | ✅ |
| `tui/src/components/editor.ts` | `pkg/tui/components/editor.go` | ✅ |
| `tui/src/components/markdown.ts` | `pkg/tui/components/markdown.go` | ✅ |
| `tui/src/components/select-list.ts` | `pkg/tui/components/selectlist.go` | ✅ |
| `tui/src/components/loader.ts` | `pkg/tui/components/loader.go` | ✅ |
| `tui/src/components/spacer.ts` | `pkg/tui/components/spacer.go` | ✅ |
| `tui/src/components/cancellable-loader.ts` | `pkg/tui/components/cancellable_loader.go` | ✅ |

## Interactive Mode (`pkg/modes/interactive/`)

| TS Source | Go File | Status |
|---|---|---|
| `coding-agent/src/modes/interactive/theme/theme.ts` | `pkg/modes/interactive/theme/theme.go` | ✅ |
| `coding-agent/src/modes/interactive/interactive-mode.ts` | `pkg/modes/interactive/mode.go` | ✅ |
| `tui/src/autocomplete.ts` | `pkg/modes/interactive/autocomplete.go` | ✅ |
| (platform signals) | `pkg/modes/interactive/signal_unix.go` + `signal_windows.go` | ✅ |

## Interactive Components (`pkg/modes/interactive/components/`)

| TS Source | Go File | Status |
|---|---|---|
| `.../components/assistant-message.ts` | `.../components/assistant_message.go` | ✅ |
| `.../components/armin.ts` | `.../components/armin.go` | ✅ |
| `.../components/bash-execution.ts` | `.../components/bash_execution.go` | ✅ |
| `.../components/bordered-loader.ts` | `.../components/bordered_loader.go` | ✅ |
| `.../components/branch-summary-message.ts` | `.../components/branch_summary_message.go` | ✅ |
| `.../components/compaction-summary-message.ts` | `.../components/compaction_summary_message.go` | ✅ |
| `.../components/config-selector.ts` | `.../components/config_selector.go` | ✅ |
| `.../components/countdown-timer.ts` | `.../components/countdown_timer.go` | ✅ |
| `.../components/custom-editor.ts` | `.../components/custom_editor.go` | ✅ |
| `.../components/custom-message.ts` | `.../components/custom_message.go` | ✅ |
| `.../components/daxnuts.ts` | `.../components/daxnuts.go` | ✅ |
| `.../components/diff.ts` | `.../components/diff.go` | ✅ |
| `.../components/dynamic-border.ts` | `.../components/dynamic_border.go` | ✅ |
| `.../components/extension-editor.ts` | `.../components/extension_editor.go` | ✅ |
| `.../components/extension-input.ts` | `.../components/extension_input.go` | ✅ |
| `.../components/extension-selector.ts` | `.../components/extension_selector.go` | ✅ |
| `.../components/footer.ts` | `.../components/footer.go` | ✅ |
| `.../components/keybinding-hints.ts` | `.../components/keybinding_hints.go` | ✅ |
| `.../components/login-dialog.ts` | `.../components/login_dialog.go` | ✅ |
| `.../components/model-selector.ts` | `.../components/model_selector.go` | ✅ |
| `.../components/oauth-selector.ts` | `.../components/oauth_selector.go` | ✅ |
| `.../components/scoped-models-selector.ts` | `.../components/scoped_models_selector.go` | ✅ |
| `.../components/session-selector.ts` | `.../components/session_selector.go` | ✅ |
| `.../components/session-selector-search.ts` | `.../components/session_selector_search.go` | ✅ |
| `.../components/settings-selector.ts` | `.../components/settings_selector.go` | ✅ |
| `.../components/show-images-selector.ts` | `.../components/show_images_selector.go` | ✅ |
| `.../components/skill-invocation-message.ts` | `.../components/skill_invocation_message.go` | ✅ |
| `.../components/theme-selector.ts` | `.../components/theme_selector.go` | ✅ |
| `.../components/thinking-selector.ts` | `.../components/thinking_selector.go` | ✅ |
| `.../components/tool-execution.ts` | `.../components/tool_execution.go` | ✅ |
| `.../components/tree-selector.ts` | `.../components/tree_selector.go` | ✅ |
| `.../components/user-message.ts` | `.../components/user_message.go` | ✅ |
| `.../components/user-message-selector.ts` | `.../components/user_message_selector.go` | ✅ |
| `.../components/visual-truncate.ts` | `.../components/visual_truncate.go` | ✅ |

## Print & RPC Modes

| TS Source | Go File | Status |
|---|---|---|
| `coding-agent/src/modes/print-mode.ts` | `pkg/modes/print/print.go` | ✅ |
| (shared mode types) | `pkg/modes/modes.go` | ✅ |
| `coding-agent/src/modes/rpc/rpc-mode.ts` | `pkg/modes/rpc/server.go` | ✅ |
| `coding-agent/src/modes/rpc/rpc-types.ts` | `pkg/modes/rpc/types.go` | ✅ |

## CLI Entry Point (`cmd/pi/`)

| TS Source | Go File | Status |
|---|---|---|
| `coding-agent/src/cli.ts` | `cmd/pi/main.go` | ✅ |
| `coding-agent/src/main.ts` | `cmd/pi/app.go` | ✅ |
| `coding-agent/src/cli/args.ts` | `cmd/pi/args.go` | ✅ |
