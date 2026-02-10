# Upstream Map: TS → Go

Master mapping of TypeScript source files to their Go ports.

| TS Source (relative to `pi-mono/packages/`) | Go File | Status |
|---|---|---|
| `ai/src/types.ts` | `pkg/ai/types.go` | ✅ |
| `ai/src/utils/event-stream.ts` | `pkg/ai/eventstream.go` | ✅ |
| `ai/src/models.ts` | `pkg/ai/models.go` | ✅ |
| `ai/src/models.generated.ts` | `pkg/ai/models_generated.go` | ✅ (auto-generated) |
| `ai/src/env-api-keys.ts` | `pkg/ai/envkeys.go` | ✅ |
| `ai/src/api-registry.ts` | `pkg/ai/registry.go` | ✅ |
| `ai/src/utils/overflow.ts` | `pkg/ai/overflow.go` | ✅ |
| `ai/src/utils/json-parse.ts` | `pkg/ai/jsonparse.go` | ✅ |
| `ai/src/stream.ts` | `pkg/ai/stream.go` | ✅ |
| `ai/src/providers/simple-options.ts` | `pkg/ai/providers/options.go` | ✅ |
| `ai/src/providers/transform-messages.ts` | `pkg/ai/providers/transform.go` | ✅ |
| `ai/src/providers/anthropic.ts` | `pkg/ai/providers/anthropic.go` | ☐ |
| `ai/src/providers/openai-completions.ts` | `pkg/ai/providers/openai.go` | ☐ |
| `ai/src/providers/openai-responses.ts` | `pkg/ai/providers/openai_responses.go` | ☐ |
| `ai/src/providers/google.ts` | `pkg/ai/providers/google.go` | ☐ |
| `ai/src/providers/amazon-bedrock.ts` | `pkg/ai/providers/bedrock.go` | ☐ |
| `coding-agent/src/core/footer-data-provider.ts` | `pkg/core/footerdataprovider.go` | ✅ |
| `coding-agent/src/core/timings.ts` | `pkg/core/timings.go` | ✅ |
| `coding-agent/src/utils/clipboard.ts` | `pkg/core/clipboard.go` | ✅ |
| `coding-agent/src/utils/changelog.ts` | `pkg/core/changelog.go` | ✅ |
| `coding-agent/src/utils/clipboard-image.ts` | `pkg/core/clipboardimage.go` | ✅ (partial — extensionForImageMimeType, ReadClipboardImage stub) |
| `agent/src/types.ts` | `pkg/agent/types.go` | ☐ |
| `agent/src/agent-loop.ts` | `pkg/agent/loop.go` | ☐ |
| `agent/src/agent.ts` | `pkg/agent/agent.go` | ☐ |
| `tui/src/components/cancellable-loader.ts` | `pkg/tui/components/cancellable_loader.go` | ✅ |
| `coding-agent/src/modes/interactive/components/bordered-loader.ts` | `pkg/modes/interactive/components/bordered_loader.go` | ✅ |
| `coding-agent/src/modes/interactive/components/bash-execution.ts` | `pkg/modes/interactive/components/bash_execution.go` | ✅ |
| `coding-agent/src/modes/interactive/components/user-message.ts` | `pkg/modes/interactive/components/user_message.go` | ✅ |
| `coding-agent/src/modes/interactive/components/show-images-selector.ts` | `pkg/modes/interactive/components/show_images_selector.go` | ✅ |
| `coding-agent/src/modes/interactive/components/thinking-selector.ts` | `pkg/modes/interactive/components/thinking_selector.go` | ✅ |
| `coding-agent/src/modes/interactive/components/theme-selector.ts` | `pkg/modes/interactive/components/theme_selector.go` | ✅ |
| `coding-agent/src/modes/interactive/components/branch-summary-message.ts` | `pkg/modes/interactive/components/branch_summary_message.go` | ✅ |
| `coding-agent/src/modes/interactive/components/compaction-summary-message.ts` | `pkg/modes/interactive/components/compaction_summary_message.go` | ✅ |
| `coding-agent/src/modes/interactive/components/skill-invocation-message.ts` | `pkg/modes/interactive/components/skill_invocation_message.go` | ✅ |
| `coding-agent/src/modes/interactive/components/custom-message.ts` | `pkg/modes/interactive/components/custom_message.go` | ✅ |
| `coding-agent/src/modes/interactive/components/extension-input.ts` | `pkg/modes/interactive/components/extension_input.go` | ✅ |
| `coding-agent/src/modes/interactive/components/extension-selector.ts` | `pkg/modes/interactive/components/extension_selector.go` | ✅ |
| `coding-agent/src/modes/interactive/components/user-message-selector.ts` | `pkg/modes/interactive/components/user_message_selector.go` | ✅ |
| `coding-agent/src/modes/interactive/components/model-selector.ts` | `pkg/modes/interactive/components/model_selector.go` | ✅ |
| `coding-agent/src/modes/interactive/components/scoped-models-selector.ts` | `pkg/modes/interactive/components/scoped_models_selector.go` | ✅ |
| `coding-agent/src/modes/interactive/components/session-selector-search.ts` | `pkg/modes/interactive/components/session_selector_search.go` | ✅ |
| `coding-agent/src/modes/interactive/components/assistant-message.ts` | `pkg/modes/interactive/components/assistant_message.go` | ✅ |
| `coding-agent/src/modes/interactive/components/custom-editor.ts` | `pkg/modes/interactive/components/custom_editor.go` | ✅ |
| `coding-agent/src/modes/interactive/components/diff.ts` | `pkg/modes/interactive/components/diff.go` | ✅ |
| `coding-agent/src/modes/interactive/components/dynamic-border.ts` | `pkg/modes/interactive/components/dynamic_border.go` | ✅ |
| `coding-agent/src/modes/interactive/components/extension-editor.ts` | `pkg/modes/interactive/components/extension_editor.go` | ✅ |
| `coding-agent/src/modes/interactive/components/footer.ts` | `pkg/modes/interactive/components/footer.go` | ✅ |
| `coding-agent/src/modes/interactive/components/countdown-timer.ts` | `pkg/modes/interactive/components/countdown_timer.go` | ✅ |
| `coding-agent/src/modes/interactive/components/keybinding-hints.ts` | `pkg/modes/interactive/components/keybinding_hints.go` | ✅ |
| `coding-agent/src/modes/interactive/components/visual-truncate.ts` | `pkg/modes/interactive/components/visual_truncate.go` | ✅ |
| `coding-agent/src/modes/interactive/components/login-dialog.ts` | `pkg/modes/interactive/components/login_dialog.go` | ✅ |
| `coding-agent/src/modes/interactive/components/oauth-selector.ts` | `pkg/modes/interactive/components/oauth_selector.go` | ✅ |
| `coding-agent/src/modes/interactive/components/tool-execution.ts` | `pkg/modes/interactive/components/tool_execution.go` | ✅ |
| `coding-agent/src/modes/interactive/components/armin.ts` | `pkg/modes/interactive/components/armin.go` | 🔲 stub |
| `coding-agent/src/modes/interactive/components/daxnuts.ts` | `pkg/modes/interactive/components/daxnuts.go` | 🔲 stub |
| `coding-agent/src/modes/interactive/components/config-selector.ts` | `pkg/modes/interactive/components/config_selector.go` | 🔲 stub |
| `coding-agent/src/modes/interactive/components/session-selector.ts` | `pkg/modes/interactive/components/session_selector.go` | 🔲 stub |
| `coding-agent/src/modes/interactive/components/settings-selector.ts` | `pkg/modes/interactive/components/settings_selector.go` | 🔲 stub |
| `coding-agent/src/modes/interactive/components/tree-selector.ts` | `pkg/modes/interactive/components/tree_selector.go` | 🔲 stub |
