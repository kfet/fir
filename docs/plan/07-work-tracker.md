# Work Tracker

Status: `[ ]` todo, `[~]` in progress, `[x]` done

## Phase 0: Scaffolding
- [ ] `go mod init github.com/kfet/pi-go`
- [ ] Makefile (build, build-all, test, sync-check)
- [ ] Directory structure
- [ ] `sync/UPSTREAM_MAP.md`
- [ ] `sync/sync-check.sh` + `sync/sync-record.sh`

## Phase 1: AI Layer (`pkg/ai/`)
- [ ] `pkg/ai/types.go` ← `packages/ai/src/types.ts` (295 lines)
- [ ] `pkg/ai/eventstream.go` ← `packages/ai/src/utils/event-stream.ts` (87 lines)
- [ ] `pkg/ai/models.go` ← `packages/ai/src/models.ts` + `models.generated.ts` (12284 lines)
- [ ] `pkg/ai/envkeys.go` ← `packages/ai/src/env-api-keys.ts` (115 lines)
- [ ] `pkg/ai/registry.go` ← `packages/ai/src/api-registry.ts` (98 lines)
- [ ] `pkg/ai/overflow.go` ← `packages/ai/src/utils/overflow.ts` (121 lines)
- [ ] `pkg/ai/jsonparse.go` ← `packages/ai/src/utils/json-parse.ts` (28 lines)
- [ ] `pkg/ai/stream.go` ← `packages/ai/src/stream.ts` (60 lines)
- [ ] `pkg/ai/providers/options.go` ← `packages/ai/src/providers/simple-options.ts` (45 lines)
- [ ] `pkg/ai/providers/transform.go` ← `packages/ai/src/providers/transform-messages.ts` (167 lines)
- [ ] `pkg/ai/providers/anthropic.go` ← `packages/ai/src/providers/anthropic.ts` (808 lines)
- [ ] `pkg/ai/providers/openai.go` ← `packages/ai/src/providers/openai-completions.ts` (847 lines)
- [ ] `pkg/ai/providers/openai_responses.go` ← `packages/ai/src/providers/openai-responses.ts` + `openai-responses-shared.ts` (754 lines)
- [ ] `pkg/ai/providers/google.go` ← `packages/ai/src/providers/google.ts` + `google-shared.ts` (769 lines)
- [ ] `pkg/ai/providers/bedrock.go` ← `packages/ai/src/providers/amazon-bedrock.ts` (731 lines)

## Phase 2: Agent Loop (`pkg/agent/`)
- [ ] `pkg/agent/types.go` ← `packages/agent/src/types.ts` (194 lines)
- [ ] `pkg/agent/loop.go` ← `packages/agent/src/agent-loop.ts` (417 lines)
- [ ] `pkg/agent/agent.go` ← `packages/agent/src/agent.ts` (536 lines)

## Phase 3: Tools (`pkg/core/tools/`)
- [ ] `pkg/core/tools/truncate.go` ← `packages/coding-agent/src/core/tools/truncate.ts` (265 lines)
- [ ] `pkg/core/tools/pathutils.go` ← `packages/coding-agent/src/core/tools/path-utils.ts` (94 lines)
- [ ] `pkg/core/tools/read.go` ← `packages/coding-agent/src/core/tools/read.ts` (222 lines)
- [ ] `pkg/core/tools/bash.go` ← `packages/coding-agent/src/core/tools/bash.ts` (321 lines)
- [ ] `pkg/core/tools/edit.go` ← `packages/coding-agent/src/core/tools/edit.ts` (227 lines)
- [ ] `pkg/core/tools/write.go` ← `packages/coding-agent/src/core/tools/write.ts` (118 lines)
- [ ] `pkg/core/tools/grep.go` ← `packages/coding-agent/src/core/tools/grep.ts` (346 lines)
- [ ] `pkg/core/tools/find.go` ← `packages/coding-agent/src/core/tools/find.ts` (273 lines)
- [ ] `pkg/core/tools/ls.go` ← `packages/coding-agent/src/core/tools/ls.ts` (170 lines)

## Phase 4: Session Manager
- [ ] `pkg/core/session.go` ← `packages/coding-agent/src/core/session-manager.ts` (1401 lines)

