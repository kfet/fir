package ai

import (
	"context"
	"testing"
)

func TestLocalAuthURLSink(t *testing.T) {
	ReportLocalAuthURL(context.Background(), "x") // no sink: no-op
	ctx := context.Background()
	if WithLocalAuthURLSink(ctx, nil) != ctx {
		t.Fatal("nil sink should return ctx unchanged")
	}
	var got string
	ReportLocalAuthURL(WithLocalAuthURLSink(ctx, func(u string) { got = u }), "http://localhost:1/cb")
	if got != "http://localhost:1/cb" {
		t.Fatalf("got %q", got)
	}
}
