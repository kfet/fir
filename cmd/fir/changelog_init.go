package main

import (
	_ "embed"

	"github.com/kfet/fir/pkg/session"
)

//go:embed CHANGELOG.md
var changelogContent string

func init() {
	session.SetEmbeddedChangelog(changelogContent)
}