## Phase 5: Core Infrastructure
- [ ] `pkg/core/defaults.go` ← `packages/coding-agent/src/core/defaults.ts` (3 lines)
- [ ] `pkg/core/messages.go` ← `packages/coding-agent/src/core/messages.ts` (195 lines)
- [ ] `pkg/core/authstorage.go` ← `packages/coding-agent/src/core/auth-storage.ts` (348 lines)
- [ ] `pkg/core/settings.go` ← `packages/coding-agent/src/core/settings-manager.ts` (751 lines)
- [ ] `pkg/core/systemprompt.go` ← `packages/coding-agent/src/core/system-prompt.ts` (188 lines)
- [ ] `pkg/core/skills.go` ← `packages/coding-agent/src/core/skills.ts` (459 lines)
- [ ] `pkg/core/prompttemplates.go` ← `packages/coding-agent/src/core/prompt-templates.ts` (299 lines)
- [ ] `pkg/core/modelregistry.go` ← `packages/coding-agent/src/core/model-registry.ts` (665 lines)
- [ ] `pkg/core/modelresolver.go` ← `packages/coding-agent/src/core/model-resolver.ts` (405 lines)
- [ ] `pkg/core/resourceloader.go` ← `packages/coding-agent/src/core/resource-loader.ts` (871 lines)
- [ ] `pkg/core/keybindings.go` ← `packages/coding-agent/src/core/keybindings.ts` (211 lines)
- [ ] `pkg/core/bashexec.go` ← `packages/coding-agent/src/core/bash-executor.ts` (278 lines)
- [ ] `pkg/core/slashcmds.go` ← `packages/coding-agent/src/core/slash-commands.ts` (38 lines)
- [ ] `pkg/core/eventbus.go` ← `packages/coding-agent/src/core/event-bus.ts` (33 lines)
- [ ] `pkg/core/compaction/compaction.go` ← `packages/coding-agent/src/core/compaction/compaction.ts` (809 lines)
- [ ] `pkg/core/compaction/utils.go` ← `packages/coding-agent/src/core/compaction/utils.ts` (154 lines)
- [ ] `pkg/core/sdk.go` ← `packages/coding-agent/src/core/sdk.ts` (365 lines)
- [ ] `pkg/core/agentsession.go` ← `packages/coding-agent/src/core/agent-session.ts` (2785 lines)
- [ ] **🎯 MILESTONE: `echo "Hello" | pi-go -p` works**

## Phase 6: TUI (`pkg/tui/`)
- [ ] `pkg/tui/terminal.go` ← `packages/tui/src/terminal.ts` (288 lines)
- [ ] `pkg/tui/keys.go` ← `packages/tui/src/keys.ts` (1152 lines)
- [ ] `pkg/tui/utils.go` ← `packages/tui/src/utils.ts` (889 lines)
- [ ] `pkg/tui/tui.go` ← `packages/tui/src/tui.ts` (1154 lines)
- [ ] `pkg/tui/fuzzy.go` ← `packages/tui/src/fuzzy.ts` (133 lines)
- [ ] `pkg/tui/image.go` ← `packages/tui/src/terminal-image.ts` (381 lines)
- [ ] `pkg/tui/components/text.go` ← `packages/tui/src/components/text.ts` (106 lines)
- [ ] `pkg/tui/components/box.go` ← `packages/tui/src/components/box.ts` (137 lines)
- [ ] `pkg/tui/components/input.go` ← `packages/tui/src/components/input.ts` (510 lines)
- [ ] `pkg/tui/components/editor.go` ← `packages/tui/src/components/editor.ts` (1999 lines)
- [ ] `pkg/tui/components/markdown.go` ← `packages/tui/src/components/markdown.ts` (770 lines)
- [ ] `pkg/tui/components/selectlist.go` ← `packages/tui/src/components/select-list.ts` (188 lines)
- [ ] `pkg/tui/components/loader.go` ← `packages/tui/src/components/loader.ts` (55 lines)
- [ ] `pkg/tui/components/spacer.go` ← `packages/tui/src/components/spacer.ts` (28 lines)

## Phase 7: Interactive Mode (`pkg/modes/interactive/`)
- [ ] `pkg/modes/interactive/mode.go` ← `packages/coding-agent/src/modes/interactive/interactive-mode.ts` (4362 lines)
- [ ] `pkg/modes/interactive/theme.go` ← `packages/coding-agent/src/modes/interactive/theme/theme.ts` (1100 lines)
- [ ] Interactive mode components (many files, ~5000 lines total)
- [ ] **🎯 MILESTONE: Full interactive TUI works**

## Phase 8: Print & RPC Modes
- [ ] `pkg/modes/print.go` ← `packages/coding-agent/src/modes/print-mode.ts` (124 lines)
- [ ] `pkg/modes/rpc/server.go` ← `packages/coding-agent/src/modes/rpc/rpc-mode.ts` (634 lines)
- [ ] `pkg/modes/rpc/types.go` ← `packages/coding-agent/src/modes/rpc/rpc-types.ts` (263 lines)

## Phase 9: CLI Entry Point
- [ ] `cmd/pi/main.go` ← `packages/coding-agent/src/cli.ts` (12 lines)
- [ ] `cmd/pi/app.go` ← `packages/coding-agent/src/main.ts` (726 lines)
- [ ] CLI arg parsing ← `packages/coding-agent/src/cli/args.ts` (304 lines)
