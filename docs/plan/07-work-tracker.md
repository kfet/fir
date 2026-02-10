# Work Tracker

`[ ]` = todo, `[x]` = done (code + tests + `go vet` clean)

**Before starting a task:** check if the `.go` file already exists — another agent may have written it.
**Before updating this file:** re-read it — another agent may have checked off items.

---
## Phase 0: Scaffolding

- [x] `go.mod` + `go.sum` — `go.mod` + `go.sum`
- [x] `Makefile`
- [x] Directory structure (create all `pkg/` subdirs)
- [x] `sync/UPSTREAM_MAP.md` — master TS↔Go file map
- [x] `sync/sync-check.sh` + `sync/sync-record.sh`

## Phase 1: AI Layer (`pkg/ai/`)

Each item means: write `foo.go` AND `foo_test.go`.

| Status | Go file + test | TS source | Lines | Lock key | Deps |
|---|---|---|---|---|---|
| [x] | `types.go` `types_test.go` | `ai/src/types.ts` | 295 | `pkg-ai-types` | none |
| [x] | `eventstream.go` `eventstream_test.go` | `ai/src/utils/event-stream.ts` | 87 | `pkg-ai-eventstream` | types |
| [x] | `models.go` `models_test.go` | `ai/src/models.ts` + `models.generated.ts` | 12284 | `pkg-ai-models` | types |
| [x] | `envkeys.go` `envkeys_test.go` | `ai/src/env-api-keys.ts` | 115 | `pkg-ai-envkeys` | types |
| [x] | `registry.go` `registry_test.go` | `ai/src/api-registry.ts` | 98 | `pkg-ai-registry` | types, eventstream |
| [x] | `overflow.go` `overflow_test.go` | `ai/src/utils/overflow.ts` | 121 | `pkg-ai-overflow` | types |
| [x] | `jsonparse.go` `jsonparse_test.go` | `ai/src/utils/json-parse.ts` | 28 | `pkg-ai-jsonparse` | none |
| [x] | `stream.go` `stream_test.go` | `ai/src/stream.ts` | 60 | `pkg-ai-stream` | registry |
| [x] | `providers/options.go` `providers/options_test.go` | `ai/src/providers/simple-options.ts` | 45 | `pkg-ai-providers-options` | types |
| [x] | `providers/transform.go` `providers/transform_test.go` | `ai/src/providers/transform-messages.ts` | 167 | `pkg-ai-providers-transform` | types |
| [x] | `providers/anthropic.go` `providers/anthropic_test.go` | `ai/src/providers/anthropic.ts` | 808 | `pkg-ai-providers-anthropic` | types, eventstream, registry, options, transform |
| [x] | `providers/openai.go` `providers/openai_test.go` | `ai/src/providers/openai-completions.ts` | 847 | `pkg-ai-providers-openai` | types, eventstream, registry, options, transform |
| [x] | `providers/openai_responses.go` `providers/openai_responses_test.go` | `ai/src/providers/openai-responses.ts` + shared | 754 | `pkg-ai-providers-openai-responses` | types, eventstream, registry, options |
| [x] | `providers/google.go` `providers/google_test.go` | `ai/src/providers/google.ts` + shared | 769 | `pkg-ai-providers-google` | types, eventstream, registry, options, transform |
| [x] | `providers/bedrock.go` `providers/bedrock_test.go` | `ai/src/providers/amazon-bedrock.ts` | 731 | `pkg-ai-providers-bedrock` | types, eventstream, registry, options, transform |
| [x] | `providers/testutil_test.go` + `providers/testdata/` | — | — | `pkg-ai-providers-testutil` | types, eventstream |

Each item = `foo.go` + `foo_test.go`. TS paths relative to `../pi-mono/packages/`.

