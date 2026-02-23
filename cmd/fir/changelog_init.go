package main

import (
	_ "embed"

	"github.com/kfet/fir/pkg/core"
)

//go:embed CHANGELOG.md
var changelogContent string

func init() {
	core.SetEmbeddedChangelog(changelogContent)
}
