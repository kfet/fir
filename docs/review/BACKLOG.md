## Reviewed 2026-03-02 (cycle 1 — post-extproc-demo work)

Files: pkg/extension/integration.go, pkg/extproc/sdk/python/fir_ext.py,
.fir/extensions/demo.py, pkg/extproc/sdk/python/demo_ext_test.py,
pkg/extproc/integration/demo_integration_test.go, pkg/extensions/claudeusage/*,
pkg/extension/extproc_hook_test.go (new)

---

## Simplification

- `pkg/extproc/sdk/python/fir_ext.py:365,412` — The `_workers` list is appended to on
  every tool call and event but completed threads are never pruned during the session.
  Threads are daemon threads and all joined at shutdown, so this is safe. For very
  long-running sessions with thousands of tool calls the list stays alive for the
  process lifetime. Low priority; acceptable as-is.

## Test Coverage

- `pkg/extproc/sdk/python/extensions_test.py` (carry-over from cycle 4) — tests use
  inline copies of functions rather than importing from the actual extension files. If
  the real implementations diverge, tests won't catch it.
