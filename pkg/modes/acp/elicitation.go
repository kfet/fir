package acp

import (
	"context"
	"encoding/json"
	"fmt"

	acpsdk "github.com/coder/acp-go-sdk"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kfet/fir/pkg/mcp"
)

// mcpElicitation forwards MCP servers' elicitation requests to the ACP
// client with the (unstable) elicitation/create method, in the modes the
// client declared. It returns a nil fn when the client declared none: fir
// then does not advertise elicitation to MCP servers and declines.
func mcpElicitation(conn acpConn, sessionID string, caps *acpsdk.ElicitationCapabilities) (mcp.ElicitFn, mcp.ElicitModes) {
	if caps == nil {
		return nil, mcp.ElicitModes{}
	}
	// An empty elicitation object predates modes and means form.
	modes := mcp.ElicitModes{Form: caps.Form != nil || caps.Url == nil, URL: caps.Url != nil}
	meta := map[string]any{"sessionId": sessionID}
	fn := func(ctx context.Context, req *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
		p := req.Params
		var areq acpsdk.UnstableCreateElicitationRequest
		switch p.Mode {
		case "url":
			if !modes.URL {
				return &sdk.ElicitResult{Action: "decline"}, nil
			}
			areq.Url = &acpsdk.UnstableCreateElicitationUrl{
				Meta: meta, Mode: "url", Message: mcp.ElicitMessage(req),
				ElicitationId: acpsdk.UnstableElicitationId(p.ElicitationID), Url: p.URL,
			}
		case "", "form":
			if !modes.Form {
				return &sdk.ElicitResult{Action: "decline"}, nil
			}
			var schema acpsdk.UnstableElicitationSchema
			if p.RequestedSchema != nil {
				b, err := json.Marshal(p.RequestedSchema)
				if err != nil {
					return nil, err
				}
				if err := json.Unmarshal(b, &schema); err != nil {
					return nil, fmt.Errorf("elicitation schema: %w", err)
				}
			}
			areq.Form = &acpsdk.UnstableCreateElicitationForm{
				Meta: meta, Mode: "form", Message: mcp.ElicitMessage(req), RequestedSchema: schema,
			}
		default:
			return &sdk.ElicitResult{Action: "decline"}, nil
		}
		resp, err := conn.UnstableCreateElicitation(ctx, areq)
		if err != nil {
			return nil, err
		}
		switch {
		case resp.Accept != nil:
			return &sdk.ElicitResult{Action: "accept", Content: resp.Accept.Content}, nil
		case resp.Cancel != nil:
			return &sdk.ElicitResult{Action: "cancel"}, nil
		default:
			return &sdk.ElicitResult{Action: "decline"}, nil
		}
	}
	return fn, modes
}
