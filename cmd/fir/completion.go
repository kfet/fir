// Package main — `fir completion` subcommand.
//
// Embeds the static completion scripts under completions/ and prints the one
// matching the requested shell. The scripts are also shipped to disk (e.g. by
// Homebrew or the kfet/fir-dist install.sh) so users can install once; this
// subcommand exists as a fallback for users who installed fir via
// `go install` or a raw download.
package main

import (
	_ "embed"
	"fmt"
	"os"
)

//go:embed completions/_fir
var completionZsh string

//go:embed completions/fir.bash
var completionBash string

// runCompletion implements the "fir completion <shell>" subcommand.
func runCompletion() error {
	args := os.Args[2:]
	if len(args) == 0 {
		return fmt.Errorf("usage: fir completion <bash|zsh>")
	}
	switch args[0] {
	case "bash":
		fmt.Print(completionBash)
	case "zsh":
		fmt.Print(completionZsh)
	case "-h", "--help":
		fmt.Print(completionHelpText)
	default:
		return fmt.Errorf("unsupported shell: %s (supported: bash, zsh)", args[0])
	}
	return nil
}

const completionHelpText = `Usage: fir completion <shell>

Print a shell completion script. Supported shells: bash, zsh.

Bash:
  fir completion bash > /etc/bash_completion.d/fir
  # per-user (no sudo):
  fir completion bash > ~/.local/share/bash-completion/completions/fir

Zsh:
  fir completion zsh > "${fpath[1]}/_fir"
  # then run: compinit
`