| | Go file | TS source | Lines | Depends on |
|---|---|---|---|---|
| [x] | `types.go` | `ai/src/types.ts` | 295 | — |
| [x] | `eventstream.go` | `ai/src/utils/event-stream.ts` | 87 | types |
| [x] | `models.go` | `ai/src/models.ts` + `models.generated.ts` | 12284 | types |
| [x] | `envkeys.go` | `ai/src/env-api-keys.ts` | 115 | types |
| [x] | `registry.go` | `ai/src/api-registry.ts` | 98 | types, eventstream |
| [x] | `overflow.go` | `ai/src/utils/overflow.ts` | 121 | types |
| [x] | `jsonparse.go` | `ai/src/utils/json-parse.ts` | 28 | — |
| [x] | `stream.go` | `ai/src/stream.ts` | 60 | registry |
| [x] | `providers/options.go` | `ai/src/providers/simple-options.ts` | 45 | types |
| [x] | `providers/transform.go` | `ai/src/providers/transform-messages.ts` | 167 | types |
| [x] | `providers/anthropic.go` | `ai/src/providers/anthropic.ts` | 808 | all above |
| [x] | `providers/openai.go` | `ai/src/providers/openai-completions.ts` | 847 | all above |
| [x] | `providers/openai_responses.go` | `ai/src/providers/openai-responses.ts` + shared | 754 | all above |
| [x] | `providers/google.go` | `ai/src/providers/google.ts` + shared | 769 | all above |
| [x] | `providers/bedrock.go` | `ai/src/providers/amazon-bedrock.ts` | 731 | all above |
| [x] | `providers/testutil_test.go` + `testdata/` | — | — | types, eventstream |

## Phase 2: Agent Loop (`pkg/agent/`)

