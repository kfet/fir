# Work Tracker

`[ ]` = todo, `[x]` = done (code + tests + `go vet` clean)

**Before starting a task:** check if the `.go` file already exists — another agent may have written it.
**Before updating this file:** re-read it — another agent may have checked off items.

---
 ==  (code + tests + `go vet` clean)

**Before starting a task:** check if the `.go` file already exists — another agent may have written it.
**Before updating this file:** re-read it — another agent may have checked off items.

---
## Phase 0: Scaffolding

- [ ] `go.mod` + `go.sum` — `go.mod` + `go.sum`
- [ ] `Makefile``
- [ ] Directory structure (create all `pkg/` subdirs)
- [ ] `sync/UPSTREAM_MAP.md` — master TS↔Go file map
- [ ] `sync/sync-check.sh` + `sync/sync-record.sh`

## Phase 1: AI Layer (`pkg/ai/`)

Each item means: write `foo.go` AND `foo_test.go`.

| Status | Go file + test | TS source | Lines | Lock key | Deps |
|---|---|---|---|---|---|
| [ ] | `types.go` `types_test.go` | `ai/src/types.ts` | 295 | `pkg-ai-types` | none |
| [ ] | `eventstream.go` `eventstream_test.go` | `ai/src/utils/event-stream.ts` | 87 | `pkg-ai-eventstream` | types |
| [ ] | `models.go` `models_test.go` | `ai/src/models.ts` + `models.generated.ts` | 12284 | `pkg-ai-models` | types |
| [ ] | `envkeys.go` `envkeys_test.go` | `ai/src/env-api-keys.ts` | 115 | `pkg-ai-envkeys` | types |
| [ ] | `registry.go` `registry_test.go` | `ai/src/api-registry.ts` | 98 | `pkg-ai-registry` | types, eventstream |
| [ ] | `overflow.go` `overflow_test.go` | `ai/src/utils/overflow.ts` | 121 | `pkg-ai-overflow` | types |
| [ ] | `jsonparse.go` `jsonparse_test.go` | `ai/src/utils/json-parse.ts` | 28 | `pkg-ai-jsonparse` | none |
| [ ] | `stream.go` `stream_test.go` | `ai/src/stream.ts` | 60 | `pkg-ai-stream` | registry |
| [ ] | `providers/options.go` `providers/options_test.go` | `ai/src/providers/simple-options.ts` | 45 | `pkg-ai-providers-options` | types |
| [ ] | `providers/transform.go` `providers/transform_test.go` | `ai/src/providers/transform-messages.ts` | 167 | `pkg-ai-providers-transform` | types |
| [ ] | `providers/anthropic.go` `providers/anthropic_test.go` | `ai/src/providers/anthropic.ts` | 808 | `pkg-ai-providers-anthropic` | types, eventstream, registry, options, transform |
| [ ] | `providers/openai.go` `providers/openai_test.go` | `ai/src/providers/openai-completions.ts` | 847 | `pkg-ai-providers-openai` | types, eventstream, registry, options, transform |
| [ ] | `providers/openai_responses.go` `providers/openai_responses_test.go` | `ai/src/providers/openai-responses.ts` + shared | 754 | `pkg-ai-providers-openai-responses` | types, eventstream, registry, options |
| [ ] | `providers/google.go` `providers/google_test.go` | `ai/src/providers/google.ts` + shared | 769 | `pkg-ai-providers-google` | types, eventstream, registry, options, transform |
| [ ] | `providers/bedrock.go` `providers/bedrock_test.go` | `ai/src/providers/amazon-bedrock.ts` | 731 | `pkg-ai-providers-bedrock` | types, eventstream, registry, options, transform |
| [ ] | `providers/testutil_test.go` + `providers/testdata/` | — | — | `pkg-ai-providers-testutil` | types, eventstream |

Each item = `foo.go` + `foo_test.go`. TS paths relative to `../pi-mono/packages/`.

| | Go file | TS source | Lines | Depends on |
|---|---|---|---|---|
| [ ] | `types.go` | `ai/src/types.ts` | 295 | — |
| [ ] | `eventstream.go` | `ai/src/utils/event-stream.ts` | 87 | types |
| [ ] | `models.go` | `ai/src/models.ts` + `models.generated.ts` | 12284 | types |
| [ ] | `envkeys.go` | `ai/src/env-api-keys.ts` | 115 | types |
| [ ] | `registry.go` | `ai/src/api-registry.ts` | 98 | types, eventstream |
| [ ] | `overflow.go` | `ai/src/utils/overflow.ts` | 121 | types |
| [ ] | `jsonparse.go` | `ai/src/utils/json-parse.ts` | 28 | — |
| [ ] | `stream.go` | `ai/src/stream.ts` | 60 | registry |
| [ ] | `providers/options.go` | `ai/src/providers/simple-options.ts` | 45 | types |
| [ ] | `providers/transform.go` | `ai/src/providers/transform-messages.ts` | 167 | types |
| [ ] | `providers/anthropic.go` | `ai/src/providers/anthropic.ts` | 808 | all above |
| [ ] | `providers/openai.go` | `ai/src/providers/openai-completions.ts` | 847 | all above |
| [ ] | `providers/openai_responses.go` | `ai/src/providers/openai-responses.ts` + shared | 754 | all above |
| [ ] | `providers/google.go` | `ai/src/providers/google.ts` + shared | 769 | all above |
| [ ] | `providers/bedrock.go` | `ai/src/providers/amazon-bedrock.ts` | 731 | all above |
| [ ] | `providers/testutil_test.go` + `testdata/` | — | — | types, eventstream |

## Phase 2: Agent Loop (`pkg/agent/`)

| Status | Go file + test | TS source | Lines | Lock key | Deps |
|---|---|---|---|---|---|
|
| | Go file | TS source | Lines | Depends on |
|---|---|---|---|---|
| [ ] | `types.go` `types_test.go` || `agent/src/types.ts` | | 194 | `pkg| ai/types ||agent-types` | ai/types |
| [ ] | `loop.go` `loop_test.go` || `agent/src/agent-loop.ts` | | 417 | `pkg| agent/types, ai/* ||agent-loop` | agent/types, ai/* |
| [ ] | `agent.go` `agent_test.go` || `agent/src/agent.ts` | | 536 | `pkg-agent-agent` | agent/types, loop || agent/types, loop |

## Phase 3: Tools (`pkg/core/tools/`)

After `truncate` and `pathutils`, remaining tools can be worked in parallel.

| Status | Go file + test | TS source | Lines | Lock key | Deps |
|---|---|---|---|---|---|
| [ ] | `truncate.go` `truncate_test.go` | `coding-agent/src/core/tools/truncate.ts` | 265 | `pkg-core-tools-truncate` | none |
| [ ] | `pathutils.go` `pathutils_test.go` | `coding-agent/src/core/tools/path-utils.ts` | 94 | `pkg-core-tools-pathutils` | none |
| [ ] | `read.go` `read_test.go` | `coding-agent/src/core/tools/read.ts` | 222 | `pkg-core-tools-read` | truncate, pathutils, agent/types |
| [ ] | `bash.go` `bash_test.go` | `coding-agent/src/core/tools/bash.ts` | 321 | `pkg-core-tools-bash` | truncate, agent/types |
| [ ] | `edit.go` `edit_test.go` | `coding-agent/src/core/tools/edit.ts` | 227 | `pkg-core-tools-edit` | pathutils, agent/types |
| [ ] | `write.go` `write_test.go` | `coding-agent/src/core/tools/write.ts` | 118 | `pkg-core-tools-write` | pathutils, agent/types |
| [ ] | `grep.go` `grep_test.go` | `coding-agent/src/core/tools/grep.ts` | 346 | `pkg-core-tools-grep` | truncate, pathutils, agent/types |
| [ ] | `find.go` `find_test.go` | `coding-agent/src/core/tools/find.ts` | 273 | `pkg-core-tools-find` | pathutils, agent/types |
| [ ] | `ls.go` `ls_test.go` | `coding-agent/src/core/tools/ls.ts` | 170 | `pkg-core-tools-ls` | pathutils, agent/types |

After truncate + pathutils, remaining tools are independent — parallelizable.

| | Go file | TS source | Lines | Depends on |
|---|---|---|---|---|
| [ ] | `truncate.go` | `coding-agent/src/core/tools/truncate.ts` | 265 | — |
| [ ] | `pathutils.go` | `coding-agent/src/core/tools/path-utils.ts` | 94 | — |
| [ ] | `read.go` | `coding-agent/src/core/tools/read.ts` | 222 | truncate, pathutils |
| [ ] | `bash.go` | `coding-agent/src/core/tools/bash.ts` | 321 | truncate |
| [ ] | `edit.go` | `coding-agent/src/core/tools/edit.ts` | 227 | pathutils |
| [ ] | `write.go` | `coding-agent/src/core/tools/write.ts` | 118 | pathutils |
| [ ] | `grep.go` | `coding-agent/src/core/tools/grep.ts` | 346 | truncate, pathutils |
| [ ] | `find.go` | `coding-agent/src/core/tools/find.ts` | 273 | pathutils |
| [ ] | `ls.go` | `coding-agent/src/core/tools/ls.ts` | 170 | pathutils |

## Phase 4: Session Manager

| Status | Go file + test | TS source | Lines | Lock key | Deps |
|---|---|---|---|---|---|
| [ ] | `session.go` `session_test.go` | `coding-agent/src/core/session-manager.ts` | 1401 | `pkg-core-session` | agent/types, ai/types |

## Phase 5: Core Infrastructure (`pkg/core/`)

| Status | Go file + test | TS source | Lines | Lock key | Deps |
|---|---|---|---|---|---|
| [ ] | `defaults.go` | `coding-agent/src/core/defaults.ts` | 3 | `pkg-core-defaults` | none |
| [ ] | `messages.go` `messages_test.go` | `coding-agent/src/core/messages.ts` | 195 | `pkg-core-messages` | agent/types, ai/types |
| [ ] | `authstorage.go` `authstorage_test.go` | `coding-agent/src/core/auth-storage.ts` | 348 | `pkg-core-authstorage` | none |
| [ ] | `settings.go` `settings_test.go` | `coding-agent/src/core/settings-manager.ts` | 751 | `pkg-core-settings` | defaults |
| [ ] | `systemprompt.go` `systemprompt_test.go` | `coding-agent/src/core/system-prompt.ts` | 188 | `pkg-core-systemprompt` | skills |
| [ ] | `skills.go` `skills_test.go` | `coding-agent/src/core/skills.ts` | 459 | `pkg-core-skills` | none |
| [ ] | `prompttemplates.go` `prompttemplates_test.go` | `coding-agent/src/core/prompt-templates.ts` | 299 | `pkg-core-prompttemplates` | none |
| [ ] | `modelregistry.go` `modelregistry_test.go` | `coding-agent/src/core/model-registry.ts` | 665 | `pkg-core-modelregistry` | ai/types, ai/models, authstorage |
| [ ] | `modelresolver.go` `modelresolver_test.go` | `coding-agent/src/core/model-resolver.ts` | 405 | `pkg-core-modelresolver` | modelregistry |
| [ ] | `resourceloader.go` `resourceloader_test.go` | `coding-agent/src/core/resource-loader.ts` | 871 | `pkg-core-resourceloader` | settings, skills, prompttemplates |
| [ ] | `keybindings.go` `keybindings_test.go` | `coding-agent/src/core/keybindings.ts` | 211 | `pkg-core-keybindings` | none |
| [ ] | `bashexec.go` `bashexec_test.go` | `coding-agent/src/core/bash-executor.ts` | 278 | `pkg-core-bashexec` | tools/bash |
| [ ] | `slashcmds.go` `slashcmds_test.go` | `coding-agent/src/core/slash-commands.ts` | 38 | `pkg-core-slashcmds` | none |
| [ ] | `eventbus.go` `eventbus_test.go` | `coding-agent/src/core/event-bus.ts` | 33 | `pkg-core-eventbus` | none |
| [ ] | `compaction/compaction.go` `compaction/compaction_test.go` | `coding-agent/src/core/compaction/compaction.ts` | 809 | `pkg-core-compaction-compaction` | session, messages, ai/* |
| [ ] | `compaction/utils.go` `compaction/utils_test.go` | `coding-agent/src/core/compaction/utils.ts` | 154 | `pkg-core-compaction-utils` | messages |
| [ ] | `sdk.go` `sdk_test.go` | `coding-agent/src/core/sdk.ts` | 365 | `pkg-core-sdk` | all above |
| [ ] | `agentsession.go` `agentsession_test.go` | `coding-agent/src/core/agent-session.ts` | 2785 | `pkg-core-agentsession` | all above |
| [ ] | **🎯 MILESTONE: `echo "Hello" \| pi-go -p` works** | | | | |
| | Go file | TS source | Lines | Depends on |
|---|---|---|---|---|
| [ ] | `session.go` | `coding-agent/src/core/session-manager.ts` | 1401 | agent/types, ai/types |

## Phase 5: Core Infrastructure (`pkg/core/`)

| | Go file | TS source | Lines | Depends on |
|---|---|---|---|---|
| [ ] | `defaults.go` | `coding-agent/src/core/defaults.ts` | 3 | — |
| [ ] | `messages.go` | `coding-agent/src/core/messages.ts` | 195 | agent/types, ai/types |
| [ ] | `authstorage.go` | `coding-agent/src/core/auth-storage.ts` | 348 | — |
| [ ] | `settings.go` | `coding-agent/src/core/settings-manager.ts` | 751 | defaults |
| [ ] | `systemprompt.go` | `coding-agent/src/core/system-prompt.ts` | 188 | skills |
| [ ] | `skills.go` | `coding-agent/src/core/skills.ts` | 459 | — |
| [ ] | `prompttemplates.go` | `coding-agent/src/core/prompt-templates.ts` | 299 | — |
| [ ] | `modelregistry.go` | `coding-agent/src/core/model-registry.ts` | 665 | ai/types, authstorage |
| [ ] | `modelresolver.go` | `coding-agent/src/core/model-resolver.ts` | 405 | modelregistry |
| [ ] | `resourceloader.go` | `coding-agent/src/core/resource-loader.ts` | 871 | settings, skills, prompttemplates |
| [ ] | `keybindings.go` | `coding-agent/src/core/keybindings.ts` | 211 | — |
| [ ] | `bashexec.go` | `coding-agent/src/core/bash-executor.ts` | 278 | tools/bash |
| [ ] | `slashcmds.go` | `coding-agent/src/core/slash-commands.ts` | 38 | — |
| [ ] | `eventbus.go` | `coding-agent/src/core/event-bus.ts` | 33 | — |
| [ ] | `compaction/compaction.go` | `coding-agent/src/core/compaction/compaction.ts` | 809 | session, messages, ai/* |
| [ ] | `compaction/utils.go` | `coding-agent/src/core/compaction/utils.ts` | 154 | messages |
| [ ] | `sdk.go` | `coding-agent/src/core/sdk.ts` | 365 | all above |
| [ ] | `agentsession.go` | `coding-agent/src/core/agent-session.ts` | 2785 | all above |
| [ ] | 🎯 **MILESTONE: `echo "Hello" \| pi-go -p` works** | | | |

## Phase 6: TUI (`pkg/tui/`)

| Status | Go file + test | TS source | Lines | Lock key | Deps |
|---|---|---|---|---|---|
| [ ] | `terminal.go` `terminal_test.go` | `tui/src/terminal.ts` | 288 | `pkg-tui-terminal` | none |
| [ ] | `keys.go` `keys_test.go` | `tui/src/keys.ts` | 1152 | `pkg-tui-keys` | none |
| [ ] | `utils.go` `utils_test.go` | `tui/src/utils.ts` | 889 | `pkg-tui-utils` | none |
| [ ] | `tui.go` `tui_test.go` | `tui/src/tui.ts` | 1154 | `pkg-tui-tui` | terminal, keys, utils |
| [ ] | `fuzzy.go` `fuzzy_test.go` | `tui/src/fuzzy.ts` | 133 | `pkg-tui-fuzzy` | none |
| [ ] | `image.go` `image_test.go` | `tui/src/terminal-image.ts` | 381 | `pkg-tui-image` | none |
| [ ] | `components/text.go` `components/text_test.go` | `tui/src/components/text.ts` | 106 | `pkg-tui-components-text` | utils |
| [ ] | `components/box.go` `components/box_test.go` | `tui/src/components/box.ts` | 137 | `pkg-tui-components-box` | utils, text |
| [ ] | `components/input.go` `components/input_test.go` | `tui/src/components/input.ts` | 510 | `pkg-tui-components-input` | utils, keys |
| [ ] | `components/editor.go` `components/editor_test.go` | `tui/src/components/editor.ts` | 1999 | `pkg-tui-components-editor` | utils, keys, input |
| [ ] | `components/markdown.go` `components/markdown_test.go` | `tui/src/components/markdown.ts` | 770 | `pkg-tui-components-markdown` | utils, text |
| [ ] | `components/selectlist.go` `components/selectlist_test.go` | `tui/src/components/select-list.ts` | 188 | `pkg-tui-components-selectlist` | utils, keys |
| [ ] | `components/loader.go` `components/loader_test.go` | `tui/src/components/loader.ts` | 55 | `pkg-tui-components-loader` | none |
| [ ] | `components/spacer.go` `components/spacer_test.go` | `tui/src/components/spacer.ts` | 28 | `pkg-tui-components-spacer` | none |

keys, utils, fuzzy, image are independent — parallelizable.

| | Go file | TS source | Lines | Depends on |
|---|---|---|---|---|
| [ ] | `terminal.go` | `tui/src/terminal.ts` | 288 | — |
| [ ] | `keys.go` | `tui/src/keys.ts` | 1152 | — |
| [ ] | `utils.go` | `tui/src/utils.ts` | 889 | — |
| [ ] | `fuzzy.go` | `tui/src/fuzzy.ts` | 133 | — |
| [ ] | `image.go` | `tui/src/terminal-image.ts` | 381 | — |
| [ ] | `tui.go` | `tui/src/tui.ts` | 1154 | terminal, keys, utils |
| [ ] | `components/text.go` | `tui/src/components/text.ts` | 106 | utils |
| [ ] | `components/box.go` | `tui/src/components/box.ts` | 137 | utils, text |
| [ ] | `components/input.go` | `tui/src/components/input.ts` | 510 | utils, keys |
| [ ] | `components/editor.go` | `tui/src/components/editor.ts` | 1999 | utils, keys, input |
| [ ] | `components/markdown.go` | `tui/src/components/markdown.ts` | 770 | utils, text |
| [ ] | `components/selectlist.go` | `tui/src/components/select-list.ts` | 188 | utils, keys |
| [ ] | `components/loader.go` | `tui/src/components/loader.ts` | 55 | — |
| [ ] | `components/spacer.go` | `tui/src/components/spacer.ts` | 28 | — |

## Phase 7: Interactive Mode (`pkg/modes/interactive/`)

| Status | Go file + test | TS source | Lines | Lock key | Deps |
|---|---|---|---|---|---|
| [ ] | `theme.go` `theme_test.go` | `coding-agent/src/modes/interactive/theme/theme.ts` | 1100 | `pkg-modes-interactive-theme` | tui/* |
| [ ] | `mode.go` `mode_test.go` | `coding-agent/src/modes/interactive/interactive-mode.ts` | 4362 | `pkg-modes-interactive-mode` | everything |
| [ ] | Components (~20 files) | `coding-agent/src/modes/interactive/components/` | ~5000 | various | tui/*, core/* |
| [ ] | **🎯 MILESTONE: Full interactive TUI works** | | | | |
| | Go file | TS source | Lines | Depends on |
|---|---|---|---|---|
|| || | tui/* |||`.go`| `coding-agent/src/modes/interactive/interactive-mode.ts` | 4362 | everything |
| [ ] | Components~20) | `coding-agent/src/modes/interactive/components/` ||tui/*, core/* ||| ** | | | |
## Phase 8: Print & RPC Modes

| Status | Go file + test | TS source | Lines | Lock key | Deps |
|---|---|---|---|---|---|
|
| | Go file | TS source | Lines | Depends on |
|---|---|---|---|---|
| [ ] | `print.go` `print_test.go` || `coding-agent/src/modes/print-mode.ts` | | 124 | `pkg| core/* ||modes-print` | core/* |
| [ ] | `rpc/server.go` `rpc/server_test.go` || `coding-agent/src/modes/rpc/rpc-mode.ts` | | 634 | `pkg| core/* ||modes-rpc-server` | core/* |
| [ ] | `rpc/types.go` `rpc/types_test.go` || `coding-agent/src/modes/rpc/rpc-types.ts` | | 263 | `pkg-modes-rpc-types` | ai/types || ai/types |

## Phase 9: CLI Entry Point

| Status | Go file + test | TS source | Lines | Lock key | Deps |
|---|---|---|---|---|---|
|
| | Go file | TS source | Lines | Depends on |
|---|---|---|---|---|
| [ ] | `cmd/pi/main.go` || `coding-agent/src/cli.ts` | | 12 | `cmd| app ||pi-main` | app |
| [ ] | `cmd/pi/app.go` `cmd/pi/app_test.go` || `coding-agent/src/main.ts` | | 726 | `cmd| everything ||pi-app` | everything |
| [ ] || `cmdcmd/pi/args.go` | `coding-agent/src/cli/args.ts` | | 304 | `cmd-pi-args` | none || — |
