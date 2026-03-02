## Reviewed 2026-03-02 (cycle 2 — post-rename to pkg/extension)

Files: pkg/extension/*.go, pkg/extension/sdk/python/fir_ext.py,
pkg/extension/integration/demo_integration_test.go, cmd/fir/app.go,
pkg/modes/interactive/mode.go, pkg/modes/acp/acp.go

---

## Test Coverage

- `pkg/extension/sdk/python/extensions_test.py` — tests use `_load_extension()` to
  import the real `.fir/extensions/*.py` files, which is good. However `fir_ext.run()`
  is mocked out, so the decorator registrations (`@fir_ext.tool`, `@fir_ext.on`) are
  not exercised through the real SDK. Low priority; the `demo_ext_test.py` and
  `fir_ext_test.py` suites cover the SDK thoroughly.