| Status | Go file + test | TS source | Lines | Lock key | Deps |
|---|---|---|---|---|---|
|
| | Go file | TS source | Lines | Depends on |
|---|---|---|---|---|
| [x] | `types.go` `types_test.go` || `agent/src/types.ts` | | 194 | `pkg| ai/types ||agent-types` | ai/types |
| [x] | `loop.go` `loop_test.go` || `agent/src/agent-loop.ts` | | 417 | `pkg| agent/types, ai/* ||agent-loop` | agent/types, ai/* |
| [x] | `agent.go` `agent_test.go` || `agent/src/agent.ts` | | 536 | `pkg-agent-agent` | agent/types, loop || agent/types, loop |

## Phase 3: Tools (`pkg/core/tools/`)

After `truncate` and `pathutils`, remaining tools can be worked in parallel.

| Status | Go file + test | TS source | Lines | Lock key | Deps |
|---|---|---|---|---|---|
| [x] | `truncate.go` `truncate_test.go` | `coding-agent/src/core/tools/truncate.ts` | 265 | `pkg-core-tools-truncate` | none |
| [x] | `pathutils.go` `pathutils_test.go` | `coding-agent/src/core/tools/path-utils.ts` | 94 | `pkg-core-tools-pathutils` | none |
| [x] | `read.go` `read_test.go` | `coding-agent/src/core/tools/read.ts` | 222 | `pkg-core-tools-read` | truncate, pathutils, agent/types |
| [x] | `bash.go` `bash_test.go` | `coding-agent/src/core/tools/bash.ts` | 321 | `pkg-core-tools-bash` | truncate, agent/types |
| [x] | `edit.go` `edit_test.go` | `coding-agent/src/core/tools/edit.ts` | 227 | `pkg-core-tools-edit` | pathutils, agent/types |
| [x] | `write.go` `write_test.go` | `coding-agent/src/core/tools/write.ts` | 118 | `pkg-core-tools-write` | pathutils, agent/types |
| [x] | `grep.go` `grep_test.go` | `coding-agent/src/core/tools/grep.ts` | 346 | `pkg-core-tools-grep` | truncate, pathutils, agent/types |
| [x] | `find.go` `find_test.go` | `coding-agent/src/core/tools/find.ts` | 273 | `pkg-core-tools-find` | pathutils, agent/types |
| [x] | `ls.go` `ls_test.go` | `coding-agent/src/core/tools/ls.ts` | 170 | `pkg-core-tools-ls` | pathutils, agent/types |

After truncate + pathutils, remaining tools are independent — parallelizable.

| | Go file | TS source | Lines | Depends on |
|---|---|---|---|---|
| [x] | `truncate.go` | `coding-agent/src/core/tools/truncate.ts` | 265 | — |
| [x] | `pathutils.go` | `coding-agent/src/core/tools/path-utils.ts` | 94 | — |
| [x] | `read.go` | `coding-agent/src/core/tools/read.ts` | 222 | truncate, pathutils |
| [x] | `bash.go` | `coding-agent/src/core/tools/bash.ts` | 321 | truncate |
| [x] | `edit.go` | `coding-agent/src/core/tools/edit.ts` | 227 | pathutils |
| [x] | `write.go` | `coding-agent/src/core/tools/write.ts` | 118 | pathutils |
| [x] | `grep.go` | `coding-agent/src/core/tools/grep.ts` | 346 | truncate, pathutils |
| [x] | `find.go` | `coding-agent/src/core/tools/find.ts` | 273 | pathutils |
| [x] | `ls.go` | `coding-agent/src/core/tools/ls.ts` | 170 | pathutils |

## Phase 4: Session Manager

| Status | Go file + test | TS source | Lines | Lock key | Deps |
|---|---|---|---|---|---|
| [x] | `session.go` `session_test.go` | `coding-agent/src/core/session-manager.ts` | 1401 | `pkg-core-session` | agent/types, ai/types |

## Phase 5: Core Infrastructure (`pkg/core/`)

| Status | Go file + test | TS source | Lines | Lock key | Deps |
|---|---|---|---|---|---|
| [x] | `defaults.go` | `coding-agent/src/core/defaults.ts` | 3 | `pkg-core-defaults` | none |
| [x] | `messages.go` `messages_test.go` | `coding-agent/src/core/messages.ts` | 195 | `pkg-core-messages` | agent/types, ai/types |
| [x] | `authstorage.go` `authstorage_test.go` | `coding-agent/src/core/auth-storage.ts` | 348 | `pkg-core-authstorage` | none |
| [x] | `settings.go` `settings_test.go` | `coding-agent/src/core/settings-manager.ts` | 751 | `pkg-core-settings` | defaults |
| [x] | `systemprompt.go` `systemprompt_test.go` | `coding-agent/src/core/system-prompt.ts` | 188 | `pkg-core-systemprompt` | skills |
| [x] | `skills.go` `skills_test.go` | `coding-agent/src/core/skills.ts` | 459 | `pkg-core-skills` | none |
| [x] | `prompttemplates.go` `prompttemplates_test.go` | `coding-agent/src/core/prompt-templates.ts` | 299 | `pkg-core-prompttemplates` | none |
| [x] | `modelregistry.go` `modelregistry_test.go` | `coding-agent/src/core/model-registry.ts` | 665 | `pkg-core-modelregistry` | ai/types, ai/models, authstorage |
| [x] | `modelresolver.go` `modelresolver_test.go` | `coding-agent/src/core/model-resolver.ts` | 405 | `pkg-core-modelresolver` | modelregistry |
| [x] | `resourceloader.go` `resourceloader_test.go` | `coding-agent/src/core/resource-loader.ts` | 871 | `pkg-core-resourceloader` | settings, skills, prompttemplates |
| [x] | `keybindings.go` `keybindings_test.go` | `coding-agent/src/core/keybindings.ts` | 211 | `pkg-core-keybindings` | none |
| [x] | `bashexec.go` `bashexec_test.go` | `coding-agent/src/core/bash-executor.ts` | 278 | `pkg-core-bashexec` | tools/bash |
| [x] | `slashcmds.go` `slashcmds_test.go` | `coding-agent/src/core/slash-commands.ts` | 38 | `pkg-core-slashcmds` | none |
| [x] | `eventbus.go` `eventbus_test.go` | `coding-agent/src/core/event-bus.ts` | 33 | `pkg-core-eventbus` | none |
| [x] | `compaction/compaction.go` `compaction/compaction_test.go` | `coding-agent/src/core/compaction/compaction.ts` | 809 | `pkg-core-compaction-compaction` | session, messages, ai/* |
| [x] | `compaction/utils.go` `compaction/utils_test.go` | `coding-agent/src/core/compaction/utils.ts` | 154 | `pkg-core-compaction-utils` | messages |
| [x] | `sdk.go` `sdk_test.go` | `coding-agent/src/core/sdk.ts` | 365 | `pkg-core-sdk` | all above |
| [x] | `agentsession.go` `agentsession_test.go` | `coding-agent/src/core/agent-session.ts` | 2785 | `pkg-core-agentsession` | all above |
| [x] | **🎯 MILESTONE: `echo "Hello" \| pi-go -p` works** | | | | |
| | Go file | TS source | Lines | Depends on |
|---|---|---|---|---|
| [x] | `session.go` | `coding-agent/src/core/session-manager.ts` | 1401 | agent/types, ai/types |

## Phase 5: Core Infrastructure (`pkg/core/`)

| | Go file | TS source | Lines | Depends on |
|---|---|---|---|---|
| [x] | `defaults.go` | `coding-agent/src/core/defaults.ts` | 3 | — |
| [x] | `messages.go` | `coding-agent/src/core/messages.ts` | 195 | agent/types, ai/types |
| [x] | `authstorage.go` | `coding-agent/src/core/auth-storage.ts` | 348 | — |
| [x] | `settings.go` | `coding-agent/src/core/settings-manager.ts` | 751 | defaults |
| [x] | `systemprompt.go` | `coding-agent/src/core/system-prompt.ts` | 188 | skills |
| [x] | `skills.go` | `coding-agent/src/core/skills.ts` | 459 | — |
| [x] | `prompttemplates.go` | `coding-agent/src/core/prompt-templates.ts` | 299 | — |
| [x] | `modelregistry.go` | `coding-agent/src/core/model-registry.ts` | 665 | ai/types, authstorage |
| [x] | `modelresolver.go` | `coding-agent/src/core/model-resolver.ts` | 405 | modelregistry |
| [x] | `resourceloader.go` | `coding-agent/src/core/resource-loader.ts` | 871 | settings, skills, prompttemplates |
| [x] | `keybindings.go` | `coding-agent/src/core/keybindings.ts` | 211 | — |
| [x] | `bashexec.go` | `coding-agent/src/core/bash-executor.ts` | 278 | tools/bash |
| [x] | `slashcmds.go` | `coding-agent/src/core/slash-commands.ts` | 38 | — |
| [x] | `eventbus.go` | `coding-agent/src/core/event-bus.ts` | 33 | — |
| [x] | `compaction/compaction.go` | `coding-agent/src/core/compaction/compaction.ts` | 809 | session, messages, ai/* |
| [x] | `compaction/utils.go` | `coding-agent/src/core/compaction/utils.ts` | 154 | messages |
| [x] | `sdk.go` | `coding-agent/src/core/sdk.ts` | 365 | all above |
| [x] | `agentsession.go` | `coding-agent/src/core/agent-session.ts` | 2785 | all above |
| [x] | 🎯 **MILESTONE: `echo "Hello" \| pi-go -p` works** | | | |

## Phase 6: TUI (`pkg/tui/`)

| Status | Go file + test | TS source | Lines | Lock key | Deps |
|---|---|---|---|---|---|
| [x] | `terminal.go` `terminal_test.go` | `tui/src/terminal.ts` | 288 | `pkg-tui-terminal` | none |
| [x] | `keys.go` `keys_test.go` | `tui/src/keys.ts` | 1152 | `pkg-tui-keys` | none |
| [x] | `utils.go` `utils_test.go` | `tui/src/utils.ts` | 889 | `pkg-tui-utils` | none |
| [x] | `tui.go` `tui_test.go` | `tui/src/tui.ts` | 1154 | `pkg-tui-tui` | terminal, keys, utils |
| [x] | `fuzzy.go` `fuzzy_test.go` | `tui/src/fuzzy.ts` | 133 | `pkg-tui-fuzzy` | none |
| [x] | `image.go` `image_test.go` | `tui/src/terminal-image.ts` | 381 | `pkg-tui-image` | none |
| [x] | `components/text.go` `components/text_test.go` | `tui/src/components/text.ts` | 106 | `pkg-tui-components-text` | utils |
| [x] | `components/box.go` `components/box_test.go` | `tui/src/components/box.ts` | 137 | `pkg-tui-components-box` | utils, text |
| [x] | `components/input.go` `components/input_test.go` | `tui/src/components/input.ts` | 510 | `pkg-tui-components-input` | utils, keys |
| [x] | `components/editor.go` `components/editor_test.go` | `tui/src/components/editor.ts` | 1999 | `pkg-tui-components-editor` | utils, keys, input |
| [x] | `components/markdown.go` `components/markdown_test.go` | `tui/src/components/markdown.ts` | 770 | `pkg-tui-components-markdown` | utils, text |
| [x] | `components/selectlist.go` `components/selectlist_test.go` | `tui/src/components/select-list.ts` | 188 | `pkg-tui-components-selectlist` | utils, keys |
| [x] | `components/loader.go` `components/loader_test.go` | `tui/src/components/loader.ts` | 55 | `pkg-tui-components-loader` | none |
| [x] | `components/spacer.go` `components/spacer_test.go` | `tui/src/components/spacer.ts` | 28 | `pkg-tui-components-spacer` | none |

keys, utils, fuzzy, image are independent — parallelizable.

| | Go file | TS source | Lines | Depends on |
|---|---|---|---|---|
| [x] | `terminal.go` | `tui/src/terminal.ts` | 288 | — |
| [x] | `keys.go` | `tui/src/keys.ts` | 1152 | — |
| [x] | `utils.go` | `tui/src/utils.ts` | 889 | — |
| [x] | `fuzzy.go` | `tui/src/fuzzy.ts` | 133 | — |
| [x] | `image.go` | `tui/src/terminal-image.ts` | 381 | — |
| [x] | `tui.go` | `tui/src/tui.ts` | 1154 | terminal, keys, utils |
| [x] | `components/text.go` | `tui/src/components/text.ts` | 106 | utils |
| [x] | `components/box.go` | `tui/src/components/box.ts` | 137 | utils, text |
| [x] | `components/input.go` | `tui/src/components/input.ts` | 510 | utils, keys |
| [x] | `components/editor.go` | `tui/src/components/editor.ts` | 1999 | utils, keys, input |
| [x] | `components/markdown.go` | `tui/src/components/markdown.ts` | 770 | utils, text |
| [x] | `components/selectlist.go` | `tui/src/components/select-list.ts` | 188 | utils, keys |
| [x] | `components/loader.go` | `tui/src/components/loader.ts` | 55 | — |
| [x] | `components/spacer.go` | `tui/src/components/spacer.ts` | 28 | — |

## Phase 7: Interactive Mode (`pkg/modes/interactive/`)

| Status | Go file + test | TS source | Lines | Lock key | Deps |
|---|---|---|---|---|---|
| [x] | `theme/theme.go` `theme/theme_test.go` | `coding-agent/src/modes/interactive/theme/theme.ts` | 1100 | `pkg-modes-interactive-theme` | tui/* |
| [x] | `mode.go` `mode_test.go` (skeleton) | `coding-agent/src/modes/interactive/interactive-mode.ts` | 4362 | `pkg-modes-interactive-mode` | everything |
| [x] | Components (34/35 ported, index.ts N/A) | `coding-agent/src/modes/interactive/components/` | ~7500 | various | tui/*, core/* |
| [ ] | **🎯 MILESTONE: Full interactive TUI works** | | | | |
## Phase 8: Print & RPC Modes

| Status | Go file + test | TS source | Lines | Lock key | Deps |
|---|---|---|---|---|---|
|
| | Go file | TS source | Lines | Depends on |
|---|---|---|---|---|
| [x] | `print.go` `print_test.go` || `coding-agent/src/modes/print-mode.ts` | | 124 | `pkg| core/* ||modes-print` | core/* |
| [x] | `rpc/server.go` `rpc/server_test.go` || `coding-agent/src/modes/rpc/rpc-mode.ts` | | 634 | `pkg| core/* ||modes-rpc-server` | core/* |
| [x] | `rpc/types.go` `rpc/types_test.go` || `coding-agent/src/modes/rpc/rpc-types.ts` | | 263 | `pkg-modes-rpc-types` | ai/types || ai/types |

## Phase 9: CLI Entry Point

| Status | Go file + test | TS source | Lines | Lock key | Deps |
|---|---|---|---|---|---|
|
| | Go file | TS source | Lines | Depends on |
|---|---|---|---|---|
| [x] | `cmd/pi/main.go` || `coding-agent/src/cli.ts` | | 12 | `cmd| app ||pi-main` | app |
| [x] | `cmd/pi/app.go` `cmd/pi/app_test.go` || `coding-agent/src/main.ts` | | 726 | `cmd| everything ||pi-app` | everything |
| [x] | `cmd/pi/args.go` `cmd/pi/args_test.go` | `coding-agent/src/cli/args.ts` | 304 | `cmd-pi-args` | none |
