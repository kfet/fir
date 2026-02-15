Use idiomatic Go. Keep it simple.

Split large files into smaller ones, making sure to update the mapping in `sync/UPSTREAM_MAP.md`

See `PLAN.md` for the current plan and process.

Do not ignore any issues, address them promptly, even if preexisting. Do not postpone any work, even if it seems daunting - just break it down into smaller tasks.

Do not leave incomplete or stubbed code. Ensure all code is functional and tested.

## Extensions

Extensions register via `init()` using `extension.Register(...)`. For an extension to be available at runtime, it **must** be blank-imported in `cmd/tau/app.go`:

```go
_ "github.com/kfet/tau/pkg/extensions/sandbox"
```

If you add a new extension package under `pkg/extensions/`, always add the corresponding blank import to `cmd/tau/app.go` — otherwise its `init()` never runs and the extension silently does not load.
