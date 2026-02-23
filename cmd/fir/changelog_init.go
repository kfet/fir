package main

import (
	_ "embed"

	"github.com/kfet/fir/pkg/core"
)

// CHANGELOG.md is copied here from the repo root by `make generate-changelog`
// (or `make build`/`make install`). The copy is committed to the repo so that
// plain `go build` and `go test` work without running make first.
//
//go:embed CHANGELOG.md
var changelogContent string

func init() {
	core.SetEmbeddedChangelog(changelogContent)
}
