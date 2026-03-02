## Reviewed 2026-03-01 (cycle 4)

Files: pkg/core/bashexec.go, pkg/core/bashexec_test.go, pkg/core/tools/bash.go, pkg/core/tools/ansi.go, pkg/core/tools/ansi_test.go, pkg/modes/interactive/components/tool_execution.go, pkg/modes/interactive/components/bash_execution_test.go, pkg/update/update.go, pkg/extproc/sdk/python/fir_ext.py, .fir/extensions/notify.py

## Test Coverage
- `pkg/extproc/sdk/python/extensions_test.py` — tests use inline copies of functions rather than importing from the actual extension files. If the real implementations diverge, tests won't catch it.
