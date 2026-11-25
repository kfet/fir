package session

import (
	"context"
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/pinoauth"
)

func TestWithLocalAuthURL(t *testing.T) {
	ctx, pick := WithLocalAuthURL(context.Background())
	info := pinoauth.AuthInfo{URL: "https://manual", ShortURL: "https://short"}
	if got := pick(info); got != "https://short" {
		t.Fatalf("before report: %q", got)
	}
	ai.ReportLocalAuthURL(ctx, "http://localhost:1/callback")
	if got := pick(info); got != "http://localhost:1/callback" {
		t.Fatalf("after report: %q", got)
	}
}
