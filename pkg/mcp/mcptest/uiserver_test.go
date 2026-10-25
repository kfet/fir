package mcptest

import (
	"context"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestUIServer(t *testing.T) {
	ctx := context.Background()
	st, ct := sdk.NewInMemoryTransports()
	ss, err := NewUIServer().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "c", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	for _, args := range []any{map[string]any{"n": 4}, nil} {
		res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: UIToolName, Arguments: args})
		if err != nil || len(res.Content) != 3 || res.StructuredContent == nil {
			t.Fatalf("call(%v) = %+v, %v", args, res, err)
		}
	}
	rr, err := cs.ReadResource(ctx, &sdk.ReadResourceParams{URI: UIResourceURI})
	if err != nil || len(rr.Contents) != 1 || rr.Contents[0].Text != UIHTML {
		t.Fatalf("read = %+v, %v", rr, err)
	}
}
