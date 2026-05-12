package tools

// This file re-exports a small set of output-handling primitives from
// github.com/kfet/pinexec so existing call sites in pkg/agent/tools keep
// compiling unchanged after pkg/exec was extracted into pinexec.
//
// The primitives (ANSI stripping, env color injection, head/tail
// truncation) describe how the bash runner's output is shaped, so they
// live with the runner in pinexec. New code should import them from
// pinexec directly; this file exists to keep the existing call sites
// working without a wide sweep.

import "github.com/kfet/pinexec"

// Constants.
const (
	DefaultMaxBytes   = pinexec.DefaultMaxBytes
	DefaultMaxLines   = pinexec.DefaultMaxLines
	GrepMaxLineLength = pinexec.GrepMaxLineLength
)

// Types.
type (
	TruncationOptions = pinexec.TruncationOptions
	TruncationResult  = pinexec.TruncationResult
)

// Functions — re-exported as vars so the godoc and signature stay
// canonical in pinexec and don't drift here.
var (
	StripAnsi      = pinexec.StripAnsi
	AppendColorEnv = pinexec.AppendColorEnv
	FormatSize     = pinexec.FormatSize
	TruncateLine   = pinexec.TruncateLine
	TruncateHead   = pinexec.TruncateHead
	TruncateTail   = pinexec.TruncateTail
)
